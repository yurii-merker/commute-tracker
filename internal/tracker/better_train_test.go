package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/yurii-merker/commute-tracker/internal/domain"
)

func TestCheckBetterTrain_CachedWithinTolerance(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	routeID := testRouteID()
	cached := &domain.TrainStatus{
		ServiceID:          "svc1",
		ScheduledDeparture: makeTime(7, 45),
	}

	boardClient := &mockBoardClient{}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.checkBetterTrain(context.Background(), route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.betterOffers) != 0 {
		t.Error("expected no better train offer when cached is within tolerance")
	}
}

func TestCheckBetterTrain_AlreadyDeclined(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	cached := &domain.TrainStatus{
		ServiceID:          "svc-far",
		ScheduledDeparture: makeTime(8, 30),
	}

	rdb.Set(ctx, betterDeclinedPrefix+formatUUID(routeID), "1", time.Hour)

	boardClient := &mockBoardClient{}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.checkBetterTrain(ctx, route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.betterOffers) != 0 {
		t.Error("expected no offer when already declined")
	}
}

func TestCheckBetterTrain_AlreadyOffered(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	cached := &domain.TrainStatus{
		ServiceID:          "svc-far",
		ScheduledDeparture: makeTime(8, 30),
	}

	rdb.Set(ctx, betterOfferedPrefix+formatUUID(routeID), "1", time.Hour)

	boardClient := &mockBoardClient{}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.checkBetterTrain(ctx, route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.betterOffers) != 0 {
		t.Error("expected no offer when already offered today")
	}
}

func TestCheckBetterTrain_AutoSwitchWithinTolerance(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	cached := &domain.TrainStatus{
		ServiceID:          "svc-far",
		ScheduledDeparture: makeTime(8, 30),
		Destination:        "London",
	}

	now := time.Now()
	betterService := domain.TrainStatus{
		ServiceID:          "svc-better",
		ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 44, 0, 0, time.UTC),
		Destination:        "London",
	}

	boardClient := &mockBoardClient{services: []domain.TrainStatus{betterService}}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.checkBetterTrain(ctx, route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.betterOffers) != 0 {
		t.Error("expected no offer — should auto-switch when within tolerance")
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 auto-switch notification, got %d", len(notifier.sent))
	}

	cachedAfter, err := (&Daemon{rdb: rdb}).getCachedService(ctx, routeID)
	if err != nil {
		t.Fatal(err)
	}
	if cachedAfter == nil || cachedAfter.ServiceID != "svc-better" {
		t.Error("expected cached service to be updated to svc-better")
	}
}

func TestCheckBetterTrain_NoBetterAvailable(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	cached := &domain.TrainStatus{
		ServiceID:          "svc-far",
		ScheduledDeparture: makeTime(8, 30),
	}

	now := time.Now()
	farService := domain.TrainStatus{
		ServiceID:          "svc-also-far",
		ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.UTC),
	}

	boardClient := &mockBoardClient{services: []domain.TrainStatus{farService}}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.checkBetterTrain(ctx, route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.betterOffers) != 0 {
		t.Error("expected no offer when no better train available")
	}

	if daemon.betterOffered(ctx, routeID) {
		t.Error("expected better_offered flag NOT to be set")
	}
}

func TestCheckBetterTrain_SameServiceReturned(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	cached := &domain.TrainStatus{
		ServiceID:          "svc-far",
		ScheduledDeparture: makeTime(8, 30),
	}

	now := time.Now()
	sameService := domain.TrainStatus{
		ServiceID:          "svc-far",
		ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 45, 0, 0, time.UTC),
	}

	boardClient := &mockBoardClient{services: []domain.TrainStatus{sameService}}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.checkBetterTrain(ctx, route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.betterOffers) != 0 {
		t.Error("expected no offer when same service is returned as closest")
	}
}

