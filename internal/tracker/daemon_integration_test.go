package tracker

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

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
)

func setupRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return rdb, mr
}

func testRouteID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, Valid: true}
}

func TestCacheAndGetService(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	routeID := testRouteID()
	status := &domain.TrainStatus{
		ServiceID:          "svc123",
		ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
		Platform:           "3",
	}

	data, _ := json.Marshal(status)
	key := serviceCacheKey(routeID)
	rdb.Set(context.Background(), key, data, time.Hour)

	daemon := &Daemon{rdb: rdb}
	got, err := daemon.getCachedService(context.Background(), routeID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected cached service")
	}
	if got.ServiceID != "svc123" {
		t.Errorf("got service %q, want svc123", got.ServiceID)
	}
	if got.Platform != "3" {
		t.Errorf("got platform %q, want 3", got.Platform)
	}
}

func TestGetCachedServiceMiss(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	daemon := &Daemon{rdb: rdb}
	got, err := daemon.getCachedService(context.Background(), testRouteID())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for cache miss")
	}
}

func TestSaveAndGetLastState(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	routeID := testRouteID()
	status := &domain.TrainStatus{
		ServiceID: "svc123",
		Platform:  "5",
		DelayMins: 7,
	}

	daemon := &Daemon{rdb: rdb}
	daemon.saveLastState(context.Background(), routeID, status)

	got, err := daemon.getLastState(context.Background(), routeID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected last state")
	}
	if got.Platform != "5" {
		t.Errorf("platform = %q, want 5", got.Platform)
	}
	if got.DelayMins != 7 {
		t.Errorf("delay = %d, want 7", got.DelayMins)
	}
}

func TestGetLastStateMiss(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	daemon := &Daemon{rdb: rdb}
	got, err := daemon.getLastState(context.Background(), testRouteID())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for no last state")
	}
}

func TestClearRouteCache(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	rdb.Set(ctx, serviceCacheKey(routeID), "data", time.Hour)
	rdb.Set(ctx, lastStatePrefix+formatUUID(routeID), "state", time.Hour)

	daemon := &Daemon{rdb: rdb}
	daemon.clearRouteCache(ctx, routeID)

	if rdb.Exists(ctx, serviceCacheKey(routeID)).Val() != 0 {
		t.Error("expected service cache to be cleared")
	}
	if rdb.Exists(ctx, lastStatePrefix+formatUUID(routeID)).Val() != 0 {
		t.Error("expected last state to be cleared")
	}
}

func TestCheckRouteFirstPollSilent(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	cached := &domain.TrainStatus{ServiceID: "svc1", ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC), Platform: "3"}
	data, _ := json.Marshal(cached)
	rdb.Set(ctx, serviceCacheKey(routeID), data, time.Hour)

	fresh := &domain.TrainStatus{ServiceID: "svc1", ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC), Platform: "3"}
	notifier := &mockNotifier{}
	trainClient := &mockTrainClient{serviceDetails: fresh}
	cb := NewCircuitBreaker(trainClient, notifier, noChatIDs)

	daemon := &Daemon{rdb: rdb, trainClient: trainClient, notifier: notifier, circuitBreaker: cb}

	route := makeTestRouteRow(routeID)
	daemon.checkRoute(ctx, route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 0 {
		t.Fatalf("expected 0 notifications on first poll (silent save), got %d", len(notifier.sent))
	}

	last, err := daemon.getLastState(ctx, routeID)
	if err != nil {
		t.Fatal(err)
	}
	if last == nil {
		t.Fatal("expected last state to be saved on first poll")
	}
}

