package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yurii-merker/commute-tracker/internal/domain"
)

func TestIsValidTimeFormat(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"07:45", true},
		{"00:00", true},
		{"23:59", true},
		{"18:30", true},
		{"7:45", false},
		{"24:00", false},
		{"12:60", false},
		{"abc", false},
		{"", false},
		{"07:45:00", false},
		{"7:5", false},
		{"07.45", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isValidTimeFormat(tt.input); got != tt.want {
				t.Errorf("isValidTimeFormat(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeTime(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"7:45", "07:45"},
		{"9:00", "09:00"},
		{"07:45", "07:45"},
		{"18:30", "18:30"},
		{"abc", "abc"},
		{"", ""},
		{"7:5", "7:5"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := normalizeTime(tt.input); got != tt.want {
				t.Errorf("normalizeTime(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatTime(t *testing.T) {
	tests := []struct {
		name string
		time pgtype.Time
		want string
	}{
		{
			name: "morning",
			time: pgtype.Time{Microseconds: 7*3600000000 + 45*60000000, Valid: true},
			want: "07:45",
		},
		{
			name: "evening",
			time: pgtype.Time{Microseconds: 18*3600000000 + 30*60000000, Valid: true},
			want: "18:30",
		},
		{
			name: "midnight",
			time: pgtype.Time{Microseconds: 0, Valid: true},
			want: "00:00",
		},
		{
			name: "invalid",
			time: pgtype.Time{Valid: false},
			want: "N/A",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTime(tt.time); got != tt.want {
				t.Errorf("formatTime() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDraftKey(t *testing.T) {
	got := draftKey(12345)
	want := "route_draft:12345"
	if got != want {
		t.Errorf("draftKey(12345) = %q, want %q", got, want)
	}
}

func TestParseUUID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid with dashes", "01020304-0506-0708-090a-0b0c0d0e0f10", false},
		{"valid without dashes", "0102030405060708090a0b0c0d0e0f10", false},
		{"too short", "01020304", true},
		{"too long", "0102030405060708090a0b0c0d0e0f1011", true},
		{"invalid hex", "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseUUID(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Valid {
				t.Error("expected valid UUID")
			}
		})
	}
}

func TestParseUUIDRoundTrip(t *testing.T) {
	uuid := pgtype.UUID{
		Bytes: [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		Valid: true,
	}
	formatted := formatUUID(uuid)
	parsed, err := parseUUID(formatted)
	if err != nil {
		t.Fatalf("parseUUID(formatUUID()) failed: %v", err)
	}
	if parsed.Bytes != uuid.Bytes {
		t.Errorf("round-trip failed: got %v, want %v", parsed.Bytes, uuid.Bytes)
	}
}

func TestFormatUUID(t *testing.T) {
	tests := []struct {
		name string
		uuid pgtype.UUID
		want string
	}{
		{
			"valid",
			pgtype.UUID{Bytes: [16]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}, Valid: true},
			"12345678-9abc-def0-1234-56789abcdef0",
		},
		{"invalid", pgtype.UUID{Valid: false}, "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatUUID(tt.uuid); got != tt.want {
				t.Errorf("formatUUID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseDaysMask(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int32
		wantErr bool
	}{
		{"weekdays", "weekdays", 0b0011111, false},
		{"weekends", "weekends", 0b1100000, false},
		{"all", "all", 0b1111111, false},
		{"single day", "Mon", 0b0000001, false},
		{"multiple days", "Mon Wed Fri", 0b0010101, false},
		{"case insensitive", "WEEKDAYS", 0b0011111, false},
		{"mixed group and day", "weekends Mon", 0b1100001, false},
		{"invalid day", "Xyz", 0, true},
		{"empty", "", 0, true},
		{"empty spaces", "   ", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDaysMask(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseDaysMask(%q) = %07b, want %07b", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatDaysMask(t *testing.T) {
	tests := []struct {
		name string
		mask int32
		want string
	}{
		{"weekdays", 0b0011111, "Mon-Fri"},
		{"weekends", 0b1100000, "Sat, Sun"},
		{"all", 0b1111111, "All days"},
		{"none", 0, "None"},
		{"single", 0b0000100, "Wed"},
		{"custom", 0b0010101, "Mon, Wed, Fri"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatDaysMask(tt.mask); got != tt.want {
				t.Errorf("formatDaysMask(%07b) = %q, want %q", tt.mask, got, tt.want)
			}
		})
	}
}

func TestFormatStationChoices(t *testing.T) {
	result := formatStationChoices("king")
	if !strings.Contains(result, "Multiple stations found") {
		t.Errorf("expected 'Multiple stations found', got: %s", result)
	}
	if !strings.Contains(result, "station code") {
		t.Errorf("expected prompt for station code, got: %s", result)
	}
}

func TestResolveStation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantCRS string
		wantErr bool
	}{
		{"valid CRS", "KGX", "KGX", false},
		{"valid CRS lowercase", "kgx", "KGX", false},
		{"no match", "ZZZZZZ", "", true},
		{"single fuzzy match", "St Mary Cray", "SMY", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crs, _, err := resolveStation(tt.input)
			if tt.wantErr {
				if err == nil && crs != "" {
					t.Errorf("expected error or empty CRS, got CRS=%q", crs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if crs != tt.wantCRS {
				t.Errorf("resolveStation(%q) CRS = %q, want %q", tt.input, crs, tt.wantCRS)
			}
		})
	}
}

func TestFormatTrainFound(t *testing.T) {
	tests := []struct {
		name   string
		status *domain.TrainStatus
		want   string
	}{
		{
			"on time with platform",
			&domain.TrainStatus{
				ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
				Destination:        "London",
				Platform:           "3",
			},
			"🟢",
		},
		{
			"delayed",
			&domain.TrainStatus{
				ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
				DelayMins:          5,
			},
			"🟠",
		},
		{
			"cancelled",
			&domain.TrainStatus{
				ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
				IsCancelled:        true,
			},
			"🔴",
		},
		{
			"no platform",
			&domain.TrainStatus{
				ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
			},
			"Plt TBC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTrainFound(tt.status)
			if !strings.Contains(got, tt.want) {
				t.Errorf("formatTrainFound() = %q, want to contain %q", got, tt.want)
			}
		})
	}
}

func TestFormatTrainOptions(t *testing.T) {
	before := &domain.TrainStatus{
		ScheduledDeparture: time.Date(2026, 1, 1, 7, 30, 0, 0, time.UTC),
		Destination:        "London",
	}
	after := &domain.TrainStatus{
		ScheduledDeparture: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
		Destination:        "Brighton",
	}

	t.Run("both options", func(t *testing.T) {
		msg, options := formatTrainOptions("07:45", &domain.NearestTrains{Before: before, After: after})
		if !strings.Contains(msg, "07:45") {
			t.Errorf("expected requested time in message, got: %s", msg)
		}
		if len(options) != 2 {
			t.Fatalf("expected 2 options, got %d", len(options))
		}
		if options[0].Time != "07:30" {
			t.Errorf("option 1 time = %q, want 07:30", options[0].Time)
		}
		if options[1].Time != "08:00" {
			t.Errorf("option 2 time = %q, want 08:00", options[1].Time)
		}
	})

	t.Run("only before", func(t *testing.T) {
		_, options := formatTrainOptions("07:45", &domain.NearestTrains{Before: before})
		if len(options) != 1 {
			t.Fatalf("expected 1 option, got %d", len(options))
		}
	})

	t.Run("only after", func(t *testing.T) {
		_, options := formatTrainOptions("07:45", &domain.NearestTrains{After: after})
		if len(options) != 1 {
			t.Fatalf("expected 1 option, got %d", len(options))
		}
	})
}

func TestHexVal(t *testing.T) {
	tests := []struct {
		input   byte
		want    byte
		wantErr bool
	}{
		{'0', 0, false},
		{'9', 9, false},
		{'a', 10, false},
		{'f', 15, false},
		{'A', 10, false},
		{'F', 15, false},
		{'g', 0, true},
		{'z', 0, true},
		{' ', 0, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			got, err := hexVal(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("hexVal(%c) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatRouteUUID(t *testing.T) {
	uuid := pgtype.UUID{
		Bytes: [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		Valid: true,
	}
	got := formatRouteUUID(uuid)
	if got != formatUUID(uuid) {
		t.Errorf("formatRouteUUID and formatUUID differ: %q vs %q", got, formatUUID(uuid))
	}

	invalid := pgtype.UUID{Valid: false}
	if formatRouteUUID(invalid) != "invalid" {
		t.Error("expected 'invalid' for invalid UUID")
	}
}