func TestCheckBetterTrain_APIFailure(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	cached := &domain.TrainStatus{
		ServiceID:          "svc-far",
		ScheduledDeparture: makeTime(8, 30),
	}

	boardClient := &mockBoardClient{err: fmt.Errorf("api down")}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.checkBetterTrain(ctx, route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.betterOffers) != 0 {
		t.Error("expected no offer on API failure")
	}

	if daemon.betterOffered(ctx, routeID) {
		t.Error("expected better_offered NOT to be set on API failure")
	}
}

func TestCheckBetterTrain_DeclinedExpiresNextDay(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	uid := formatUUID(routeID)
	rdb.Set(ctx, betterDeclinedPrefix+uid, "1", time.Hour)

	if !(&Daemon{rdb: rdb}).betterDeclined(ctx, routeID) {
		t.Fatal("expected betterDeclined to be true")
	}

	rdb.Del(ctx, betterDeclinedPrefix+uid)

	if (&Daemon{rdb: rdb}).betterDeclined(ctx, routeID) {
		t.Error("expected betterDeclined to be false after key deletion (simulating next day)")
	}
}

func TestClearRouteCache_ClearsBetterKeys(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	uid := formatUUID(routeID)

	rdb.Set(ctx, serviceCacheKey(routeID), "data", time.Hour)
	rdb.Set(ctx, lastStatePrefix+uid, "state", time.Hour)
	rdb.Set(ctx, betterDeclinedPrefix+uid, "1", time.Hour)
	rdb.Set(ctx, betterOfferedPrefix+uid, "1", time.Hour)

	daemon := &Daemon{rdb: rdb}
	daemon.clearRouteCache(ctx, routeID)

	if rdb.Exists(ctx, betterDeclinedPrefix+uid).Val() != 0 {
		t.Error("expected better_declined to be cleared")
	}
	if rdb.Exists(ctx, betterOfferedPrefix+uid).Val() != 0 {
		t.Error("expected better_offered to be cleared")
	}
}

func TestFindBetterTrain_Found(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "svc-better", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 44, 0, 0, time.UTC)},
		{ServiceID: "svc-far", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.UTC)},
	}

	boardClient := &mockBoardClient{services: services}
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	targetMins := 7*60 + 45
	result, err := planner.FindBetterTrain(context.Background(), "SMH", "CTK", targetMins, "svc-current")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected a better train to be found")
	}
	if result.ServiceID != "svc-better" {
		t.Errorf("got %q, want svc-better", result.ServiceID)
	}
}

func TestFindBetterTrain_SameService(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "svc-current", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 45, 0, 0, time.UTC)},
	}

	boardClient := &mockBoardClient{services: services}
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	result, err := planner.FindBetterTrain(context.Background(), "SMH", "CTK", 7*60+45, "svc-current")
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Errorf("expected nil when closest is same service, got %q", result.ServiceID)
	}
}

func TestFindBetterTrain_NoneWithinTolerance(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "svc-far", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)},
	}

	boardClient := &mockBoardClient{services: services}
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	result, err := planner.FindBetterTrain(context.Background(), "SMH", "CTK", 7*60+45, "svc-current")
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Errorf("expected nil when no train within tolerance, got %q", result.ServiceID)
	}
}

func TestFindBetterTrain_APIError(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	boardClient := &mockBoardClient{err: fmt.Errorf("api down")}
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	result, err := planner.FindBetterTrain(context.Background(), "SMH", "CTK", 7*60+45, "svc-current")
	if err == nil {
		t.Error("expected error")
	}
	if result != nil {
		t.Error("expected nil result on error")
	}
}

func TestFindBetterTrain_EmptyBoard(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	boardClient := &mockBoardClient{services: []domain.TrainStatus{}}
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	result, err := planner.FindBetterTrain(context.Background(), "SMH", "CTK", 7*60+45, "svc-current")
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Error("expected nil for empty departure board")
	}
}

func TestMarkAndCheckBetterDeclined(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	daemon := &Daemon{rdb: rdb}

	if daemon.betterDeclined(ctx, routeID) {
		t.Error("expected false before marking")
	}

	daemon.MarkBetterDeclined(ctx, routeID)

	if !daemon.betterDeclined(ctx, routeID) {
		t.Error("expected true after marking")
	}
}

