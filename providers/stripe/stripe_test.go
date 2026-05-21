package stripe_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/madmike/go-billing/core"
	"github.com/madmike/go-billing/providers/stripe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── test helpers ────────────────────────────────────────────────────────────

type testCfg struct {
	secretKey     string
	webhookSecret string
}

func (c testCfg) GetSecretKey() string     { return c.secretKey }
func (c testCfg) GetWebhookSecret() string { return c.webhookSecret }

const (
	testSecretKey     = "sk_test_fake_key"
	testWebhookSecret = "whsec_test_fake_secret"
)

func newProvider(t *testing.T) core.Provider {
	t.Helper()
	p, err := stripe.New(testCfg{secretKey: testSecretKey, webhookSecret: testWebhookSecret})
	require.NoError(t, err)
	return p
}

// signedWebhook constructs a valid Stripe webhook with a correct Stripe-Signature header.
// The Stripe SDK verifies timestamp freshness within a 300s tolerance; we set
// timestamp = now so the constructed event is always accepted.
func signedWebhook(t *testing.T, secret, evtType string, dataRaw map[string]any) core.WebhookRequest {
	t.Helper()
	timestamp := time.Now().Unix()

	payload := map[string]any{
		"id":          "evt_" + fmt.Sprintf("%d", timestamp),
		"type":        evtType,
		"created":     timestamp,
		"api_version": "2024-09-30.acacia", // must match stripe-go v80 expectation
		"data": map[string]any{
			"object": dataRaw,
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	// Compute Stripe HMAC-SHA256 signature: HMAC(secret, "<timestamp>.<body>")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.", timestamp)))
	mac.Write(body)
	sig := fmt.Sprintf("%x", mac.Sum(nil))
	sigHeader := fmt.Sprintf("t=%d,v1=%s", timestamp, sig)

	return core.WebhookRequest{
		Headers: map[string]string{"Stripe-Signature": sigHeader},
		Body:    body,
	}
}

// stripeSubscription builds a minimal Stripe subscription JSON object.
func stripeSubscription(subID, status, priceID, tenantID, userID, productID string) map[string]any {
	now := time.Now()
	return map[string]any{
		"id":                   subID,
		"object":               "subscription",
		"status":               status,
		"current_period_start": now.Unix(),
		"current_period_end":   now.Add(30 * 24 * time.Hour).Unix(),
		"canceled_at":          nil,
		"trial_end":            nil,
		"metadata": map[string]string{
			"tenant_id":  tenantID,
			"user_id":    userID,
			"product_id": productID,
		},
		"items": map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{
					"id": "si_test",
					"price": map[string]any{
						"id":       priceID,
						"currency": "usd",
					},
				},
			},
		},
	}
}

// ─── Provider construction ────────────────────────────────────────────────────

func TestNew_Success(t *testing.T) {
	p, err := stripe.New(testCfg{secretKey: "sk_test_x", webhookSecret: "whsec_x"})
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "stripe", p.Name())
}

func TestNewFromFactory_Success(t *testing.T) {
	cfg := map[string]any{
		"SecretKey":     "sk_test_x",
		"WebhookSecret": "whsec_x",
	}
	p, err := stripe.NewFromFactory(cfg)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "stripe", p.Name())
}

// ─── HandleWebhook — signature verification ───────────────────────────────────

func TestHandleWebhook_InvalidSignature(t *testing.T) {
	p := newProvider(t)
	req := core.WebhookRequest{
		Headers: map[string]string{"Stripe-Signature": "t=1234,v1=invalidsig"},
		Body:    []byte(`{"type":"customer.subscription.created","data":{"object":{}}}`),
	}
	_, err := p.HandleWebhook(context.Background(), req)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "webhook signature") ||
		strings.Contains(err.Error(), "signature"),
		"expected signature error, got: %v", err)
}

func TestHandleWebhook_MissingSignatureHeader(t *testing.T) {
	p := newProvider(t)
	req := core.WebhookRequest{
		Headers: map[string]string{},
		Body:    []byte(`{"type":"customer.subscription.created","data":{"object":{}}}`),
	}
	_, err := p.HandleWebhook(context.Background(), req)
	require.Error(t, err)
}

// ─── HandleWebhook — event type routing ──────────────────────────────────────

func TestHandleWebhook_SubscriptionCreated(t *testing.T) {
	p := newProvider(t)
	sub := stripeSubscription("sub_123", "active", "price_monthly", "tenant-1", "user-1", "prod_basic")
	req := signedWebhook(t, testWebhookSecret, "customer.subscription.created", sub)

	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, evt)

	assert.Equal(t, core.EventCreated, evt.Type)
	assert.Equal(t, "sub_123", evt.SubscriptionID)
	assert.Equal(t, "stripe", evt.Provider)
	assert.Equal(t, "tenant-1", evt.TenantID)
	assert.Equal(t, "user-1", evt.UserID)
	assert.Equal(t, "prod_basic", evt.ProductID)
	assert.Equal(t, "price_monthly", evt.Plan)
	assert.Equal(t, core.StatusActive, evt.Status)
	assert.NotEmpty(t, evt.EventID)
}

