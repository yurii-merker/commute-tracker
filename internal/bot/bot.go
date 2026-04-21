package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"gopkg.in/telebot.v4"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
)

type RoutePlanner interface {
	PlanRoute(ctx context.Context, route db.Route) (*domain.TrainStatus, error)
	PlanRouteFromSchedule(ctx context.Context, route db.Route, date time.Time) (*domain.TrainStatus, error)
	FindNearestTrains(ctx context.Context, fromCRS, toCRS string, targetMins int) (*domain.NearestTrains, error)
	FindScheduledTrains(ctx context.Context, fromCRS, toCRS string, date time.Time, targetMins int) (*domain.NearestTrains, error)
	CacheService(ctx context.Context, routeID pgtype.UUID, status *domain.TrainStatus) error
}

type Bot struct {
	bot     *telebot.Bot
	queries Repository
	rdb     *redis.Client
	planner RoutePlanner
}

func New(token string, queries Repository, rdb *redis.Client, planner RoutePlanner) (*Bot, error) {
	pref := telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}

	b, err := telebot.NewBot(pref)
	if err != nil {
		return nil, fmt.Errorf("creating telegram bot: %w", err)
	}

	b.Use(rateLimitMiddleware(rdb))

	bot := &Bot{
		bot:     b,
		queries: queries,
		rdb:     rdb,
		planner: planner,
	}

	bot.registerHandlers()

	return bot, nil
}

func (b *Bot) Start() {
	slog.Info("starting telegram bot")
	b.bot.Start()
}

func (b *Bot) Stop() {
	slog.Info("stopping telegram bot")
	b.bot.Stop()
}

func (b *Bot) Send(ctx context.Context, chatID int64, message string) error {
	chat := &telebot.Chat{ID: chatID}
	_, err := b.bot.Send(chat, message)
	if err != nil {
		return fmt.Errorf("sending message to %d: %w", chatID, err)
	}
	return nil
}

func (b *Bot) Broadcast(ctx context.Context, chatIDs []int64, message string) error {
	var errs []error
	for _, chatID := range chatIDs {
		if err := b.Send(ctx, chatID, message); err != nil {
			slog.Error("broadcast failed for user", "chat_id", chatID, "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (b *Bot) BroadcastSilent(ctx context.Context, chatIDs []int64, message string) error {
	var errs []error
	for _, chatID := range chatIDs {
		chat := &telebot.Chat{ID: chatID}
		if _, err := b.bot.Send(chat, message, telebot.Silent); err != nil {
			slog.Error("silent broadcast failed for user", "chat_id", chatID, "error", err)
			errs = append(errs, fmt.Errorf("sending silent message to %d: %w", chatID, err))
		}
	}
	return errors.Join(errs...)
}

func (b *Bot) SendTrainChoice(ctx context.Context, chatID int64, routeID string, requestedTime string, before, after *domain.TrainOption) error {
	if before == nil && after == nil {
		return nil
	}
	chat := &telebot.Chat{ID: chatID}
	msg, menu := buildTrainChoiceMessage(routeID, requestedTime, before, after)
	_, err := b.bot.Send(chat, msg, menu)
	if err != nil {
		return fmt.Errorf("sending train choice to %d: %w", chatID, err)
	}
	return nil
}

func (b *Bot) SendBetterTrainOffer(ctx context.Context, chatID int64, routeID string, currentTime string, betterTime string, betterDestination string) error {
	chat := &telebot.Chat{ID: chatID}
	msg, menu := buildBetterTrainMessage(routeID, currentTime, betterTime, betterDestination)
	_, err := b.bot.Send(chat, msg, menu)
	if err != nil {
		return fmt.Errorf("sending better train offer to %d: %w", chatID, err)
	}
	return nil
}

func buildBetterTrainMessage(routeID string, currentTime string, betterTime string, betterDestination string) (string, *telebot.ReplyMarkup) {
	menu := &telebot.ReplyMarkup{}

	dest := ""
	if betterDestination != "" {
		dest = " to " + betterDestination
	}

	msg := fmt.Sprintf("🔔 Better train found!\n\nCurrently tracking: %s\nBetter option: %s%s\n\nSwitch to the closer train?", currentTime, betterTime, dest)

	switchBtn := menu.Data("✅ Switch to "+betterTime, "betterswitch", routeID, betterTime)
	keepBtn := menu.Data("❌ Keep "+currentTime, "betterkeep", routeID)
	menu.Inline(menu.Row(switchBtn, keepBtn))

	return msg, menu
}

func buildTrainChoiceMessage(routeID string, requestedTime string, before, after *domain.TrainOption) (string, *telebot.ReplyMarkup) {
	menu := &telebot.ReplyMarkup{}
	var btns []telebot.Btn

	var msg strings.Builder
	fmt.Fprintf(&msg, "🔍 No exact train at %s. Nearest options:", requestedTime)

	if before != nil {
		label := "⬅️ " + before.Time
		if before.Destination != "" {
			fmt.Fprintf(&msg, "\n\n⬅️ %s to %s", before.Time, before.Destination)
			label += " to " + before.Destination
		} else {
			fmt.Fprintf(&msg, "\n\n⬅️ %s", before.Time)
		}
		btns = append(btns, menu.Data(label, "routetrain", routeID, before.Time))
	}

	if after != nil {
		label := "➡️ " + after.Time
		if after.Destination != "" {
			fmt.Fprintf(&msg, "\n\n➡️ %s to %s", after.Time, after.Destination)
			label += " to " + after.Destination
		} else {
			fmt.Fprintf(&msg, "\n\n➡️ %s", after.Time)
		}
		btns = append(btns, menu.Data(label, "routetrain", routeID, after.Time))
	}

	menu.Inline(menu.Row(btns...))
	return msg.String(), menu
}

func (b *Bot) registerHandlers() {
	b.bot.Handle("/start", b.handleStart)
	b.bot.Handle("/help", b.handleHelp)
	b.bot.Handle("/add", b.handleAdd)
	b.bot.Handle("/status", b.handleStatus)
	b.bot.Handle("/stop", b.handleStop)
	b.bot.Handle("/resume", b.handleResume)
	b.bot.Handle("/delete", b.handleDelete)
	b.bot.Handle("/systemalerts", b.handleSystemAlerts)
	b.bot.Handle(telebot.OnText, b.handleText)
	b.bot.Handle("\fdelete", b.handleDeleteCallback)
	b.bot.Handle("\fconfirmdelete", b.handleConfirmDeleteCallback)
	b.bot.Handle("\fcanceldelete", b.handleCancelDeleteCallback)
	b.bot.Handle("\ftrain", b.handleTrainCallback)
	b.bot.Handle("\froutetrain", b.handleRouteTrainCallback)
	b.bot.Handle("\fbetterswitch", b.handleBetterSwitchCallback)
	b.bot.Handle("\fbetterkeep", b.handleBetterKeepCallback)
}
