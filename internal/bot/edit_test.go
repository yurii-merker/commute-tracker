package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
	"github.com/yurii-merker/commute-tracker/internal/tracker"
)

func TestReconcileAlertOffsets(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}

	routeID := pgtype.UUID{Bytes: [16]byte{0x11, 0x22, 0x33, 0x44}, Valid: true}

	// dep = today 09:00 UK
	now := time.Date(2026, 6, 3, 8, 50, 0, 0, loc) // 10 min before dep
	dep := time.Date(2026, 6, 3, 9, 0, 0, 0, loc)
	cached := &domain.TrainStatus{ScheduledDeparture: dep}

	tests := []struct {
		name    string
		oldOffs []int32
		newOffs []int32
		now     time.Time
		cached  *domain.TrainStatus
		wantSet []int32
		wantDel []int32
	}{
		{
			name:    "removed offset key is preserved",
			oldOffs: []int32{60, 30},
			newOffs: []int32{60},
			now:     now,
			cached:  cached,
			wantSet: []int32{30},
			wantDel: nil,
		},
		{
			name:    "added offset in past window is SET",
			oldOffs: []int32{10},
			newOffs: []int32{60, 10},
			now:     now,
			cached:  cached,
			wantSet: []int32{60},
			wantDel: nil,
		},
		{
			name:    "added offset in future window is not touched",
			oldOffs: []int32{60},
			newOffs: []int32{60, 5},
			now:     now,
			cached:  cached,
			wantSet: nil,
			wantDel: nil,
		},
		{
			name:    "train already departed — no SET",
			oldOffs: []int32{30},
			newOffs: []int32{60, 30},
			now:     dep.Add(time.Minute),
			cached:  cached,
			wantSet: nil,
			wantDel: nil,
		},
		{
			name:    "no cached service — no writes",
			oldOffs: []int32{30},
			newOffs: []int32{60},
			now:     now,
			cached:  nil,
			wantSet: nil,
			wantDel: nil,
		},
		{
			name:    "no-op edit — no writes",
			oldOffs: []int32{60, 30},
			newOffs: []int32{60, 30},
			now:     now,
			cached:  cached,
			wantSet: nil,
			wantDel: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr, err := miniredis.Run()
			if err != nil {
				t.Fatal(err)
			}
			defer mr.Close()
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

			for _, o := range tt.oldOffs {
				_ = rdb.Set(context.Background(), tracker.AlertSentKey(routeID, o), "1", time.Hour).Err()
			}

			reconcileAlertOffsets(context.Background(), rdb, routeID, tt.cached, tt.now, tt.oldOffs, tt.newOffs)

			for _, o := range tt.wantSet {
				key := tracker.AlertSentKey(routeID, o)
				if exists, _ := rdb.Exists(context.Background(), key).Result(); exists == 0 {
					t.Errorf("expected SET %s, missing", key)
				}
			}
			for _, o := range tt.wantDel {
				key := tracker.AlertSentKey(routeID, o)
				if exists, _ := rdb.Exists(context.Background(), key).Result(); exists != 0 {
					t.Errorf("expected DEL %s, still present", key)
				}
			}
		})
	}
}

