package tracker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/yurii-merker/commute-tracker/internal/domain"
)

type APIState int

const (
	APIUp APIState = iota
	APIDown

	failureThreshold    = 3
	healthCheckInterval = 5 * time.Minute
	recoveryCooldown    = 10 * time.Minute
)

type CircuitBreaker struct {
	mu               sync.Mutex
	state            APIState
	consecutiveFails int
	lastRecovery     time.Time
	trainClient      domain.TrainClient
	notifier         domain.Notifier
	getChatIDs       func(ctx context.Context) ([]int64, error)
}

func NewCircuitBreaker(trainClient domain.TrainClient, notifier domain.Notifier, getChatIDs func(ctx context.Context) ([]int64, error)) *CircuitBreaker {
	return &CircuitBreaker{
		state:       APIUp,
		trainClient: trainClient,
		notifier:    notifier,
		getChatIDs:  getChatIDs,
	}
}

func (cb *CircuitBreaker) State() APIState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFails = 0
}

func (cb *CircuitBreaker) RecordFailure(ctx context.Context, err error) {
	cb.mu.Lock()
	if cb.state == APIDown {
		cb.mu.Unlock()
		return
	}
	if !cb.lastRecovery.IsZero() && time.Since(cb.lastRecovery) < recoveryCooldown {
		cb.mu.Unlock()
		slog.Debug("darwin API failure ignored during recovery cooldown", "error", err)
		return
	}
	cb.consecutiveFails++
	fails := cb.consecutiveFails
	shouldTrip := fails >= failureThreshold
	cb.mu.Unlock()

	slog.Warn("darwin API failure recorded", "consecutive", fails, "error", err)

	if shouldTrip {
		cb.transitionToDown(ctx)
	}
}

func (cb *CircuitBreaker) transitionToDown(ctx context.Context) {
	cb.mu.Lock()
	if cb.state == APIDown {
		cb.mu.Unlock()
		return
	}
	cb.state = APIDown
	cb.mu.Unlock()

	slog.Error("circuit breaker tripped: API is DOWN")
	cb.broadcastSilent(ctx, "⚠️ National Rail API appears to be experiencing issues. Monitoring is paused and will resume automatically when the API recovers.")

	if ctx.Err() == nil {
		go cb.startHealthCheck(ctx)
	}
}

func (cb *CircuitBreaker) transitionToUp(ctx context.Context) {
	cb.mu.Lock()
	cb.state = APIUp
	cb.consecutiveFails = 0
	cb.lastRecovery = time.Now()
	cb.mu.Unlock()

	slog.Info("circuit breaker recovered: API is UP")
	cb.broadcastSilent(ctx, "✅ National Rail API is back online. Monitoring has resumed.")
}

func (cb *CircuitBreaker) startHealthCheck(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("circuit breaker health check panic recovered", "panic", r)
		}
	}()

	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if cb.healthPing(ctx) {
				cb.transitionToUp(ctx)
				return
			}
		}
	}
}

func (cb *CircuitBreaker) healthPing(ctx context.Context) bool {
	_, err := cb.trainClient.GetDepartureBoard(ctx, "KGX", "", 0, 20)
	if err != nil {
		slog.Warn("health check failed", "error", err)
		return false
	}
	slog.Info("health check succeeded")
	return true
}

func (cb *CircuitBreaker) broadcast(ctx context.Context, message string) {
	chatIDs, err := cb.getChatIDs(ctx)
	if err != nil {
		slog.Error("failed to get chat IDs for broadcast", "error", err)
		return
	}

	if len(chatIDs) == 0 {
		return
	}

	if err := cb.notifier.Broadcast(ctx, chatIDs, message); err != nil {
		slog.Error("circuit breaker broadcast failed", "error", err)
	}
}

func (cb *CircuitBreaker) broadcastSilent(ctx context.Context, message string) {
	chatIDs, err := cb.getChatIDs(ctx)
	if err != nil {
		slog.Error("failed to get chat IDs for broadcast", "error", err)
		return
	}

	if len(chatIDs) == 0 {
		return
	}

	if err := cb.notifier.BroadcastSilent(ctx, chatIDs, message); err != nil {
		slog.Error("circuit breaker silent broadcast failed", "error", err)
	}
}
