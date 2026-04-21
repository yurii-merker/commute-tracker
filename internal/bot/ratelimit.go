package bot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"gopkg.in/telebot.v4"
)

const (
	rateLimitWindow = 1 * time.Minute
	rateLimitMax    = 60
)

func rateLimitMiddleware(rdb *redis.Client) telebot.MiddlewareFunc {
	return func(next telebot.HandlerFunc) telebot.HandlerFunc {
		return func(c telebot.Context) error {
			chatID := c.Chat().ID
			key := fmt.Sprintf("ratelimit:%d", chatID)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			count, err := rdb.Incr(ctx, key).Result()
			if err != nil {
				slog.Error("rate limit check failed", "chat_id", chatID, "error", err)
				return next(c)
			}

			rdb.Expire(ctx, key, rateLimitWindow)

			if count > rateLimitMax {
				slog.Warn("rate limit exceeded", "chat_id", chatID, "count", count)
				return c.Send("⏳ Too many requests. Please wait a moment and try again.")
			}

			return next(c)
		}
	}
}
