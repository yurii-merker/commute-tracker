package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
	"github.com/yurii-merker/commute-tracker/internal/station"
)

const (
	lastStatePrefix      = "last_state:"
	alertSentPrefix      = "alert_sent:"
	betterDeclinedPrefix = "better_declined:"
	betterOfferedPrefix  = "better_offered:"
	firstMissPrefix      = "first_miss:"
	timetableBackoff     = "timetable_backoff:"
	defaultTickInterval  = 2 * time.Minute
	maxAPIRangeMins      = 240
	planGracePeriod      = 30 * time.Minute
	maxChoiceRangeMins   = 90
	timetableRetryDelay  = time.Hour
)

type Daemon struct {
	queries        DaemonRepository
	trainClient    domain.TrainClient
	notifier       domain.Notifier
	rdb            *redis.Client
	circuitBreaker *CircuitBreaker
	planner        *Planner
	tickInterval   time.Duration
	wg             sync.WaitGroup
}

func NewDaemon(queries DaemonRepository, trainClient domain.TrainClient, notifier domain.Notifier, rdb *redis.Client, cb *CircuitBreaker, planner *Planner, tickInterval time.Duration) *Daemon {
	if tickInterval <= 0 {
		tickInterval = defaultTickInterval
	}
	return &Daemon{
		queries:        queries,
		trainClient:    trainClient,
		notifier:       notifier,
		rdb:            rdb,
		circuitBreaker: cb,
		planner:        planner,
		tickInterval:   tickInterval,
	}
}

func (d *Daemon) Start(ctx context.Context) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("daemon panic recovered", "panic", r)
			}
		}()

		ticker := time.NewTicker(d.tickInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("tracker daemon stopped")
				return
			case <-ticker.C:
				d.tick(ctx)
			}
		}
	}()
}

func (d *Daemon) Wait() {
	d.wg.Wait()
}

func (d *Daemon) tick(ctx context.Context) {
	if d.circuitBreaker.State() == APIDown {
		slog.Debug("daemon tick skipped: API is down")
		return
	}

	weekday := TodayWeekdayBit()

	routes, err := d.queries.GetActiveRoutesWithChatID(ctx, weekday)
	if err != nil {
		slog.Error("daemon failed to fetch routes", "error", err)
		return
	}

	now := ukNow()
	checked := 0
	for _, route := range routes {
		if isDeparted(route, now) {
			d.clearRouteCache(ctx, route.ID)
			continue
		}

		cached, err := d.getCachedService(ctx, route.ID)
		if err != nil {
			slog.Error("daemon failed to get cached service", "route_id", formatUUID(route.ID), "error", err)
			continue
		}

		if cached != nil && cached.IsScheduleOnly && isWithinAPIRange(route, now) {
			d.tryTransitionToLive(ctx, route)
			continue
		}

		if cached != nil && !cached.IsScheduleOnly {
			d.checkRoute(ctx, route, cached)
			d.checkBetterTrain(ctx, route, cached)
			checked++

			for _, offset := range route.AlertOffsets {
				if isWithinAlertWindow(route, now, offset) && !d.alertSent(ctx, route.ID, offset) {
					d.sendDepartureReminder(ctx, route, cached, offset)
					d.markAlertSent(ctx, route.ID, offset)
				}
			}
			continue
		}

		if cached != nil && cached.IsScheduleOnly {
			continue
		}

		if isWithinAPIRange(route, now) {
			d.tryPlanRoute(ctx, route)
			continue
		}

		d.tryFetchTimetable(ctx, route)
	}

	slog.Debug("daemon tick completed", "total_routes", len(routes), "checked", checked)
}

func (d *Daemon) checkRoute(ctx context.Context, route db.GetActiveRoutesWithChatIDRow, cached *domain.TrainStatus) {
	fresh, err := d.trainClient.GetServiceDetails(ctx, cached.ServiceID)
	if err != nil {
		slog.Error("daemon failed to get service details",
			"route_id", formatUUID(route.ID),
			"service_id", cached.ServiceID,
			"error", err,
		)
		d.circuitBreaker.RecordFailure(ctx, err)
		return
	}
	d.circuitBreaker.RecordSuccess()

	last, err := d.getLastState(ctx, route.ID)
	if err != nil {
		slog.Error("daemon failed to get last state", "route_id", formatUUID(route.ID), "error", err)
		return
	}

	if last == nil {
		d.saveLastState(ctx, route.ID, fresh)
		return
	}

	if statusChanged(last, fresh) {
		d.sendAlert(ctx, route, fresh, last)
		d.saveLastState(ctx, route.ID, fresh)
		d.updateServiceCache(ctx, route.ID, fresh)
	}
}