func TestMarkAndCheckBetterOffered(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()
	daemon := &Daemon{rdb: rdb}

	if daemon.betterOffered(ctx, routeID) {
		t.Error("expected false before marking")
	}

	daemon.markBetterOffered(ctx, routeID)

	if !daemon.betterOffered(ctx, routeID) {
		t.Error("expected true after marking")
	}

	daemon.ClearBetterOffered(ctx, routeID)

	if daemon.betterOffered(ctx, routeID) {
		t.Error("expected false after clearing")
	}
}

func TestCheckBetterTrain_OnlyChecksWhenNotIdeal(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	routeID := testRouteID()

	tests := []struct {
		name       string
		cachedHour int
		cachedMin  int
		expectCall bool
	}{
		{"exact match", 7, 45, false},
		{"within 3 min", 7, 48, false},
		{"within 5 min", 7, 50, false},
		{"outside tolerance 6 min", 7, 51, true},
		{"way outside tolerance", 8, 30, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr.FlushAll()

			callCount := 0
			boardClient := &callCountingBoardClient{
				services:  []domain.TrainStatus{},
				callCount: &callCount,
			}
			notifier := &mockNotifier{}
			cb := NewCircuitBreaker(boardClient, notifier, noChatIDs)
			planner := &Planner{trainClient: boardClient, rdb: rdb}

			daemon := &Daemon{rdb: rdb, trainClient: boardClient, notifier: notifier, circuitBreaker: cb, planner: planner}

			cached := &domain.TrainStatus{
				ServiceID:          "svc1",
				ScheduledDeparture: makeTime(tt.cachedHour, tt.cachedMin),
			}

			route := makeTestRouteRow(routeID)
			daemon.checkBetterTrain(context.Background(), route, cached)

			if tt.expectCall && callCount == 0 {
				t.Error("expected GetDepartureBoard call but got none")
			}
			if !tt.expectCall && callCount > 0 {
				t.Errorf("expected no GetDepartureBoard call but got %d", callCount)
			}
		})
	}
}

func TestCheckBetterTrain_IntegrationWithCheckRoute(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	ctx := context.Background()
	routeID := testRouteID()

	cached := &domain.TrainStatus{
		ServiceID:          "svc-far",
		ScheduledDeparture: makeTime(8, 30),
		Platform:           "3",
		EstimatedDeparture: makeTime(8, 30),
	}
	data, _ := json.Marshal(cached)
	rdb.Set(ctx, serviceCacheKey(routeID), data, time.Hour)
	rdb.Set(ctx, lastStatePrefix+formatUUID(routeID), data, time.Hour)

	now := time.Now()
	betterService := domain.TrainStatus{
		ServiceID:          "svc-better",
		ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 44, 0, 0, time.UTC),
		Destination:        "London",
	}

	boardClient := &mockBoardClient{services: []domain.TrainStatus{betterService}}
	serviceClient := &mockTrainClient{serviceDetails: cached}
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(serviceClient, notifier, noChatIDs)
	planner := &Planner{trainClient: boardClient, rdb: rdb}

	daemon := &Daemon{rdb: rdb, trainClient: serviceClient, notifier: notifier, circuitBreaker: cb, planner: planner}

	route := makeTestRouteRow(routeID)
	daemon.checkRoute(ctx, route, cached)
	daemon.checkBetterTrain(ctx, route, cached)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()

	if len(notifier.betterOffers) != 0 {
		t.Errorf("expected no offer — should auto-switch when within tolerance, got %d", len(notifier.betterOffers))
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 auto-switch notification, got %d", len(notifier.sent))
	}
}

type callCountingBoardClient struct {
	services  []domain.TrainStatus
	callCount *int
}

func (m *callCountingBoardClient) GetDepartureBoard(_ context.Context, _, _ string, _, _ int) ([]domain.TrainStatus, error) {
	*m.callCount++
	return m.services, nil
}

func (m *callCountingBoardClient) GetServiceDetails(_ context.Context, _ string) (*domain.TrainStatus, error) {
	return nil, fmt.Errorf("not implemented")
}
