package tracker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
)

var errTest = fmt.Errorf("test error")

func TestFindClosestService(t *testing.T) {
	services := []domain.TrainStatus{
		{ServiceID: "early", ScheduledDeparture: makeTime(7, 15)},
		{ServiceID: "exact", ScheduledDeparture: makeTime(7, 45)},
		{ServiceID: "late", ScheduledDeparture: makeTime(8, 15)},
	}

	tests := []struct {
		name      string
		targetMin int
		wantID    string
		wantNil   bool
	}{
		{"exact match", 7*60 + 45, "exact", false},
		{"closest to early", 7*60 + 12, "early", false},
		{"closest to late", 8*60 + 12, "late", false},
		{"too far from any", 12 * 60, "", true},
		{"edge of tolerance", 8*60 + 20, "late", false},
		{"just outside tolerance", 8*60 + 21, "", true},
		{"outside new tolerance", 7*60 + 30, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findClosestService(services, tt.targetMin)
			if tt.wantNil {
				if result != nil {
					t.Errorf("expected nil, got service %q", result.ServiceID)
				}
				return
			}
			if result == nil {
				t.Fatal("expected service, got nil")
			}
			if result.ServiceID != tt.wantID {
				t.Errorf("got %q, want %q", result.ServiceID, tt.wantID)
			}
		})
	}
}

func TestFindClosestServiceEmpty(t *testing.T) {
	result := findClosestService(nil, 7*60+45)
	if result != nil {
		t.Error("expected nil for empty services")
	}
}

func TestPgTimeToMinutes(t *testing.T) {
	tests := []struct {
		name string
		time pgtype.Time
		want int
	}{
		{"morning", pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true}, 7*60 + 45},
		{"midnight", pgtype.Time{Microseconds: 0, Valid: true}, 0},
		{"evening", pgtype.Time{Microseconds: 18*3600000000 + 30*60000000, Valid: true}, 18*60 + 30},
		{"invalid", pgtype.Time{Valid: false}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pgTimeToMinutes(tt.time); got != tt.want {
				t.Errorf("pgTimeToMinutes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTodayWeekdayBit(t *testing.T) {
	bit := TodayWeekdayBit()
	if bit == 0 || bit > 64 {
		t.Errorf("unexpected weekday bit: %d", bit)
	}

	day := time.Now().Weekday()
	var expected int32
	if day == time.Sunday {
		expected = 1 << 6
	} else {
		expected = 1 << (day - 1)
	}
	if bit != expected {
		t.Errorf("got bit %d, want %d for %s", bit, expected, day)
	}
}

func TestServiceCacheKey(t *testing.T) {
	uuid := pgtype.UUID{
		Bytes: [16]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0},
		Valid: true,
	}
	got := serviceCacheKey(uuid)
	want := "route_service:12345678-9abc-def0-1234-56789abcdef0"
	if got != want {
		t.Errorf("serviceCacheKey() = %q, want %q", got, want)
	}
}

func TestFormatUUID(t *testing.T) {
	invalid := pgtype.UUID{Valid: false}
	if got := formatUUID(invalid); got != "invalid" {
		t.Errorf("formatUUID(invalid) = %q, want %q", got, "invalid")
	}
}

func TestAbsDiff(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{10, 5, 5},
		{5, 10, 5},
		{7, 7, 0},
	}
	for _, tt := range tests {
		if got := absDiff(tt.a, tt.b); got != tt.want {
			t.Errorf("absDiff(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestTimeUntilEndOfDay(t *testing.T) {
	ttl := timeUntilEndOfDay()
	if ttl <= 0 || ttl > 24*time.Hour {
		t.Errorf("unexpected TTL: %v", ttl)
	}
}

func makeTime(hour, min int) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, time.UTC)
}

func TestFindNearestTrainsExactMatch(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "svc1", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 45, 0, 0, time.UTC)},
	}

	planner := &Planner{trainClient: &mockBoardClient{services: services}, rdb: rdb}
	result, err := planner.FindNearestTrains(context.Background(), "SMH", "CTK", 7*60+45)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exact == nil {
		t.Fatal("expected exact match")
	}
	if result.Exact.ServiceID != "svc1" {
		t.Errorf("exact ServiceID = %q, want svc1", result.Exact.ServiceID)
	}
	if result.Before != nil || result.After != nil {
		t.Error("expected Before and After to be nil when exact match found")
	}
}

func TestFindNearestTrainsBeforeAndAfter(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "before", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 30, 0, 0, time.UTC)},
		{ServiceID: "after", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, time.UTC)},
	}

	planner := &Planner{trainClient: &mockBoardClient{services: services}, rdb: rdb}
	result, err := planner.FindNearestTrains(context.Background(), "SMH", "CTK", 7*60+45)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exact != nil {
		t.Error("expected no exact match")
	}
	if result.Before == nil || result.Before.ServiceID != "before" {
		t.Errorf("Before = %v, want service 'before'", result.Before)
	}
	if result.After == nil || result.After.ServiceID != "after" {
		t.Errorf("After = %v, want service 'after'", result.After)
	}
}

