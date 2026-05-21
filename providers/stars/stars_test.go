package stars_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/madmike/go-billing/core"
	"github.com/madmike/go-billing/providers/stars"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── construction ─────────────────────────────────────────────────────────────

func TestNew_Success(t *testing.T) {
	cfg := map[string]any{"BotToken": "bot123:TEST"}
	p, err := stars.New(cfg)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "telegram_stars", p.Name())
}

func TestNew_MissingBotToken(t *testing.T) {
	_, err := stars.New(map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bot_token")
}

func TestNew_EmptyBotToken(t *testing.T) {
	_, err := stars.New(map[string]any{"BotToken": ""})
	require.Error(t, err)
}

// ─── CreateCheckout ────────────────────────────────────────────────────────────

func TestCreateCheckout_ReturnsSession(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	req := core.CheckoutRequest{
		TenantID:  "tenant-1",
		UserID:    "user-1",
		ProductID: "course-101",
		PriceID:   "100", // Stars amount
	}

	sess, err := p.CreateCheckout(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, sess)

	assert.NotEmpty(t, sess.SessionID)
	assert.NotEmpty(t, sess.URL)
	assert.True(t, sess.ExpiresAt.After(time.Now()))
}

func TestCreateCheckout_URLContainsPayload(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	req := core.CheckoutRequest{
		TenantID:  "tenant-42",
		UserID:    "user-99",
		ProductID: "course-999",
		PriceID:   "50",
	}

	sess, err := p.CreateCheckout(context.Background(), req)
	require.NoError(t, err)

	// URL is a JSON blob with invoice parameters.
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(sess.URL), &payload), "URL must be valid JSON")
	assert.Equal(t, "tenant-42", payload["tenant_id"])
	assert.Equal(t, "user-99", payload["user_id"])
	assert.Equal(t, "course-999", payload["product_id"])
	assert.Equal(t, "bot123:TEST", payload["bot_token"])
}

func TestCreateCheckout_SessionIDContainsTenantAndUser(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	req := core.CheckoutRequest{TenantID: "abc", UserID: "xyz"}
	sess, err := p.CreateCheckout(context.Background(), req)
	require.NoError(t, err)

	assert.True(t, strings.Contains(sess.SessionID, "abc"), "session ID should contain tenant_id")
	assert.True(t, strings.Contains(sess.SessionID, "xyz"), "session ID should contain user_id")
}

// ─── HandleWebhook ────────────────────────────────────────────────────────────

func successfulPaymentBody(chargeID, invoicePayload string) []byte {
	update := map[string]any{
		"update_id": 123456,
		"message": map[string]any{
			"message_id": 1,
			"successful_payment": map[string]any{
				"currency":                  "XTR",
				"total_amount":              100,
				"invoice_payload":           invoicePayload,
				"telegram_payment_charge_id": chargeID,
				"provider_payment_charge_id": "stripe_" + chargeID,
			},
		},
	}
	body, _ := json.Marshal(update)
	return body
}

func TestHandleWebhook_SuccessfulPayment(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	invoicePayload, _ := json.Marshal(map[string]string{
		"tenant_id":  "tenant-1",
		"user_id":    "user-1",
		"product_id": "course-101",
		"plan":       "monthly",
	})
	req := core.WebhookRequest{
		Headers: map[string]string{},
		Body:    successfulPaymentBody("charge_abc123", string(invoicePayload)),
	}

	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, evt)

	assert.Equal(t, "charge_abc123", evt.EventID)
	assert.Equal(t, "charge_abc123", evt.SubscriptionID)
	assert.Equal(t, core.EventCreated, evt.Type)
	assert.Equal(t, core.StatusActive, evt.Status)
	assert.Equal(t, "telegram_stars", evt.Provider)
	assert.Equal(t, "tenant-1", evt.TenantID)
	assert.Equal(t, "user-1", evt.UserID)
	assert.Equal(t, "course-101", evt.ProductID)
	assert.Equal(t, "monthly", evt.Plan)
	assert.False(t, evt.PeriodStart.IsZero())
	assert.True(t, evt.PeriodEnd.After(evt.PeriodStart))
}

func TestHandleWebhook_PeriodIsOneMonth(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	invoicePayload, _ := json.Marshal(map[string]string{
		"tenant_id": "t1", "user_id": "u1",
	})
	req := core.WebhookRequest{
		Body: successfulPaymentBody("charge_period", string(invoicePayload)),
	}
	before := time.Now()
	evt, err := p.HandleWebhook(context.Background(), req)
	after := time.Now()
	require.NoError(t, err)

	// Period must start near now.
	assert.True(t, !evt.PeriodStart.Before(before.Add(-time.Second)))
	assert.True(t, !evt.PeriodStart.After(after.Add(time.Second)))
	// Period end must be ~1 month after start.
	diff := evt.PeriodEnd.Sub(evt.PeriodStart)
	assert.True(t, diff >= 28*24*time.Hour && diff <= 32*24*time.Hour,
		"expected ~1 month period, got %v", diff)
}

func TestHandleWebhook_NotAPaymentUpdate(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	// A message update without successful_payment.
	body := []byte(`{"update_id":1,"message":{"message_id":1,"text":"hello"}}`)
	req := core.WebhookRequest{Body: body}

	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	assert.Nil(t, evt, "non-payment updates must return nil without error")
}

func TestHandleWebhook_MalformedJSON(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	req := core.WebhookRequest{Body: []byte(`not json`)}
	_, err = p.HandleWebhook(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestHandleWebhook_EmptyBody(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	req := core.WebhookRequest{Body: []byte(`{}`)}
	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	assert.Nil(t, evt)
}

func TestHandleWebhook_MissingInvoicePayloadFields(t *testing.T) {
	// Invoice payload with missing metadata — should still return an event
	// (graceful degradation: empty strings for missing fields).
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	req := core.WebhookRequest{
		Body: successfulPaymentBody("charge_empty_meta", `{}`),
	}
	evt, err := p.HandleWebhook(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.Equal(t, "charge_empty_meta", evt.EventID)
	assert.Empty(t, evt.TenantID, "graceful degradation: empty tenant_id")
}

// ─── GetSubscription ──────────────────────────────────────────────────────────

func TestGetSubscription_ReturnsError(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	_, err = p.GetSubscription(context.Background(), "sub_xxx")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GetSubscription not available")
}

// ─── CancelSubscription ───────────────────────────────────────────────────────

func TestCancelSubscription_NoOp(t *testing.T) {
	p, err := stars.New(map[string]any{"BotToken": "bot123:TEST"})
	require.NoError(t, err)

	// Stars has no server-side cancel API — must return nil.
	err = p.CancelSubscription(context.Background(), "sub_stars_123")
	assert.NoError(t, err)
}
