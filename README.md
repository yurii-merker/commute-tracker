# Commute Tracker

A Telegram bot that monitors UK train routes and sends real-time push notifications about delays, cancellations, and platform changes. Built on the National Rail Darwin OpenLDBWS API for live data and the Realtime Trains API for timetable/schedule data.

## Features

- Configure up to 2 daily train routes per user (e.g., morning and evening commute)
- Guided route creation with fuzzy station search, scheduled/live train lookup, and inline button UI
- Days-of-week scheduling (weekdays, weekends, specific days via bitmask)
- Automatic daily planning — resolves train service IDs at 02:00 AM UK time
- Real-time polling every 2 minutes with smart diffing (only notifies on state changes)
- Push notifications for delays, cancellations, and platform changes
- Circuit breaker for API downtime with automatic recovery and user broadcast
- Scheduled train preview — see timetable data immediately when creating routes for future times
- Seamless timetable → live transition with proactive "Live data now available" notification
- Station code validation and fuzzy search against the official CRS dictionary

## Bot Commands

| Command   | Description                                          |
|-----------|------------------------------------------------------|
| `/start`  | Register and see a welcome message                   |
| `/add`    | Add a new route (guided flow with train discovery)   |
| `/status` | View your active routes and their current status     |
| `/delete` | Remove a route (inline buttons if multiple)          |
| `/stop`   | Deactivate all active routes                         |
| `/resume` | Reactivate all paused routes                         |

## How It Works

1. **Route creation** — `/add` walks you through a guided flow: pick origin and destination stations (by name or CRS code), enter a departure time, choose days of the week, and give the route a label. If the departure is within Darwin's live range (~4h), the bot queries Darwin for matching trains. Otherwise, it queries the Realtime Trains timetable API to show scheduled trains immediately. Inline buttons are shown if the exact time doesn't match.

2. **Timetable pre-fetch** — The daemon fetches timetable data from Realtime Trains for routes that don't have live data yet. `/status` shows these as "📅 Scheduled" until live data becomes available. On RTT failure, a 1-hour backoff prevents excessive retries.

3. **Timetable → live transition** — When Darwin live data becomes available (~4h before departure), the daemon automatically transitions from timetable to live data and sends a "🔔 Live data now available" notification with the current train status.

4. **Real-time monitoring (every 2 min)** — The tracker daemon polls each cached live service. It compares the latest status against the last known state in Redis and only notifies you when something changes (delay, platform change, cancellation, or recovery to on-time). Departure reminders only fire with live data.

5. **Circuit breaker** — After 3 consecutive Darwin API failures, the bot enters `API_DOWN` mode and broadcasts a warning to opted-in users. A background health check pings the API every 5 minutes and automatically recovers when the API stabilizes.

## Limitations and Edge Cases

- **UK trains only** — The bot uses the National Rail Darwin OpenLDBWS API, which covers mainline rail services in Great Britain. London Underground, trams, and bus services are not supported.
- **Max 2 routes per user** — Enforced to stay within Darwin API rate limits (3M requests/month free tier).
- **All times are UK time** — Departure times, planning, and alert windows use `Europe/London` (handles GMT/BST automatically).
- **Two data sources** — Live data comes from Darwin (available ~4h before departure). Timetable data comes from Realtime Trains (available for any future date). The bot automatically uses whichever is available.
- **5-minute match tolerance** — The daily planner auto-matches your configured departure time to the closest real service within ±5 minutes. If no exact match is found, the bot offers the nearest earlier and later trains via inline buttons so you can pick one. If you don't pick, no alert is sent for that day.
- **Single bot instance** — There is no horizontal scaling or distributed locking. Run one instance at a time to avoid duplicate notifications.

## Getting Started

### Prerequisites

- Go 1.26+
- Docker & Docker Compose
- National Rail Darwin API token ([register here](https://realtime.nationalrail.co.uk/OpenLDBWSRegistration))
- Realtime Trains API token ([register here](https://api-portal.rtt.io))
- Telegram Bot token (via [@BotFather](https://t.me/BotFather))

### Setup

1. Clone the repository:
   ```bash
   git clone https://github.com/yurii-merker/commute-tracker.git
   cd commute-tracker
   ```

2. Copy the environment template and fill in your tokens:
   ```bash
   cp .env.example .env
   ```

3. Start the infrastructure:
   ```bash
   make docker-up
   ```

4. Run database migrations:
   ```bash
   make migrate-up
   ```

5. Run the bot:
   ```bash
   make run
   ```

## Tech Stack

| Component       | Technology                          |
|-----------------|-------------------------------------|
| Language      | Go 1.26                                                      |
| Bot Framework | [telebot v4](https://gopkg.in/telebot.v4)                    |
| Database      | PostgreSQL 18                                                 |
| Migrations    | [golang-migrate](https://github.com/golang-migrate/migrate)  |
| Query Gen     | [sqlc](https://sqlc.dev)                                      |
| Cache / Queue | Redis 8                                                       |
| Linting       | [golangci-lint](https://golangci-lint.run)                    |
| Mocks         | [mockgen](https://github.com/uber-go/mock) (`go.uber.org/mock`) |
| Train API (live) | National Rail Darwin OpenLDBWS (SOAP)                      |
| Train API (timetable) | Realtime Trains Next Generation (REST/JSON)          |
| Deployment    | Docker Compose                                                |

## Project Structure

```
commute-tracker/
├── cmd/
│   └── bot/                # Application entrypoint
│       └── main.go
├── internal/
│   ├── bot/                # Telegram bot setup, handlers, state machine, inline button UI
│   ├── config/             # App configuration (env loading)
│   ├── darwin/             # National Rail SOAP client (TrainClient impl)
│   ├── db/                 # sqlc generated code + DB connection
│   ├── domain/             # Domain models + interfaces (TrainClient, ScheduleClient, Notifier)
│   ├── redis/              # Redis client (cache + future task queue)
│   ├── rtt/                # Realtime Trains REST client (ScheduleClient impl)
│   ├── station/            # UK CRS station codes with fuzzy search (embedded JSON)
│   ├── timezone/           # UK timezone helper (Europe/London)
│   └── tracker/            # Daily Planner, Tracker Daemon, Circuit Breaker
├── migrations/             # PostgreSQL migration files (golang-migrate)
├── queries/                # sqlc SQL query definitions
├── docs/                   # Design doc (DFA.md) and phase plans (PHASE-0.md, PHASE-1.md)
├── docker-compose.yml
├── Dockerfile
├── Makefile
├── sqlc.yaml
└── go.mod
```

## License

See [LICENSE](LICENSE) for details.