package tracker

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yurii-merker/commute-tracker/internal/db"
	"github.com/yurii-merker/commute-tracker/internal/domain"
)

func TestStatusChanged(t *testing.T) {
	base := &domain.TrainStatus{
		Platform:           "3",
		IsCancelled:        false,
		DelayMins:          0,
		EstimatedDeparture: makeTime(7, 45),
	}

	tests := []struct {
		name      string
		newStatus *domain.TrainStatus
		want      bool
	}{
		{
			"no change",
			&domain.TrainStatus{Platform: "3", IsCancelled: false, DelayMins: 0, EstimatedDeparture: makeTime(7, 45)},
			false,
		},
		{
			"platform changed",
			&domain.TrainStatus{Platform: "7", IsCancelled: false, DelayMins: 0, EstimatedDeparture: makeTime(7, 45)},
			true,
		},
		{
			"cancelled",
			&domain.TrainStatus{Platform: "3", IsCancelled: true, DelayMins: 0, EstimatedDeparture: makeTime(7, 45)},
			true,
		},
		{
			"delayed",
			&domain.TrainStatus{Platform: "3", IsCancelled: false, DelayMins: 5, EstimatedDeparture: makeTime(7, 50)},
			true,
		},
		{
			"estimated time changed",
			&domain.TrainStatus{Platform: "3", IsCancelled: false, DelayMins: 0, EstimatedDeparture: makeTime(7, 48)},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusChanged(base, tt.newStatus); got != tt.want {
				t.Errorf("statusChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsWithinAlertWindow(t *testing.T) {
	route := makeRouteRow(8, 0, 60)

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"before window", makeTimeToday(6, 30), false},
		{"start of window", makeTimeToday(7, 0), true},
		{"middle of window", makeTimeToday(7, 30), true},
		{"at departure", makeTimeToday(8, 0), true},
		{"after departure", makeTimeToday(8, 1), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWithinAlertWindow(route, tt.now, route.AlertOffsets[0]); got != tt.want {
				t.Errorf("isWithinAlertWindow() at %s = %v, want %v", tt.now.Format("15:04"), got, tt.want)
			}
		})
	}

	earlyRoute := makeRouteRow(0, 20, 60)
	earlyTests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"early route midnight in window", makeTimeToday(0, 10), true},
		{"early route before clamped start", makeTimeToday(23, 50), false},
	}

	for _, tt := range earlyTests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWithinAlertWindow(earlyRoute, tt.now, earlyRoute.AlertOffsets[0]); got != tt.want {
				t.Errorf("isWithinAlertWindow() at %s = %v, want %v", tt.now.Format("15:04"), got, tt.want)
			}
		})
	}
}

func TestIsDeparted(t *testing.T) {
	route := makeRouteRow(8, 0, 30)

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"before", makeTimeToday(7, 59), false},
		{"at departure", makeTimeToday(8, 0), false},
		{"after", makeTimeToday(8, 1), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDeparted(route, tt.now); got != tt.want {
				t.Errorf("isDeparted() at %s = %v, want %v", tt.now.Format("15:04"), got, tt.want)
			}
		})
	}
}

func TestFormatAlertInitial(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning Commute"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	status := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 45),
		EstimatedDeparture: makeTime(7, 45),
		Platform:           "3",
		IsCancelled:        false,
		DelayMins:          0,
	}

	msg := formatAlert(route, status, nil)
	if !strings.Contains(msg, "Morning Commute") {
		t.Error("expected route label in message")
	}
	if !strings.Contains(msg, "Platform: 3") {
		t.Error("expected platform in message")
	}
	if !strings.Contains(msg, "On time") {
		t.Error("expected on time status")
	}
}

func TestFormatAlertDelayed(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	prev := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 45),
		EstimatedDeparture: makeTime(7, 45),
		Platform:           "3",
		DelayMins:          0,
	}
	curr := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 45),
		EstimatedDeparture: makeTime(7, 52),
		Platform:           "3",
		DelayMins:          7,
	}

	msg := formatAlert(route, curr, prev)
	if !strings.Contains(msg, "Delayed 7 min") {
		t.Errorf("expected delay info, got: %s", msg)
	}
}