func TestFindNearestTrainsOnlyBefore(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "before", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 30, 0, 0, time.UTC)},
	}

	planner := &Planner{trainClient: &mockBoardClient{services: services}, rdb: rdb}
	result, err := planner.FindNearestTrains(context.Background(), "SMH", "CTK", 7*60+45)
	if err != nil {
		t.Fatal(err)
	}
	if result.Before == nil {
		t.Fatal("expected Before to be set")
	}
	if result.After != nil {
		t.Error("expected After to be nil")
	}
}

func TestFindNearestTrainsOnlyAfter(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "after", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, time.UTC)},
	}

	planner := &Planner{trainClient: &mockBoardClient{services: services}, rdb: rdb}
	result, err := planner.FindNearestTrains(context.Background(), "SMH", "CTK", 7*60+45)
	if err != nil {
		t.Fatal(err)
	}
	if result.Before != nil {
		t.Error("expected Before to be nil")
	}
	if result.After == nil {
		t.Fatal("expected After to be set")
	}
}

func TestFindNearestTrainsEmpty(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	planner := &Planner{trainClient: &mockBoardClient{services: []domain.TrainStatus{}}, rdb: rdb}
	result, err := planner.FindNearestTrains(context.Background(), "SMH", "CTK", 7*60+45)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exact != nil || result.Before != nil || result.After != nil {
		t.Error("expected all nil for empty board")
	}
}

func TestFindNearestTrainsAPIError(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	planner := &Planner{trainClient: &mockBoardClient{err: errTest}, rdb: rdb}
	_, err := planner.FindNearestTrains(context.Background(), "SMH", "CTK", 7*60+45)
	if err == nil {
		t.Error("expected error")
	}
}

func TestFindNearestTrainsPicksClosest(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "far-before", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 6, 0, 0, 0, time.UTC)},
		{ServiceID: "close-before", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 7, 30, 0, 0, time.UTC)},
		{ServiceID: "close-after", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, time.UTC)},
		{ServiceID: "far-after", ScheduledDeparture: time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.UTC)},
	}

	planner := &Planner{trainClient: &mockBoardClient{services: services}, rdb: rdb}
	result, err := planner.FindNearestTrains(context.Background(), "SMH", "CTK", 7*60+45)
	if err != nil {
		t.Fatal(err)
	}
	if result.Before == nil || result.Before.ServiceID != "close-before" {
		t.Errorf("Before = %v, want close-before", result.Before)
	}
	if result.After == nil || result.After.ServiceID != "close-after" {
		t.Errorf("After = %v, want close-after", result.After)
	}
}

func TestFindScheduledTrainsExactMatch(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	services := []domain.TrainStatus{
		{ServiceID: "S1", ScheduledDeparture: makeTime(7, 43), IsScheduleOnly: true},
		{ServiceID: "S2", ScheduledDeparture: makeTime(8, 13), IsScheduleOnly: true},
	}

	sc := &mockScheduleClient{services: services}
	planner := &Planner{scheduleClient: sc, rdb: rdb}

	result, err := planner.FindScheduledTrains(context.Background(), "WAT", "WOK", date, 7*60+43)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exact == nil {
		t.Fatal("expected exact match")
	}
	if result.Exact.ServiceID != "S1" {
		t.Errorf("exact = %q, want S1", result.Exact.ServiceID)
	}
	if !result.Exact.IsScheduleOnly {
		t.Error("expected IsScheduleOnly on exact match")
	}
}

func TestFindScheduledTrainsBeforeAfter(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	date := time.Now()
	services := []domain.TrainStatus{
		{ServiceID: "before", ScheduledDeparture: makeTime(7, 30), IsScheduleOnly: true},
		{ServiceID: "after", ScheduledDeparture: makeTime(8, 0), IsScheduleOnly: true},
	}

	sc := &mockScheduleClient{services: services}
	planner := &Planner{scheduleClient: sc, rdb: rdb}

	result, err := planner.FindScheduledTrains(context.Background(), "WAT", "WOK", date, 7*60+45)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exact != nil {
		t.Error("expected no exact match")
	}
	if result.Before == nil || result.Before.ServiceID != "before" {
		t.Errorf("Before = %v, want 'before'", result.Before)
	}
	if result.After == nil || result.After.ServiceID != "after" {
		t.Errorf("After = %v, want 'after'", result.After)
	}
}

func TestFindScheduledTrainsEmpty(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	sc := &mockScheduleClient{services: []domain.TrainStatus{}}
	planner := &Planner{scheduleClient: sc, rdb: rdb}

	result, err := planner.FindScheduledTrains(context.Background(), "WAT", "WOK", time.Now(), 7*60+45)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exact != nil || result.Before != nil || result.After != nil {
		t.Error("expected all nil for empty services")
	}
}

