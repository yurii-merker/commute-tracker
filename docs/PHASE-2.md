# Phase 2 — Edit Route Reminders [DONE]

## Goal

Let users change `alert_offsets` on an existing route without recreating it.

## Scope

- New `/edit` Telegram command
- Three new states: `StateAwaitingEditRoute`, `StateAwaitingEditField`, `StateAwaitingEditAlerts`
- Single editable field for now: `alert_offsets`
- Same-day reconciliation of pending reminders

## Out of scope

- Editing other fields (time, days, label) — designed-in extension point
- Editing via `/status`

## Deliverables

- [x] sqlc `UpdateRouteAlertOffsets` query
- [x] `/edit` handlers and callbacks
- [x] Reconciliation helper sharing `tracker.AlertSentKey`
- [x] Unit tests for state transitions, ownership, reconciliation table
- [x] `/edit` listed in `/help`
- [x] `docs/DFA.md` updated
