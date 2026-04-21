package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/yurii-merker/commute-tracker/internal/bot"
	"github.com/yurii-merker/commute-tracker/internal/config"
	"github.com/yurii-merker/commute-tracker/internal/darwin"
	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/redis"
	"github.com/yurii-merker/commute-tracker/internal/rtt"
	"github.com/yurii-merker/commute-tracker/internal/station"
	"github.com/yurii-merker/commute-tracker/internal/timezone"
	"github.com/yurii-merker/commute-tracker/internal/tracker"
)

const (
	migrationsPath  = "file://migrations"
	shutdownTimeout = 10 * time.Second
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	setupLogger(cfg.LogLevel)

	if err := timezone.Init(); err != nil {
		slog.Error("failed to load timezone", "error", err)
		os.Exit(1)
	}

	if err := station.Init(); err != nil {
		slog.Error("failed to load station data", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := runMigrations(cfg.DatabaseURL); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("connected to database")

	rdb, err := redis.Connect(ctx, cfg.RedisURL)
	if err != nil {
		slog.Error("failed to connect to redis", "error", err)
		os.Exit(1)
	}
	defer func() { _ = rdb.Close() }()
	slog.Info("connected to redis")

	queries := db.New(pool)
	trainClient := darwin.NewClient(cfg.NationalRailToken)
	rttClient := rtt.NewClient(cfg.RTTToken)

	planner := tracker.NewPlanner(queries, trainClient, rttClient, rdb)

	tgBot, err := bot.New(cfg.TelegramBotToken, queries, rdb, planner)
	if err != nil {
		slog.Error("failed to create bot", "error", err)
		os.Exit(1)
	}

	cb := tracker.NewCircuitBreaker(trainClient, tgBot, func(ctx context.Context) ([]int64, error) {
		return queries.GetSystemAlertsChatIDs(ctx)
	})

	daemon := tracker.NewDaemon(queries, trainClient, tgBot, rdb, cb, planner, cfg.DaemonTickInterval)
	daemon.Start(ctx)

	go tgBot.Start()

	slog.Info("commute-tracker is running")
	<-ctx.Done()

	slog.Info("shutting down")
	tgBot.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	done := make(chan struct{})
	go func() {
		daemon.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("daemon drained")
	case <-shutdownCtx.Done():
		slog.Warn("shutdown timeout exceeded, forcing exit")
	}
}

func runMigrations(databaseURL string) error {
	m, err := migrate.New(migrationsPath, databaseURL)
	if err != nil {
		return fmt.Errorf("initializing migrations: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("applying migrations: %w", err)
	}

	slog.Info("migrations applied")
	return nil
}

func setupLogger(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(handler))
}
