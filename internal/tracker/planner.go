package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
	"github.com/yurii-merker/commute-tracker/internal/timezone"
)

const (
	serviceCachePrefix = "route_service:"
	matchToleranceMins = 5
	searchMargin       = 10
)

type Planner struct {
	queries        PlannerRepository
	trainClient    domain.TrainClient
	scheduleClient domain.ScheduleClient
	rdb            *redis.Client
}

func NewPlanner(queries PlannerRepository, trainClient domain.TrainClient, scheduleClient domain.ScheduleClient, rdb *redis.Client) *Planner {
	return &Planner{
		queries:        queries,
		trainClient:    trainClient,
		scheduleClient: scheduleClient,
		rdb:            rdb,
	}
}

func (p *Planner) PlanRoute(ctx context.Context, route db.Route) (*domain.TrainStatus, error) {
	targetMins := pgTimeToMinutes(route.DepartureTime)
	offset, window := departureSearch(targetMins)

	services, err := p.trainClient.GetDepartureBoard(ctx, route.FromStationCrs, route.ToStationCrs, offset, window)
	if err != nil {
		return nil, fmt.Errorf("fetching departure board: %w", err)
	}

	slog.Debug("departure board fetched",
		"from", route.FromStationCrs,
		"to", route.ToStationCrs,
		"offset", offset,
		"window", window,
		"target", fmt.Sprintf("%02d:%02d", targetMins/60, targetMins%60),
		"services_found", len(services),
	)

	best := findClosestService(services, targetMins)
	if best == nil {
		return nil, fmt.Errorf("no matching service found within %d min of %02d:%02d (got %d services)",
			matchToleranceMins, targetMins/60, targetMins%60, len(services))
	}

	if err := p.CacheService(ctx, route.ID, best); err != nil {
		return nil, err
	}

	return best, nil
}

func (p *Planner) CacheService(ctx context.Context, routeID pgtype.UUID, status *domain.TrainStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshaling train status: %w", err)
	}

	key := serviceCacheKey(routeID)
	ttl := timeUntilEndOfDay()

	if err := p.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("caching service: %w", err)
	}

	slog.Debug("cached service for route",
		"route_id", formatUUID(routeID),
		"service_id", status.ServiceID,
		"ttl", ttl,
	)

	return nil
}

func (p *Planner) FindNearestTrains(ctx context.Context, fromCRS, toCRS string, targetMins int) (*domain.NearestTrains, error) {
	offset, window := nearestTrainSearch(targetMins)

	services, err := p.trainClient.GetDepartureBoard(ctx, fromCRS, toCRS, offset, window)
	if err != nil {
		return nil, fmt.Errorf("fetching departure board: %w", err)
	}

	result := &domain.NearestTrains{}

	exact := findClosestService(services, targetMins)
	if exact != nil {
		result.Exact = exact
		return result, nil
	}

	var closestBefore, closestAfter *domain.TrainStatus
	bestBeforeDiff := math.MaxInt
	bestAfterDiff := math.MaxInt

	for i := range services {
		svcMins := services[i].ScheduledDeparture.Hour()*60 + services[i].ScheduledDeparture.Minute()
		diff := svcMins - targetMins
		if diff < 0 && -diff < bestBeforeDiff {
			bestBeforeDiff = -diff
			closestBefore = &services[i]
		}
		if diff > 0 && diff < bestAfterDiff {
			bestAfterDiff = diff
			closestAfter = &services[i]
		}
	}

	result.Before = closestBefore
	result.After = closestAfter
	return result, nil
}

func (p *Planner) FindScheduledTrains(ctx context.Context, fromCRS, toCRS string, date time.Time, targetMins int) (*domain.NearestTrains, error) {
	services, err := p.scheduleClient.GetScheduledDepartures(ctx, fromCRS, toCRS, date, targetMins)
	if err != nil {
		return nil, fmt.Errorf("fetching scheduled departures: %w", err)
	}

	result := &domain.NearestTrains{}

	exact := findClosestService(services, targetMins)
	if exact != nil {
		result.Exact = exact
		return result, nil
	}

	var closestBefore, closestAfter *domain.TrainStatus
	bestBeforeDiff := math.MaxInt
	bestAfterDiff := math.MaxInt

	for i := range services {
		svcMins := services[i].ScheduledDeparture.Hour()*60 + services[i].ScheduledDeparture.Minute()
		diff := svcMins - targetMins
		if diff < 0 && -diff < bestBeforeDiff {
			bestBeforeDiff = -diff
			closestBefore = &services[i]
		}
		if diff > 0 && diff < bestAfterDiff {
			bestAfterDiff = diff
			closestAfter = &services[i]
		}
	}

	result.Before = closestBefore
	result.After = closestAfter
	return result, nil
}

