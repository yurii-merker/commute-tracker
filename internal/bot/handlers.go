package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"gopkg.in/telebot.v4"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
	"github.com/yurii-merker/commute-tracker/internal/station"
	"github.com/yurii-merker/commute-tracker/internal/timezone"
)

const (
	maxRoutesPerUser = 2
	handlerTimeout   = 10 * time.Second
)

func (b *Bot) handleHelp(c telebot.Context) error {
	return c.Send(
		"🚆 *Commute Tracker — Help*\n\n"+
			"*Commands:*\n"+
			"/add — Set up a new route (max 2)\n"+
			"/status — View your routes and live train info\n"+
			"/stop — Pause all route monitoring\n"+
			"/resume — Resume paused monitoring\n"+
			"/delete — Remove a route\n"+
			"/systemalerts — Toggle API outage notifications\n"+
			"/help — Show this message\n\n"+
			"*How it works:*\n"+
			"1️⃣ Add a route with /add — enter departure station, destination, time, days, and a label.\n"+
			"2️⃣ I'll automatically monitor your trains and send alerts about delays, cancellations, and platform changes.\n"+
			"3️⃣ You'll get a reminder 30 minutes before departure.\n\n"+
			"*Tips:*\n"+
			"• Station names and CRS codes both work (e.g. \"Kings Cross\" or KGX).\n"+
			"• For days, type weekdays, weekends, all, or specific days like Mon, Tue.\n"+
			"• If no exact train is found, I'll suggest the nearest options.",
		telebot.ModeMarkdown,
	)
}

