package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"gopkg.in/telebot.v4"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
	"github.com/yurii-merker/commute-tracker/internal/timezone"
)

type testContext struct {
	telebot.Context
	chatVal  *telebot.Chat
	textVal  string
	lastSent string
	allSent  []string
}

func (tc *testContext) Chat() *telebot.Chat { return tc.chatVal }
func (tc *testContext) Text() string        { return tc.textVal }
func (tc *testContext) Send(msg interface{}, _ ...interface{}) error {
	s, _ := msg.(string)
	tc.lastSent = s
	tc.allSent = append(tc.allSent, tc.lastSent)
	return nil
}

func (tc *testContext) anySentContains(sub string) bool {
	for _, s := range tc.allSent {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func newTC(chatID int64) *testContext {
	return &testContext{chatVal: &telebot.Chat{ID: chatID}}
}

func newTestBot(t *testing.T, repo Repository) (*Bot, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return &Bot{queries: repo, rdb: rdb, planner: nil}, mr
}

func TestHandleHelp(t *testing.T) {
	repo := newMockRepository()
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	if err := b.handleHelp(tc); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(tc.lastSent, "Commute Tracker") {
		t.Errorf("expected help header, got: %s", tc.lastSent)
	}

	expectedCommands := []string{"/add", "/status", "/stop", "/resume", "/delete", "/help"}
	for _, cmd := range expectedCommands {
		if !strings.Contains(tc.lastSent, cmd) {
			t.Errorf("expected help to mention %s", cmd)
		}
	}
}

func TestHandleStartNewUser(t *testing.T) {
	repo := newMockRepository()
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	if err := b.handleStart(tc); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(tc.lastSent, "Welcome") {
		t.Errorf("expected welcome message, got: %s", tc.lastSent)
	}
	if _, ok := repo.users[100]; !ok {
		t.Error("expected user to be created")
	}
}

func TestHandleStartExistingUser(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{TelegramChatID: 100, State: "ready"}
	repo.state[100] = "ready"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	if err := b.handleStart(tc); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(tc.lastSent, "Welcome") {
		t.Errorf("expected welcome for existing user, got: %s", tc.lastSent)
	}
}

func TestHandleAddUnregistered(t *testing.T) {
	repo := newMockRepository()
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	if err := b.handleAdd(tc); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(tc.lastSent, "/start") {
		t.Errorf("expected start prompt, got: %s", tc.lastSent)
	}
}

func TestHandleAddSuccess(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{
		ID:             pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		TelegramChatID: 100,
	}
	repo.state[100] = "ready"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	if err := b.handleAdd(tc); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(tc.lastSent, "departure station") {
		t.Errorf("expected station prompt, got: %s", tc.lastSent)
	}
	if repo.state[100] != domain.StateAwaitingFrom.String() {
		t.Errorf("expected awaiting_from, got: %s", repo.state[100])
	}
}

func TestHandleAddMaxRoutes(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{}, {}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	if err := b.handleAdd(tc); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(tc.lastSent, "max") {
		t.Errorf("expected max routes message, got: %s", tc.lastSent)
	}
}

func TestFullRouteCreationFlow(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAdd(tc)

	tc.textVal = "SMY"
	_ = b.handleAwaitingFrom(tc, context.Background(), 100, "SMY")
	if !strings.Contains(tc.lastSent, "St Mary Cray") {
		t.Errorf("expected station name, got: %s", tc.lastSent)
	}

	_ = b.handleAwaitingTo(tc, context.Background(), 100, "CTK")
	if repo.state[100] != "awaiting_time" {
		t.Fatalf("expected awaiting_time, got %s", repo.state[100])
	}

	_ = b.handleAwaitingTime(tc, context.Background(), 100, "07:45")
	if repo.state[100] != "awaiting_days" {
		t.Fatalf("expected awaiting_days, got %s", repo.state[100])
	}

	_ = b.handleAwaitingDays(tc, context.Background(), 100, "Mon Tue Fri")
	if repo.state[100] != "awaiting_alerts" {
		t.Fatalf("expected awaiting_alerts, got %s", repo.state[100])
	}

	_ = b.handleAwaitingAlerts(tc, context.Background(), 100, "60 30 10")
	if repo.state[100] != "awaiting_label" {
		t.Fatalf("expected awaiting_label, got %s", repo.state[100])
	}

	_ = b.handleAwaitingLabel(tc, context.Background(), 100, "Morning Commute", userID)
	if repo.state[100] != "ready" {
		t.Fatalf("expected ready, got %s", repo.state[100])
	}
	if !tc.anySentContains("Route created") {
		t.Errorf("expected route created in messages, got: %v", tc.allSent)
	}
	if len(repo.routes[uuidKey(userID)]) != 1 {
		t.Fatal("expected 1 route")
	}
}

func TestHandleAwaitingFromInvalidCRS(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingFrom(tc, context.Background(), 100, "ZZZ")

	if !strings.Contains(tc.lastSent, "no station found") {
		t.Errorf("expected invalid station, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingToSameStation(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	b.rdb.HSet(context.Background(), draftKey(100), "from", "KGX")

	tc := newTC(100)
	_ = b.handleAwaitingTo(tc, context.Background(), 100, "KGX")

	if !strings.Contains(tc.lastSent, "different") {
		t.Errorf("expected different station, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingToInvalidCRS(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingTo(tc, context.Background(), 100, "ZZZ")

	if !strings.Contains(tc.lastSent, "no station found") {
		t.Errorf("expected invalid station, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingTimeInvalid(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, context.Background(), 100, "abc")

	if !strings.Contains(tc.lastSent, "Invalid time") {
		t.Errorf("expected invalid time, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingLabelExpiredDraft(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	_ = b.handleAwaitingLabel(tc, context.Background(), 100, "Morning", userID)

	if !strings.Contains(tc.lastSent, "expired") {
		t.Errorf("expected expired, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingLabelTooLong(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	_ = b.handleAwaitingLabel(tc, context.Background(), 100, strings.Repeat("a", 51), userID)

	if !strings.Contains(tc.lastSent, "1 and 50") {
		t.Errorf("expected length message, got: %s", tc.lastSent)
	}
}

func TestHandleStatusNoRoutes(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, "no routes") {
		t.Errorf("expected no routes, got: %s", tc.lastSent)
	}
}

func TestHandleStatusWithRoutes(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{
		Label: "Morning", FromStationCrs: "SMH", ToStationCrs: "CTK",
		DepartureTime: pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
		AlertOffsets:  []int32{60}, IsActive: true,
	}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, "Morning") || !strings.Contains(tc.lastSent, "🟢") {
		t.Errorf("expected route details, got: %s", tc.lastSent)
	}
}

func cacheTrainStatus(t *testing.T, b *Bot, routeID pgtype.UUID, status *domain.TrainStatus) {
	t.Helper()
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	key := "route_service:" + formatRouteUUID(routeID)
	b.rdb.Set(context.Background(), key, data, 0)
}

func TestHandleStatusWithLiveTrainInfo(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"

	futureTime := time.Now().Add(2 * time.Hour)
	futureTimeStr := futureTime.Format("15:04")
	futurePgTime := pgtype.Time{
		Microseconds: int64(futureTime.Hour())*3600000000 + int64(futureTime.Minute())*60000000,
		Valid:        true,
	}
	repo.routes[uuidKey(userID)] = []db.Route{{
		ID: routeID, Label: "Morning", FromStationCrs: "SMY", ToStationCrs: "BFR",
		DepartureTime: futurePgTime,
		AlertOffsets:  []int32{30}, IsActive: true,
	}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()
	cacheTrainStatus(t, b, routeID, &domain.TrainStatus{
		Destination:        "London Blackfriars",
		ScheduledDeparture: futureTime,
		EstimatedDeparture: futureTime,
		Platform:           "2",
	})

	tc := newTC(100)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, futureTimeStr+" to London Blackfriars") {
		t.Errorf("expected train name, got: %s", tc.lastSent)
	}
	if !strings.Contains(tc.lastSent, "Plt 2") {
		t.Errorf("expected platform, got: %s", tc.lastSent)
	}
	if !strings.Contains(tc.lastSent, "On time") {
		t.Errorf("expected on time status, got: %s", tc.lastSent)
	}
}

func TestHandleStatusWithDelayedTrain(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"

	delayedFutureTime := time.Now().Add(2 * time.Hour)
	delayedPgTime := pgtype.Time{
		Microseconds: int64(delayedFutureTime.Hour())*3600000000 + int64(delayedFutureTime.Minute())*60000000,
		Valid:        true,
	}
	repo.routes[uuidKey(userID)] = []db.Route{{
		ID: routeID, Label: "Morning", FromStationCrs: "SMY", ToStationCrs: "BFR",
		DepartureTime: delayedPgTime,
		AlertOffsets:  []int32{30}, IsActive: true,
	}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()
	cacheTrainStatus(t, b, routeID, &domain.TrainStatus{
		Destination:        "London Blackfriars",
		ScheduledDeparture: delayedFutureTime,
		EstimatedDeparture: delayedFutureTime.Add(7 * time.Minute),
		DelayMins:          7,
	})

	tc := newTC(100)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, "Plt TBC") {
		t.Errorf("expected TBC platform, got: %s", tc.lastSent)
	}
	if !strings.Contains(tc.lastSent, "Delayed 7 min") {
		t.Errorf("expected delay info, got: %s", tc.lastSent)
	}
}

func TestHandleStatusNoCachedTrain(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{
		Label: "Morning", FromStationCrs: "SMY", ToStationCrs: "BFR",
		DepartureTime: pgtype.Time{Microseconds: 12*3600000000 + 15*60000000, Valid: true},
		AlertOffsets:  []int32{30}, IsActive: true,
	}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, "Morning") {
		t.Errorf("expected route to still show, got: %s", tc.lastSent)
	}
	if strings.Contains(tc.lastSent, "Plt") {
		t.Errorf("expected no train line when no cache, got: %s", tc.lastSent)
	}
}

func TestHandleStopDeactivates(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{ID: routeID, IsActive: true}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStop(tc)

	if !strings.Contains(tc.lastSent, "Stopped monitoring 1") {
		t.Errorf("expected stop message, got: %s", tc.lastSent)
	}
	if !strings.Contains(tc.lastSent, "/resume") {
		t.Errorf("expected resume hint, got: %s", tc.lastSent)
	}
}

func TestHandleStopNoActive(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{IsActive: false}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStop(tc)

	if !strings.Contains(tc.lastSent, "No active routes") {
		t.Errorf("expected no active, got: %s", tc.lastSent)
	}
}

func TestHandleDeleteSingle(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{ID: routeID, Label: "Morning"}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleDelete(tc)

	if !strings.Contains(tc.lastSent, "Are you sure") {
		t.Errorf("expected confirmation, got: %s", tc.lastSent)
	}
	if !strings.Contains(tc.lastSent, "Morning") {
		t.Errorf("expected route name in confirmation, got: %s", tc.lastSent)
	}
	if repo.state[100] != "awaiting_delete_confirm" {
		t.Errorf("expected awaiting_delete_confirm, got: %s", repo.state[100])
	}
}

func TestHandleDeleteMultiple(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{
		{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, Label: "Morning", FromStationCrs: "SMH", ToStationCrs: "CTK"},
		{ID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}, Label: "Evening", FromStationCrs: "CTK", ToStationCrs: "SMH"},
	}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleDelete(tc)

	if !strings.Contains(tc.lastSent, "1.") || !strings.Contains(tc.lastSent, "2.") {
		t.Errorf("expected route list, got: %s", tc.lastSent)
	}
	if repo.state[100] != "awaiting_delete" {
		t.Errorf("expected awaiting_delete, got: %s", repo.state[100])
	}
}

func TestHandleAwaitingDeleteChoice(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.routes[uuidKey(userID)] = []db.Route{
		{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, Label: "Morning"},
		{ID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}, Label: "Evening"},
	}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingDelete(tc, context.Background(), 100, "2", userID)

	if !strings.Contains(tc.lastSent, "Are you sure") {
		t.Errorf("expected confirmation, got: %s", tc.lastSent)
	}
	if !strings.Contains(tc.lastSent, "Evening") {
		t.Errorf("expected route name in confirmation, got: %s", tc.lastSent)
	}
	if repo.state[100] != "awaiting_delete_confirm" {
		t.Errorf("expected awaiting_delete_confirm, got: %s", repo.state[100])
	}
}

func TestHandleAwaitingDeleteEmptyRoutes(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingDelete(tc, context.Background(), 100, "1", userID)

	if !strings.Contains(tc.lastSent, "already deleted") {
		t.Errorf("expected already deleted, got: %s", tc.lastSent)
	}
}

func TestHandleConfirmDelete(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "awaiting_delete_confirm"
	repo.routes[uuidKey(userID)] = []db.Route{{ID: routeID, Label: "Morning"}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleConfirmDelete(tc, context.Background(), 100, "1", userID)

	if !strings.Contains(tc.lastSent, "Deleted route: Morning") {
		t.Errorf("expected deleted, got: %s", tc.lastSent)
	}
	if repo.state[100] != "ready" {
		t.Errorf("expected ready, got: %s", repo.state[100])
	}
}

func TestHandleCancelDelete(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "awaiting_delete_confirm"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleCancelDelete(tc, context.Background(), 100)

	if !strings.Contains(tc.lastSent, "Nothing was deleted") {
		t.Errorf("expected cancel message, got: %s", tc.lastSent)
	}
	if repo.state[100] != "ready" {
		t.Errorf("expected ready, got: %s", repo.state[100])
	}
}

func TestHandleTextUnknownState(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "ready"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	tc.textVal = "random"
	_ = b.handleText(tc)

	if !strings.Contains(tc.lastSent, "didn't understand") {
		t.Errorf("expected fallback, got: %s", tc.lastSent)
	}
}

func TestHandleTextUnregistered(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(999)
	tc.textVal = "hello"
	_ = b.handleText(tc)

	if !strings.Contains(tc.lastSent, "/start") {
		t.Errorf("expected start prompt, got: %s", tc.lastSent)
	}
}

func TestHandleTextDispatchesStates(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tests := []struct {
		state    string
		text     string
		contains string
	}{
		{domain.StateAwaitingFrom.String(), "KGX", "Kings Cross"},
		{domain.StateAwaitingTime.String(), "abc", "Invalid time"},
	}

	for _, tt := range tests {
		repo.state[100] = tt.state
		tc := newTC(100)
		tc.textVal = tt.text
		_ = b.handleText(tc)

		if !strings.Contains(tc.lastSent, tt.contains) {
			t.Errorf("state %s, text %q: expected %q in response, got: %s", tt.state, tt.text, tt.contains, tc.lastSent)
		}
	}
}

func TestHandleTextAllStates(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY")
	b.rdb.HSet(ctx, draftKey(100), "to", "CTK")
	b.rdb.HSet(ctx, draftKey(100), "time", "07:45")
	b.rdb.HSet(ctx, draftKey(100), "days", "31")
	b.rdb.HSet(ctx, draftKey(100), "alerts", "60 30")

	tests := []struct {
		state    string
		text     string
		contains string
	}{
		{domain.StateAwaitingFrom.String(), "PAD", "Paddington"},
		{domain.StateAwaitingTo.String(), "BTN", "Brighton"},
		{domain.StateAwaitingTime.String(), "08:30", "08:30"},
		{domain.StateAwaitingDays.String(), "Mon Tue Wed Thu Fri", "Active on"},
		{domain.StateAwaitingAlerts.String(), "60 30", "Reminders"},
		{domain.StateAwaitingLabel.String(), "Evening", "Route created"},
	}

	for _, tt := range tests {
		repo.state[100] = tt.state
		tc := newTC(100)
		tc.textVal = tt.text
		_ = b.handleText(tc)

		if !tc.anySentContains(tt.contains) {
			t.Errorf("state %s: expected %q, got: %v", tt.state, tt.contains, tc.allSent)
		}
	}
}

func TestHandleStatusPausedRoute(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{
		Label: "Evening", FromStationCrs: "CTK", ToStationCrs: "SMH",
		DepartureTime: pgtype.Time{Microseconds: 18*3600000000 + 30*60000000, Valid: true},
		AlertOffsets:  []int32{60}, IsActive: false,
	}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, "⏸️") {
		t.Errorf("expected paused icon, got: %s", tc.lastSent)
	}
}

func TestHandleDeleteNoRoutes(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleDelete(tc)

	if !strings.Contains(tc.lastSent, "no routes to delete") {
		t.Errorf("expected no routes message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingTimeValid(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "awaiting_time"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY")
	b.rdb.HSet(ctx, draftKey(100), "to", "CTK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, "18:30")

	if !strings.Contains(tc.lastSent, "18:30") {
		t.Errorf("expected time confirmation, got: %s", tc.lastSent)
	}
	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days, got: %s", repo.state[100])
	}
}

func TestHandleStatusUnregistered(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(999)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, "/start") {
		t.Errorf("expected start prompt, got: %s", tc.lastSent)
	}
}

func TestHandleStopUnregistered(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(999)
	_ = b.handleStop(tc)

	if !strings.Contains(tc.lastSent, "/start") {
		t.Errorf("expected start prompt, got: %s", tc.lastSent)
	}
}

func TestHandleDeleteUnregistered(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(999)
	_ = b.handleDelete(tc)

	if !strings.Contains(tc.lastSent, "/start") {
		t.Errorf("expected start prompt, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingDeleteInvalidChoice(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.routes[uuidKey(userID)] = []db.Route{{Label: "Only"}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingDelete(tc, context.Background(), 100, "5", userID)

	if !strings.Contains(tc.lastSent, "1 or 2") {
		t.Errorf("expected prompt, got: %s", tc.lastSent)
	}
}

func TestHandleStartDBError(t *testing.T) {
	repo := &errorRepository{mockRepository: *newMockRepository(), failOn: "CreateUser"}
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStart(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleAddDBError(t *testing.T) {
	repo := &errorRepository{mockRepository: *newMockRepository(), failOn: "GetUserByChatID"}
	repo.users[100] = db.User{TelegramChatID: 100}
	repo.state[100] = "ready"
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAdd(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleAddCountError(t *testing.T) {
	repo := &errorRepository{mockRepository: *newMockRepository(), failOn: "CountRoutesByUserID"}
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "ready"
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAdd(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleAddUpdateStateError(t *testing.T) {
	repo := &errorRepository{mockRepository: *newMockRepository(), failOn: "UpdateUserState"}
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "ready"
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAdd(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleStatusDBError(t *testing.T) {
	repo := &errorRepository{mockRepository: *newMockRepository(), failOn: "GetRoutesByUserID"}
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "ready"
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleResumeActivates(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{ID: routeID, IsActive: false}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleResume(tc)

	if !strings.Contains(tc.lastSent, "Resumed monitoring 1") {
		t.Errorf("expected resume message, got: %s", tc.lastSent)
	}
}

func TestHandleResumeNoPaused(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{IsActive: true}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleResume(tc)

	if !strings.Contains(tc.lastSent, "No paused routes") {
		t.Errorf("expected no paused, got: %s", tc.lastSent)
	}
}

func TestHandleResumeUnregistered(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(999)
	_ = b.handleResume(tc)

	if !strings.Contains(tc.lastSent, "/start") {
		t.Errorf("expected start prompt, got: %s", tc.lastSent)
	}
}

func TestHandleResumeDBError(t *testing.T) {
	repo := &errorRepository{mockRepository: *newMockRepository(), failOn: "GetRoutesByUserID"}
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "ready"
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleResume(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleStopDBError(t *testing.T) {
	repo := &errorRepository{mockRepository: *newMockRepository(), failOn: "GetRoutesByUserID"}
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "ready"
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStop(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingFromAmbiguous(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingFrom(tc, context.Background(), 100, "king")

	if !strings.Contains(tc.lastSent, "Multiple stations found") {
		t.Errorf("expected ambiguous message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingFromValidCRS(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingFrom(tc, context.Background(), 100, "PAD")

	if !strings.Contains(tc.lastSent, "PAD") {
		t.Errorf("expected PAD in response, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingToExpiredDraft(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingTo(tc, context.Background(), 100, "PAD")

	if !strings.Contains(tc.lastSent, "expired") {
		t.Errorf("expected expired message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingToAmbiguous(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY")

	tc := newTC(100)
	_ = b.handleAwaitingTo(tc, ctx, 100, "king")

	if !strings.Contains(tc.lastSent, "Multiple stations found") {
		t.Errorf("expected ambiguous message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingTimeExpiredDraft(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, context.Background(), 100, "07:45")

	if !strings.Contains(tc.lastSent, "expired") {
		t.Errorf("expected expired message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingTimeNormalization(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, "7:45")

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days (normalized time should work), got: %s", repo.state[100])
	}
}

func currentTimeStr() string {
	now := time.Now()
	return fmt.Sprintf("%02d:%02d", now.Hour(), now.Minute())
}

func TestHandleAwaitingTimeWithPlannerExactMatch(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	now := time.Now()
	timeStr := currentTimeStr()

	planner := &mockPlanner{
		nearestResult: &domain.NearestTrains{
			Exact: &domain.TrainStatus{
				ServiceID:          "svc1",
				ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), 0, 0, time.UTC),
			},
		},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, timeStr)

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days for exact match, got: %s", repo.state[100])
	}
}

func TestHandleAwaitingTimeWithPlannerNoTrains(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	timeStr := currentTimeStr()

	planner := &mockPlanner{
		nearestResult: &domain.NearestTrains{},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, timeStr)

	if !strings.Contains(tc.lastSent, "No trains found") {
		t.Errorf("expected no trains message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingTimeWithPlannerChoices(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	now := time.Now()
	targetHour := now.Hour()
	targetMin := now.Minute()
	timeStr := fmt.Sprintf("%02d:%02d", targetHour, targetMin)

	beforeHour := targetHour
	beforeMin := targetMin - 15
	if beforeMin < 0 {
		beforeHour--
		beforeMin += 60
	}
	afterHour := targetHour
	afterMin := targetMin + 15
	if afterMin >= 60 {
		afterHour++
		afterMin -= 60
	}

	planner := &mockPlanner{
		nearestResult: &domain.NearestTrains{
			Before: &domain.TrainStatus{
				ServiceID:          "before",
				ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), beforeHour, beforeMin, 0, 0, time.UTC),
				Destination:        "London",
			},
			After: &domain.TrainStatus{
				ServiceID:          "after",
				ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), afterHour, afterMin, 0, 0, time.UTC),
				Destination:        "London",
			},
		},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, timeStr)

	if repo.state[100] != "awaiting_train_choice" {
		t.Errorf("expected awaiting_train_choice, got: %s", repo.state[100])
	}
}

func TestHandleAwaitingTrainChoiceOption1(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "awaiting_train_choice"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK", "train_option_1", "07:30", "train_option_2", "08:00")

	tc := newTC(100)
	_ = b.handleAwaitingTrainChoice(tc, ctx, 100, "1")

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days, got: %s", repo.state[100])
	}
	if !tc.anySentContains("07:30") {
		t.Errorf("expected 07:30 in response, got: %v", tc.allSent)
	}
}

func TestHandleAwaitingTrainChoiceOption2(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "awaiting_train_choice"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK", "train_option_1", "07:30", "train_option_2", "08:00")

	tc := newTC(100)
	_ = b.handleAwaitingTrainChoice(tc, ctx, 100, "2")

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days, got: %s", repo.state[100])
	}
}

func TestHandleAwaitingTrainChoiceInvalid(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK", "train_option_1", "07:30", "train_option_2", "08:00")

	tc := newTC(100)
	_ = b.handleAwaitingTrainChoice(tc, ctx, 100, "invalid")

	if !strings.Contains(tc.lastSent, "reply with 1, 2") {
		t.Errorf("expected prompt for 1 or 2, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingTrainChoiceOption2MissingFallsBack(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK", "train_option_1", "07:30")

	tc := newTC(100)
	_ = b.handleAwaitingTrainChoice(tc, ctx, 100, "2")

	if !strings.Contains(tc.lastSent, "reply with 1") {
		t.Errorf("expected prompt for 1, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingTrainChoiceExpiredDraft(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingTrainChoice(tc, context.Background(), 100, "1")

	if !strings.Contains(tc.lastSent, "expired") {
		t.Errorf("expected expired message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingTrainChoiceTimeInput(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK", "train_option_1", "07:30")

	tc := newTC(100)
	_ = b.handleAwaitingTrainChoice(tc, ctx, 100, "09:00")

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days for time input, got: %s", repo.state[100])
	}
}

func TestProceedToDays(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.proceedToDays(tc, context.Background(), 100, "07:45")

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days, got: %s", repo.state[100])
	}
	if !strings.Contains(tc.lastSent, "07:45") {
		t.Errorf("expected time in response, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingDaysValid(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingDays(tc, context.Background(), 100, "Mon Tue Fri")

	if repo.state[100] != "awaiting_alerts" {
		t.Errorf("expected awaiting_alerts, got: %s", repo.state[100])
	}
	if !strings.Contains(tc.lastSent, "Mon, Tue, Fri") {
		t.Errorf("expected days confirmation, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingDaysWeekdays(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingDays(tc, context.Background(), 100, "weekdays")

	if !strings.Contains(tc.lastSent, "Mon-Fri") {
		t.Errorf("expected Mon-Fri, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingDaysInvalid(t *testing.T) {
	repo := newMockRepository()
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleAwaitingDays(tc, context.Background(), 100, "InvalidDay")

	if !strings.Contains(tc.lastSent, "invalid day") {
		t.Errorf("expected invalid day message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingLabelEmpty(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	_ = b.handleAwaitingLabel(tc, context.Background(), 100, "", userID)

	if !strings.Contains(tc.lastSent, "1 and 50") {
		t.Errorf("expected length message, got: %s", tc.lastSent)
	}
}

func TestBuildTrainChoiceMessage(t *testing.T) {
	t.Run("both options with destinations", func(t *testing.T) {
		before := &domain.TrainOption{Time: "07:30", Destination: "London"}
		after := &domain.TrainOption{Time: "08:00", Destination: "Brighton"}

		msg, menu := buildTrainChoiceMessage("route-id", "07:45", before, after)

		if !strings.Contains(msg, "07:45") {
			t.Errorf("expected requested time, got: %s", msg)
		}
		if !strings.Contains(msg, "London") {
			t.Errorf("expected London, got: %s", msg)
		}
		if !strings.Contains(msg, "Brighton") {
			t.Errorf("expected Brighton, got: %s", msg)
		}
		if menu == nil {
			t.Error("expected menu to be non-nil")
		}
	})

	t.Run("only before no destination", func(t *testing.T) {
		before := &domain.TrainOption{Time: "07:30"}

		msg, _ := buildTrainChoiceMessage("route-id", "07:45", before, nil)

		if !strings.Contains(msg, "07:30") {
			t.Errorf("expected 07:30, got: %s", msg)
		}
	})

	t.Run("only after no destination", func(t *testing.T) {
		after := &domain.TrainOption{Time: "08:00"}

		msg, _ := buildTrainChoiceMessage("route-id", "07:45", nil, after)

		if !strings.Contains(msg, "08:00") {
			t.Errorf("expected 08:00, got: %s", msg)
		}
	})
}

func TestBuildBetterTrainMessage(t *testing.T) {
	t.Run("with destination", func(t *testing.T) {
		msg, menu := buildBetterTrainMessage("route-id", "07:45", "07:30", "London")

		if !strings.Contains(msg, "Better train found") {
			t.Errorf("expected better train message, got: %s", msg)
		}
		if !strings.Contains(msg, "07:30") {
			t.Errorf("expected better time, got: %s", msg)
		}
		if !strings.Contains(msg, "London") {
			t.Errorf("expected destination, got: %s", msg)
		}
		if menu == nil {
			t.Error("expected menu to be non-nil")
		}
	})

	t.Run("without destination", func(t *testing.T) {
		msg, _ := buildBetterTrainMessage("route-id", "07:45", "07:30", "")

		if strings.Contains(msg, "07:30 to ") {
			t.Errorf("expected no destination after time, got: %s", msg)
		}
	})
}

func TestPlanRouteNowNoPlanner(t *testing.T) {
	b, mr := newTestBot(t, newMockRepository())
	defer mr.Close()

	tc := newTC(100)
	_ = b.planRouteNow(tc, context.Background(), db.Route{})

	if !strings.Contains(tc.lastSent, "next scheduled check") {
		t.Errorf("expected fallback message, got: %s", tc.lastSent)
	}
}

func TestPlanRouteNowSuccess(t *testing.T) {
	repo := newMockRepository()
	planner := &mockPlanner{
		planResult: &domain.TrainStatus{
			ServiceID:          "svc1",
			ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
			Platform:           "3",
		},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	tc := newTC(100)
	_ = b.planRouteNow(tc, context.Background(), db.Route{
		ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	})

	if !strings.Contains(tc.lastSent, "Train found") {
		t.Errorf("expected train found message, got: %s", tc.lastSent)
	}
}

func TestPlanRouteNowFailNotInRange(t *testing.T) {
	repo := newMockRepository()
	planner := &mockPlanner{
		planErr:         errTestBot,
		planScheduleErr: errTestBot,
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	tc := newTC(100)
	route := db.Route{
		ID:            pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		DepartureTime: pgtype.Time{Microseconds: 23 * 3600000000, Valid: true},
	}
	_ = b.planRouteNow(tc, context.Background(), route)

	if !strings.Contains(tc.lastSent, "Could not find scheduled trains") {
		t.Errorf("expected scheduled trains fallback message, got: %s", tc.lastSent)
	}
}

func TestPlanRouteNowFailWithNearestTrains(t *testing.T) {
	repo := newMockRepository()

	now := time.Now()
	nearHour := now.Hour()
	nearMin := now.Minute()

	planner := &mockPlanner{
		planErr: errTestBot,
		nearestResult: &domain.NearestTrains{
			Before: &domain.TrainStatus{
				ServiceID:          "before",
				ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), nearHour, nearMin, 0, 0, time.UTC),
				Destination:        "London",
			},
			After: &domain.TrainStatus{
				ServiceID:          "after",
				ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), nearHour, nearMin+10, 0, 0, time.UTC),
				Destination:        "London",
			},
		},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	tc := newTC(100)
	depMicro := int64(nearHour)*3600000000 + int64(nearMin)*60000000
	route := db.Route{
		ID:             pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		FromStationCrs: "SMY",
		ToStationCrs:   "CTK",
		DepartureTime:  pgtype.Time{Microseconds: depMicro, Valid: true},
	}
	_ = b.planRouteNow(tc, context.Background(), route)

	if !tc.anySentContains("No exact train") {
		t.Errorf("expected train choice message, got: %v", tc.allSent)
	}
}

func TestSendRouteTrainChoiceSingleOption(t *testing.T) {
	repo := newMockRepository()
	planner := &mockPlanner{}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	tc := newTC(100)
	route := db.Route{
		ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
	nearest := &domain.NearestTrains{
		Before: &domain.TrainStatus{
			ServiceID:          "only",
			ScheduledDeparture: time.Date(2026, 1, 1, 7, 30, 0, 0, time.UTC),
			Destination:        "London",
			Platform:           "3",
		},
	}

	_ = b.sendRouteTrainChoice(tc, context.Background(), route, 7*60+45, nearest)

	if !tc.anySentContains("adjusted to") {
		t.Errorf("expected auto-select message, got: %v", tc.allSent)
	}
}

func TestAutoSelectTrain(t *testing.T) {
	repo := newMockRepository()
	planner := &mockPlanner{}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	tc := newTC(100)
	route := db.Route{
		ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
	train := &domain.TrainStatus{
		ServiceID:          "svc1",
		ScheduledDeparture: time.Date(2026, 1, 1, 7, 30, 0, 0, time.UTC),
		Destination:        "London",
		Platform:           "3",
	}

	_ = b.autoSelectTrain(tc, context.Background(), route, train)

	if !strings.Contains(tc.lastSent, "adjusted to 07:30") {
		t.Errorf("expected adjusted time, got: %s", tc.lastSent)
	}
}

func TestProceedToDaysDBError(t *testing.T) {
	repo := &errorRepository{mockRepository: *newMockRepository(), failOn: "UpdateUserState"}
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.proceedToDays(tc, context.Background(), 100, "07:45")

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleAwaitingTimeWithPlannerError(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	timeStr := currentTimeStr()

	planner := &mockPlanner{
		nearestErr: errTestBot,
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "SMY", "to", "CTK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, timeStr)

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days when planner errors (graceful fallback), got: %s", repo.state[100])
	}
}

func TestHandleRouteTrainCallbackOwnershipMismatch(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	otherUserID := pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.routes[uuidKey(otherUserID)] = []db.Route{{
		ID:     routeID,
		UserID: otherUserID,
	}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	routeIDStr := formatUUID(routeID)
	tc := newCallbackTC(routeIDStr + "|07:30")
	_ = b.handleRouteTrainCallback(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error for ownership mismatch, got: %s", tc.lastSent)
	}
}

func TestHandleConfirmDeleteEmptyRoutes(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleConfirmDelete(tc, context.Background(), 100, "1", userID)

	if !strings.Contains(tc.lastSent, "already deleted") {
		t.Errorf("expected already deleted, got: %s", tc.lastSent)
	}
}

func TestHandleConfirmDeleteInvalidChoice(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.routes[uuidKey(userID)] = []db.Route{{Label: "Only"}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleConfirmDelete(tc, context.Background(), 100, "5", userID)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error, got: %s", tc.lastSent)
	}
}

func TestIsWithinDarwinRange(t *testing.T) {
	now := timezone.Now()
	nowMins := now.Hour()*60 + now.Minute()

	tests := []struct {
		name       string
		targetMins int
		want       bool
	}{
		{"exact now", nowMins, true},
		{"1 hour ahead", nowMins + 60, true},
		{"2 hours ahead", nowMins + 120, true},
		{"3 hours ahead", nowMins + 180, true},
		{"4 hours ahead", nowMins + 240, true},
		{"5 hours ahead", nowMins + 300, false},
		{"1 hour behind", nowMins - 60, true},
		{"3 hours behind", nowMins - 180, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWithinDarwinRange(tt.targetMins); got != tt.want {
				t.Errorf("isWithinDarwinRange(%d) = %v, want %v (nowMins=%d)", tt.targetMins, got, tt.want, nowMins)
			}
		})
	}
}

func TestTimeUntilEndOfDay(t *testing.T) {
	ttl := timeUntilEndOfDay()
	if ttl <= 0 || ttl > 24*time.Hour {
		t.Errorf("unexpected TTL: %v", ttl)
	}
}

func TestHandleAwaitingTimeRTTExactMatch(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	planner := &mockPlanner{
		nearestErr: errTestBot,
		scheduledResult: &domain.NearestTrains{
			Exact: &domain.TrainStatus{
				ServiceID:          "S1",
				ScheduledDeparture: time.Date(2026, 4, 21, 7, 43, 0, 0, time.UTC),
				IsScheduleOnly:     true,
			},
		},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "WAT", "to", "WOK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, "23:45")

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days for RTT exact match, got: %s", repo.state[100])
	}
}

func TestHandleAwaitingTimeRTTBeforeAfter(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	planner := &mockPlanner{
		nearestErr: errTestBot,
		scheduledResult: &domain.NearestTrains{
			Before: &domain.TrainStatus{
				ServiceID:          "before",
				ScheduledDeparture: time.Date(2026, 4, 21, 23, 30, 0, 0, time.UTC),
				Destination:        "Woking",
				IsScheduleOnly:     true,
			},
			After: &domain.TrainStatus{
				ServiceID:          "after",
				ScheduledDeparture: time.Date(2026, 4, 21, 23, 55, 0, 0, time.UTC),
				Destination:        "Woking",
				IsScheduleOnly:     true,
			},
		},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "WAT", "to", "WOK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, "23:45")

	if repo.state[100] != "awaiting_train_choice" {
		t.Errorf("expected awaiting_train_choice for RTT options, got: %s", repo.state[100])
	}
	if !tc.anySentContains("No exact train") {
		t.Errorf("expected train options message, got: %v", tc.allSent)
	}
}

func TestHandleAwaitingTimeRTTEmpty(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	planner := &mockPlanner{
		nearestErr:      errTestBot,
		scheduledResult: &domain.NearestTrains{},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "WAT", "to", "WOK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, "23:45")

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days when RTT returns empty, got: %s", repo.state[100])
	}
}

func TestHandleAwaitingTimeRTTError(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	planner := &mockPlanner{
		nearestErr:   errTestBot,
		scheduledErr: errTestBot,
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	ctx := context.Background()
	b.rdb.HSet(ctx, draftKey(100), "from", "WAT", "to", "WOK")

	tc := newTC(100)
	_ = b.handleAwaitingTime(tc, ctx, 100, "23:45")

	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days when RTT errors, got: %s", repo.state[100])
	}
}

func TestPlanRouteNowRTTFallback(t *testing.T) {
	repo := newMockRepository()
	planner := &mockPlanner{
		planErr: errTestBot,
		planScheduleResult: &domain.TrainStatus{
			ServiceID:          "S1",
			ScheduledDeparture: time.Date(2026, 4, 21, 7, 43, 0, 0, time.UTC),
			Destination:        "Woking",
			IsScheduleOnly:     true,
		},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	tc := newTC(100)
	route := db.Route{
		ID:            pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		DepartureTime: pgtype.Time{Microseconds: 23 * 3600000000, Valid: true},
	}
	_ = b.planRouteNow(tc, context.Background(), route)

	if !strings.Contains(tc.lastSent, "Scheduled train") {
		t.Errorf("expected scheduled train message, got: %s", tc.lastSent)
	}
	if !strings.Contains(tc.lastSent, "07:43 to Woking") {
		t.Errorf("expected train details in message, got: %s", tc.lastSent)
	}
}

func TestHandleStatusWithTimetableData(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}

	now := time.Now()
	depHour := now.Hour() + 2
	if depHour >= 24 {
		depHour = now.Hour()
	}

	repo.routes[uuidKey(userID)] = []db.Route{{
		ID:             routeID,
		UserID:         userID,
		Label:          "Morning",
		FromStationCrs: "WAT",
		ToStationCrs:   "WOK",
		DepartureTime:  pgtype.Time{Microseconds: int64(depHour) * 3600000000, Valid: true},
		DaysOfWeek:     0b1111111,
		AlertOffsets:   []int32{30},
		IsActive:       true,
	}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	train := &domain.TrainStatus{
		ServiceID:          "S1",
		ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), depHour, 0, 0, 0, time.UTC),
		Destination:        "Woking",
		IsScheduleOnly:     true,
	}
	data, _ := json.Marshal(train)
	b.rdb.Set(context.Background(), "route_service:"+formatUUID(routeID), data, time.Hour)

	tc := newTC(100)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, "Scheduled:") {
		t.Errorf("expected 'Scheduled:' label, got: %s", tc.lastSent)
	}
	if strings.Contains(tc.lastSent, "Plt") {
		t.Errorf("should not show platform for timetable data, got: %s", tc.lastSent)
	}
}

func TestHandleStatusAwaitingData(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.routes[uuidKey(userID)] = []db.Route{{
		ID:             routeID,
		UserID:         userID,
		Label:          "Morning",
		FromStationCrs: "WAT",
		ToStationCrs:   "WOK",
		DepartureTime:  pgtype.Time{Microseconds: 7 * 3600000000, Valid: true},
		DaysOfWeek:     0b1111111,
		AlertOffsets:   []int32{30},
		IsActive:       true,
	}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	_ = b.handleStatus(tc)

	if !strings.Contains(tc.lastSent, "Awaiting train data") {
		t.Errorf("expected 'Awaiting train data', got: %s", tc.lastSent)
	}
}

func TestFormatScheduledTrainFound(t *testing.T) {
	status := &domain.TrainStatus{
		ServiceID:          "S1",
		ScheduledDeparture: time.Date(2026, 4, 21, 7, 43, 0, 0, time.UTC),
		Destination:        "Woking",
		IsScheduleOnly:     true,
	}

	result := formatScheduledTrainFound(status)
	if !strings.Contains(result, "07:43 to Woking") {
		t.Errorf("expected time and destination, got: %s", result)
	}
	if !strings.Contains(result, "Scheduled train") {
		t.Errorf("expected 'Scheduled train' label, got: %s", result)
	}
	if !strings.Contains(result, "live updates") {
		t.Errorf("expected live updates note, got: %s", result)
	}
}

func TestFormatScheduledTrainFoundNoDestination(t *testing.T) {
	status := &domain.TrainStatus{
		ScheduledDeparture: time.Date(2026, 4, 21, 7, 43, 0, 0, time.UTC),
		IsScheduleOnly:     true,
	}

	result := formatScheduledTrainFound(status)
	if !strings.Contains(result, "07:43") {
		t.Errorf("expected time, got: %s", result)
	}
	if strings.Contains(result, " to ") {
		t.Errorf("should not contain 'to' without destination, got: %s", result)
	}
}