func TestCheckRouteNoChange(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	status := &domain.TrainStatus{
		ServiceID:          "svc1",
		ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
		EstimatedDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
		Platform:           "3",
	}
	data, _ := json.Marshal(status)
	rdb.Set(ctx, serviceCacheKey(routeID), data, time.Hour)
	rdb.Set(ctx, lastStatePrefix+formatUUID(routeID), data, time.Hour)

	notifier := &mockNotifier{}
	trainClient := &mockTrainClient{serviceDetails: status}
	cb := NewCircuitBreaker(trainClient, notifier, noChatIDs)

	daemon := &Daemon{rdb: rdb, trainClient: trainClient, notifier: notifier, circuitBreaker: cb}

	route := makeTestRouteRow(routeID)
	daemon.checkRoute(ctx, route, status)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 0 {
		t.Errorf("expected no notifications for unchanged status, got %d", len(notifier.sent))
	}
}

func TestCheckRouteStatusChange(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	old := &domain.TrainStatus{
		ServiceID: "svc1", Platform: "3",
		ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
		EstimatedDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
	}
	oldData, _ := json.Marshal(old)
	rdb.Set(ctx, serviceCacheKey(routeID), oldData, time.Hour)
	rdb.Set(ctx, lastStatePrefix+formatUUID(routeID), oldData, time.Hour)

	fresh := &domain.TrainStatus{
		ServiceID: "svc1", Platform: "7",
		ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
		EstimatedDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
	}
	notifier := &mockNotifier{}
	trainClient := &mockTrainClient{serviceDetails: fresh}
	cb := NewCircuitBreaker(trainClient, notifier, noChatIDs)

	daemon := &Daemon{rdb: rdb, trainClient: trainClient, notifier: notifier, circuitBreaker: cb}

	route := makeTestRouteRow(routeID)
	daemon.checkRoute(ctx, route, old)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 notification for status change, got %d", len(notifier.sent))
	}
}

func TestCheckRouteAPIFailure(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	cached := &domain.TrainStatus{ServiceID: "svc1"}
	data, _ := json.Marshal(cached)
	rdb.Set(ctx, serviceCacheKey(routeID), data, time.Hour)

	notifier := &mockNotifier{}
	trainClient := &mockTrainClient{err: fmt.Errorf("api down")}
	cb := NewCircuitBreaker(trainClient, notifier, noChatIDs)

	daemon := &Daemon{rdb: rdb, trainClient: trainClient, notifier: notifier, circuitBreaker: cb}

	route := makeTestRouteRow(routeID)
	daemon.checkRoute(ctx, route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 0 {
		t.Error("expected no notification on API failure")
	}
}

type mockDaemonRepo struct {
	routes []db.GetActiveRoutesWithChatIDRow
	err    error
}

func (m *mockDaemonRepo) GetActiveRoutesWithChatID(_ context.Context, _ int32) ([]db.GetActiveRoutesWithChatIDRow, error) {
	return m.routes, m.err
}

func TestTickSkipsWhenAPIDown(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	notifier := &mockNotifier{}
	trainClient := &mockTrainClient{err: fmt.Errorf("down")}
	cb := NewCircuitBreaker(trainClient, notifier, noChatIDs)

	cb.mu.Lock()
	cb.state = APIDown
	cb.mu.Unlock()

	daemon := NewDaemon(&mockDaemonRepo{}, trainClient, notifier, rdb, cb, nil, 0)
	daemon.tick(context.Background())
}

func TestTickProcessesRoutes(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	routeID := testRouteID()
	now := time.Now()
	depHour := now.Hour()
	depMin := now.Minute()

	cached := &domain.TrainStatus{
		ServiceID:          "svc1",
		ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), depHour, depMin, 0, 0, time.UTC),
		Platform:           "3",
	}
	data, _ := json.Marshal(cached)
	rdb.Set(context.Background(), serviceCacheKey(routeID), data, time.Hour)

	routes := []db.GetActiveRoutesWithChatIDRow{{
		ID:             routeID,
		Label:          "Test",
		FromStationCrs: "SMH",
		ToStationCrs:   "CTK",
		TelegramChatID: 100,
		DepartureTime:  pgtype.Time{Microseconds: int64(depHour)*3600000000 + int64(depMin)*60000000, Valid: true},
		AlertOffsets:   []int32{60},
		IsActive:       true,
	}}

	notifier := &mockNotifier{}
	trainClient := &mockTrainClient{serviceDetails: cached}
	cb := NewCircuitBreaker(trainClient, notifier, noChatIDs)

	daemon := NewDaemon(&mockDaemonRepo{routes: routes}, trainClient, notifier, rdb, cb, nil, 0)
	daemon.tick(context.Background())
}