func TestFormatAlertCancelled(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	prev := &domain.TrainStatus{IsCancelled: false, Platform: "3", ScheduledDeparture: makeTime(7, 45)}
	curr := &domain.TrainStatus{IsCancelled: true, Platform: "3", ScheduledDeparture: makeTime(7, 45)}

	msg := formatAlert(route, curr, prev)
	if !strings.Contains(msg, "CANCELLED") {
		t.Errorf("expected CANCELLED, got: %s", msg)
	}
}

func TestFormatAlertPlatformChange(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	prev := &domain.TrainStatus{Platform: "3", ScheduledDeparture: makeTime(7, 45), EstimatedDeparture: makeTime(7, 45)}
	curr := &domain.TrainStatus{Platform: "7", ScheduledDeparture: makeTime(7, 45), EstimatedDeparture: makeTime(7, 45)}

	msg := formatAlert(route, curr, prev)
	if !strings.Contains(msg, "Platform changed: 3 → 7") {
		t.Errorf("expected platform change, got: %s", msg)
	}
}

func TestFormatAlertNoPlatform(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	status := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 45),
		EstimatedDeparture: makeTime(7, 45),
		Platform:           "",
	}

	msg := formatAlert(route, status, nil)
	if !strings.Contains(msg, "Platform: TBC") {
		t.Errorf("expected TBC for empty platform, got: %s", msg)
	}
}

func TestFormatAlertInitialDelayed(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	status := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 45),
		EstimatedDeparture: makeTime(7, 52),
		Platform:           "3",
		DelayMins:          7,
	}

	msg := formatAlert(route, status, nil)
	if !strings.Contains(msg, "Delayed 7 min") {
		t.Errorf("expected delay in initial alert, got: %s", msg)
	}
}

func TestFormatAlertInitialCancelled(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	status := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 45),
		IsCancelled:        true,
	}

	msg := formatAlert(route, status, nil)
	if !strings.Contains(msg, "CANCELLED") {
		t.Errorf("expected CANCELLED in initial alert, got: %s", msg)
	}
}

func TestFormatAlertNowOnTime(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	prev := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 45),
		EstimatedDeparture: makeTime(7, 52),
		Platform:           "3",
		DelayMins:          7,
	}
	curr := &domain.TrainStatus{
		ScheduledDeparture: makeTime(7, 45),
		EstimatedDeparture: makeTime(7, 45),
		Platform:           "3",
		DelayMins:          0,
	}

	msg := formatAlert(route, curr, prev)
	if !strings.Contains(msg, "Now on time") {
		t.Errorf("expected 'Now on time', got: %s", msg)
	}
}

func TestOrTBC(t *testing.T) {
	if got := orTBC(""); got != "TBC" {
		t.Errorf("orTBC(\"\") = %q, want \"TBC\"", got)
	}
	if got := orTBC("3"); got != "3" {
		t.Errorf("orTBC(\"3\") = %q, want \"3\"", got)
	}
}

func TestIsWithinAPIRange(t *testing.T) {
	tests := []struct {
		name    string
		depHour int
		depMin  int
		nowHour int
		nowMin  int
		want    bool
	}{
		{"within range 2h before", 18, 15, 16, 15, true},
		{"within range 4h before", 18, 15, 14, 16, true},
		{"at boundary 240 min", 18, 15, 14, 15, true},
		{"outside range 241 min", 18, 15, 14, 14, false},
		{"way too early", 18, 15, 10, 0, false},
		{"already departed", 14, 0, 14, 5, false},
		{"at departure time", 18, 15, 18, 15, false},
		{"1 min before departure", 18, 15, 18, 14, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := makeRouteRow(tt.depHour, tt.depMin, 60)
			now := makeTimeToday(tt.nowHour, tt.nowMin)
			if got := isWithinAPIRange(route, now); got != tt.want {
				t.Errorf("isWithinAPIRange() at %02d:%02d for dep %02d:%02d = %v, want %v",
					tt.nowHour, tt.nowMin, tt.depHour, tt.depMin, got, tt.want)
			}
		})
	}
}

