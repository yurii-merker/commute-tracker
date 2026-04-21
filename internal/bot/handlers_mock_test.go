package bot

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yurii-merker/commute-tracker/internal/db"
)

type mockRepository struct {
	users  map[int64]db.User
	routes map[string][]db.Route
	state  map[int64]string
}

func newMockRepository() *mockRepository {
	return &mockRepository{
		users:  make(map[int64]db.User),
		routes: make(map[string][]db.Route),
		state:  make(map[int64]string),
	}
}

func (m *mockRepository) CreateUser(_ context.Context, arg db.CreateUserParams) (db.User, error) {
	if _, exists := m.users[arg.TelegramChatID]; exists {
		return db.User{}, pgx.ErrNoRows
	}
	user := db.User{
		ID:             pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		TelegramChatID: arg.TelegramChatID,
		State:          arg.State,
	}
	m.users[arg.TelegramChatID] = user
	m.state[arg.TelegramChatID] = arg.State
	return user, nil
}

func (m *mockRepository) GetUserByChatID(_ context.Context, chatID int64) (db.User, error) {
	user, ok := m.users[chatID]
	if !ok {
		return db.User{}, pgx.ErrNoRows
	}
	user.State = m.state[chatID]
	return user, nil
}

func (m *mockRepository) UpdateUserState(_ context.Context, arg db.UpdateUserStateParams) error {
	m.state[arg.TelegramChatID] = arg.State
	return nil
}

func (m *mockRepository) CountRoutesByUserID(_ context.Context, userID pgtype.UUID) (int64, error) {
	key := uuidKey(userID)
	return int64(len(m.routes[key])), nil
}

func (m *mockRepository) GetRoutesByUserID(_ context.Context, userID pgtype.UUID) ([]db.Route, error) {
	key := uuidKey(userID)
	return m.routes[key], nil
}

func (m *mockRepository) CreateRoute(_ context.Context, arg db.CreateRouteParams) (db.Route, error) {
	route := db.Route{
		ID:             pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		UserID:         arg.UserID,
		Label:          arg.Label,
		FromStationCrs: arg.FromStationCrs,
		ToStationCrs:   arg.ToStationCrs,
		DepartureTime:  arg.DepartureTime,
		DaysOfWeek:     arg.DaysOfWeek,
		AlertOffsets:   arg.AlertOffsets,
		IsActive:       true,
	}
	key := uuidKey(arg.UserID)
	m.routes[key] = append(m.routes[key], route)
	return route, nil
}

func (m *mockRepository) UpdateRouteActive(_ context.Context, arg db.UpdateRouteActiveParams) error {
	for key, routes := range m.routes {
		for i, r := range routes {
			if r.ID == arg.ID {
				m.routes[key][i].IsActive = arg.IsActive
				return nil
			}
		}
	}
	return nil
}

func (m *mockRepository) DeleteRoute(_ context.Context, id pgtype.UUID) error {
	for key, routes := range m.routes {
		for i, r := range routes {
			if r.ID == id {
				m.routes[key] = append(routes[:i], routes[i+1:]...)
				return nil
			}
		}
	}
	return nil
}

func (m *mockRepository) GetRouteByID(_ context.Context, id pgtype.UUID) (db.Route, error) {
	for _, routes := range m.routes {
		for _, r := range routes {
			if r.ID == id {
				return r, nil
			}
		}
	}
	return db.Route{}, pgx.ErrNoRows
}

func (m *mockRepository) ToggleSystemAlerts(_ context.Context, _ int64) (bool, error) {
	return true, nil
}

func uuidKey(u pgtype.UUID) string {
	return string(u.Bytes[:])
}

type errorRepository struct {
	mockRepository
	failOn string
}

func (e *errorRepository) CreateUser(ctx context.Context, arg db.CreateUserParams) (db.User, error) {
	if e.failOn == "CreateUser" {
		return db.User{}, fmt.Errorf("db error")
	}
	return e.mockRepository.CreateUser(ctx, arg)
}

func (e *errorRepository) GetUserByChatID(ctx context.Context, chatID int64) (db.User, error) {
	if e.failOn == "GetUserByChatID" {
		return db.User{}, fmt.Errorf("db error")
	}
	return e.mockRepository.GetUserByChatID(ctx, chatID)
}

func (e *errorRepository) UpdateUserState(ctx context.Context, arg db.UpdateUserStateParams) error {
	if e.failOn == "UpdateUserState" {
		return fmt.Errorf("db error")
	}
	return e.mockRepository.UpdateUserState(ctx, arg)
}

func (e *errorRepository) CountRoutesByUserID(ctx context.Context, userID pgtype.UUID) (int64, error) {
	if e.failOn == "CountRoutesByUserID" {
		return 0, fmt.Errorf("db error")
	}
	return e.mockRepository.CountRoutesByUserID(ctx, userID)
}

func (e *errorRepository) GetRoutesByUserID(ctx context.Context, userID pgtype.UUID) ([]db.Route, error) {
	if e.failOn == "GetRoutesByUserID" {
		return nil, fmt.Errorf("db error")
	}
	return e.mockRepository.GetRoutesByUserID(ctx, userID)
}
