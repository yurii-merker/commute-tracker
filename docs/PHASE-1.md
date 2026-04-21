# Phase 1 — Scheduled Train Preview [DONE]

Add Realtime Trains (RTT) API as a timetable data source. Users see scheduled trains immediately during route creation, even when Darwin live data isn't available (>4h before departure). Two-mode system: **timetable** (RTT) → **live** (Darwin) with seamless daemon-driven transition and proactive notification.

Previous work (initial build) is in [PHASE-0.md](PHASE-0.md).

---

## Architecture Overview

```
Route Creation (bot)           Daemon (tracker)
        │                            │
        ▼                            ▼
  Within Darwin range? ──yes──► Darwin API (live)
        │no                          │
        ▼                            ▼
  RTT API (timetable)         Timetable cached?
        │                       │yes        │no
        ▼                       ▼            ▼
  Show scheduled options   Within Darwin?  RTT API
        │                   │yes    │no      │
        ▼                   ▼       ▼        ▼
  User picks train     Transition  Skip    Cache timetable
                       to live
                           │
                           ▼
                    Notify user: "🔔 Live data available"
```

### Key Design Decisions

- **RTT credentials are required** — bot fails to start without them. This is a core feature, not optional.
- **`IsScheduleOnly` flag on `TrainStatus`** — single boolean distinguishes timetable vs live data. Existing Redis cache is backward-compatible (defaults to `false` on unmarshal).
- **Target date logic** — if requested time already passed today, RTT queries tomorrow's timetable.
- **Daemon pre-fetches timetable** — ensures `/status` shows data for routes created on previous days.
- **Proactive transition notification** — user receives a message when their train switches from timetable to live data.
- **Alerts only fire with live data** — departure reminders require real-time status; timetable data is informational only.

### Go Patterns Applied

| Pattern | Where | Why |
|---------|-------|-----|
| **Domain interface** | `ScheduleClient` in `internal/domain/` | Consumed by planner, implemented by RTT client. Follows existing `TrainClient` pattern. |
| **Consumer-defined interface** | `RoutePlanner` extended in `internal/bot/` | Bot defines the subset of planner methods it needs, enabling isolated testing. |
| **Constructor injection** | `NewPlanner(queries, trainClient, scheduleClient, rdb)` | All dependencies explicit. No global state. Mirrors existing constructors. |
| **Unexported response types** | `internal/rtt/types.go` | JSON response structs are internal — only `domain.TrainStatus` crosses package boundaries. Mirrors Darwin's `xml.go`. |
| **Error classification** | `TransientError`/`PermanentError` in `internal/rtt/` | Same pattern as `internal/darwin/errors.go`. RTT errors don't affect circuit breaker (separate concern). |
| **Hand-written mocks** | Test files | Project convention — no code generation. Mock `ScheduleClient` follows existing `mockBoardClient` pattern. |
| **Table-driven tests** | All new test files | Project convention — named subtests with `t.Run()`. |

---

## Phase 14 — Domain & RTT Client

> Goal: `ScheduleClient` interface, `IsScheduleOnly` flag, working RTT HTTP client with JSON parsing.

### 14.1 Domain changes (`internal/domain/`)

- [x] Add `IsScheduleOnly bool` field to `TrainStatus` in `train.go`
  - JSON tag: `json:"is_schedule_only,omitempty"` — backward-compatible with existing Redis cache (unmarshals as `false`)
- [x] Add `ScheduleClient` interface to `interfaces.go`:
  ```go
  type ScheduleClient interface {
      GetScheduledDepartures(ctx context.Context, fromCRS, toCRS string, date time.Time, targetMins int) ([]TrainStatus, error)
  }
  ```
  - `targetMins` is minutes-since-midnight (0–1439), consistent with planner internals
  - Client formats it to `HHMM` for the RTT URL
  - Returns `[]TrainStatus` with `IsScheduleOnly: true` on all entries

### 14.2 Configuration (`internal/config/`)

- [x] Add required field to `Config`:
  ```go
  RTTToken string `env:"RTT_TOKEN,required"`
  ```
- [x] Update `.env.example` with `RTT_TOKEN`

### 14.3 RTT client (`internal/rtt/client.go`)

- [x] Constructor: `NewClient(token string) *Client`
  - `http.Client` with 10s timeout (same as Darwin)
  - Base URL: `https://data.rtt.io` (Next Generation API)
