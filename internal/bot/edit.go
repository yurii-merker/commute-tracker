package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"gopkg.in/telebot.v4"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
	"github.com/yurii-merker/commute-tracker/internal/station"
	"github.com/yurii-merker/commute-tracker/internal/tracker"
)

func reconcileAlertOffsets(
	ctx context.Context,
	rdb *redis.Client,
	routeID pgtype.UUID,
	cached *domain.TrainStatus,
	now time.Time,
	oldOffsets []int32,
	newOffsets []int32,
) {
	oldSet := offsetSet(oldOffsets)
	newSet := offsetSet(newOffsets)

	if cached == nil {
		return
	}

	dep := todayDepartureAt(cached.ScheduledDeparture, now)
	if !now.Before(dep) {
		return
	}

	ttl := timeUntilEndOfDay()

	for o := range newSet {
		if oldSet[o] {
			continue
		}
		windowStart := dep.Add(-time.Duration(o) * time.Minute)
		if now.Before(windowStart) {
			continue
		}
		key := tracker.AlertSentKey(routeID, o)
		if err := rdb.Set(ctx, key, "1", ttl).Err(); err != nil {
			slog.Error("reconcile SET failed", "key", key, "error", err)
		}
	}
}

func offsetSet(s []int32) map[int32]bool {
	out := make(map[int32]bool, len(s))
	for _, v := range s {
		out[v] = true
	}
	return out
}

func todayDepartureAt(scheduled time.Time, now time.Time) time.Time {
	loc := now.Location()
	return time.Date(now.Year(), now.Month(), now.Day(), scheduled.Hour(), scheduled.Minute(), 0, 0, loc)
}