type mockPlannerRepo struct {
	routes []db.Route
	err    error
}

func (m *mockPlannerRepo) GetActiveRoutesForWeekday(_ context.Context, _ int32) ([]db.Route, error) {
	return m.routes, m.err
}

func makeTestRouteRow(routeID pgtype.UUID) db.GetActiveRoutesWithChatIDRow {
	return db.GetActiveRoutesWithChatIDRow{
		ID:             routeID,
		Label:          "Morning",
		FromStationCrs: "SMH",
		ToStationCrs:   "CTK",
		TelegramChatID: 100,
		DepartureTime:  pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
		AlertOffsets:   []int32{60},
		IsActive:       true,
	}
}

func TestTickDBError(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	notifier := &mockNotifier{}
	trainClient := &mockTrainClient{}
	cb := NewCircuitBreaker(trainClient, notifier, noChatIDs)

	daemon := NewDaemon(&mockDaemonRepo{err: fmt.Errorf("db error")}, trainClient, notifier, rdb, cb, nil, 0)
	daemon.tick(context.Background())
}

func TestTickNoRoutes(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	notifier := &mockNotifier{}
	trainClient := &mockTrainClient{}
	cb := NewCircuitBreaker(trainClient, notifier, noChatIDs)

	daemon := NewDaemon(&mockDaemonRepo{routes: []db.GetActiveRoutesWithChatIDRow{}}, trainClient, notifier, rdb, cb, nil, 0)
	daemon.tick(context.Background())
}

func TestTickDepartedRouteClearsCache(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	uid := formatUUID(routeID)

	rdb.Set(ctx, serviceCacheKey(routeID), "data", time.Hour)
	rdb.Set(ctx, lastStatePrefix+uid, "state", time.Hour)

	now := time.Now()
	pastHour := now.Hour() - 1
	if pastHour < 0 {
		pastHour = 0
	}

	routes := []db.GetActiveRoutesWithChatIDRow{{
		ID:             routeID,
		Label:          "Test",
		FromStationCrs: "SMH",
		ToStationCrs:   "CTK",
		TelegramChatID: 100,
		DepartureTime:  pgtype.Time{Microseconds: int64(pastHour) * 3600000000, Valid: true},
		AlertOffsets:   []int32{30},
		IsActive:       true,
	}}

	notifier := &mockNotifier{}
	trainClient := &mockTrainClient{}
	cb := NewCircuitBreaker(trainClient, notifier, noChatIDs)

	daemon := NewDaemon(&mockDaemonRepo{routes: routes}, trainClient, notifier, rdb, cb, nil, 0)
	daemon.tick(ctx)

	if rdb.Exists(ctx, serviceCacheKey(routeID)).Val() != 0 {
		t.Error("expected departed route cache to be cleared")
	}
}

func TestTryPlanRoute(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "svc1", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 45, 0, 0, time.UTC), Platform: "3"},
	}

	boardClient := &mockBoardClient{services: services}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	daemon.tryPlanRoute(context.Background(), route)

	cached, err := daemon.getCachedService(context.Background(), route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil || cached.ServiceID != "svc1" {
		t.Errorf("expected cached service svc1, got %v", cached)
	}
}

func TestTryPlanRouteNoMatch(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	boardClient := &mockBoardClient{services: []domain.TrainStatus{}}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	daemon.tryPlanRoute(context.Background(), route)

	cached, _ := daemon.getCachedService(context.Background(), route.ID)
	if cached != nil {
		t.Error("expected no cached service when plan fails")
	}
}