- [x] Implements `domain.ScheduleClient`
- [x] `GetScheduledDepartures(ctx, fromCRS, toCRS, date, targetMins)`:
  - Builds URL: `GET /gb-nr/location?code={fromCRS}&filterTo={toCRS}&timeFrom={ISO8601}&timeWindow=60`
  - Sets Bearer token auth via `Authorization: Bearer {token}`
  - Parses JSON response into unexported types
  - Maps to `[]domain.TrainStatus` via `mapServiceToTrainStatus()`
  - Filters: `inPassengerService == true` and `displayAs` in `["CALL", "STARTS"]`
  - Sets `IsScheduleOnly: true` on every result
  - Maps `temporalData.departure.scheduleAdvertised` (ISO 8601) → `ScheduledDeparture`
  - Maps `EstimatedDeparture = ScheduledDeparture` (no real-time data)
  - Maps first `destination[].location.description` → `Destination`
  - Maps `locationMetadata.platform.planned` → `Platform` (often nil)
  - `ServiceID` → `scheduleMetadata.identity` (note: NOT compatible with Darwin service IDs)
  - Handles HTTP 204 No Content → empty result, no error
- [x] Response size limit: 1MB (`io.LimitReader`) — same as Darwin
- [x] Rate limits: 30/min, 750/hour, 9000/day, 30000/week

### 14.4 RTT response types (`internal/rtt/types.go`)

- [x] Unexported structs matching RTT Next Generation JSON:
  ```go
  type searchResponse struct {
      Services []serviceInfo `json:"services"`
  }

  type serviceInfo struct {
      ScheduleMetadata scheduleMetadata `json:"scheduleMetadata"`
      TemporalData     temporalData     `json:"temporalData"`
      LocationMetadata locationMetadata `json:"locationMetadata"`
      Destination      []locationRef    `json:"destination"`
  }

  type scheduleMetadata struct {
      UniqueIdentity     string `json:"uniqueIdentity"`
      Identity           string `json:"identity"`
      InPassengerService bool   `json:"inPassengerService"`
  }

  type temporalData struct {
      Departure *individualTemporal `json:"departure"`
      DisplayAs string              `json:"displayAs"`
  }

  type individualTemporal struct {
      ScheduleAdvertised string `json:"scheduleAdvertised"`
      IsCancelled        bool   `json:"isCancelled"`
  }
  ```

### 14.5 RTT error types (`internal/rtt/errors.go`)

- [x] `TransientError` and `PermanentError` — same pattern as `internal/darwin/errors.go`
- [x] `IsTransient(err) bool` helper
- [x] HTTP 5xx → `TransientError`, HTTP 4xx → `PermanentError`
- [x] Network/timeout errors → `TransientError`
- [x] Note: RTT errors do NOT feed into the circuit breaker (circuit breaker is Darwin-only)

### 14.6 Tests (`internal/rtt/client_test.go`)

- [x] `TestGetScheduledDepartures` — httptest server, valid JSON response, verify parsed `[]TrainStatus`
- [x] Verify `IsScheduleOnly: true` on all results
- [x] Verify passenger-only filtering (`isPassenger: false` services excluded)
- [x] Verify `displayAs` filtering (only `CALL` and `ORIGIN`)
- [x] Empty services array → empty result, no error
- [x] HTTP 401 → `PermanentError`
- [x] HTTP 500 → `TransientError`
- [x] Network timeout → `TransientError`
- [x] `mapServiceToTrainStatus` — time parsing (4-digit `"0743"` → `time.Time`), destination mapping, platform mapping
- [x] Malformed JSON → error

---

## Phase 15 — Planner Extension

> Goal: planner can find and cache scheduled trains from RTT, alongside existing Darwin logic.

### 15.1 Planner struct extension (`internal/tracker/planner.go`)

- [x] Add `scheduleClient domain.ScheduleClient` field to `Planner`
- [x] Update constructor: `NewPlanner(queries, trainClient, scheduleClient, rdb)`
- [x] New exported method — `FindScheduledTrains`:
  ```go
  func (p *Planner) FindScheduledTrains(ctx context.Context, fromCRS, toCRS string, date time.Time, targetMins int) (*domain.NearestTrains, error)
  ```
  - Calls `p.scheduleClient.GetScheduledDepartures(ctx, fromCRS, toCRS, date, targetMins)`
  - Reuses existing `findClosestService()` for exact match within `matchToleranceMins`
  - If no exact, finds closest before/after (same algorithm as `FindNearestTrains`)
  - All returned statuses have `IsScheduleOnly: true` (set by RTT client)
