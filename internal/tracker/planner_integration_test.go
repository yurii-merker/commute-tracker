package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
)

func TestPlanRouteSuccess(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "svc1", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 45, 0, 0, time.UTC), Platform: "3"},
	}

	trainClient := &mockBoardClient{services: services}
	planner := &Planner{trainClient: trainClient, rdb: rdb}

	route := db.Route{
		ID:            pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		DepartureTime: pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
	}

	status, err := planner.PlanRoute(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	if status == nil || status.ServiceID != "svc1" {
		t.Errorf("expected service svc1, got %v", status)
	}

	key := serviceCacheKey(route.ID)
	data, err := rdb.Get(context.Background(), key).Bytes()
	if err != nil {
		t.Fatal(err)
	}

	var cached domain.TrainStatus
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatal(err)
	}
	if cached.ServiceID != "svc1" {
		t.Errorf("cached service = %q, want svc1", cached.ServiceID)
	}
}

func TestPlanRouteNoMatch(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "svc1", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)},
	}

	trainClient := &mockBoardClient{services: services}
	planner := &Planner{trainClient: trainClient, rdb: rdb}

	route := db.Route{
		ID:            pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		DepartureTime: pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
	}

	status, err := planner.PlanRoute(context.Background(), route)
	if err == nil {
		t.Error("expected error for no matching service")
	}
	if status != nil {
		t.Error("expected nil status on error")
	}
}

func TestPlanRouteAPIError(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	trainClient := &mockBoardClient{err: fmt.Errorf("api down")}
	planner := &Planner{trainClient: trainClient, rdb: rdb}

	route := db.Route{
		ID:            pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		DepartureTime: pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
	}

	status, err := planner.PlanRoute(context.Background(), route)
	if err == nil {
		t.Error("expected error for API failure")
	}
	if status != nil {
		t.Error("expected nil status on error")
	}
}

func TestCacheServiceTTL(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	planner := &Planner{rdb: rdb}
	routeID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	status := &domain.TrainStatus{ServiceID: "svc1"}

	err := planner.CacheService(context.Background(), routeID, status)
	if err != nil {
		t.Fatal(err)
	}

	ttl := rdb.TTL(context.Background(), serviceCacheKey(routeID)).Val()
	if ttl <= 0 {
		t.Error("expected positive TTL on cached service")
	}
}

func TestPlanRouteDBErrorHandled(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	trainClient := &mockBoardClient{err: fmt.Errorf("db down")}
	planner := NewPlanner(&mockPlannerRepo{}, trainClient, &mockScheduleClient{}, rdb)

	route := db.Route{
		ID:            pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		DepartureTime: pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
	}

	_, err := planner.PlanRoute(context.Background(), route)
	if err == nil {
		t.Error("expected error from PlanRoute")
	}
}

type mockScheduleClient struct {
	services []domain.TrainStatus
	err      error
}

func (m *mockScheduleClient) GetScheduledDepartures(_ context.Context, _, _ string, _ time.Time, _ int) ([]domain.TrainStatus, error) {
	return m.services, m.err
}

type mockBoardClient struct {
	services []domain.TrainStatus
	err      error
}

func (m *mockBoardClient) GetDepartureBoard(_ context.Context, _, _ string, _, _ int) ([]domain.TrainStatus, error) {
	return m.services, m.err
}

func (m *mockBoardClient) GetServiceDetails(_ context.Context, _ string) (*domain.TrainStatus, error) {
	return nil, fmt.Errorf("not implemented")
}