func TestFindScheduledTrainsError(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	sc := &mockScheduleClient{err: errTest}
	planner := &Planner{scheduleClient: sc, rdb: rdb}

	_, err := planner.FindScheduledTrains(context.Background(), "WAT", "WOK", time.Now(), 7*60+45)
	if err == nil {
		t.Error("expected error")
	}
}

func TestPlanRouteFromScheduleSuccess(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	now := time.Now()
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	services := []domain.TrainStatus{
		{ServiceID: "S1", ScheduledDeparture: makeTime(7, 45), IsScheduleOnly: true, Destination: "Woking"},
	}

	sc := &mockScheduleClient{services: services}
	planner := &Planner{scheduleClient: sc, rdb: rdb}

	route := db.Route{
		ID:             pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		FromStationCrs: "WAT",
		ToStationCrs:   "WOK",
		DepartureTime:  pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
	}

	status, err := planner.PlanRouteFromSchedule(context.Background(), route, date)
	if err != nil {
		t.Fatal(err)
	}
	if status.ServiceID != "S1" {
		t.Errorf("got %q, want S1", status.ServiceID)
	}
	if !status.IsScheduleOnly {
		t.Error("expected IsScheduleOnly on cached status")
	}

	cached, err := rdb.Get(context.Background(), serviceCacheKey(route.ID)).Bytes()
	if err != nil {
		t.Fatalf("expected cached value, got: %v", err)
	}
	if len(cached) == 0 {
		t.Error("expected non-empty cached data")
	}
}

func TestPlanRouteFromScheduleNoMatch(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	services := []domain.TrainStatus{
		{ServiceID: "S1", ScheduledDeparture: makeTime(12, 0), IsScheduleOnly: true},
	}

	sc := &mockScheduleClient{services: services}
	planner := &Planner{scheduleClient: sc, rdb: rdb}

	route := db.Route{
		ID:            pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		DepartureTime: pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
	}

	_, err := planner.PlanRouteFromSchedule(context.Background(), route, time.Now())
	if err == nil {
		t.Error("expected error for no match")
	}
}

func TestPlanRouteFromScheduleClientError(t *testing.T) {
	rdb, mr := setupRedis(t)
	defer mr.Close()

	sc := &mockScheduleClient{err: errTest}
	planner := &Planner{scheduleClient: sc, rdb: rdb}

	route := db.Route{
		ID:            pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		DepartureTime: pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
	}

	_, err := planner.PlanRouteFromSchedule(context.Background(), route, time.Now())
	if err == nil {
		t.Error("expected error")
	}
}

func TestTargetDate(t *testing.T) {
	now := ukNow()
	nowMins := now.Hour()*60 + now.Minute()

	t.Run("future time returns today", func(t *testing.T) {
		futureMins := nowMins + 60
		if futureMins >= 1440 {
			futureMins = nowMins + 1
		}
		date := TargetDate(futureMins)
		if date.Day() != now.Day() {
			t.Errorf("expected today (%d), got %d", now.Day(), date.Day())
		}
	})

	t.Run("past time returns tomorrow", func(t *testing.T) {
		pastMins := nowMins - 60
		if pastMins < 0 {
			pastMins = 0
		}
		if pastMins >= nowMins {
			t.Skip("cannot test past time at midnight")
		}
		date := TargetDate(pastMins)
		tomorrow := now.AddDate(0, 0, 1)
		if date.Day() != tomorrow.Day() {
			t.Errorf("expected tomorrow (%d), got %d", tomorrow.Day(), date.Day())
		}
	})

	t.Run("equal time returns today", func(t *testing.T) {
		date := TargetDate(nowMins)
		tomorrow := now.AddDate(0, 0, 1)
		if date.Day() != tomorrow.Day() {
			t.Errorf("equal time should return tomorrow (%d), got %d", tomorrow.Day(), date.Day())
		}
	})
}

func TestDepartureSearch(t *testing.T) {
	tests := []struct {
		name       string
		targetMins int
	}{
		{"normal offset", 7*60 + 45},
		{"very early target", 0},
		{"very late target", 23*60 + 59},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offset, window := departureSearch(tt.targetMins)
			if offset < -120 || offset > 120 {
				t.Errorf("departureSearch(%d) offset = %d, out of [-120, 120]", tt.targetMins, offset)
			}
			if window < 20 || window > 120 {
				t.Errorf("departureSearch(%d) window = %d, out of [20, 120]", tt.targetMins, window)
			}
		})
	}
}

func TestFutureOnlySearch(t *testing.T) {
	tests := []struct {
		name       string
		targetMins int
	}{
		{"near target", 7*60 + 45},
		{"far target", 23 * 60},
		{"past target", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offset, window := futureOnlySearch(tt.targetMins)
			if offset < 0 || offset > 120 {
				t.Errorf("futureOnlySearch(%d) offset = %d, out of [0, 120]", tt.targetMins, offset)
			}
			if window < 20 || window > 120 {
				t.Errorf("futureOnlySearch(%d) window = %d, out of [20, 120]", tt.targetMins, window)
			}
		})
	}
}