func TestTrySendTrainChoiceBothOptions(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "before", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 30, 0, 0, time.UTC), Destination: "London"},
		{ServiceID: "after", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, time.UTC), Destination: "London"},
	}

	choiceSent := false
	notifier := &trackingNotifier{
		mockNotifier:    &mockNotifier{},
		trainChoiceSent: &choiceSent,
	}
	boardClient := &mockBoardClient{services: services}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	daemon.trySendTrainChoice(context.Background(), route)

	if !choiceSent {
		t.Error("expected SendTrainChoice to be called")
	}

	if !daemon.choiceSent(context.Background(), route.ID) {
		t.Error("expected choice_sent flag to be set")
	}
}

func TestTrySendTrainChoiceSingleAutoSelects(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "only", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 30, 0, 0, time.UTC), Destination: "London"},
	}

	notifier := &mockNotifier{}
	boardClient := &mockBoardClient{services: services}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	daemon.trySendTrainChoice(context.Background(), route)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 auto-select message, got %d", len(notifier.sent))
	}

	cached, _ := daemon.getCachedService(context.Background(), route.ID)
	if cached == nil || cached.ServiceID != "only" {
		t.Error("expected auto-selected service to be cached")
	}
}

func TestTrySendTrainChoiceDistantSingleSendsChoice(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "far", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 0, 0, 0, time.UTC), Destination: "London"},
	}

	notifier := &mockNotifier{}
	boardClient := &mockBoardClient{services: services}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	daemon.trySendTrainChoice(context.Background(), route)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()

	if notifier.choiceSent == nil {
		t.Fatal("expected train choice to be sent for distant single option")
	}
	if notifier.choiceSent.before == nil {
		t.Error("expected before option in train choice")
	}
	if notifier.choiceSent.after != nil {
		t.Error("expected no after option in train choice")
	}
	if notifier.choiceSent.requestedTime != "07:45" {
		t.Errorf("expected requested time 07:45, got %s", notifier.choiceSent.requestedTime)
	}
	if len(notifier.sent) != 0 {
		t.Errorf("expected no auto-select messages, got %d", len(notifier.sent))
	}

	cached, _ := daemon.getCachedService(context.Background(), route.ID)
	if cached != nil {
		t.Error("expected no cached service for distant auto-select")
	}
}

func TestAutoSelectTrain(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	notifier := &mockNotifier{}
	boardClient := &mockBoardClient{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	train := &domain.TrainStatus{
		ServiceID:          "svc1",
		ScheduledDeparture: makeTime(7, 30),
		Destination:        "London",
		Platform:           "3",
	}

	daemon.autoSelectTrain(context.Background(), route, train)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(notifier.sent))
	}

	cached, _ := daemon.getCachedService(context.Background(), route.ID)
	if cached == nil || cached.ServiceID != "svc1" {
		t.Error("expected service to be cached")
	}
}

func TestFormatAutoSelectStatus(t *testing.T) {
	tests := []struct {
		name   string
		status *domain.TrainStatus
		want   string
	}{
		{
			"on time",
			&domain.TrainStatus{ScheduledDeparture: makeTime(7, 30), Destination: "London", Platform: "3"},
			"🟢",
		},
		{
			"delayed",
			&domain.TrainStatus{ScheduledDeparture: makeTime(7, 30), Destination: "London", DelayMins: 5},
			"🟠",
		},
		{
			"cancelled",
			&domain.TrainStatus{ScheduledDeparture: makeTime(7, 30), Destination: "London", IsCancelled: true},
			"🔴",
		},
		{
			"no platform",
			&domain.TrainStatus{ScheduledDeparture: makeTime(7, 30), Destination: "London"},
			"TBC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatAutoSelectStatus(tt.status)
			if !strings.Contains(got, tt.want) {
				t.Errorf("formatAutoSelectStatus() = %q, want to contain %q", got, tt.want)
			}
		})
	}
}