func (p *Planner) PlanRouteFromSchedule(ctx context.Context, route db.Route, date time.Time) (*domain.TrainStatus, error) {
	targetMins := pgTimeToMinutes(route.DepartureTime)

	services, err := p.scheduleClient.GetScheduledDepartures(ctx, route.FromStationCrs, route.ToStationCrs, date, targetMins)
	if err != nil {
		return nil, fmt.Errorf("fetching scheduled departures: %w", err)
	}

	best := findClosestService(services, targetMins)
	if best == nil {
		return nil, fmt.Errorf("no matching scheduled service found within %d min of %02d:%02d (got %d services)",
			matchToleranceMins, targetMins/60, targetMins%60, len(services))
	}

	if err := p.CacheService(ctx, route.ID, best); err != nil {
		return nil, err
	}

	return best, nil
}

func (p *Planner) FindBetterTrain(ctx context.Context, fromCRS, toCRS string, targetMins int, currentServiceID string) (*domain.TrainStatus, error) {
	offset, window := departureSearch(targetMins)
	services, err := p.trainClient.GetDepartureBoard(ctx, fromCRS, toCRS, offset, window)
	if err != nil {
		return nil, fmt.Errorf("fetching departure board for better train: %w", err)
	}

	best := findClosestService(services, targetMins)
	if best == nil || best.ServiceID == currentServiceID {
		return nil, nil
	}
	return best, nil
}

func findClosestService(services []domain.TrainStatus, targetMins int) *domain.TrainStatus {
	var best *domain.TrainStatus
	bestDiff := math.MaxInt

	for i := range services {
		svcMins := services[i].ScheduledDeparture.Hour()*60 + services[i].ScheduledDeparture.Minute()
		diff := absDiff(svcMins, targetMins)
		if diff <= matchToleranceMins && diff < bestDiff {
			bestDiff = diff
			best = &services[i]
		}
	}

	return best
}

func serviceCacheKey(routeID pgtype.UUID) string {
	return serviceCachePrefix + formatUUID(routeID)
}

func pgTimeToMinutes(t pgtype.Time) int {
	if !t.Valid {
		return 0
	}
	total := t.Microseconds
	hours := total / 3600000000
	minutes := (total % 3600000000) / 60000000
	return int(hours*60 + minutes)
}

func ukNow() time.Time {
	return timezone.Now()
}

func TodayWeekdayBit() int32 {
	day := ukNow().Weekday()
	if day == time.Sunday {
		return 1 << 6
	}
	return 1 << (day - 1)
}

func timeUntilEndOfDay() time.Duration {
	now := ukNow()
	endOfDay := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location())
	ttl := time.Until(endOfDay)
	if ttl < time.Minute {
		ttl = time.Minute
	}
	return ttl
}

func departureSearch(targetMins int) (offset, window int) {
	now := ukNow()
	return searchParams(targetMins, now.Hour()*60+now.Minute(), -120)
}

func futureOnlySearch(targetMins int) (offset, window int) {
	now := ukNow()
	return searchParams(targetMins, now.Hour()*60+now.Minute(), 0)
}

const nearestSearchMargin = 30

func nearestTrainSearch(targetMins int) (offset, window int) {
	now := ukNow()
	nowMins := now.Hour()*60 + now.Minute()

	desiredStart := targetMins - nearestSearchMargin
	desiredEnd := targetMins + nearestSearchMargin

	offset = desiredStart - nowMins
	if offset < -120 {
		offset = -120
	}
	if offset > 120 {
		offset = 120
	}

	window = desiredEnd - (nowMins + offset)
	if end := nowMins + offset + window; end > nowMins+maxAPIRangeMins {
		window = nowMins + maxAPIRangeMins - (nowMins + offset)
	}
	if window < 20 {
		window = 20
	}
	if window > 120 {
		window = 120
	}
	return offset, window
}

func searchParams(targetMins, nowMins, minOffset int) (offset, window int) {
	offset = targetMins - nowMins - searchMargin
	if offset < minOffset {
		offset = minOffset
	}
	if offset > 120 {
		offset = 120
	}

	window = targetMins - nowMins - offset + matchToleranceMins
	if window < 20 {
		window = 20
	}
	if window > 120 {
		window = 120
	}
	return offset, window
}

func TargetDate(targetMins int) time.Time {
	now := ukNow()
	nowMins := now.Hour()*60 + now.Minute()
	if targetMins <= nowMins {
		return now.AddDate(0, 0, 1)
	}
	return now
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func formatUUID(u pgtype.UUID) string {
	if !u.Valid {
		return "invalid"
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
