package bot

import (
	"context"
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
)

var errTestBot = fmt.Errorf("test error")

type callbackContext struct {
	testContext
	callbackVal *telebot.Callback
	responded   bool
}

func (cc *callbackContext) Callback() *telebot.Callback { return cc.callbackVal }
func (cc *callbackContext) Respond(_ ...*telebot.CallbackResponse) error {
	cc.responded = true
	return nil
}

func newCallbackTC(data string) *callbackContext {
	return &callbackContext{
		testContext: testContext{chatVal: &telebot.Chat{ID: 100}},
		callbackVal: &telebot.Callback{Data: data},
	}
}

type mockPlanner struct {
	planResult         *domain.TrainStatus
	planErr            error
	planScheduleResult *domain.TrainStatus
	planScheduleErr    error
	nearestResult      *domain.NearestTrains
	nearestErr         error
	scheduledResult    *domain.NearestTrains
	scheduledErr       error
	cacheErr           error
}

func (m *mockPlanner) PlanRoute(_ context.Context, _ db.Route) (*domain.TrainStatus, error) {
	return m.planResult, m.planErr
}

func (m *mockPlanner) PlanRouteFromSchedule(_ context.Context, _ db.Route, _ time.Time) (*domain.TrainStatus, error) {
	return m.planScheduleResult, m.planScheduleErr
}

func (m *mockPlanner) FindNearestTrains(_ context.Context, _, _ string, _ int) (*domain.NearestTrains, error) {
	return m.nearestResult, m.nearestErr
}

func (m *mockPlanner) FindScheduledTrains(_ context.Context, _, _ string, _ time.Time, _ int) (*domain.NearestTrains, error) {
	return m.scheduledResult, m.scheduledErr
}

func (m *mockPlanner) CacheService(_ context.Context, _ pgtype.UUID, _ *domain.TrainStatus) error {
	return m.cacheErr
}

func newTestBotWithPlanner(t *testing.T, repo Repository, planner RoutePlanner) (*Bot, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return &Bot{queries: repo, rdb: rdb, planner: planner}, mr
}

func TestHandleDeleteCallback(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "awaiting_delete"
	repo.routes[uuidKey(userID)] = []db.Route{
		{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, Label: "Morning"},
		{ID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}, Label: "Evening"},
	}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newCallbackTC("2")
	_ = b.handleDeleteCallback(tc)

	if !tc.responded {
		t.Error("expected Respond() to be called")
	}
	if !strings.Contains(tc.lastSent, "Are you sure") {
		t.Errorf("expected confirmation, got: %s", tc.lastSent)
	}
}

func TestHandleConfirmDeleteCallback(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "awaiting_delete_confirm"
	repo.routes[uuidKey(userID)] = []db.Route{{ID: routeID, Label: "Morning"}}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newCallbackTC("1")
	_ = b.handleConfirmDeleteCallback(tc)

	if !tc.responded {
		t.Error("expected Respond() to be called")
	}
	if !strings.Contains(tc.lastSent, "Deleted route: Morning") {
		t.Errorf("expected deleted, got: %s", tc.lastSent)
	}
}

func TestHandleCancelDeleteCallback(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "awaiting_delete_confirm"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newCallbackTC("")
	_ = b.handleCancelDeleteCallback(tc)

	if !tc.responded {
		t.Error("expected Respond() to be called")
	}
	if !strings.Contains(tc.lastSent, "Nothing was deleted") {
		t.Errorf("expected cancel message, got: %s", tc.lastSent)
	}
}

func TestHandleTrainCallback(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "awaiting_train_choice"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	b.rdb.HSet(context.Background(), draftKey(100), "from", "SMY", "to", "CTK", "train_option_1", "07:30", "train_option_2", "08:00")

	tc := newCallbackTC("1")
	_ = b.handleTrainCallback(tc)

	if !tc.responded {
		t.Error("expected Respond() to be called")
	}
	if repo.state[100] != "awaiting_days" {
		t.Errorf("expected awaiting_days, got: %s", repo.state[100])
	}
}