func TestChoiceSentFlag(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	daemon := &Daemon{rdb: rdb}

	if daemon.choiceSent(ctx, routeID) {
		t.Error("expected false before marking")
	}

	daemon.markChoiceSent(ctx, routeID)

	if !daemon.choiceSent(ctx, routeID) {
		t.Error("expected true after marking")
	}
}

func TestAlertSentFlag(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	daemon := &Daemon{rdb: rdb}

	if daemon.alertSent(ctx, routeID, 30) {
		t.Error("expected false before marking")
	}

	daemon.markAlertSent(ctx, routeID, 30)

	if !daemon.alertSent(ctx, routeID, 30) {
		t.Error("expected true after marking")
	}

	if daemon.alertSent(ctx, routeID, 60) {
		t.Error("expected false for different offset")
	}
}

func TestSendDepartureReminder(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	notifier := &mockNotifier{}
	daemon := &Daemon{rdb: rdb, notifier: notifier}

	route := makeTestRouteRow(routeID)
	route.FromStationCrs = "SMY"
	route.ToStationCrs = "BFR"

	cached := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 45),
		EstimatedDeparture: makeTime(7, 45),
		Platform:           "3",
		Destination:        "London Blackfriars",
	}

	daemon.sendDepartureReminder(ctx, route, cached, 30)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 reminder, got %d", len(notifier.sent))
	}
	if !strings.Contains(notifier.sent[0].message, "Departure in") {
		t.Errorf("expected departure reminder, got: %s", notifier.sent[0].message)
	}
}

type trackingNotifier struct {
	*mockNotifier
	trainChoiceSent *bool
}

func (n *trackingNotifier) SendTrainChoice(_ context.Context, _ int64, _ string, _ string, _ *domain.TrainOption, _ *domain.TrainOption) error {
	*n.trainChoiceSent = true
	return nil
}

func (n *trackingNotifier) Send(ctx context.Context, chatID int64, message string) error {
	return n.mockNotifier.Send(ctx, chatID, message)
}

func (n *trackingNotifier) Broadcast(ctx context.Context, chatIDs []int64, message string) error {
	return n.mockNotifier.Broadcast(ctx, chatIDs, message)
}

func (n *trackingNotifier) BroadcastSilent(ctx context.Context, chatIDs []int64, message string) error {
	return n.mockNotifier.BroadcastSilent(ctx, chatIDs, message)
}

func (n *trackingNotifier) SendBetterTrainOffer(ctx context.Context, chatID int64, routeID, currentTime, betterTime, betterDest string) error {
	return n.mockNotifier.SendBetterTrainOffer(ctx, chatID, routeID, currentTime, betterTime, betterDest)
}

func TestGracePeriod_FirstMissRecorded(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	daemon := &Daemon{rdb: rdb}

	if daemon.gracePeriodExpired(ctx, routeID) {
		t.Error("expected grace period NOT expired on first miss")
	}

	key := firstMissPrefix + formatUUID(routeID)
	if rdb.Exists(ctx, key).Val() == 0 {
		t.Error("expected first_miss key to be set after first call")
	}
}

func TestGracePeriod_NotExpiredWithin30Min(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	daemon := &Daemon{rdb: rdb}

	key := firstMissPrefix + formatUUID(routeID)
	rdb.Set(ctx, key, ukNow().Add(-15*time.Minute).Format(time.RFC3339), time.Hour)

	if daemon.gracePeriodExpired(ctx, routeID) {
		t.Error("expected grace period NOT expired after only 15 minutes")
	}
}

func TestGracePeriod_ExpiredAfter30Min(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	daemon := &Daemon{rdb: rdb}

	key := firstMissPrefix + formatUUID(routeID)
	rdb.Set(ctx, key, ukNow().Add(-31*time.Minute).Format(time.RFC3339), time.Hour)

	if !daemon.gracePeriodExpired(ctx, routeID) {
		t.Error("expected grace period to be expired after 31 minutes")
	}
}

