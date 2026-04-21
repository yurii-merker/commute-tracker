package tracker

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yurii-merker/commute-tracker/internal/domain"
)

type mockNotifier struct {
	mu           sync.Mutex
	sent         []sentMessage
	broadcasted  []broadcastMessage
	betterOffers []betterTrainOffer
	choiceSent   *trainChoiceRecord
}

type trainChoiceRecord struct {
	chatID        int64
	routeID       string
	requestedTime string
	before        *domain.TrainOption
	after         *domain.TrainOption
}

type sentMessage struct {
	chatID  int64
	message string
}

type broadcastMessage struct {
	chatIDs []int64
	message string
}

func (m *mockNotifier) Send(_ context.Context, chatID int64, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMessage{chatID, message})
	return nil
}

func (m *mockNotifier) Broadcast(_ context.Context, chatIDs []int64, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.broadcasted = append(m.broadcasted, broadcastMessage{chatIDs, message})
	return nil
}

func (m *mockNotifier) BroadcastSilent(_ context.Context, chatIDs []int64, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.broadcasted = append(m.broadcasted, broadcastMessage{chatIDs, message})
	return nil
}

func (m *mockNotifier) SendTrainChoice(_ context.Context, chatID int64, routeID string, requestedTime string, before *domain.TrainOption, after *domain.TrainOption) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.choiceSent = &trainChoiceRecord{chatID, routeID, requestedTime, before, after}
	return nil
}

type betterTrainOffer struct {
	chatID      int64
	routeID     string
	currentTime string
	betterTime  string
	betterDest  string
}

func (m *mockNotifier) SendBetterTrainOffer(_ context.Context, chatID int64, routeID, currentTime, betterTime, betterDest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.betterOffers = append(m.betterOffers, betterTrainOffer{chatID, routeID, currentTime, betterTime, betterDest})
	return nil
}

type mockTrainClient struct {
	err            error
	serviceDetails *domain.TrainStatus
}

func (m *mockTrainClient) GetDepartureBoard(_ context.Context, _, _ string, _, _ int) ([]domain.TrainStatus, error) {
	if m.err != nil {
		return nil, m.err
	}
	return []domain.TrainStatus{{ServiceID: "health-check"}}, nil
}

func (m *mockTrainClient) GetServiceDetails(_ context.Context, _ string) (*domain.TrainStatus, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.serviceDetails, nil
}

func TestCircuitBreakerStartsUp(t *testing.T) {
	cb := NewCircuitBreaker(&mockTrainClient{}, &mockNotifier{}, noChatIDs)
	if cb.State() != APIUp {
		t.Errorf("expected APIUp, got %d", cb.State())
	}
}

func TestCircuitBreakerTripsAfterThreshold(t *testing.T) {
	notifier := &mockNotifier{}
	chatIDs := []int64{111, 222}

	cb := NewCircuitBreaker(&mockTrainClient{err: fmt.Errorf("down")}, notifier, func(_ context.Context) ([]int64, error) {
		return chatIDs, nil
	})

	ctx := context.Background()

	cb.RecordFailure(ctx, fmt.Errorf("fail 1"))
	cb.RecordFailure(ctx, fmt.Errorf("fail 2"))
	if cb.State() != APIUp {
		t.Fatal("should still be UP after 2 failures")
	}

	cb.RecordFailure(ctx, fmt.Errorf("fail 3"))

	if cb.State() != APIDown {
		t.Fatal("should be DOWN after 3 failures")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.broadcasted) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(notifier.broadcasted))
	}
	if len(notifier.broadcasted[0].chatIDs) != 2 {
		t.Errorf("expected broadcast to 2 users, got %d", len(notifier.broadcasted[0].chatIDs))
	}
}

func TestCircuitBreakerResetsOnSuccess(t *testing.T) {
	cb := NewCircuitBreaker(&mockTrainClient{}, &mockNotifier{}, noChatIDs)

	ctx := context.Background()
	cb.RecordFailure(ctx, fmt.Errorf("fail 1"))
	cb.RecordFailure(ctx, fmt.Errorf("fail 2"))
	cb.RecordSuccess()

	cb.RecordFailure(ctx, fmt.Errorf("fail after reset"))
	if cb.State() != APIUp {
		t.Fatal("should still be UP — counter should have reset on success")
	}
}

