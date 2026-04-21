package tracker

import (
	"context"

	"github.com/yurii-merker/commute-tracker/internal/db"
)

type DaemonRepository interface {
	GetActiveRoutesWithChatID(ctx context.Context, weekday int32) ([]db.GetActiveRoutesWithChatIDRow, error)
}

type PlannerRepository interface {
	GetActiveRoutesForWeekday(ctx context.Context, weekday int32) ([]db.Route, error)
}