func TestIsWithinChoiceRange(t *testing.T) {
	tests := []struct {
		name    string
		depHour int
		depMin  int
		nowHour int
		nowMin  int
		want    bool
	}{
		{"within range 60 min before", 8, 0, 7, 0, true},
		{"within range 90 min before", 8, 0, 6, 30, true},
		{"outside range 91 min before", 8, 0, 6, 29, false},
		{"way too early", 8, 0, 5, 0, false},
		{"already departed", 8, 0, 8, 5, false},
		{"at departure time", 8, 0, 8, 0, false},
		{"1 min before departure", 8, 0, 7, 59, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := makeRouteRow(tt.depHour, tt.depMin, 60)
			now := makeTimeToday(tt.nowHour, tt.nowMin)
			if got := isWithinChoiceRange(route, now); got != tt.want {
				t.Errorf("isWithinChoiceRange() at %02d:%02d for dep %02d:%02d = %v, want %v",
					tt.nowHour, tt.nowMin, tt.depHour, tt.depMin, got, tt.want)
			}
		})
	}
}

func TestRouteFromRow(t *testing.T) {
	row := db.GetActiveRoutesWithChatIDRow{
		ID:             pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		UserID:         pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		Label:          "Morning",
		FromStationCrs: "SMH",
		ToStationCrs:   "CTK",
		DepartureTime:  pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
		DaysOfWeek:     31,
		AlertOffsets:   []int32{60},
		IsActive:       true,
		TelegramChatID: 100,
	}

	route := routeFromRow(row)
	if route.Label != "Morning" {
		t.Errorf("Label = %q, want Morning", route.Label)
	}
	if route.FromStationCrs != "SMH" {
		t.Errorf("FromStationCrs = %q, want SMH", route.FromStationCrs)
	}
	if route.DaysOfWeek != 31 {
		t.Errorf("DaysOfWeek = %d, want 31", route.DaysOfWeek)
	}
}

func TestFormatDepartureReminder(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning Commute"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	depTime := time.Now().Add(60 * time.Minute)
	status := &domain.TrainStatus{
		ScheduledDeparture: depTime,
		EstimatedDeparture: depTime,
		Platform:           "3",
	}

	msg := formatDepartureReminder(route, status)
	if !strings.Contains(msg, "Departure in 60 min") && !strings.Contains(msg, "Departure in 59 min") {
		t.Errorf("expected ~60 min departure reminder, got: %s", msg)
	}
	if !strings.Contains(msg, "Morning Commute") {
		t.Error("expected route label in reminder")
	}
	if !strings.Contains(msg, "Platform: 3") {
		t.Error("expected platform in reminder")
	}
	if !strings.Contains(msg, "On time") {
		t.Error("expected on time status in reminder")
	}
}

func TestFormatDepartureReminderDelayed(t *testing.T) {
	route := makeRouteRow(7, 45, 60)
	route.Label = "Morning"
	route.FromStationCrs = "SMH"
	route.ToStationCrs = "CTK"

	scheduled := time.Now().Add(60 * time.Minute)
	estimated := time.Now().Add(67 * time.Minute)
	status := &domain.TrainStatus{
		ScheduledDeparture: scheduled,
		EstimatedDeparture: estimated,
		Platform:           "3",
		DelayMins:          7,
	}

	msg := formatDepartureReminder(route, status)
	if !strings.Contains(msg, "Departure in 67 min") && !strings.Contains(msg, "Departure in 66 min") {
		t.Errorf("expected ~67 min departure reminder, got: %s", msg)
	}
	if !strings.Contains(msg, "Delayed 7 min") {
		t.Errorf("expected delay info in reminder, got: %s", msg)
	}
}

func makeRouteRow(hour, min int, alertOffset int32) db.GetActiveRoutesWithChatIDRow {
	return db.GetActiveRoutesWithChatIDRow{
		DepartureTime: pgtype.Time{Microseconds: int64(hour)*3600000000 + int64(min)*60000000, Valid: true},
		AlertOffsets:  []int32{alertOffset},
	}
}

func makeTimeToday(hour, min int) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
}