func TestHandleWebhook_SubscriptionUpdated(t *testing.T) {
	p := newProvider(t)
	sub := stripeSubscription("sub_456", "trialing", "price_annual", "tenant-2", "user-2", "prod_pro")
	req := signedWebhook(t, testWebhookSecret, "customer.subscription.updated", sub)

	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, evt)

	assert.Equal(t, core.EventUpdated, evt.Type)
	assert.Equal(t, core.StatusTrialing, evt.Status)
	assert.Equal(t, "price_annual", evt.Plan)
}

func TestHandleWebhook_SubscriptionDeleted(t *testing.T) {
	p := newProvider(t)
	sub := stripeSubscription("sub_789", "canceled", "price_monthly", "tenant-3", "user-3", "prod_basic")
	req := signedWebhook(t, testWebhookSecret, "customer.subscription.deleted", sub)

	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, evt)

	assert.Equal(t, core.EventCanceled, evt.Type)
	assert.Equal(t, core.StatusCanceled, evt.Status)
}

func TestHandleWebhook_UnhandledEventType(t *testing.T) {
	p := newProvider(t)
	// payment_intent.created is a valid Stripe event but not handled by the provider.
	req := signedWebhook(t, testWebhookSecret, "payment_intent.created", map[string]any{
		"id": "pi_test",
	})

	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err) // valid signature — no error
	assert.Nil(t, evt)      // but nil event — not actionable
}

// ─── HandleWebhook — field mapping ───────────────────────────────────────────

func TestHandleWebhook_TrialEnd(t *testing.T) {
	p := newProvider(t)
	trialEnd := time.Now().Add(14 * 24 * time.Hour).Unix()
	sub := stripeSubscription("sub_trial", "trialing", "price_monthly", "t1", "u1", "p1")
	sub["trial_end"] = trialEnd

	req := signedWebhook(t, testWebhookSecret, "customer.subscription.created", sub)
	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, evt)
	require.NotNil(t, evt.TrialEnd)
	assert.WithinDuration(t, time.Unix(trialEnd, 0), *evt.TrialEnd, time.Second)
}

func TestHandleWebhook_CanceledAt(t *testing.T) {
	p := newProvider(t)
	canceledAt := time.Now().Add(-time.Hour).Unix()
	sub := stripeSubscription("sub_canceled", "canceled", "price_monthly", "t1", "u1", "p1")
	sub["canceled_at"] = canceledAt

	req := signedWebhook(t, testWebhookSecret, "customer.subscription.deleted", sub)
	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, evt)
	require.NotNil(t, evt.CanceledAt)
	assert.WithinDuration(t, time.Unix(canceledAt, 0), *evt.CanceledAt, time.Second)
}

func TestHandleWebhook_EventIDPopulated(t *testing.T) {
	p := newProvider(t)
	sub := stripeSubscription("sub_evtid", "active", "price_monthly", "t1", "u1", "p1")
	req := signedWebhook(t, testWebhookSecret, "customer.subscription.created", sub)

	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.NotEmpty(t, evt.EventID, "EventID must be set for idempotency")
	assert.True(t, strings.HasPrefix(evt.EventID, "evt_"))
}

// ─── stripeStatusToCore mapping ──────────────────────────────────────────────

func TestHandleWebhook_StatusMapping(t *testing.T) {
	cases := []struct {
		stripeStatus string
		coreStatus   core.SubscriptionStatus
	}{
		{"active", core.StatusActive},
		{"trialing", core.StatusTrialing},
		{"past_due", core.StatusPastDue},
		{"unpaid", core.StatusPastDue},
		{"canceled", core.StatusCanceled},
		{"incomplete", core.StatusExpired},
		{"incomplete_expired", core.StatusExpired},
	}

	p := newProvider(t)
	for _, tc := range cases {
		sub := stripeSubscription("sub_status", tc.stripeStatus, "price_x", "t1", "u1", "p1")
		req := signedWebhook(t, testWebhookSecret, "customer.subscription.updated", sub)

		evt, err := p.HandleWebhook(context.Background(), req)
		require.NoError(t, err, "stripe_status=%q", tc.stripeStatus)
		require.NotNil(t, evt)
		assert.Equal(t, tc.coreStatus, evt.Status, "stripe_status=%q", tc.stripeStatus)
	}
}