- [x] New exported method — `PlanRouteFromSchedule`:
  ```go
  func (p *Planner) PlanRouteFromSchedule(ctx context.Context, route db.Route, date time.Time) (*domain.TrainStatus, error)
  ```
  - Calls `FindScheduledTrains` → takes the exact match or closest within `matchToleranceMins`
  - Caches result via `CacheService` (stored as `route_service:{routeID}` with `IsScheduleOnly: true`)
  - Returns the cached status
  - Mirrors `PlanRoute` but uses RTT instead of Darwin

### 15.2 Target date helper (`internal/tracker/planner.go`)

- [x] New unexported function:
  ```go
  func targetDate(targetMins int) time.Time {
      now := ukNow()
      nowMins := now.Hour()*60 + now.Minute()
      if targetMins <= nowMins {
          return now.AddDate(0, 0, 1) // tomorrow
      }
      return now // today
  }
  ```
  - Used by bot during route creation to determine which date to query RTT for

### 15.3 Update `RoutePlanner` interface (`internal/bot/bot.go`)

- [x] Add new methods to `RoutePlanner`:
  ```go
  type RoutePlanner interface {
      PlanRoute(ctx context.Context, route db.Route) (*domain.TrainStatus, error)
      PlanRouteFromSchedule(ctx context.Context, route db.Route, date time.Time) (*domain.TrainStatus, error)
      FindNearestTrains(ctx context.Context, fromCRS, toCRS string, targetMins int) (*domain.NearestTrains, error)
      FindScheduledTrains(ctx context.Context, fromCRS, toCRS string, date time.Time, targetMins int) (*domain.NearestTrains, error)
      CacheService(ctx context.Context, routeID pgtype.UUID, status *domain.TrainStatus) error
  }
  ```

### 15.4 Tests (`internal/tracker/planner_test.go`)

- [x] `TestFindScheduledTrains` — table-driven:
  - Exact match within tolerance → returns in `Exact`
  - No exact match → returns `Before` and/or `After`
  - All results have `IsScheduleOnly: true`
  - Empty services → returns empty `NearestTrains`
  - `ScheduleClient` error → propagated
- [x] `TestPlanRouteFromSchedule` — integration with miniredis:
  - Success → cached in Redis with `IsScheduleOnly: true`
  - No matching service → error
  - `ScheduleClient` error → error
- [x] `TestTargetDate`:
  - Current time 14:00, target 16:00 → today
  - Current time 21:00, target 07:00 → tomorrow
  - Current time 07:45, target 07:45 → today (edge: equal)
- [x] Update existing `TestNewPlanner` for new constructor signature
- [x] Add `mockScheduleClient` to test helpers

---

## Phase 16 — Route Creation Flow

> Goal: users see scheduled trains during route creation when Darwin data is unavailable.

### 16.1 `handleAwaitingTime` changes (`internal/bot/handlers.go`)

- [x] Current flow:
  ```
  if planner != nil && isWithinDarwinRange(targetMins):
      query Darwin → show options
  else:
      proceed to days (no train info)
  ```
- [x] New flow:
  ```
  if planner != nil && isWithinDarwinRange(targetMins):
      query Darwin → show options (live data, existing behavior)
  else if planner != nil:
      date = targetDate(targetMins)
      query RTT via FindScheduledTrains(fromCRS, toCRS, date, targetMins)
      if exact match → proceed to days with scheduled confirmation
      if before/after → show options with "📅 Scheduled" label
      if error/empty → proceed to days with friendly message
  else:
      proceed to days (no planner)
  ```

### 16.2 Scheduled train option formatting (`internal/bot/handlers.go`)

- [x] New function `formatScheduledTrainOptions(requestedTime string, nearest *domain.NearestTrains) (string, []trainOption)`:
  - Same structure as `formatTrainOptions` but header reads: `"📅 No exact scheduled train at %s. Nearest timetable options:"`
  - Options display scheduled departure times and destinations
- [x] New function `formatScheduledTrainFound(status *domain.TrainStatus) string`:
  - `"📅 Scheduled train: %s to %s (live updates start ~4h before departure)"`
  - No platform/delay info (timetable only)

### 16.3 `planRouteNow` changes (`internal/bot/handlers.go`)

- [x] Current flow:
  ```
  1. Try PlanRoute (Darwin) → show live status
  2. If fails, within Darwin range → try FindNearestTrains → show choice
  3. If fails → "Train data not yet available"
  ```
