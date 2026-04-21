package domain

import (
	"testing"
	"time"
)

func TestUserStateString(t *testing.T) {
	tests := []struct {
		state UserState
		want  string
	}{
		{StateNew, "new"},
		{StateAwaitingFrom, "awaiting_from"},
		{StateAwaitingTo, "awaiting_to"},
		{StateAwaitingTime, "awaiting_time"},
		{StateAwaitingTrainChoice, "awaiting_train_choice"},
		{StateAwaitingAlerts, "awaiting_alerts"},
		{StateAwaitingLabel, "awaiting_label"},
		{StateAwaitingDelete, "awaiting_delete"},
		{StateReady, "ready"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.state.String(); got != tt.want {
				t.Errorf("UserState.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsValidState(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"new", true},
		{"awaiting_from", true},
		{"awaiting_to", true},
		{"awaiting_time", true},
		{"awaiting_train_choice", true},
		{"awaiting_alerts", true},
		{"awaiting_label", true},
		{"awaiting_delete", true},
		{"ready", true},
		{"invalid", false},
		{"", false},
		{"NEW", false},
		{"Ready", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsValidState(tt.input); got != tt.want {
				t.Errorf("IsValidState(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSingleOption(t *testing.T) {
	train := &TrainStatus{
		ServiceID:          "svc1",
		ScheduledDeparture: time.Date(2026, 1, 1, 7, 45, 0, 0, time.UTC),
	}
	trainB := &TrainStatus{
		ServiceID:          "svc2",
		ScheduledDeparture: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name    string
		n       *NearestTrains
		wantID  string
		wantNil bool
	}{
		{"both nil", &NearestTrains{}, "", true},
		{"only before", &NearestTrains{Before: train}, "svc1", false},
		{"only after", &NearestTrains{After: train}, "svc1", false},
		{"both set", &NearestTrains{Before: train, After: trainB}, "", true},
		{"exact set, before and after nil", &NearestTrains{Exact: train}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.n.SingleOption()
			if tt.wantNil {
				if got != nil {
					t.Errorf("SingleOption() = %v, want nil", got.ServiceID)
				}
				return
			}
			if got == nil {
				t.Fatal("SingleOption() = nil, want non-nil")
			}
			if got.ServiceID != tt.wantID {
				t.Errorf("SingleOption().ServiceID = %q, want %q", got.ServiceID, tt.wantID)
			}
		})
	}
}
