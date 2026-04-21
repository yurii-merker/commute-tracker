package domain

import (
	"context"
	"time"
)

type TrainClient interface {
	GetDepartureBoard(ctx context.Context, fromCRS, toCRS string, timeOffsetMins, timeWindowMins int) ([]TrainStatus, error)
	GetServiceDetails(ctx context.Context, serviceID string) (*TrainStatus, error)
}

type ScheduleClient interface {
	GetScheduledDepartures(ctx context.Context, fromCRS, toCRS string, date time.Time, targetMins int) ([]TrainStatus, error)
}

type Notifier interface {
	Send(ctx context.Context, chatID int64, message string) error
	Broadcast(ctx context.Context, chatIDs []int64, message string) error
	BroadcastSilent(ctx context.Context, chatIDs []int64, message string) error
	SendTrainChoice(ctx context.Context, chatID int64, routeID string, requestedTime string, before, after *TrainOption) error
	SendBetterTrainOffer(ctx context.Context, chatID int64, routeID string, currentTime string, betterTime string, betterDestination string) error
}