func (d *Daemon) sendAlert(ctx context.Context, route db.GetActiveRoutesWithChatIDRow, current *domain.TrainStatus, previous *domain.TrainStatus) {
	msg := formatAlert(route, current, previous)
	if err := d.notifier.Send(ctx, route.TelegramChatID, msg); err != nil {
		slog.Error("daemon failed to send alert",
			"chat_id", route.TelegramChatID,
			"route_id", formatUUID(route.ID),
			"error", err,
		)
	}
}

func (d *Daemon) getCachedService(ctx context.Context, routeID pgtype.UUID) (*domain.TrainStatus, error) {
	key := serviceCacheKey(routeID)
	data, err := d.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting cached service: %w", err)
	}

	var status domain.TrainStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("unmarshaling cached service: %w", err)
	}

	return &status, nil
}

func (d *Daemon) getLastState(ctx context.Context, routeID pgtype.UUID) (*domain.TrainStatus, error) {
	key := lastStatePrefix + formatUUID(routeID)
	data, err := d.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting last state: %w", err)
	}

	var status domain.TrainStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("unmarshaling last state: %w", err)
	}

	return &status, nil
}

func (d *Daemon) saveLastState(ctx context.Context, routeID pgtype.UUID, status *domain.TrainStatus) {
	data, err := json.Marshal(status)
	if err != nil {
		slog.Error("failed to marshal last state", "error", err)
		return
	}

	key := lastStatePrefix + formatUUID(routeID)
	ttl := timeUntilEndOfDay()
	if err := d.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		slog.Error("failed to save last state", "route_id", formatUUID(routeID), "error", err)
	}
}

func (d *Daemon) updateServiceCache(ctx context.Context, routeID pgtype.UUID, status *domain.TrainStatus) {
	data, err := json.Marshal(status)
	if err != nil {
		slog.Error("failed to marshal service cache", "error", err)
		return
	}

	key := serviceCacheKey(routeID)
	ttl := timeUntilEndOfDay()
	if err := d.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		slog.Error("failed to update service cache", "route_id", formatUUID(routeID), "error", err)
	}
}