func TestGracePeriod_ClearedOnSuccess(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	daemon := &Daemon{rdb: rdb}

	key := firstMissPrefix + formatUUID(routeID)
	rdb.Set(ctx, key, ukNow().Add(-10*time.Minute).Format(time.RFC3339), time.Hour)

	daemon.clearFirstMiss(ctx, routeID)

	if rdb.Exists(ctx, key).Val() != 0 {
		t.Error("expected first_miss key to be cleared")
	}
}

func TestTryPlanRoute_WaitsGracePeriod(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	boardClient := &mockBoardClient{services: []domain.TrainStatus{}}
	choiceSent := false
	notifier := &trackingNotifier{
		mockNotifier:    &mockNotifier{},
		trainChoiceSent: &choiceSent,
	}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}
	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.tryPlanRoute(ctx, route)

	if choiceSent {
		t.Error("expected no train choice sent during grace period")
	}
}

func TestTryPlanRoute_SendsChoiceAfterGracePeriod(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	key := firstMissPrefix + formatUUID(routeID)
	rdb.Set(ctx, key, ukNow().Add(-31*time.Minute).Format(time.RFC3339), time.Hour)

	now := ukNow().Add(60 * time.Minute)
	depMicros := int64(now.Hour())*3600000000 + int64(now.Minute())*60000000
	services := []domain.TrainStatus{
		{ServiceID: "only", ScheduledDeparture: now.Add(-15 * time.Minute), Destination: "London"},
	}

	boardClient := &mockBoardClient{services: services}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}
	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	route.DepartureTime = pgtype.Time{Microseconds: depMicros, Valid: true}
	daemon.tryPlanRoute(ctx, route)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) == 0 {
		t.Error("expected auto-select notification after grace period expired")
	}
}

func TestAutoSwitchTrain(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	train := &domain.TrainStatus{
		ServiceID:          "svc-target",
		ScheduledDeparture: makeTime(7, 45),
		Platform:           "3",
		Destination:        "London",
	}

	notifier := &mockNotifier{}
	boardClient := &mockBoardClient{}
	planner := &Planner{trainClient: boardClient, rdb: rdb}
	daemon := &Daemon{rdb: rdb, notifier: notifier, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.autoSwitchTrain(ctx, route, train)

	cached, err := daemon.getCachedService(ctx, routeID)
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil || cached.ServiceID != "svc-target" {
		t.Error("expected cached service to be updated")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifier.sent))
	}
	if !strings.Contains(notifier.sent[0].message, "Switched to") {
		t.Errorf("expected switch message, got: %s", notifier.sent[0].message)
	}
}

func TestTryFetchTimetable(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	schedServices := []domain.TrainStatus{
		{ServiceID: "S1", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 45, 0, 0, time.UTC), IsScheduleOnly: true},
	}

	boardClient := &mockBoardClient{services: nil}
	schedClient := &mockScheduleClient{services: schedServices}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, scheduleClient: schedClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	daemon.tryFetchTimetable(context.Background(), route)

	cached, err := daemon.getCachedService(context.Background(), route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil {
		t.Fatal("expected cached timetable service")
	}
	if cached.ServiceID != "S1" {
		t.Errorf("got %q, want S1", cached.ServiceID)
	}
	if !cached.IsScheduleOnly {
		t.Error("expected IsScheduleOnly on cached timetable")
	}
}

func TestTryFetchTimetableFailure(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	boardClient := &mockBoardClient{services: nil}
	schedClient := &mockScheduleClient{err: fmt.Errorf("rtt down")}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, scheduleClient: schedClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	daemon.tryFetchTimetable(context.Background(), route)

	cached, _ := daemon.getCachedService(context.Background(), route.ID)
	if cached != nil {
		t.Error("expected no cache on RTT failure")
	}
}