func TestHandleRouteTrainCallback(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.state[100] = "ready"
	repo.routes[uuidKey(userID)] = []db.Route{{
		ID:     routeID,
		UserID: userID,
		Label:  "Morning",
		DepartureTime: pgtype.Time{
			Microseconds: 7*3600000000 + 45*60000000,
			Valid:        true,
		},
	}}

	routeIDStr := formatUUID(routeID)
	planner := &mockPlanner{
		planResult: &domain.TrainStatus{
			ServiceID:          "svc1",
			ScheduledDeparture: time.Date(2026, 1, 1, 7, 30, 0, 0, time.UTC),
			Platform:           "3",
		},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	tc := newCallbackTC(routeIDStr + "|07:30")
	_ = b.handleRouteTrainCallback(tc)

	if !tc.responded {
		t.Error("expected Respond() to be called")
	}
	if !strings.Contains(tc.lastSent, "Tracking") {
		t.Errorf("expected tracking message, got: %s", tc.lastSent)
	}
}

func TestHandleRouteTrainCallbackInvalidData(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}
	repo.state[100] = "ready"

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newCallbackTC("invalid-no-pipe")
	_ = b.handleRouteTrainCallback(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleBetterKeepCallback(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	routeIDStr := formatUUID(routeID)

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newCallbackTC(routeIDStr)
	_ = b.handleBetterKeepCallback(tc)

	if !tc.responded {
		t.Error("expected Respond() to be called")
	}
	if !strings.Contains(tc.lastSent, "Keeping current train") {
		t.Errorf("expected keep message, got: %s", tc.lastSent)
	}

	uid := formatUUID(routeID)
	exists := b.rdb.Exists(context.Background(), "better_declined:"+uid).Val()
	if exists != 1 {
		t.Error("expected better_declined flag to be set")
	}
}

func TestHandleBetterSwitchCallback(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.routes[uuidKey(userID)] = []db.Route{{
		ID:     routeID,
		UserID: userID,
		Label:  "Morning",
		DepartureTime: pgtype.Time{
			Microseconds: 7*3600000000 + 45*60000000,
			Valid:        true,
		},
	}}

	routeIDStr := formatUUID(routeID)
	planner := &mockPlanner{
		planResult: &domain.TrainStatus{
			ServiceID:          "svc-better",
			ScheduledDeparture: time.Date(2026, 1, 1, 7, 44, 0, 0, time.UTC),
			Platform:           "2",
		},
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	b.rdb.Set(context.Background(), "better_offered:"+routeIDStr, "1", time.Hour)

	tc := newCallbackTC(routeIDStr + "|07:44")
	_ = b.handleBetterSwitchCallback(tc)

	if !tc.responded {
		t.Error("expected Respond() to be called")
	}
	if !strings.Contains(tc.lastSent, "Switched to") {
		t.Errorf("expected switch message, got: %s", tc.lastSent)
	}
}

func TestHandleBetterSwitchCallbackNoLongerAvailable(t *testing.T) {
	repo := newMockRepository()
	userID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	routeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	repo.users[100] = db.User{ID: userID, TelegramChatID: 100}
	repo.routes[uuidKey(userID)] = []db.Route{{
		ID:     routeID,
		UserID: userID,
		DepartureTime: pgtype.Time{
			Microseconds: 7*3600000000 + 45*60000000,
			Valid:        true,
		},
	}}

	routeIDStr := formatUUID(routeID)
	planner := &mockPlanner{
		planErr: errTestBot,
	}

	b, mr := newTestBotWithPlanner(t, repo, planner)
	defer mr.Close()

	tc := newCallbackTC(routeIDStr + "|07:44")
	_ = b.handleBetterSwitchCallback(tc)

	if !strings.Contains(tc.lastSent, "no longer available") {
		t.Errorf("expected no longer available message, got: %s", tc.lastSent)
	}
}

func TestHandleBetterSwitchCallbackInvalidData(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newCallbackTC("no-pipe-separator")
	_ = b.handleBetterSwitchCallback(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}

func TestHandleBetterKeepCallbackInvalidUUID(t *testing.T) {
	repo := newMockRepository()
	repo.users[100] = db.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, TelegramChatID: 100}

	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newCallbackTC("not-a-uuid")
	_ = b.handleBetterKeepCallback(tc)

	if !strings.Contains(tc.lastSent, "Something went wrong") {
		t.Errorf("expected error message, got: %s", tc.lastSent)
	}
}
