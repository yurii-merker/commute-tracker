package domain

import "time"

const MaxAutoSelectMins = 15

type TrainStatus struct {
	ServiceID          string
	Destination        string
	ScheduledDeparture time.Time
	EstimatedDeparture time.Time
	Platform           string
	IsCancelled        bool
	DelayMins          int
	IsScheduleOnly     bool `json:"IsScheduleOnly,omitempty"`
}

type NearestTrains struct {
	Exact  *TrainStatus
	Before *TrainStatus
	After  *TrainStatus
}

type TrainOption struct {
	Time        string
	Destination string
}

func AbsDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func (n *NearestTrains) SingleOption() *TrainStatus {
	if n.Before != nil && n.After == nil {
		return n.Before
	}
	if n.After != nil && n.Before == nil {
		return n.After
	}
	return nil
}