func (b *Bot) handleEdit(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	user, err := b.queries.GetUserByChatID(ctx, chatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.Send("Please use /start first.")
		}
		slog.Error("failed to get user", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	routes, err := b.queries.GetRoutesByUserID(ctx, user.ID)
	if err != nil {
		slog.Error("failed to get routes", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	if len(routes) == 0 {
		return c.Send("📭 You have no routes to edit. Use /add to create one.")
	}

	b.clearDraft(ctx, chatID)

	if len(routes) == 1 {
		return b.sendEditFieldMenu(c, ctx, chatID, routes[0])
	}

	var msg strings.Builder
	msg.WriteString("✏️ Which route do you want to edit?\n\n")

	menu := &telebot.ReplyMarkup{}
	var btns []telebot.Btn
	for i, r := range routes {
		fromName, _ := station.Lookup(r.FromStationCrs)
		toName, _ := station.Lookup(r.ToStationCrs)
		depTime := formatTime(r.DepartureTime)
		fmt.Fprintf(&msg, "%d. 📝 %s\n   📅 %s | 🕐 %s\n   🚉 %s (%s) → %s (%s)\n\n",
			i+1, r.Label,
			formatDaysMask(r.DaysOfWeek), depTime,
			fromName, r.FromStationCrs, toName, r.ToStationCrs)
		btns = append(btns, menu.Data(
			fmt.Sprintf("%d. %s", i+1, r.Label),
			"editroute",
			fmt.Sprintf("%d", i+1),
		))
	}
	menu.Inline(menu.Row(btns...))

	err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingEditRoute.String(),
	})
	if err != nil {
		slog.Error("failed to update user state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send(msg.String(), menu)
}

func (b *Bot) sendEditFieldMenu(c telebot.Context, ctx context.Context, chatID int64, route db.Route) error {
	if err := b.setDraftField(ctx, chatID, "edit_route_id", formatUUID(route.ID)); err != nil {
		slog.Error("failed to save edit draft", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	err := b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingEditField.String(),
	})
	if err != nil {
		slog.Error("failed to update user state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	depTime := formatTime(route.DepartureTime)
	msg := fmt.Sprintf(
		"📝 %s\n🕐 %s | 📅 %s | ⏰ %s before\n\nWhat do you want to change?",
		route.Label, depTime,
		formatDaysMask(route.DaysOfWeek), formatAlertOffsets(route.AlertOffsets),
	)

	menu := &telebot.ReplyMarkup{}
	btnAlerts := menu.Data("⏰ Edit reminders", "editfield", "alerts")
	btnCancel := menu.Data("❌ Cancel", "editcancel")
	menu.Inline(menu.Row(btnAlerts, btnCancel))

	return c.Send(msg, menu)
}

func (b *Bot) handleEditRouteCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_ = c.Respond()
	removeInlineButtons(c)

	user, err := b.queries.GetUserByChatID(ctx, chatID)
	if err != nil {
		slog.Error("failed to get user", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	routes, err := b.queries.GetRoutesByUserID(ctx, user.ID)
	if err != nil || len(routes) == 0 {
		slog.Error("failed to get routes for edit", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	idx := 0
	switch c.Callback().Data {
	case "1":
		idx = 0
	case "2":
		if len(routes) > 1 {
			idx = 1
		} else {
			return c.Send("⚠️ Something went wrong. Please try again.")
		}
	default:
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return b.sendEditFieldMenu(c, ctx, chatID, routes[idx])
}

func (b *Bot) handleEditFieldCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_ = c.Respond()
	removeInlineButtons(c)

	draft, err := b.getDraft(ctx, chatID)
	if err != nil || draft.EditRouteID == "" {
		slog.Error("edit draft expired or missing", "chat_id", chatID, "error", err)
		return c.Send("⏰ Your session expired. Please use /edit to start over.")
	}

	routeUUID, err := parseUUID(draft.EditRouteID)
	if err != nil {
		slog.Error("invalid route ID in edit draft", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	user, err := b.queries.GetUserByChatID(ctx, chatID)
	if err != nil {
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	route, err := b.queries.GetRouteByID(ctx, routeUUID)
	if err != nil {
		slog.Error("failed to get route for edit", "route_id", draft.EditRouteID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}
	if route.UserID != user.ID {
		slog.Warn("edit route ownership mismatch", "chat_id", chatID, "route_id", draft.EditRouteID)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	switch c.Callback().Data {
	case "alerts":
		err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
			TelegramChatID: chatID,
			State:          domain.StateAwaitingEditAlerts.String(),
		})
		if err != nil {
			slog.Error("failed to update state", "chat_id", chatID, "error", err)
			return c.Send("⚠️ Something went wrong. Please try again.")
		}
		return c.Send(fmt.Sprintf(
			"Current: %s\n\nEnter up to 3 values in minutes, space-separated (e.g. `60 30 10`):",
			formatAlertOffsets(route.AlertOffsets),
		), telebot.ModeMarkdown)
	default:
		return c.Send("⚠️ Unknown field. Please try /edit again.")
	}
}

func (b *Bot) handleEditCancelCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_ = c.Respond()
	removeInlineButtons(c)

	b.clearDraft(ctx, chatID)
	if err := b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateReady.String(),
	}); err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
	}
	return c.Send("👍 No changes made.")
}

func (b *Bot) handleAwaitingEditAlerts(c telebot.Context, ctx context.Context, chatID int64, text string) error {
	offsets, err := parseAlertOffsets(text)
	if err != nil {
		return c.Send("❌ "+err.Error()+"\n\nEnter up to 3 values in minutes, space-separated (e.g. `60 30 10`):", telebot.ModeMarkdown)
	}

	draft, err := b.getDraft(ctx, chatID)
	if err != nil || draft.EditRouteID == "" {
		slog.Error("edit draft expired", "chat_id", chatID, "error", err)
		return c.Send("⏰ Your session expired. Please use /edit to start over.")
	}

	routeUUID, err := parseUUID(draft.EditRouteID)
	if err != nil {
		slog.Error("invalid route ID in edit draft", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	user, err := b.queries.GetUserByChatID(ctx, chatID)
	if err != nil {
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	route, err := b.queries.GetRouteByID(ctx, routeUUID)
	if err != nil {
		slog.Error("failed to get route for edit", "route_id", draft.EditRouteID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}
	if route.UserID != user.ID {
		slog.Warn("edit route ownership mismatch on save", "chat_id", chatID, "route_id", draft.EditRouteID)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	oldOffsets := route.AlertOffsets

	if err := b.queries.UpdateRouteAlertOffsets(ctx, db.UpdateRouteAlertOffsetsParams{
		ID:           routeUUID,
		AlertOffsets: offsets,
	}); err != nil {
		slog.Error("failed to update alert offsets", "route_id", draft.EditRouteID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	cached, cacheErr := b.getCachedTrainStatus(ctx, routeUUID)
	if cacheErr != nil {
		slog.Warn("failed to read cached service during reconcile", "route_id", draft.EditRouteID, "error", cacheErr)
	}
	reconcileAlertOffsets(ctx, b.rdb, routeUUID, cached, ukNow(), oldOffsets, offsets)

	b.clearDraft(ctx, chatID)
	if err := b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateReady.String(),
	}); err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
	}

	return c.Send(fmt.Sprintf("✅ Reminders updated to %s before departure.", formatAlertOffsets(offsets)))
}
