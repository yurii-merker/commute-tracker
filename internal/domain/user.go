package domain

type UserState string

const (
	StateNew                   UserState = "new"
	StateAwaitingFrom          UserState = "awaiting_from"
	StateAwaitingTo            UserState = "awaiting_to"
	StateAwaitingTime          UserState = "awaiting_time"
	StateAwaitingTrainChoice   UserState = "awaiting_train_choice"
	StateAwaitingDays          UserState = "awaiting_days"
	StateAwaitingAlerts        UserState = "awaiting_alerts"
	StateAwaitingLabel         UserState = "awaiting_label"
	StateAwaitingDelete        UserState = "awaiting_delete"
	StateAwaitingDeleteConfirm UserState = "awaiting_delete_confirm"
	StateReady                 UserState = "ready"
)

func (s UserState) String() string {
	return string(s)
}

func IsValidState(s string) bool {
	switch UserState(s) {
	case StateNew, StateAwaitingFrom, StateAwaitingTo, StateAwaitingTime, StateAwaitingTrainChoice, StateAwaitingDays, StateAwaitingAlerts, StateAwaitingLabel, StateAwaitingDelete, StateAwaitingDeleteConfirm, StateReady:
		return true
	default:
		return false
	}
}