func (b *Bot) handleStart(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_, err := b.queries.CreateUser(ctx, db.CreateUserParams{
		TelegramChatID: chatID,
		State:          domain.StateNew.String(),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("failed to create user", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send(
		"🚆 Welcome to Commute Tracker!\n\n" +
			"I'll monitor your UK train routes and send you real-time alerts about delays, cancellations, and platform changes.\n\n" +
			"Use /add to set up your first route.",
	)
}

func (b *Bot) handleAdd(c telebot.Context) error {
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

	count, err := b.queries.CountRoutesByUserID(ctx, user.ID)
	if err != nil {
		slog.Error("failed to count routes", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	if count >= maxRoutesPerUser {
		return c.Send(fmt.Sprintf("You already have %d routes (max %d). Use /delete to remove one first.", count, maxRoutesPerUser))
	}

	b.clearDraft(ctx, chatID)

	err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingFrom.String(),
	})
	if err != nil {
		slog.Error("failed to update user state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send("🚉 Enter the departure station name or code (e.g. \"Kings Cross\" or KGX):")
}

func (b *Bot) handleStatus(c telebot.Context) error {
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
		return c.Send("📭 You have no routes configured. Use /add to create one.")
	}

	var msg strings.Builder
	msg.WriteString("📋 Your routes:\n\n")
	for i, r := range routes {
		fromName, _ := station.Lookup(r.FromStationCrs)
		toName, _ := station.Lookup(r.ToStationCrs)
		statusIcon := "🟢"
		if !r.IsActive {
			statusIcon = "⏸️"
		}
		depTime := formatTime(r.DepartureTime)
		fmt.Fprintf(&msg,
			"%d. %s %s\n   📅 %s | 🕐 %s | ⏰ %s before\n   🚉 %s (%s) → %s (%s)\n",
			i+1, statusIcon, r.Label,
			formatDaysMask(r.DaysOfWeek), depTime, formatAlertOffsets(r.AlertOffsets),
			fromName, r.FromStationCrs, toName, r.ToStationCrs,
		)

		if r.IsActive {
			train, err := b.getCachedTrainStatus(ctx, r.ID)
			if err == nil && train != nil && train.ScheduledDeparture.After(timezone.Now()) {
				if train.IsScheduleOnly {
					trainName := train.ScheduledDeparture.Format("15:04")
					if train.Destination != "" {
						trainName += " to " + train.Destination
					}
					fmt.Fprintf(&msg, "   📅 Scheduled: %s (live updates closer to departure)\n", trainName)
				} else {
					trainTime := train.ScheduledDeparture.Format("15:04")
					if trainTime != depTime {
						fmt.Fprintf(&msg, "   🔄 No exact match — tracking %s today\n", trainTime)
					}
					trainName := trainTime
					if train.Destination != "" {
						trainName += " to " + train.Destination
					}
					platform := "TBC"
					if train.Platform != "" {
						platform = train.Platform
					}
					trainStatus := "On time"
					if train.IsCancelled {
						trainStatus = "CANCELLED"
					} else if train.DelayMins > 0 {
						trainStatus = fmt.Sprintf("Delayed %d min (exp. %s)", train.DelayMins, train.EstimatedDeparture.Format("15:04"))
					}
					fmt.Fprintf(&msg, "   🚃 %s | Plt %s | %s\n", trainName, platform, trainStatus)
				}
			} else if err == nil && train == nil {
				fmt.Fprintf(&msg, "   ⏳ Awaiting train data\n")
			}
		}

		msg.WriteString("\n")
	}

	return c.Send(msg.String())
}

func (b *Bot) handleStop(c telebot.Context) error {
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

	deactivated := 0
	for _, r := range routes {
		if r.IsActive {
			err := b.queries.UpdateRouteActive(ctx, db.UpdateRouteActiveParams{
				ID:       r.ID,
				IsActive: false,
			})
			if err != nil {
				slog.Error("failed to deactivate route", "route_id", r.ID, "error", err)
				continue
			}
			deactivated++
		}
	}

	if deactivated == 0 {
		return c.Send("⏸️ No active routes to stop.")
	}

	return c.Send(fmt.Sprintf("⏸️ Stopped monitoring %d route(s). Use /resume to reactivate.", deactivated))
}

func (b *Bot) handleResume(c telebot.Context) error {
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

	activated := 0
	for _, r := range routes {
		if !r.IsActive {
			err := b.queries.UpdateRouteActive(ctx, db.UpdateRouteActiveParams{
				ID:       r.ID,
				IsActive: true,
			})
			if err != nil {
				slog.Error("failed to activate route", "route_id", r.ID, "error", err)
				continue
			}
			activated++
		}
	}

	if activated == 0 {
		return c.Send("▶️ No paused routes to resume.")
	}

	return c.Send(fmt.Sprintf("▶️ Resumed monitoring %d route(s).", activated))
}

func (b *Bot) handleSystemAlerts(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	enabled, err := b.queries.ToggleSystemAlerts(ctx, chatID)
	if err != nil {
		slog.Error("failed to toggle system alerts", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	if enabled {
		return c.Send("🔔 System alerts enabled. You'll receive notifications about API outages and recoveries.")
	}
	return c.Send("🔕 System alerts disabled.")
}

func (b *Bot) handleDelete(c telebot.Context) error {
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
		return c.Send("📭 You have no routes to delete.")
	}

	if len(routes) == 1 {
		return b.sendDeleteConfirmation(c, ctx, chatID, routes[0], "1")
	}

	var msg strings.Builder
	msg.WriteString("🗑️ Which route do you want to delete?\n\n")

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
			"delete",
			fmt.Sprintf("%d", i+1),
		))
	}
	menu.Inline(menu.Row(btns...))

	err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingDelete.String(),
	})
	if err != nil {
		slog.Error("failed to update user state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send(msg.String(), menu)
}

func (b *Bot) handleText(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID
	text := strings.TrimSpace(c.Text())

	user, err := b.queries.GetUserByChatID(ctx, chatID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.Send("Please use /start first.")
		}
		slog.Error("failed to get user", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	switch domain.UserState(user.State) {
	case domain.StateAwaitingFrom:
		return b.handleAwaitingFrom(c, ctx, chatID, text)
	case domain.StateAwaitingTo:
		return b.handleAwaitingTo(c, ctx, chatID, text)
	case domain.StateAwaitingTime:
		return b.handleAwaitingTime(c, ctx, chatID, text)
	case domain.StateAwaitingTrainChoice:
		return b.handleAwaitingTrainChoice(c, ctx, chatID, text)
	case domain.StateAwaitingDays:
		return b.handleAwaitingDays(c, ctx, chatID, text)
	case domain.StateAwaitingAlerts:
		return b.handleAwaitingAlerts(c, ctx, chatID, text)
	case domain.StateAwaitingLabel:
		return b.handleAwaitingLabel(c, ctx, chatID, text, user.ID)
	case domain.StateAwaitingDelete:
		return b.handleAwaitingDelete(c, ctx, chatID, text, user.ID)
	default:
		return c.Send("🤔 I didn't understand that. Use /help to see available commands.")
	}
}

func (b *Bot) handleDeleteCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_ = c.Respond()
	removeInlineButtons(c)

	user, err := b.queries.GetUserByChatID(ctx, chatID)
	if err != nil {
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return b.handleAwaitingDelete(c, ctx, chatID, c.Callback().Data, user.ID)
}

func (b *Bot) handleTrainCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_ = c.Respond()
	removeInlineButtons(c)

	return b.handleAwaitingTrainChoice(c, ctx, chatID, c.Callback().Data)
}

func (b *Bot) handleRouteTrainCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_ = c.Respond()
	removeInlineButtons(c)

	parts := strings.SplitN(c.Callback().Data, "|", 2)
	if len(parts) != 2 {
		return c.Send("⚠️ Something went wrong. Please try again.")
	}
	routeIDStr, chosenTime := parts[0], parts[1]

	routeUUID, err := parseUUID(routeIDStr)
	if err != nil {
		slog.Error("invalid route ID in callback", "data", c.Callback().Data, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	user, err := b.queries.GetUserByChatID(ctx, chatID)
	if err != nil {
		slog.Error("failed to get user", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	route, err := b.queries.GetRouteByID(ctx, routeUUID)
	if err != nil {
		slog.Error("failed to get route", "route_id", routeIDStr, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	if route.UserID != user.ID {
		slog.Warn("route ownership mismatch", "chat_id", chatID, "route_id", routeIDStr)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	t, err := time.Parse("15:04", chosenTime)
	if err != nil {
		slog.Error("invalid time in callback", "data", c.Callback().Data, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	b.rdb.Del(ctx, "choice_sent:"+routeIDStr)

	if b.planner != nil {
		tempRoute := route
		tempRoute.DepartureTime = pgtype.Time{
			Microseconds: int64(t.Hour())*3600000000 + int64(t.Minute())*60000000,
			Valid:        true,
		}
		status, planErr := b.planner.PlanRoute(ctx, tempRoute)
		if planErr == nil {
			return c.Send(fmt.Sprintf("✅ Tracking %s train\n\n%s", chosenTime, formatTrainFound(status)))
		}
	}

	return c.Send(fmt.Sprintf("✅ Tracking %s train. Monitoring will start shortly.", chosenTime))
}

func (b *Bot) handleBetterSwitchCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_ = c.Respond()
	removeInlineButtons(c)

	parts := strings.SplitN(c.Callback().Data, "|", 2)
	if len(parts) != 2 {
		return c.Send("Something went wrong. Please try again.")
	}
	routeIDStr, betterTime := parts[0], parts[1]

	routeUUID, err := parseUUID(routeIDStr)
	if err != nil {
		slog.Error("invalid route ID in better switch callback", "data", c.Callback().Data, "error", err)
		return c.Send("Something went wrong. Please try again.")
	}

	user, err := b.queries.GetUserByChatID(ctx, chatID)
	if err != nil {
		slog.Error("failed to get user", "chat_id", chatID, "error", err)
		return c.Send("Something went wrong. Please try again.")
	}

	route, err := b.queries.GetRouteByID(ctx, routeUUID)
	if err != nil {
		slog.Error("failed to get route", "route_id", routeIDStr, "error", err)
		return c.Send("Something went wrong. Please try again.")
	}

	if route.UserID != user.ID {
		slog.Warn("route ownership mismatch in better switch", "chat_id", chatID, "route_id", routeIDStr)
		return c.Send("Something went wrong. Please try again.")
	}

	t, err := time.Parse("15:04", betterTime)
	if err != nil {
		slog.Error("invalid time in better switch callback", "data", c.Callback().Data, "error", err)
		return c.Send("Something went wrong. Please try again.")
	}

	b.rdb.Del(ctx, "better_offered:"+routeIDStr)

	if b.planner != nil {
		tempRoute := route
		tempRoute.DepartureTime = pgtype.Time{
			Microseconds: int64(t.Hour())*3600000000 + int64(t.Minute())*60000000,
			Valid:        true,
		}
		status, planErr := b.planner.PlanRoute(ctx, tempRoute)
		if planErr == nil {
			return c.Send(fmt.Sprintf("✅ Switched to %s train\n\n%s", betterTime, formatTrainFound(status)))
		}
		slog.Error("failed to plan better train", "route_id", routeIDStr, "error", planErr)
	}

	return c.Send(fmt.Sprintf("⚠️ Train %s is no longer available. Keeping current train.", betterTime))
}

func (b *Bot) handleBetterKeepCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()

	_ = c.Respond()
	removeInlineButtons(c)

	routeIDStr := c.Callback().Data

	routeUUID, err := parseUUID(routeIDStr)
	if err != nil {
		slog.Error("invalid route ID in better keep callback", "data", c.Callback().Data, "error", err)
		return c.Send("Something went wrong. Please try again.")
	}

	uid := formatUUID(routeUUID)
	b.rdb.Set(ctx, "better_declined:"+uid, "1", timeUntilEndOfDay())
	b.rdb.Del(ctx, "better_offered:"+uid)

	return c.Send("👌 Keeping current train for today.")
}

func removeInlineButtons(c telebot.Context) {
	defer func() { _ = recover() }()
	if msg := c.Message(); msg != nil {
		_, _ = c.Bot().Edit(msg, msg.Text)
	}
}

func parseUUID(s string) (pgtype.UUID, error) {
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID length: %d", len(s))
	}
	var b [16]byte
	for i := 0; i < 16; i++ {
		high, err := hexVal(s[i*2])
		if err != nil {
			return pgtype.UUID{}, err
		}
		low, err := hexVal(s[i*2+1])
		if err != nil {
			return pgtype.UUID{}, err
		}
		b[i] = high<<4 | low
	}
	return pgtype.UUID{Bytes: b, Valid: true}, nil
}

func hexVal(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	default:
		return 0, fmt.Errorf("invalid hex character: %c", c)
	}
}

func (b *Bot) handleAwaitingFrom(c telebot.Context, ctx context.Context, chatID int64, text string) error {
	crs, name, err := resolveStation(text)
	if err != nil {
		return c.Send(err.Error())
	}
	if crs == "" {
		return c.Send(formatStationChoices(text))
	}

	if err := b.setDraftField(ctx, chatID, "from", crs); err != nil {
		slog.Error("failed to save draft", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingTo.String(),
	})
	if err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send(fmt.Sprintf("✅ From: %s (%s)\n\n🚉 Enter the destination station (name or code):", name, crs))
}

func (b *Bot) handleAwaitingTo(c telebot.Context, ctx context.Context, chatID int64, text string) error {
	crs, name, err := resolveStation(text)
	if err != nil {
		return c.Send(err.Error())
	}
	if crs == "" {
		return c.Send(formatStationChoices(text))
	}

	draft, draftErr := b.getDraft(ctx, chatID)
	if draftErr != nil || draft.FromCRS == "" {
		slog.Error("draft expired or missing", "chat_id", chatID, "error", draftErr)
		return c.Send("⏰ Your session expired. Please use /add to start over.")
	}

	if crs == draft.FromCRS {
		return c.Send("❌ Destination must be different from departure station. Try again:")
	}

	if err := b.setDraftField(ctx, chatID, "to", crs); err != nil {
		slog.Error("failed to save draft", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingTime.String(),
	})
	if err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send(fmt.Sprintf("✅ To: %s (%s)\n\n🕐 Enter your departure time (HH:MM, 24h format, e.g. 07:45):", name, crs))
}

func (b *Bot) handleAwaitingTime(c telebot.Context, ctx context.Context, chatID int64, text string) error {
	text = normalizeTime(text)
	if !isValidTimeFormat(text) {
		return c.Send("❌ Invalid time format. Please use HH:MM in 24-hour format (e.g. 07:45, 18:30):")
	}

	draft, err := b.getDraft(ctx, chatID)
	if err != nil || draft.FromCRS == "" || draft.ToCRS == "" {
		slog.Error("draft expired or missing", "chat_id", chatID, "error", err)
		return c.Send("⏰ Your session expired. Please use /add to start over.")
	}

	t, _ := time.Parse("15:04", text)
	targetMins := t.Hour()*60 + t.Minute()

	if b.planner != nil && isWithinDarwinRange(targetMins) {
		nearest, searchErr := b.planner.FindNearestTrains(ctx, draft.FromCRS, draft.ToCRS, targetMins)
		if searchErr != nil {
			slog.Debug("train search unavailable", "chat_id", chatID, "error", searchErr)
		} else if nearest.Exact != nil {
			return b.proceedToDays(c, ctx, chatID, nearest.Exact.ScheduledDeparture.Format("15:04"))
		} else {
			if nearest.Before != nil || nearest.After != nil {
				return b.showTrainChoices(c, ctx, chatID, text, nearest)
			}
			return c.Send(fmt.Sprintf("❌ No trains found from %s to %s around %s. Try a different time:",
				draft.FromCRS, draft.ToCRS, text))
		}
	} else if b.planner != nil {
		date := targetDate(targetMins)
		nearest, searchErr := b.planner.FindScheduledTrains(ctx, draft.FromCRS, draft.ToCRS, date, targetMins)
		if searchErr != nil {
			slog.Debug("scheduled train search unavailable", "chat_id", chatID, "error", searchErr)
		} else if nearest.Exact != nil {
			return b.proceedToDays(c, ctx, chatID, nearest.Exact.ScheduledDeparture.Format("15:04"))
		} else if nearest.Before != nil || nearest.After != nil {
			return b.showTrainChoices(c, ctx, chatID, text, nearest)
		}
	}

	return b.proceedToDays(c, ctx, chatID, text)
}

func (b *Bot) handleAwaitingTrainChoice(c telebot.Context, ctx context.Context, chatID int64, text string) error {
	draft, err := b.getDraft(ctx, chatID)
	if err != nil || draft.FromCRS == "" || draft.ToCRS == "" {
		slog.Error("draft expired or missing", "chat_id", chatID, "error", err)
		return c.Send("⏰ Your session expired. Please use /add to start over.")
	}

	opt1 := draft.TrainOption1
	opt2 := draft.TrainOption2

	var selectedTime string
	switch text {
	case "1":
		if opt1 == "" {
			return c.Send("❌ Invalid choice. Please reply with a valid number:")
		}
		selectedTime = opt1
	case "2":
		if opt2 == "" {
			return c.Send("❌ Invalid choice. Please reply with 1:")
		}
		selectedTime = opt2
	default:
		normalized := normalizeTime(text)
		if isValidTimeFormat(normalized) {
			return b.handleAwaitingTime(c, ctx, chatID, text)
		}
		if opt2 != "" {
			return c.Send("❌ Please reply with 1, 2, or enter a different time (HH:MM):")
		}
		return c.Send("❌ Please reply with 1 or enter a different time (HH:MM):")
	}

	return b.proceedToDays(c, ctx, chatID, selectedTime)
}

func (b *Bot) showTrainChoices(c telebot.Context, ctx context.Context, chatID int64, requestedTime string, nearest *domain.NearestTrains) error {
	msg, options := formatTrainOptions(requestedTime, nearest)
	for i, opt := range options {
		if err := b.setDraftField(ctx, chatID, fmt.Sprintf("train_option_%d", i+1), opt.Time); err != nil {
			slog.Error("failed to save train option", "chat_id", chatID, "error", err)
			return c.Send("⚠️ Something went wrong. Please try again.")
		}
	}
	err := b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingTrainChoice.String(),
	})
	if err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	menu := &telebot.ReplyMarkup{}
	var btns []telebot.Btn
	for i, opt := range options {
		btns = append(btns, menu.Data(opt.ButtonLabel, "train", fmt.Sprintf("%d", i+1)))
	}
	menu.Inline(menu.Row(btns...))
	return c.Send(msg, menu)
}

func targetDate(targetMins int) time.Time {
	now := ukNow()
	nowMins := now.Hour()*60 + now.Minute()
	if targetMins <= nowMins {
		return now.AddDate(0, 0, 1)
	}
	return now
}

func (b *Bot) proceedToDays(c telebot.Context, ctx context.Context, chatID int64, timeStr string) error {
	if err := b.setDraftField(ctx, chatID, "time", timeStr); err != nil {
		slog.Error("failed to save draft", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	err := b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingDays.String(),
	})
	if err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send(fmt.Sprintf("✅ Departure at %s\n\n📅 Which days should this route be active?\n"+
		"Enter day names or shortcuts:\n\n"+
		"Examples:\n"+
		"  `weekdays` — Mon to Fri\n"+
		"  `weekends` — Sat, Sun\n"+
		"  `all` — all week\n"+
		"  `Mon Wed Fri` — specific days", timeStr), telebot.ModeMarkdown)
}

func (b *Bot) handleAwaitingDays(c telebot.Context, ctx context.Context, chatID int64, text string) error {
	mask, err := parseDaysMask(text)
	if err != nil {
		return c.Send("❌ "+err.Error()+"\n\nEnter day names, or use `weekdays`, `weekends`, `all`:", telebot.ModeMarkdown)
	}

	if err := b.setDraftField(ctx, chatID, "days", fmt.Sprintf("%d", mask)); err != nil {
		slog.Error("failed to save draft", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingAlerts.String(),
	})
	if err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send(fmt.Sprintf("✅ Active on: %s\n\n⏰ When should I remind you? Enter up to 3 values in minutes, space-separated (e.g. `60 30 10`):", formatDaysMask(mask)), telebot.ModeMarkdown)
}

func parseAlertOffsets(input string) ([]int32, error) {
	parts := strings.Fields(input)
	seen := make(map[int32]bool)
	var offsets []int32
	for _, p := range parts {
		val, err := strconv.ParseInt(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("'%s' is not a valid number", p)
		}
		v := int32(val)
		if v < 1 || v > 180 {
			return nil, fmt.Errorf("%d is out of range (1-180 minutes)", v)
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		offsets = append(offsets, v)
	}
	if len(offsets) == 0 {
		return nil, fmt.Errorf("enter at least 1 reminder time")
	}
	if len(offsets) > 3 {
		return nil, fmt.Errorf("maximum 3 reminders allowed, got %d", len(offsets))
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] > offsets[j] })
	return offsets, nil
}

func formatAlertOffsets(offsets []int32) string {
	parts := make([]string, len(offsets))
	for i, o := range offsets {
		if o >= 60 && o%60 == 0 {
			parts[i] = fmt.Sprintf("%dh", o/60)
		} else {
			parts[i] = fmt.Sprintf("%dm", o)
		}
	}
	return strings.Join(parts, ", ")
}

func (b *Bot) handleAwaitingAlerts(c telebot.Context, ctx context.Context, chatID int64, text string) error {
	offsets, err := parseAlertOffsets(text)
	if err != nil {
		return c.Send("❌ "+err.Error()+"\n\nEnter up to 3 values in minutes, space-separated (e.g. `60 30 10`):", telebot.ModeMarkdown)
	}

	alertsStr := make([]string, len(offsets))
	for i, o := range offsets {
		alertsStr[i] = strconv.FormatInt(int64(o), 10)
	}

	if err := b.setDraftField(ctx, chatID, "alerts", strings.Join(alertsStr, " ")); err != nil {
		slog.Error("failed to save draft", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingLabel.String(),
	})
	if err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send(fmt.Sprintf("✅ Reminders: %s before departure\n\n✏️ Give this route a name (e.g. \"Morning Commute\"):", formatAlertOffsets(offsets)))
}

func (b *Bot) handleAwaitingLabel(c telebot.Context, ctx context.Context, chatID int64, text string, userID pgtype.UUID) error {
	if len(text) == 0 || len(text) > 50 {
		return c.Send("❌ Route name must be between 1 and 50 characters. Try again:")
	}

	draft, err := b.getDraft(ctx, chatID)
	if err != nil || draft.FromCRS == "" || draft.ToCRS == "" || draft.Time == "" || draft.Days == "" || draft.Alerts == "" {
		slog.Error("draft expired or incomplete", "chat_id", chatID, "error", err)
		return c.Send("⏰ Your session expired. Please use /add to start over.")
	}

	depTime, err := time.Parse("15:04", draft.Time)
	if err != nil {
		slog.Error("invalid time in draft", "chat_id", chatID, "time", draft.Time, "error", err)
		return c.Send("⏰ Your session expired. Please use /add to start over.")
	}
	pgTime := pgtype.Time{
		Microseconds: int64(depTime.Hour())*3600000000 + int64(depTime.Minute())*60000000,
		Valid:        true,
	}

	daysMask, err := strconv.ParseInt(draft.Days, 10, 32)
	if err != nil {
		slog.Error("invalid days in draft", "chat_id", chatID, "days", draft.Days, "error", err)
		return c.Send("⏰ Your session expired. Please use /add to start over.")
	}

	alertOffsets, err := parseAlertOffsets(draft.Alerts)
	if err != nil {
		slog.Error("invalid alerts in draft", "chat_id", chatID, "alerts", draft.Alerts, "error", err)
		return c.Send("⏰ Your session expired. Please use /add to start over.")
	}

	route, err := b.queries.CreateRoute(ctx, db.CreateRouteParams{
		UserID:         userID,
		Label:          text,
		FromStationCrs: draft.FromCRS,
		ToStationCrs:   draft.ToCRS,
		DepartureTime:  pgTime,
		DaysOfWeek:     int32(daysMask),
		AlertOffsets:   alertOffsets,
	})
	if err != nil {
		slog.Error("failed to create route", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try /add again.")
	}

	b.clearDraft(ctx, chatID)

	err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateReady.String(),
	})
	if err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
	}

	fromName, _ := station.Lookup(route.FromStationCrs)
	toName, _ := station.Lookup(route.ToStationCrs)

	err = c.Send(fmt.Sprintf(
		"✅ Route created!\n\n"+
			"📝 %s\n"+
			"📅 %s | 🕐 %s | ⏰ %s before\n"+
			"🚉 %s (%s) → %s (%s)",
		route.Label,
		formatDaysMask(int32(daysMask)), draft.Time, formatAlertOffsets(route.AlertOffsets),
		fromName, route.FromStationCrs, toName, route.ToStationCrs,
	))
	if err != nil {
		return err
	}

	return b.planRouteNow(c, ctx, route)
}

func (b *Bot) planRouteNow(c telebot.Context, ctx context.Context, route db.Route) error {
	if b.planner == nil {
		return c.Send("🔍 Monitoring will start at the next scheduled check.")
	}

	status, err := b.planner.PlanRoute(ctx, route)
	if err != nil {
		slog.Warn("immediate route planning failed", "route_id", route.ID, "error", err)

		depMicro := route.DepartureTime.Microseconds
		targetMins := int(depMicro/3600000000)*60 + int((depMicro%3600000000)/60000000)
		if isWithinDarwinRange(targetMins) {
			nearest, searchErr := b.planner.FindNearestTrains(ctx, route.FromStationCrs, route.ToStationCrs, targetMins)
			if searchErr == nil && (nearest.Before != nil || nearest.After != nil) {
				return b.sendRouteTrainChoice(c, ctx, route, targetMins, nearest)
			}
		}

		date := targetDate(targetMins)
		schedStatus, schedErr := b.planner.PlanRouteFromSchedule(ctx, route, date)
		if schedErr == nil && schedStatus != nil {
			slog.Info("route planned from schedule after creation", "route_id", route.ID, "service_id", schedStatus.ServiceID)
			return c.Send(formatScheduledTrainFound(schedStatus))
		}
		if schedErr != nil {
			slog.Debug("scheduled route planning also failed", "route_id", route.ID, "error", schedErr)
		}

		return c.Send("📅 Could not find scheduled trains. I'll keep checking and notify you when data is available.")
	}

	slog.Info("route planned immediately after creation", "route_id", route.ID, "service_id", status.ServiceID)
	return c.Send(formatTrainFound(status))
}

func (b *Bot) sendRouteTrainChoice(c telebot.Context, ctx context.Context, route db.Route, targetMins int, nearest *domain.NearestTrains) error {
	only := nearest.SingleOption()
	if only != nil {
		svcMins := only.ScheduledDeparture.Hour()*60 + only.ScheduledDeparture.Minute()
		if domain.AbsDiff(svcMins, targetMins) <= domain.MaxAutoSelectMins {
			return b.autoSelectTrain(c, ctx, route, only)
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

	msg, menu := buildTrainChoiceMessage(routeID, requestedTime, before, after)
	return c.Send(msg, menu)
}

func (b *Bot) autoSelectTrain(c telebot.Context, ctx context.Context, route db.Route, train *domain.TrainStatus) error {
	chosenTime := train.ScheduledDeparture.Format("15:04")

	if b.planner != nil {
		if err := b.planner.CacheService(ctx, route.ID, train); err != nil {
			slog.Error("failed to cache auto-selected train", "route_id", route.ID, "error", err)
		}
	}

	return c.Send(fmt.Sprintf("🔄 No exact train found — adjusted to %s\n\n%s", chosenTime, formatTrainFound(train)))
}

func formatUUID(u pgtype.UUID) string {
	if !u.Valid {
		return "invalid"
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func formatTrainFound(status *domain.TrainStatus) string {
	statusIcon := "🟢"
	statusText := "On time"
	if status.IsCancelled {
		statusIcon = "🔴"
		statusText = "Cancelled"
	} else if status.DelayMins > 0 {
		statusIcon = "🟠"
		statusText = fmt.Sprintf("Delayed %d min", status.DelayMins)
	}

	platform := "TBC"
	if status.Platform != "" {
		platform = status.Platform
	}

	trainName := status.ScheduledDeparture.Format("15:04")
	if status.Destination != "" {
		trainName += " to " + status.Destination
	}

	return fmt.Sprintf("%s Train found: %s | Plt %s | %s",
		statusIcon, trainName, platform, statusText)
}

func formatScheduledTrainFound(status *domain.TrainStatus) string {
	trainName := status.ScheduledDeparture.Format("15:04")
	if status.Destination != "" {
		trainName += " to " + status.Destination
	}
	return fmt.Sprintf("📅 Scheduled train: %s (live updates start ~4h before departure)", trainName)
}

func (b *Bot) handleAwaitingDelete(c telebot.Context, ctx context.Context, chatID int64, text string, userID pgtype.UUID) error {
	routes, err := b.queries.GetRoutesByUserID(ctx, userID)
	if err != nil {
		slog.Error("failed to get routes", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	if len(routes) == 0 {
		return c.Send("No routes found. They may have been already deleted.")
	}

	choice := 0
	if text == "1" {
		choice = 0
	} else if text == "2" && len(routes) > 1 {
		choice = 1
	} else {
		return c.Send("Please reply with 1 or 2:")
	}

	return b.sendDeleteConfirmation(c, ctx, chatID, routes[choice], text)
}

func (b *Bot) sendDeleteConfirmation(c telebot.Context, ctx context.Context, chatID int64, route db.Route, choice string) error {
	fromName, _ := station.Lookup(route.FromStationCrs)
	toName, _ := station.Lookup(route.ToStationCrs)
	depTime := formatTime(route.DepartureTime)

	msg := fmt.Sprintf("⚠️ Are you sure you want to delete this route?\n\n📝 %s\n📅 %s | 🕐 %s\n🚉 %s (%s) → %s (%s)",
		route.Label,
		formatDaysMask(route.DaysOfWeek), depTime,
		fromName, route.FromStationCrs, toName, route.ToStationCrs)

	menu := &telebot.ReplyMarkup{}
	btnYes := menu.Data("✅ Yes, delete", "confirmdelete", choice)
	btnNo := menu.Data("❌ No, keep it", "canceldelete")
	menu.Inline(menu.Row(btnYes, btnNo))

	err := b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateAwaitingDeleteConfirm.String(),
	})
	if err != nil {
		slog.Error("failed to update user state", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return c.Send(msg, menu)
}

func (b *Bot) handleConfirmDeleteCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_ = c.Respond()
	removeInlineButtons(c)

	user, err := b.queries.GetUserByChatID(ctx, chatID)
	if err != nil {
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	return b.handleConfirmDelete(c, ctx, chatID, c.Callback().Data, user.ID)
}

func (b *Bot) handleConfirmDelete(c telebot.Context, ctx context.Context, chatID int64, data string, userID pgtype.UUID) error {
	routes, err := b.queries.GetRoutesByUserID(ctx, userID)
	if err != nil {
		slog.Error("failed to get routes", "chat_id", chatID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	if len(routes) == 0 {
		return c.Send("No routes found. They may have been already deleted.")
	}

	choice := 0
	if data == "1" {
		choice = 0
	} else if data == "2" && len(routes) > 1 {
		choice = 1
	} else {
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	err = b.queries.DeleteRoute(ctx, routes[choice].ID)
	if err != nil {
		slog.Error("failed to delete route", "route_id", routes[choice].ID, "error", err)
		return c.Send("⚠️ Something went wrong. Please try again.")
	}

	err = b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateReady.String(),
	})
	if err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
	}

	return c.Send(fmt.Sprintf("✅ Deleted route: %s", routes[choice].Label))
}

func (b *Bot) handleCancelDeleteCallback(c telebot.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	chatID := c.Chat().ID

	_ = c.Respond()
	removeInlineButtons(c)

	return b.handleCancelDelete(c, ctx, chatID)
}

func (b *Bot) handleCancelDelete(c telebot.Context, ctx context.Context, chatID int64) error {
	err := b.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		TelegramChatID: chatID,
		State:          domain.StateReady.String(),
	})
	if err != nil {
		slog.Error("failed to update state", "chat_id", chatID, "error", err)
	}

	return c.Send("👍 Route kept. Nothing was deleted.")
}

func resolveStation(input string) (crs string, name string, err error) {
	upper := strings.ToUpper(strings.TrimSpace(input))
	if station.IsValid(upper) {
		n, _ := station.Lookup(upper)
		return upper, n, nil
	}

	results := station.Search(input)

	if len(results) == 0 {
		return "", "", fmt.Errorf("no station found for %q, try a different name or enter a 3-letter code", input)
	}

	if len(results) == 1 {
		return results[0].CRS, results[0].Name, nil
	}

	return "", "", nil
}

func formatStationChoices(query string) string {
	results := station.Search(query)
	var msg strings.Builder
	fmt.Fprintf(&msg, "🔍 Multiple stations found for \"%s\":\n\n", query)
	for _, r := range results {
		fmt.Fprintf(&msg, "  • %s (%s)\n", r.Name, r.CRS)
	}
	msg.WriteString("\nPlease enter the station code from the list above:")
	return msg.String()
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

func isWithinDarwinRange(targetMins int) bool {
	now := ukNow()
	nowMins := now.Hour()*60 + now.Minute()
	diff := targetMins - nowMins
	return diff >= -120 && diff <= 240
}

func ukNow() time.Time {
	return timezone.Now()
}

func normalizeTime(s string) string {
	if len(s) == 4 && s[1] == ':' {
		return "0" + s
	}
	return s
}

func isValidTimeFormat(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	_, err := time.Parse("15:04", s)
	return err == nil
}

func formatTime(t pgtype.Time) string {
	if !t.Valid {
		return "N/A"
	}
	total := t.Microseconds
	hours := total / 3600000000
	minutes := (total % 3600000000) / 60000000
	return fmt.Sprintf("%02d:%02d", hours, minutes)
}

func (b *Bot) getCachedTrainStatus(ctx context.Context, routeID pgtype.UUID) (*domain.TrainStatus, error) {
	key := "route_service:" + formatRouteUUID(routeID)
	data, err := b.rdb.Get(ctx, key).Bytes()
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

func formatRouteUUID(u pgtype.UUID) string {
	if !u.Valid {
		return "invalid"
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var dayNames = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

var dayLookup = map[string]int{
	"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6,
}

var dayGroups = map[string]int32{
	"weekdays": 0b0011111,
	"weekends": 0b1100000,
	"all":      0b1111111,
}

func parseDaysMask(input string) (int32, error) {
	normalized := strings.ToLower(strings.TrimSpace(input))

	if groupMask, ok := dayGroups[normalized]; ok {
		return groupMask, nil
	}

	fields := strings.Fields(input)
	if len(fields) == 0 {
		return 0, fmt.Errorf("please enter at least one day")
	}

	var mask int32
	for _, f := range fields {
		lower := strings.ToLower(f)
		if groupMask, ok := dayGroups[lower]; ok {
			mask |= groupMask
			continue
		}
		idx, ok := dayLookup[lower]
		if !ok {
			return 0, fmt.Errorf("invalid day: %q — use Mon-Sun, `weekdays`, `weekends`, or `all`", f)
		}
		mask |= 1 << idx
	}
	return mask, nil
}

func formatDaysMask(mask int32) string {
	var days []string
	for i := 0; i < 7; i++ {
		if mask&(1<<i) != 0 {
			days = append(days, dayNames[i])
		}
	}
	if len(days) == 0 {
		return "None"
	}
	if mask == 0b0011111 {
		return "Mon-Fri"
	}
	if mask == 0b1100000 {
		return "Sat, Sun"
	}
	if mask == 0b1111111 {
		return "All days"
	}
	return strings.Join(days, ", ")
}

type trainOption struct {
	Time        string
	ButtonLabel string
}

func formatTrainOptions(requestedTime string, nearest *domain.NearestTrains) (string, []trainOption) {
	var msg strings.Builder
	var options []trainOption

	fmt.Fprintf(&msg, "🔍 No exact train at %s. Nearest options:", requestedTime)

	if nearest.Before != nil {
		t := nearest.Before.ScheduledDeparture.Format("15:04")
		dest := nearest.Before.Destination
		label := "⬅️ " + t
		if dest != "" {
			fmt.Fprintf(&msg, "\n\n⬅️ %s to %s", t, dest)
			label += " to " + dest
		} else {
			fmt.Fprintf(&msg, "\n\n⬅️ %s", t)
		}
		options = append(options, trainOption{Time: t, ButtonLabel: label})
	}

	if nearest.After != nil {
		t := nearest.After.ScheduledDeparture.Format("15:04")
		dest := nearest.After.Destination
		label := "➡️ " + t
		if dest != "" {
			fmt.Fprintf(&msg, "\n\n➡️ %s to %s", t, dest)
			label += " to " + dest
		} else {
			fmt.Fprintf(&msg, "\n\n➡️ %s", t)
		}
		options = append(options, trainOption{Time: t, ButtonLabel: label})
	}
	return msg.String(), options
}