func (d *Daemon) clearRouteCache(ctx context.Context, routeID pgtype.UUID) {
	uid := formatUUID(routeID)
	keys := []string{serviceCacheKey(routeID), lastStatePrefix + uid, choiceSentPrefix + uid, betterDeclinedPrefix + uid, betterOfferedPrefix + uid, firstMissPrefix + uid, timetableBackoff + uid}
	pattern := alertSentPrefix + uid + ":*"
	var cursor uint64
	for {
		batch, next, err := d.rdb.Scan(ctx, cursor, pattern, 10).Result()
		if err != nil {
			break
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	d.rdb.Del(ctx, keys...)
}

func (d *Daemon) tryFetchTimetable(ctx context.Context, route db.GetActiveRoutesWithChatIDRow) {
	if d.timetableBackedOff(ctx, route.ID) {
		return
	}

	status, err := d.planner.PlanRouteFromSchedule(ctx, routeFromRow(route), ukNow())
	if err != nil {
		slog.Debug("daemon could not fetch timetable",
			"route_id", formatUUID(route.ID),
			"error", err,
		)
		d.setTimetableBackoff(ctx, route.ID)
		return
	}
	slog.Debug("daemon cached timetable for route",
		"route_id", formatUUID(route.ID),
		"service_id", status.ServiceID,
	)
}

func (d *Daemon) timetableBackedOff(ctx context.Context, routeID pgtype.UUID) bool {
	key := timetableBackoff + formatUUID(routeID)
	exists, err := d.rdb.Exists(ctx, key).Result()
	if err != nil {
		return false
	}
	return exists > 0
}

func (d *Daemon) setTimetableBackoff(ctx context.Context, routeID pgtype.UUID) {
	key := timetableBackoff + formatUUID(routeID)
	d.rdb.Set(ctx, key, "1", timetableRetryDelay)
}

func (d *Daemon) tryTransitionToLive(ctx context.Context, route db.GetActiveRoutesWithChatIDRow) {
	status, err := d.planner.PlanRoute(ctx, routeFromRow(route))
	if err != nil {
		slog.Debug("daemon could not transition to live yet",
			"route_id", formatUUID(route.ID),
			"error", err,
		)
		return
	}

	d.saveLastState(ctx, route.ID, status)

	msg := formatTransitionNotification(route, status)
	if err := d.notifier.Send(ctx, route.TelegramChatID, msg); err != nil {
		slog.Error("daemon failed to send transition notification",
			"chat_id", route.TelegramChatID,
			"route_id", formatUUID(route.ID),
			"error", err,
		)
	}

	slog.Info("daemon transitioned route to live data",
		"route_id", formatUUID(route.ID),
		"service_id", status.ServiceID,
	)
}

func formatTransitionNotification(route db.GetActiveRoutesWithChatIDRow, status *domain.TrainStatus) string {
	return fmt.Sprintf("🔔 Live data now available for %s\n\n%s", route.Label, formatAutoSelectStatus(status))
}

func (d *Daemon) tryPlanRoute(ctx context.Context, route db.GetActiveRoutesWithChatIDRow) {
	status, err := d.planner.PlanRoute(ctx, routeFromRow(route))
	if err != nil {
		slog.Debug("daemon could not pin train yet",
			"route_id", formatUUID(route.ID),
			"error", err,
		)
		if !d.choiceSent(ctx, route.ID) && d.gracePeriodExpired(ctx, route.ID) && isWithinChoiceRange(route, ukNow()) {
			d.trySendTrainChoice(ctx, route)
		}
		return
	}
	d.clearFirstMiss(ctx, route.ID)
	slog.Info("daemon pinned train for route",
		"route_id", formatUUID(route.ID),
		"service_id", status.ServiceID,
	)
}

func (d *Daemon) trySendTrainChoice(ctx context.Context, route db.GetActiveRoutesWithChatIDRow) {
	targetMins := pgTimeToMinutes(route.DepartureTime)
	nearest, err := d.planner.FindNearestTrains(ctx, route.FromStationCrs, route.ToStationCrs, targetMins)
	if err != nil {
		slog.Debug("daemon could not find nearest trains", "route_id", formatUUID(route.ID), "error", err)
		return
	}
	if nearest.Before == nil && nearest.After == nil {
		return
	}

	only := nearest.SingleOption()
	if only != nil {
		svcMins := only.ScheduledDeparture.Hour()*60 + only.ScheduledDeparture.Minute()
		if absDiff(svcMins, targetMins) <= domain.MaxAutoSelectMins {
			d.autoSelectTrain(ctx, route, only)
			return
		}
	}

	routeID := formatUUID(route.ID)
	requestedTime := fmt.Sprintf("%02d:%02d", targetMins/60, targetMins%60)

	var before, after *domain.TrainOption
	if nearest.Before != nil {
		before = &domain.TrainOption{
			Time:        nearest.Before.ScheduledDeparture.Format("15:04"),
			Destination: nearest.Before.Destination,
		}
	}
	if nearest.After != nil {
		after = &domain.TrainOption{
			Time:        nearest.After.ScheduledDeparture.Format("15:04"),
			Destination: nearest.After.Destination,
		}
	}

	if err := d.notifier.SendTrainChoice(ctx, route.TelegramChatID, routeID, requestedTime, before, after); err != nil {
		slog.Error("daemon failed to send train choice", "route_id", routeID, "error", err)
		return
	}

	d.markChoiceSent(ctx, route.ID)
	slog.Info("daemon sent train choice to user", "route_id", routeID, "chat_id", route.TelegramChatID)
}

func (d *Daemon) autoSelectTrain(ctx context.Context, route db.GetActiveRoutesWithChatIDRow, train *domain.TrainStatus) {
	chosenTime := train.ScheduledDeparture.Format("15:04")

	if err := d.planner.CacheService(ctx, route.ID, train); err != nil {
		slog.Error("daemon failed to cache auto-selected train", "route_id", formatUUID(route.ID), "error", err)
		return
	}

	msg := fmt.Sprintf("🔄 No exact train found — adjusted to %s\n\n%s", chosenTime, formatAutoSelectStatus(train))
	if err := d.notifier.Send(ctx, route.TelegramChatID, msg); err != nil {
		slog.Error("daemon failed to notify auto-select", "route_id", formatUUID(route.ID), "error", err)
	}
}

func formatAutoSelectStatus(s *domain.TrainStatus) string {
	icon := "🟢"
	text := "On time"
	if s.IsCancelled {
		icon = "🔴"
		text = "Cancelled"
	} else if s.DelayMins > 0 {
		icon = "🟠"
		text = fmt.Sprintf("Delayed %d min", s.DelayMins)
	}
	platform := "TBC"
	if s.Platform != "" {
		platform = s.Platform
	}
	return fmt.Sprintf("%s %s | Platform %s | %s → %s",
		icon, text, platform,
		s.ScheduledDeparture.Format("15:04"), s.Destination)
}

func (d *Daemon) checkBetterTrain(ctx context.Context, route db.GetActiveRoutesWithChatIDRow, cached *domain.TrainStatus) {
	targetMins := pgTimeToMinutes(route.DepartureTime)
	cachedMins := cached.ScheduledDeparture.Hour()*60 + cached.ScheduledDeparture.Minute()
	if absDiff(targetMins, cachedMins) <= matchToleranceMins {
		return
	}

	routeID := route.ID
	if d.betterOffered(ctx, routeID) || d.betterDeclined(ctx, routeID) {
		return
	}

	better, err := d.planner.FindBetterTrain(ctx, route.FromStationCrs, route.ToStationCrs, targetMins, cached.ServiceID)
	if err != nil {
		slog.Error("daemon failed to check for better train", "route_id", formatUUID(routeID), "error", err)
		d.circuitBreaker.RecordFailure(ctx, err)
		return
	}
	if better == nil {
		return
	}

	routeIDStr := formatUUID(routeID)
	d.autoSwitchTrain(ctx, route, better)
	slog.Info("daemon auto-switched to target train", "route_id", routeIDStr, "train", better.ScheduledDeparture.Format("15:04"))
}

func (d *Daemon) betterDeclined(ctx context.Context, routeID pgtype.UUID) bool {
	key := betterDeclinedPrefix + formatUUID(routeID)
	exists, err := d.rdb.Exists(ctx, key).Result()
	if err != nil {
		slog.Error("failed to check better declined", "route_id", formatUUID(routeID), "error", err)
		return false
	}
	return exists > 0
}

func (d *Daemon) MarkBetterDeclined(ctx context.Context, routeID pgtype.UUID) {
	key := betterDeclinedPrefix + formatUUID(routeID)
	d.rdb.Set(ctx, key, "1", timeUntilEndOfDay())
}

func (d *Daemon) betterOffered(ctx context.Context, routeID pgtype.UUID) bool {
	key := betterOfferedPrefix + formatUUID(routeID)
	exists, err := d.rdb.Exists(ctx, key).Result()
	if err != nil {
		slog.Error("failed to check better offered", "route_id", formatUUID(routeID), "error", err)
		return false
	}
	return exists > 0
}

func (d *Daemon) markBetterOffered(ctx context.Context, routeID pgtype.UUID) {
	key := betterOfferedPrefix + formatUUID(routeID)
	d.rdb.Set(ctx, key, "1", timeUntilEndOfDay())
}

func (d *Daemon) ClearBetterOffered(ctx context.Context, routeID pgtype.UUID) {
	key := betterOfferedPrefix + formatUUID(routeID)
	d.rdb.Del(ctx, key)
}

const choiceSentPrefix = "choice_sent:"

func (d *Daemon) choiceSent(ctx context.Context, routeID pgtype.UUID) bool {
	key := choiceSentPrefix + formatUUID(routeID)
	exists, err := d.rdb.Exists(ctx, key).Result()
	if err != nil {
		slog.Error("failed to check choice sent", "route_id", formatUUID(routeID), "error", err)
		return false
	}
	return exists > 0
}

func (d *Daemon) markChoiceSent(ctx context.Context, routeID pgtype.UUID) {
	key := choiceSentPrefix + formatUUID(routeID)
	d.rdb.Set(ctx, key, "1", timeUntilEndOfDay())
}

func (d *Daemon) gracePeriodExpired(ctx context.Context, routeID pgtype.UUID) bool {
	key := firstMissPrefix + formatUUID(routeID)
	val, err := d.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		d.rdb.Set(ctx, key, ukNow().Format(time.RFC3339), timeUntilEndOfDay())
		return false
	}
	if err != nil {
		slog.Error("failed to check first miss", "route_id", formatUUID(routeID), "error", err)
		return false
	}

	firstMiss, err := time.Parse(time.RFC3339, val)
	if err != nil {
		slog.Error("failed to parse first miss time", "route_id", formatUUID(routeID), "error", err)
		return true
	}

	return ukNow().Sub(firstMiss) >= planGracePeriod
}

func (d *Daemon) clearFirstMiss(ctx context.Context, routeID pgtype.UUID) {
	key := firstMissPrefix + formatUUID(routeID)
	d.rdb.Del(ctx, key)
}

func (d *Daemon) autoSwitchTrain(ctx context.Context, route db.GetActiveRoutesWithChatIDRow, train *domain.TrainStatus) {
	if err := d.planner.CacheService(ctx, route.ID, train); err != nil {
		slog.Error("daemon failed to cache auto-switched train", "route_id", formatUUID(route.ID), "error", err)
		return
	}

	d.saveLastState(ctx, route.ID, train)

	msg := fmt.Sprintf("✅ Switched to %s train\n\n%s", train.ScheduledDeparture.Format("15:04"), formatAutoSelectStatus(train))
	if err := d.notifier.Send(ctx, route.TelegramChatID, msg); err != nil {
		slog.Error("daemon failed to notify auto-switch", "route_id", formatUUID(route.ID), "error", err)
	}
}

func alertSentKey(routeID pgtype.UUID, offset int32) string {
	return fmt.Sprintf("%s%s:%d", alertSentPrefix, formatUUID(routeID), offset)
}

func (d *Daemon) alertSent(ctx context.Context, routeID pgtype.UUID, offset int32) bool {
	key := alertSentKey(routeID, offset)
	exists, err := d.rdb.Exists(ctx, key).Result()
	if err != nil {
		slog.Error("failed to check alert sent", "route_id", formatUUID(routeID), "offset", offset, "error", err)
		return false
	}
	return exists > 0
}

func (d *Daemon) markAlertSent(ctx context.Context, routeID pgtype.UUID, offset int32) {
	key := alertSentKey(routeID, offset)
	if err := d.rdb.Set(ctx, key, "1", timeUntilEndOfDay()).Err(); err != nil {
		slog.Error("failed to mark alert sent", "route_id", formatUUID(routeID), "offset", offset, "error", err)
	}
}

func (d *Daemon) sendDepartureReminder(ctx context.Context, route db.GetActiveRoutesWithChatIDRow, cached *domain.TrainStatus, offset int32) {
	fresh, err := d.getCachedService(ctx, route.ID)
	if err != nil || fresh == nil {
		fresh = cached
	}

	last, _ := d.getLastState(ctx, route.ID)
	if last != nil {
		fresh = last
	}

	msg := formatDepartureReminder(route, fresh, offset)
	if err := d.notifier.Send(ctx, route.TelegramChatID, msg); err != nil {
		slog.Error("daemon failed to send departure reminder",
			"chat_id", route.TelegramChatID,
			"route_id", formatUUID(route.ID),
			"error", err,
		)
	}
}

func formatDepartureReminder(route db.GetActiveRoutesWithChatIDRow, status *domain.TrainStatus, offset int32) string {
	depTime := status.ScheduledDeparture.Format("15:04")
	trainName := depTime
	if status.Destination != "" {
		trainName += " to " + status.Destination
	}

	fromName, _ := station.Lookup(route.FromStationCrs)
	toName, _ := station.Lookup(route.ToStationCrs)

	statusIcon := "🟢"
	statusText := "On time"
	if status.IsCancelled {
		statusIcon = "🔴"
		statusText = "CANCELLED"
	} else if status.DelayMins > 0 {
		statusIcon = "🟠"
		statusText = fmt.Sprintf("Delayed %d min (exp. %s)", status.DelayMins, status.EstimatedDeparture.Format("15:04"))
	}

	platform := "TBC"
	if status.Platform != "" {
		platform = status.Platform
	}

	dep := status.ScheduledDeparture
	if status.DelayMins > 0 {
		dep = status.EstimatedDeparture
	}
	minsUntil := int(time.Until(dep).Minutes())
	if minsUntil < 0 {
		minsUntil = 0
	}

	return fmt.Sprintf(
		"⏰ Departure in %d min!\n🚆 %s\n🚉 %s (%s) → %s (%s)\n🚃 %s\n🔢 Platform: %s\n%s %s",
		minsUntil, route.Label,
		fromName, route.FromStationCrs, toName, route.ToStationCrs,
		trainName, platform, statusIcon, statusText,
	)
}

func isWithinAPIRange(route db.GetActiveRoutesWithChatIDRow, now time.Time) bool {
	depMins := pgTimeToMinutes(route.DepartureTime)
	nowMins := now.Hour()*60 + now.Minute()
	diff := depMins - nowMins
	return diff > 0 && diff <= maxAPIRangeMins
}

func isWithinChoiceRange(route db.GetActiveRoutesWithChatIDRow, now time.Time) bool {
	depMins := pgTimeToMinutes(route.DepartureTime)
	nowMins := now.Hour()*60 + now.Minute()
	diff := depMins - nowMins
	return diff > 0 && diff <= maxChoiceRangeMins
}

func routeFromRow(row db.GetActiveRoutesWithChatIDRow) db.Route {
	return db.Route{
		ID:             row.ID,
		UserID:         row.UserID,
		Label:          row.Label,
		FromStationCrs: row.FromStationCrs,
		ToStationCrs:   row.ToStationCrs,
		DepartureTime:  row.DepartureTime,
		DaysOfWeek:     row.DaysOfWeek,
		AlertOffsets:   row.AlertOffsets,
		IsActive:       row.IsActive,
	}
}

func isWithinAlertWindow(route db.GetActiveRoutesWithChatIDRow, now time.Time, offset int32) bool {
	depMins := pgTimeToMinutes(route.DepartureTime)
	nowMins := now.Hour()*60 + now.Minute()
	alertStart := depMins - int(offset)
	if alertStart < 0 {
		alertStart = 0
	}
	return nowMins >= alertStart && nowMins <= depMins
}

func isDeparted(route db.GetActiveRoutesWithChatIDRow, now time.Time) bool {
	depMins := pgTimeToMinutes(route.DepartureTime)
	nowMins := now.Hour()*60 + now.Minute()
	return nowMins > depMins
}

func statusChanged(previous, current *domain.TrainStatus) bool {
	if previous.Platform != current.Platform {
		return true
	}
	if previous.IsCancelled != current.IsCancelled {
		return true
	}
	if previous.DelayMins != current.DelayMins {
		return true
	}
	if !previous.EstimatedDeparture.Equal(current.EstimatedDeparture) {
		return true
	}
	return false
}

func formatAlert(route db.GetActiveRoutesWithChatIDRow, current *domain.TrainStatus, previous *domain.TrainStatus) string {
	fromName, _ := station.Lookup(route.FromStationCrs)
	toName, _ := station.Lookup(route.ToStationCrs)
	depTime := current.ScheduledDeparture.Format("15:04")
	trainName := depTime
	if current.Destination != "" {
		trainName += " to " + current.Destination
	}

	if previous == nil {
		statusIcon := "🟢"
		statusText := "On time"
		if current.IsCancelled {
			statusIcon = "🔴"
			statusText = "CANCELLED"
		} else if current.DelayMins > 0 {
			statusIcon = "🟠"
			statusText = fmt.Sprintf("Delayed %d min (exp. %s)", current.DelayMins, current.EstimatedDeparture.Format("15:04"))
		}

		platform := "TBC"
		if current.Platform != "" {
			platform = current.Platform
		}

		return fmt.Sprintf(
			"🚆 %s\n🚉 %s (%s) → %s (%s)\n🚃 %s\n🔢 Platform: %s\n%s %s",
			route.Label, fromName, route.FromStationCrs, toName, route.ToStationCrs,
			trainName, platform, statusIcon, statusText,
		)
	}

	var changes []string

	if previous.IsCancelled != current.IsCancelled && current.IsCancelled {
		changes = append(changes, "🔴 CANCELLED")
	}

	if previous.Platform != current.Platform && current.Platform != "" {
		changes = append(changes, fmt.Sprintf("🔀 Platform changed: %s → %s", orTBC(previous.Platform), current.Platform))
	}

	if previous.DelayMins != current.DelayMins {
		if current.DelayMins == 0 {
			changes = append(changes, "🟢 Now on time")
		} else {
			changes = append(changes, fmt.Sprintf("🟠 Delayed %d min (exp. %s)", current.DelayMins, current.EstimatedDeparture.Format("15:04")))
		}
	}

	if len(changes) == 0 {
		changes = append(changes, "ℹ️ Status updated")
	}

	header := fmt.Sprintf("🚆 %s\n🚉 %s (%s) → %s (%s)\n🚃 %s", route.Label, fromName, route.FromStationCrs, toName, route.ToStationCrs, trainName)
	result := header
	for _, c := range changes {
		result += "\n" + c
	}
	return result
}

func orTBC(s string) string {
	if s == "" {
		return "TBC"
	}
	return s
}