func TestTryTransitionToLive(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	liveServices := []domain.TrainStatus{
		{ServiceID: "L1", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 45, 0, 0, time.UTC), Platform: "3"},
	}

	boardClient := &mockBoardClient{services: liveServices}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	daemon.tryTransitionToLive(context.Background(), route)

	cached, err := daemon.getCachedService(context.Background(), route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil {
		t.Fatal("expected cached live service")
	}
	if cached.ServiceID != "L1" {
		t.Errorf("got %q, want L1", cached.ServiceID)
	}
	if cached.IsScheduleOnly {
		t.Error("expected IsScheduleOnly=false after transition to live")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 transition notification, got %d", len(notifier.sent))
	}
	if !strings.Contains(notifier.sent[0].message, "Live data now available") {
		t.Errorf("expected transition notification, got: %s", notifier.sent[0].message)
	}
}

func TestTryTransitionToLiveFailure(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	timetable := &domain.TrainStatus{ServiceID: "S1", ScheduledDeparture: makeTime(7, 45), IsScheduleOnly: true}

	boardClient := &mockBoardClient{services: []domain.TrainStatus{}}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(testRouteID())
	_ = planner.CacheService(context.Background(), route.ID, timetable)

	daemon.tryTransitionToLive(context.Background(), route)

	cached, _ := daemon.getCachedService(context.Background(), route.ID)
	if cached == nil {
		t.Fatal("expected timetable to remain cached")
	}
	if !cached.IsScheduleOnly {
		t.Error("expected IsScheduleOnly to remain true after failed transition")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 0 {
		t.Errorf("expected no notification on failed transition, got %d", len(notifier.sent))
	}
}

func TestDaemonNoAlertsForTimetable(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	depHour := now.Hour()
	depMin := now.Minute() + 30
	if depMin >= 60 {
		depHour++
		depMin -= 60
	}

	timetable := &domain.TrainStatus{
		ServiceID:          "S1",
		ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), depHour, depMin, 0, 0, now.Location()),
		IsScheduleOnly:     true,
	}

	routeID := testRouteID()
	route := db.GetActiveRoutesWithChatIDRow{
		ID:             routeID,
		Label:          "Test",
		FromStationCrs: "SMH",
		ToStationCrs:   "CTK",
		TelegramChatID: 100,
		DepartureTime:  pgtype.Time{Microseconds: int64(depHour)*3600000000 + int64(depMin)*60000000, Valid: true},
		AlertOffsets:   []int32{60},
		IsActive:       true,
	}

	boardClient := &mockBoardClient{}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{
		rdb:            rdb,
		trainClient:    boardClient,
		notifier:       notifier,
		circuitBreaker: cb,
		planner:        planner,
		queries:        &mockDaemonRepo{routes: []db.GetActiveRoutesWithChatIDRow{route}},
		tickInterval:   time.Minute,
	}

	_ = planner.CacheService(context.Background(), routeID, timetable)

	daemon.tick(context.Background())

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.sent) != 0 {
		t.Errorf("expected no alerts for timetable-only data, got %d: %v", len(notifier.sent), notifier.sent)
	}
}

func TestFormatTransitionNotification(t *testing.T) {
	route := makeTestRouteRow(testRouteID())
	status := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 43),
		EstimatedDeparture: makeTime(7, 43),
		Destination:        "London",
		Platform:           "3",
	}

	msg := formatTransitionNotification(route, status)
	if !strings.Contains(msg, "Live data now available") {
		t.Errorf("expected 'Live data now available', got: %s", msg)
	}
	if !strings.Contains(msg, "Morning") {
		t.Errorf("expected route label 'Morning', got: %s", msg)
	}
	if !strings.Contains(msg, "On time") {
		t.Errorf("expected status text, got: %s", msg)
	}
}
