package bot

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yurii-merker/commute-tracker/internal/db"
)

type Repository interface {
	CreateUser(ctx context.Context, arg db.CreateUserParams) (db.User, error)
	GetUserByChatID(ctx context.Context, telegramChatID int64) (db.User, error)
	UpdateUserState(ctx context.Context, arg db.UpdateUserStateParams) error
	CountRoutesByUserID(ctx context.Context, userID pgtype.UUID) (int64, error)
	GetRoutesByUserID(ctx context.Context, userID pgtype.UUID) ([]db.Route, error)
	CreateRoute(ctx context.Context, arg db.CreateRouteParams) (db.Route, error)
	UpdateRouteActive(ctx context.Context, arg db.UpdateRouteActiveParams) error
	DeleteRoute(ctx context.Context, id pgtype.UUID) error
	GetRouteByID(ctx context.Context, id pgtype.UUID) (db.Route, error)
	ToggleSystemAlerts(ctx context.Context, telegramChatID int64) (bool, error)
}