- [x] New flow:
  ```
  1. Try PlanRoute (Darwin) → show live status
  2. If fails, within Darwin range → try FindNearestTrains → show choice
  3. If fails → try PlanRouteFromSchedule (RTT) for target date
     → cache timetable data
     → show with formatScheduledTrainFound
  4. If all fail → "📅 Could not find scheduled trains. I'll keep checking and notify you when data is available."
  ```

### 16.4 Tests

- [x] `TestHandleAwaitingTimeRTTFallback` — Darwin out of range, RTT returns options
- [x] `TestHandleAwaitingTimeRTTExactMatch` — RTT finds exact scheduled train
- [x] `TestHandleAwaitingTimeRTTNoResults` — RTT returns empty, proceeds to days
- [x] `TestHandleAwaitingTimeRTTError` — RTT fails, proceeds to days with message
- [x] `TestPlanRouteNowRTTFallback` — Darwin fails, RTT succeeds, shows scheduled info
- [x] `TestPlanRouteNowAllFail` — both Darwin and RTT fail, shows friendly message
- [x] Update `mockRoutePlanner` with new methods

---

## Phase 17 — Daemon Changes

> Goal: daemon pre-fetches timetable data and transitions to live with proactive notification.

### 17.1 Daemon tick flow update (`internal/tracker/daemon.go`)

- [x] Restructure the per-route handling in `tick()`:
  ```
  1. Skip if departed (existing)
  2. Get cached service (existing)
  3. If cached AND IsScheduleOnly AND within Darwin range:
       → tryTransitionToLive(route, cached)
       → continue (wait for next tick to poll live)
  4. If cached AND NOT IsScheduleOnly:
       → checkRoute (existing live polling)
       → checkBetterTrain (existing)
       → check alerts (existing)
  5. If cached AND IsScheduleOnly AND NOT within Darwin range:
       → skip (already have timetable, wait for Darwin range)
  6. If NOT cached AND within Darwin range:
       → tryPlanRoute (existing Darwin planning)
  7. If NOT cached AND NOT within Darwin range:
       → tryFetchTimetable (new RTT pre-fetch)
  ```

### 17.2 Timetable pre-fetch (`internal/tracker/daemon.go`)

- [x] New method `tryFetchTimetable`:
  ```go
  func (d *Daemon) tryFetchTimetable(ctx context.Context, route db.GetActiveRoutesWithChatIDRow)
  ```
  - Calls `d.planner.PlanRouteFromSchedule(ctx, routeFromRow(route), ukNow())`
  - Always uses today's date (daemon runs for today's routes)
  - On success: log at DEBUG level
  - On failure: log at DEBUG level, set `timetable_backoff:{routeID}` with 1h TTL to avoid hammering RTT
  - Backoff check: skip if `timetable_backoff` key exists (retries after 1 hour)
  - No grace period or train choice logic needed (route already has correct departure time)

### 17.3 Timetable → live transition (`internal/tracker/daemon.go`)