func seedUserWithRoutes(t *testing.T, repo *mockRepository, n int) []db.Route {
	t.Helper()
	user, err := repo.CreateUser(context.Background(), db.CreateUserParams{
		TelegramChatID: 100,
		State:          "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		_, err := repo.CreateRoute(context.Background(), db.CreateRouteParams{
			UserID:         user.ID,
			Label:          "Route" + string(rune('A'+i)),
			FromStationCrs: "KGX",
			ToStationCrs:   "CBG",
			DepartureTime:  pgtype.Time{Microseconds: 9 * 3600000000, Valid: true},
			DaysOfWeek:     0b0011111,
			AlertOffsets:   []int32{60, 30, 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		repo.routes[uuidKey(user.ID)][i].ID = pgtype.UUID{Bytes: [16]byte{byte(0xA0 + i)}, Valid: true}
	}
	return repo.routes[uuidKey(user.ID)]
}

func TestHandleEdit_NoRoutes(t *testing.T) {
	repo := newMockRepository()
	_, err := repo.CreateUser(context.Background(), db.CreateUserParams{TelegramChatID: 100, State: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	if err := b.handleEdit(tc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tc.lastSent, "no routes to edit") {
		t.Errorf("expected 'no routes' message, got: %q", tc.lastSent)
	}
}

func TestHandleEdit_OneRoute_GoesToFieldMenu(t *testing.T) {
	repo := newMockRepository()
	seedUserWithRoutes(t, repo, 1)
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	if err := b.handleEdit(tc); err != nil {
		t.Fatal(err)
	}
	if repo.state[100] != domain.StateAwaitingEditField.String() {
		t.Errorf("expected state %s, got %s", domain.StateAwaitingEditField, repo.state[100])
	}
	if !strings.Contains(tc.lastSent, "What do you want to change?") {
		t.Errorf("expected field menu, got: %q", tc.lastSent)
	}
}

func TestHandleEdit_TwoRoutes_GoesToRoutePicker(t *testing.T) {
	repo := newMockRepository()
	seedUserWithRoutes(t, repo, 2)
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	tc := newTC(100)
	if err := b.handleEdit(tc); err != nil {
		t.Fatal(err)
	}
	if repo.state[100] != domain.StateAwaitingEditRoute.String() {
		t.Errorf("expected state %s, got %s", domain.StateAwaitingEditRoute, repo.state[100])
	}
	if !strings.Contains(tc.lastSent, "Which route do you want to edit?") {
		t.Errorf("expected picker message, got: %q", tc.lastSent)
	}
}

func TestHandleAwaitingEditAlerts_InvalidInput(t *testing.T) {
	repo := newMockRepository()
	routes := seedUserWithRoutes(t, repo, 1)
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	if err := b.setDraftField(ctx, 100, "edit_route_id", formatUUID(routes[0].ID)); err != nil {
		t.Fatal(err)
	}
	repo.state[100] = domain.StateAwaitingEditAlerts.String()

	tc := newTC(100)
	if err := b.handleAwaitingEditAlerts(tc, ctx, 100, "999"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tc.lastSent, "out of range") {
		t.Errorf("expected validation error, got: %q", tc.lastSent)
	}
	if repo.state[100] != domain.StateAwaitingEditAlerts.String() {
		t.Errorf("expected state to stay %s, got %s", domain.StateAwaitingEditAlerts, repo.state[100])
	}
	if got := repo.routes[uuidKey(routes[0].UserID)][0].AlertOffsets; !equalOffsets(got, []int32{60, 30, 10}) {
		t.Errorf("alert offsets changed unexpectedly: %v", got)
	}
}

func TestHandleAwaitingEditAlerts_ValidInput(t *testing.T) {
	repo := newMockRepository()
	routes := seedUserWithRoutes(t, repo, 1)
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	ctx := context.Background()
	if err := b.setDraftField(ctx, 100, "edit_route_id", formatUUID(routes[0].ID)); err != nil {
		t.Fatal(err)
	}
	repo.state[100] = domain.StateAwaitingEditAlerts.String()

	tc := newTC(100)
	if err := b.handleAwaitingEditAlerts(tc, ctx, 100, "45 15"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tc.lastSent, "Reminders updated") {
		t.Errorf("expected success, got: %q", tc.lastSent)
	}
	if repo.state[100] != domain.StateReady.String() {
		t.Errorf("expected state %s, got %s", domain.StateReady, repo.state[100])
	}
	got := repo.routes[uuidKey(routes[0].UserID)][0].AlertOffsets
	want := []int32{45, 15}
	if !equalOffsets(got, want) {
		t.Errorf("got offsets %v, want %v", got, want)
	}
}

func TestHandleAwaitingEditAlerts_ExpiredDraft(t *testing.T) {
	repo := newMockRepository()
	seedUserWithRoutes(t, repo, 1)
	b, mr := newTestBot(t, repo)
	defer mr.Close()

	repo.state[100] = domain.StateAwaitingEditAlerts.String()

	tc := newTC(100)
	if err := b.handleAwaitingEditAlerts(tc, context.Background(), 100, "45 15"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tc.lastSent, "session expired") {
		t.Errorf("expected expired message, got: %q", tc.lastSent)
	}
}

func equalOffsets(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
