// Package stars implements the billing core.Provider interface for
// Telegram Stars payments. Stars are Telegram's native in-app currency;
// payment events arrive as Telegram Bot API updates (pre_checkout_query,
// successful_payment) which the messenger-gateway routes to billing-service.
package stars

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/madmike/go-billing/core"
)

// Provider implements core.Provider for Telegram Stars.
type Provider struct {
	botToken string
}

// New creates a Telegram Stars billing provider.
func New(cfg any) (core.Provider, error) {
	type starsCfg struct {
		BotToken string
	}
	data, _ := json.Marshal(cfg)
	var sc starsCfg
	_ = json.Unmarshal(data, &sc)
	if sc.BotToken == "" {
		return nil, fmt.Errorf("stars: bot_token is required")
	}
	return &Provider{botToken: sc.BotToken}, nil
}

func (p *Provider) Name() string { return "telegram_stars" }

// CreateCheckout for Stars returns a send_invoice payload URL.
// The caller (billing-service) uses the Telegram Bot API to send an invoice
// to the user directly; there is no external checkout URL.
func (p *Provider) CreateCheckout(_ context.Context, req core.CheckoutRequest) (*core.CheckoutSession, error) {
	// Encode the invoice parameters as an opaque JSON payload. The
	// billing-service will call Telegram Bot API sendInvoice with these.
	payload := map[string]any{
		"bot_token":  p.botToken,
		"title":      req.ProductID,
		"tenant_id":  req.TenantID,
		"user_id":    req.UserID,
		"product_id": req.ProductID,
		"price_id":   req.PriceID, // Stars amount encoded here
	}
	data, _ := json.Marshal(payload)
	return &core.CheckoutSession{
		SessionID: fmt.Sprintf("stars-%s-%s", req.TenantID, req.UserID),
		URL:       string(data), // billing-service reads this as JSON
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}, nil
}

// HandleWebhook processes a Telegram successful_payment update forwarded by
// the messenger-gateway. The body is a JSON-encoded Telegram Update.
func (p *Provider) HandleWebhook(_ context.Context, req core.WebhookRequest) (*core.SubscriptionEvent, error) {
	var update struct {
		Message struct {
			SuccessfulPayment *struct {
				Currency                string `json:"currency"`
				TotalAmount             int    `json:"total_amount"` // Stars amount
				InvoicePayload          string `json:"invoice_payload"`
				TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
				ProviderPaymentChargeID string `json:"provider_payment_charge_id"`
			} `json:"successful_payment"`
		} `json:"message"`
	}

	if err := json.Unmarshal(req.Body, &update); err != nil {
		return nil, fmt.Errorf("unmarshal stars update: %w", err)
	}
	sp := update.Message.SuccessfulPayment
	if sp == nil {
		return nil, nil // not a payment update
	}

	var meta struct {
		TenantID  string `json:"tenant_id"`
		UserID    string `json:"user_id"`
		ProductID string `json:"product_id"`
		Plan      string `json:"plan"`
	}
	_ = json.Unmarshal([]byte(sp.InvoicePayload), &meta)

	now := time.Now()
	end := now.AddDate(0, 1, 0) // 1-month period by default
	return &core.SubscriptionEvent{
		EventID:        sp.TelegramPaymentChargeID,
		Type:           core.EventCreated,
		SubscriptionID: sp.TelegramPaymentChargeID,
		Provider:       p.Name(),
		TenantID:       meta.TenantID,
		UserID:         meta.UserID,
		ProductID:      meta.ProductID,
		Plan:           meta.Plan,
		Status:         core.StatusActive,
		PeriodStart:    now,
		PeriodEnd:      end,
	}, nil
}

// GetSubscription is not supported for Stars (no recurring subscription API).
// Stars payments are one-time; subscriptions are managed via billing-service DB.
func (p *Provider) GetSubscription(_ context.Context, subID string) (*core.Subscription, error) {
	return nil, fmt.Errorf("stars: GetSubscription not available (manage via billing-service DB)")
}

// CancelSubscription marks the subscription cancelled in the caller's DB.
// There is no Stripe-style cancellation API for Stars.
func (p *Provider) CancelSubscription(_ context.Context, _ string) error {
	return nil // caller updates DB directly
}
