package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSubscriptionEventTypeConstants verifies all event types are defined.
func TestSubscriptionEventTypeConstants(t *testing.T) {
	types := []SubscriptionEventType{
		EventCreated,
		EventRenewed,
		EventUpdated,
		EventCanceled,
		EventExpired,
		EventPastDue,
		EventTrialEnd,
	}

	for _, et := range types {
		require.NotEmpty(t, et)
	}
}

// TestSubscriptionStatusConstants verifies all status values are defined.
func TestSubscriptionStatusConstants(t *testing.T) {
	statuses := []SubscriptionStatus{
		StatusActive,
		StatusTrialing,
		StatusPastDue,
		StatusCanceled,
		StatusExpired,
	}

	for _, s := range statuses {
		require.NotEmpty(t, s)
	}
}

// TestSubscriptionIsEntitled verifies entitlement logic.
func TestSubscriptionIsEntitled(t *testing.T) {
	tests := []struct {
		name       string
		status     SubscriptionStatus
		entitled   bool
	}{
		{"active", StatusActive, true},
		{"trialing", StatusTrialing, true},
		{"canceled", StatusCanceled, false},
		{"expired", StatusExpired, false},
		{"past_due", StatusPastDue, false},
	}

	now := time.Now()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := Subscription{
				ID:        "sub-123",
				Provider:  "stripe",
				Status:    tt.status,
				CreatedAt: now,
				UpdatedAt: now,
			}
			require.Equal(t, tt.entitled, sub.IsEntitled())
		})
	}
}

// MockProvider implements Provider for testing.
type MockProvider struct {
	name                string
	checkoutErr         error
	webhookErr          error
	subscriptionErr     error
	cancelErr           error
	checkoutSession     *CheckoutSession
	subscriptionEvent   *SubscriptionEvent
	subscription        *Subscription
}

func (m *MockProvider) Name() string {
	return m.name
}

func (m *MockProvider) CreateCheckout(ctx context.Context, req CheckoutRequest) (*CheckoutSession, error) {
	if m.checkoutErr != nil {
		return nil, m.checkoutErr
	}
	if m.checkoutSession != nil {
		return m.checkoutSession, nil
	}
	return &CheckoutSession{
		SessionID: "sess-123",
		URL:       "https://checkout.example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func (m *MockProvider) HandleWebhook(ctx context.Context, req WebhookRequest) (*SubscriptionEvent, error) {
	if m.webhookErr != nil {
		return nil, m.webhookErr
	}
	return m.subscriptionEvent, nil
}

func (m *MockProvider) GetSubscription(ctx context.Context, subscriptionID string) (*Subscription, error) {
	if m.subscriptionErr != nil {
		return nil, m.subscriptionErr
	}
	if m.subscription != nil {
		return m.subscription, nil
	}
	return &Subscription{
		ID:       subscriptionID,
		Provider: m.name,
		Status:   StatusActive,
	}, nil
}

func (m *MockProvider) CancelSubscription(ctx context.Context, subscriptionID string) error {
	return m.cancelErr
}

// TestMockProviderCreateCheckout tests mock provider checkout.
func TestMockProviderCreateCheckout(t *testing.T) {
	provider := &MockProvider{
		name: "test-provider",
	}

	ctx := context.Background()
	req := CheckoutRequest{
		TenantID:  "t1",
		UserID:    "u1",
		ProductID: "prod-123",
		PriceID:   "price-456",
	}

	session, err := provider.CreateCheckout(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Equal(t, "sess-123", session.SessionID)
}

// TestMockProviderWebhookError tests webhook error handling.
func TestMockProviderWebhookError(t *testing.T) {
	provider := &MockProvider{
		name:       "test-provider",
		webhookErr: ErrWebhookValidationFailed,
	}

	ctx := context.Background()
	req := WebhookRequest{
		Headers: map[string]string{},
		Body:    []byte("{}"),
	}

	event, err := provider.HandleWebhook(ctx, req)
	require.Error(t, err)
	require.Nil(t, event)
}

// TestMockProviderGetSubscription tests subscription fetch.
func TestMockProviderGetSubscription(t *testing.T) {
	now := time.Now()
	expectedSub := &Subscription{
		ID:        "sub-789",
		Provider:  "stripe",
		UserID:    "u1",
		TenantID:  "t1",
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	provider := &MockProvider{
		name:         "test-provider",
		subscription: expectedSub,
	}

	ctx := context.Background()
	sub, err := provider.GetSubscription(ctx, "sub-789")
	require.NoError(t, err)
	require.Equal(t, expectedSub, sub)
}

// TestMockProviderCancelSubscription tests cancellation.
func TestMockProviderCancelSubscription(t *testing.T) {
	provider := &MockProvider{
		name: "test-provider",
	}

	ctx := context.Background()
	err := provider.CancelSubscription(ctx, "sub-123")
	require.NoError(t, err)
}

// TestCheckoutRequestMetadata tests metadata map handling.
func TestCheckoutRequestMetadata(t *testing.T) {
	req := CheckoutRequest{
		TenantID:  "t1",
		UserID:    "u1",
		ProductID: "prod-123",
		Metadata: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}

	require.NotNil(t, req.Metadata)
	require.Equal(t, 2, len(req.Metadata))
	require.Equal(t, "value1", req.Metadata["key1"])
}

// TestSubscriptionEventRawPayload tests raw webhook payload handling.
func TestSubscriptionEventRawPayload(t *testing.T) {
	event := SubscriptionEvent{
		EventID:        "evt-123",
		Type:           EventCreated,
		SubscriptionID: "sub-456",
		RawPayload: map[string]any{
			"id":     "evt-123",
			"type":   "customer.subscription.created",
			"amount": 9999,
		},
	}

	require.NotNil(t, event.RawPayload)
	require.Equal(t, "evt-123", event.RawPayload["id"])
	require.Equal(t, 9999, event.RawPayload["amount"])
}

// ErrWebhookValidationFailed is the sentinel used by the mock provider.
var ErrWebhookValidationFailed = errors.New("webhook signature validation failed")

// TestSubscriptionEventWithTrialEnd tests trial end handling.
func TestSubscriptionEventWithTrialEnd(t *testing.T) {
	trialEnd := time.Now().Add(30 * 24 * time.Hour)
	event := SubscriptionEvent{
		EventID:        "evt-123",
		Type:           EventCreated,
		SubscriptionID: "sub-456",
		TrialEnd:       &trialEnd,
	}

	require.NotNil(t, event.TrialEnd)
	require.WithinDuration(t, trialEnd, *event.TrialEnd, time.Second)
}

// TestSubscriptionEventWithCancelation tests cancellation timestamp.
func TestSubscriptionEventWithCancelation(t *testing.T) {
	canceledAt := time.Now()
	event := SubscriptionEvent{
		EventID:        "evt-123",
		Type:           EventCanceled,
		SubscriptionID: "sub-456",
		CanceledAt:     &canceledAt,
	}

	require.NotNil(t, event.CanceledAt)
	require.WithinDuration(t, canceledAt, *event.CanceledAt, time.Second)
}