- [x] New method `tryTransitionToLive`:
  ```go
  func (d *Daemon) tryTransitionToLive(ctx context.Context, route db.GetActiveRoutesWithChatIDRow, timetable *domain.TrainStatus)
  ```
  - Calls `d.planner.PlanRoute(ctx, routeFromRow(route))` (Darwin)
  - On success:
    - Live data replaces timetable in cache (PlanRoute calls CacheService internally)
    - Save as last known state
    - Send transition notification via `d.notifier.Send()`
    - Log at INFO level
  - On failure:
    - Keep timetable data (don't clear cache)
    - Log at DEBUG level
    - Retry next tick (Darwin data may not be fully available yet)

### 17.4 Transition notification format (`internal/tracker/daemon.go`)

- [x] New function `formatTransitionNotification`:
  ```go
  func formatTransitionNotification(route db.GetActiveRoutesWithChatIDRow, status *domain.TrainStatus) string
  ```
  - Format: `"🔔 Live data now available for [label]\n\n[standard status line]"`
  - Status line reuses logic from `formatAutoSelectStatus` (icon + text + platform + time → destination)

### 17.5 Guard existing logic against timetable data

- [x] `checkRoute`: already guarded by step 3/4 split in tick flow — only called for live data
- [x] `checkBetterTrain`: only called for live data (same split)
- [x] Alert reminders: only fired for live data (same split)
- [x] `sendDepartureReminder`: no changes needed — only reached for live cached data

### 17.6 Tests

- [x] `TestDaemonTimetablePreFetch` — route outside Darwin range, no cache → calls `PlanRouteFromSchedule`, caches result
- [x] `TestDaemonTimetablePreFetchFailure` — RTT fails → no cache, no error propagation
- [x] `TestDaemonTimetableToLiveTransition` — cached timetable + within Darwin range → calls `PlanRoute`, sends notification
- [x] `TestDaemonTimetableToLiveTransitionFailure` — Darwin fails → keeps timetable, retries next tick
- [x] `TestDaemonTimetableSkipWhenOutsideRange` — cached timetable + outside Darwin range → no API calls
- [x] `TestDaemonLiveFlowUnchanged` — cached live data → existing checkRoute/checkBetterTrain/alerts work unchanged
- [x] `TestDaemonNoAlertsForTimetable` — cached timetable → no departure reminders sent
- [x] `TestTransitionNotificationFormat` — verify message format

---

## Phase 18 — Status Command

> Goal: `/status` distinguishes timetable vs live data with clear labels.

### 18.1 `/status` display changes (`internal/bot/handlers.go`)

- [x] In `handleStatus`, modify the cached train display block:
  ```
  if train.IsScheduleOnly:
      "📅 Scheduled: 07:43 to London Waterloo (live updates closer to departure)"
  else:
      existing live display (platform, status, delay — unchanged)
  ```
- [x] No platform or delay info for timetable-only trains (would be misleading)
- [x] If no cached data at all (neither timetable nor live):
  - Show `"⏳ Awaiting train data"` instead of showing nothing
  - Only for active routes on today's weekday

### 18.2 Tests

- [x] `TestStatusWithTimetableData` — route with `IsScheduleOnly: true` cached → shows "📅 Scheduled" label
- [x] `TestStatusWithLiveData` — existing behavior unchanged
- [x] `TestStatusWithNoData` — no cache → shows awaiting message
- [x] `TestStatusMixedRoutes` — one timetable, one live → correct labels per route

---

## Phase 19 — Wiring & Documentation

> Goal: everything connected in main.go, docs up to date.

### 19.1 Wiring (`cmd/bot/main.go`)

- [x] Create RTT client after config load:
  ```go
  rttClient := rtt.NewClient(cfg.RTTToken)
  ```
- [x] Update `NewPlanner` call to include `rttClient`:
  ```go
  planner := tracker.NewPlanner(queries, trainClient, rttClient, rdb)
  ```
- [x] No other wiring changes — bot and daemon access RTT through the planner

### 19.2 Documentation

- [x] Update `CLAUDE.md`:
  - Add `internal/rtt/` to project structure
  - Add `RTT_TOKEN` to environment variables section
- [x] Update `docs/DFA.md`:
  - Add timetable → live transition flow
  - Update route creation sequence diagram with RTT fallback
  - Add scheduled data display in `/status`
- [x] Update `.env.example`:
  - Add `RTT_TOKEN=`

### 19.3 Lint & verify

- [x] `go test -race ./...` passes
- [x] `golangci-lint run ./...` passes with 0 issues
- [x] `go build ./cmd/bot` succeeds

---

## File Change Summary

| File | Change |
|------|--------|
| `internal/domain/train.go` | Add `IsScheduleOnly` field |
| `internal/domain/interfaces.go` | Add `ScheduleClient` interface |
| `internal/config/config.go` | Add `RTTToken` (required) |
| `internal/rtt/client.go` | **New** — RTT HTTP client |
| `internal/rtt/types.go` | **New** — JSON response types |
| `internal/rtt/errors.go` | **New** — error classification |
| `internal/rtt/client_test.go` | **New** — client tests |
| `internal/tracker/planner.go` | Add `scheduleClient` field, `FindScheduledTrains`, `PlanRouteFromSchedule`, `targetDate` |
| `internal/tracker/planner_test.go` | Add schedule-related tests, update constructor calls |
| `internal/bot/bot.go` | Extend `RoutePlanner` interface |
| `internal/bot/handlers.go` | Modify `handleAwaitingTime`, `planRouteNow`, `handleStatus`; add format functions |
| `internal/bot/handlers_test.go` | Add RTT fallback tests, update mock |
| `internal/tracker/daemon.go` | Add `tryFetchTimetable` (with 1h backoff), `tryTransitionToLive`, restructure tick flow |
| `internal/tracker/daemon_test.go` | Add timetable/transition tests |
| `cmd/bot/main.go` | Create RTT client, update `NewPlanner` call |
| `.env.example` | Add `RTT_TOKEN` |
| `CLAUDE.md` | Update project structure, env vars |
| `docs/DFA.md` | Update flows and diagrams |
