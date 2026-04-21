package bot

import (
	"context"
	"fmt"
	"time"
)

const (
	draftTTL       = 30 * time.Minute
	draftKeyPrefix = "route_draft:"
)

type routeDraft struct {
	FromCRS      string
	ToCRS        string
	Time         string
	Days         string
	Alerts       string
	TrainOption1 string
	TrainOption2 string
}

func draftKey(chatID int64) string {
	return fmt.Sprintf("%s%d", draftKeyPrefix, chatID)
}

func (b *Bot) getDraft(ctx context.Context, chatID int64) (*routeDraft, error) {
	key := draftKey(chatID)
	result, err := b.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("getting draft: %w", err)
	}
	if len(result) == 0 {
		return &routeDraft{}, nil
	}
	return &routeDraft{
		FromCRS:      result["from"],
		ToCRS:        result["to"],
		Time:         result["time"],
		Days:         result["days"],
		Alerts:       result["alerts"],
		TrainOption1: result["train_option_1"],
		TrainOption2: result["train_option_2"],
	}, nil
}

func (b *Bot) setDraftField(ctx context.Context, chatID int64, field, value string) error {
	key := draftKey(chatID)
	pipe := b.rdb.Pipeline()
	pipe.HSet(ctx, key, field, value)
	pipe.Expire(ctx, key, draftTTL)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("setting draft field %s: %w", field, err)
	}
	return nil
}

func (b *Bot) clearDraft(ctx context.Context, chatID int64) {
	b.rdb.Del(ctx, draftKey(chatID))
}