func TestCircuitBreakerDoubleTransitionIgnored(t *testing.T) {
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(&mockTrainClient{err: fmt.Errorf("down")}, notifier, func(_ context.Context) ([]int64, error) {
		return []int64{111}, nil
	})

	ctx := context.Background()
	for i := 0; i < 6; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail %d", i))
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.broadcasted) != 1 {
		t.Errorf("expected exactly 1 broadcast even with 6 failures, got %d", len(notifier.broadcasted))
	}
}

func TestHealthPingSuccess(t *testing.T) {
	client := &mockTrainClient{}
	cb := NewCircuitBreaker(client, &mockNotifier{}, noChatIDs)

	if !cb.healthPing(context.Background()) {
		t.Error("health ping should succeed with working client")
	}
}

func TestHealthPingFailure(t *testing.T) {
	client := &mockTrainClient{err: fmt.Errorf("still down")}
	cb := NewCircuitBreaker(client, &mockNotifier{}, noChatIDs)

	if cb.healthPing(context.Background()) {
		t.Error("health ping should fail with broken client")
	}
}

func TestTransitionToUp(t *testing.T) {
	notifier := &mockNotifier{}
	chatIDs := []int64{111}

	cb := NewCircuitBreaker(&mockTrainClient{err: fmt.Errorf("down")}, notifier, func(_ context.Context) ([]int64, error) {
		return chatIDs, nil
	})

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail"))
	}

	if cb.State() != APIDown {
		t.Fatal("expected APIDown")
	}

	cb.transitionToUp(ctx)

	if cb.State() != APIUp {
		t.Fatal("expected APIUp after recovery")
	}

	cb.mu.Lock()
	if cb.consecutiveFails != 0 {
		t.Error("expected counter reset after recovery")
	}
	cb.mu.Unlock()

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.broadcasted) != 2 {
		t.Fatalf("expected 2 broadcasts (down + up), got %d", len(notifier.broadcasted))
	}
	if !strings.Contains(notifier.broadcasted[1].message, "back online") {
		t.Errorf("expected recovery message, got: %s", notifier.broadcasted[1].message)
	}
}

func TestBroadcastWithChatIDs(t *testing.T) {
	notifier := &mockNotifier{}
	chatIDs := []int64{111, 222, 333}

	cb := NewCircuitBreaker(&mockTrainClient{}, notifier, func(_ context.Context) ([]int64, error) {
		return chatIDs, nil
	})

	cb.broadcast(context.Background(), "test message")

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.broadcasted) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(notifier.broadcasted))
	}
	if len(notifier.broadcasted[0].chatIDs) != 3 {
		t.Errorf("expected 3 recipients, got %d", len(notifier.broadcasted[0].chatIDs))
	}
}

func TestBroadcastNoChatIDs(t *testing.T) {
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(&mockTrainClient{}, notifier, noChatIDs)

	cb.broadcast(context.Background(), "test")

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.broadcasted) != 0 {
		t.Error("expected no broadcast with empty chat IDs")
	}
}

func TestRecordFailureWhileDown(t *testing.T) {
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(&mockTrainClient{err: fmt.Errorf("down")}, notifier, func(_ context.Context) ([]int64, error) {
		return []int64{111}, nil
	})

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail"))
	}

	cb.RecordFailure(ctx, fmt.Errorf("extra fail while down"))

	cb.mu.Lock()
	fails := cb.consecutiveFails
	cb.mu.Unlock()

	if fails != 3 {
		t.Errorf("expected counter frozen at 3 while down, got %d", fails)
	}
}

func TestBroadcastGetChatIDsError(t *testing.T) {
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(&mockTrainClient{}, notifier, func(_ context.Context) ([]int64, error) {
		return nil, fmt.Errorf("db error")
	})

	cb.broadcast(context.Background(), "test")

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.broadcasted) != 0 {
		t.Error("expected no broadcast when getChatIDs fails")
	}
}

func noChatIDs(_ context.Context) ([]int64, error) {
	return nil, nil
}

func TestRecoveryCooldownPreventsFlapping(t *testing.T) {
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(&mockTrainClient{err: fmt.Errorf("down")}, notifier, func(_ context.Context) ([]int64, error) {
		return []int64{111}, nil
	})

	ctx := context.Background()

	for i := 0; i < 3; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail"))
	}
	if cb.State() != APIDown {
		t.Fatal("expected APIDown")
	}

	cb.transitionToUp(ctx)
	if cb.State() != APIUp {
		t.Fatal("expected APIUp after recovery")
	}

	for i := 0; i < 10; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail during cooldown"))
	}
	if cb.State() != APIUp {
		t.Fatal("expected APIUp during recovery cooldown — failures should be ignored")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	downCount := 0
	for _, b := range notifier.broadcasted {
		if strings.Contains(b.message, "issues") {
			downCount++
		}
	}
	if downCount != 1 {
		t.Errorf("expected 1 DOWN broadcast (no re-trip during cooldown), got %d", downCount)
	}
}

func TestRecoveryCooldownExpiresAllowsReTrip(t *testing.T) {
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(&mockTrainClient{err: fmt.Errorf("down")}, notifier, func(_ context.Context) ([]int64, error) {
		return []int64{111}, nil
	})

	ctx := context.Background()

	for i := 0; i < 3; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail"))
	}

	cb.mu.Lock()
	cb.state = APIUp
	cb.consecutiveFails = 0
	cb.lastRecovery = time.Now().Add(-recoveryCooldown - time.Second)
	cb.mu.Unlock()

	for i := 0; i < 3; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail after cooldown"))
	}
	if cb.State() != APIDown {
		t.Fatal("expected APIDown after cooldown expired")
	}
}

func TestRecordSuccessPreservesCooldown(t *testing.T) {
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(&mockTrainClient{err: fmt.Errorf("down")}, notifier, func(_ context.Context) ([]int64, error) {
		return []int64{111}, nil
	})

	ctx := context.Background()

	for i := 0; i < 3; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail"))
	}

	cb.transitionToUp(ctx)

	cb.RecordSuccess()

	cb.mu.Lock()
	if cb.lastRecovery.IsZero() {
		t.Error("expected lastRecovery to be preserved after RecordSuccess")
	}
	cb.mu.Unlock()

	for i := 0; i < 3; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail during cooldown"))
	}
	if cb.State() != APIUp {
		t.Fatal("expected APIUp — cooldown should still protect against flapping after RecordSuccess")
	}
}

func TestHealthPingRecoveryFlow(t *testing.T) {
	notifier := &mockNotifier{}
	chatIDs := []int64{111}

	client := &mockTrainClient{}
	cb := NewCircuitBreaker(client, notifier, func(_ context.Context) ([]int64, error) {
		return chatIDs, nil
	})

	ctx := context.Background()

	for i := 0; i < 3; i++ {
		cb.RecordFailure(ctx, fmt.Errorf("fail"))
	}

	if cb.State() != APIDown {
		t.Fatal("expected APIDown")
	}

	if !cb.healthPing(ctx) {
		t.Fatal("health ping should succeed with working client")
	}

	cb.transitionToUp(ctx)
	if cb.State() != APIUp {
		t.Fatal("expected APIUp after recovery")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	found := false
	for _, b := range notifier.broadcasted {
		if strings.Contains(b.message, "back online") {
			found = true
		}
	}
	if !found {
		t.Error("expected 'back online' broadcast")
	}
}

func TestStartHealthCheckContextCancel(t *testing.T) {
	cb := NewCircuitBreaker(&mockTrainClient{err: fmt.Errorf("down")}, &mockNotifier{}, noChatIDs)
	cb.mu.Lock()
	cb.state = APIDown
	cb.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cb.startHealthCheck(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startHealthCheck did not exit on context cancellation")
	}

	if cb.State() != APIDown {
		t.Error("expected state to remain APIDown after context cancel")
	}
}

func TestTransitionToDownAlreadyDown(t *testing.T) {
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(&mockTrainClient{err: fmt.Errorf("down")}, notifier, func(_ context.Context) ([]int64, error) {
		return []int64{111}, nil
	})

	ctx := context.Background()
	cb.transitionToDown(ctx)
	cb.transitionToDown(ctx)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.broadcasted) != 1 {
		t.Errorf("expected 1 broadcast for double transitionToDown, got %d", len(notifier.broadcasted))
	}
}

func TestTransitionToDownCancelledContext(t *testing.T) {
	notifier := &mockNotifier{}
	cb := NewCircuitBreaker(&mockTrainClient{}, notifier, func(_ context.Context) ([]int64, error) {
		return []int64{111}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cb.transitionToDown(ctx)

	if cb.State() != APIDown {
		t.Error("expected APIDown")
	}
}
