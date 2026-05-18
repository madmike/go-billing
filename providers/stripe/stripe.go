// Package stripe implements the billing core.Provider interface using the
// Stripe API. It handles subscription checkout, webhook processing, and
// entitlement checks.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/madmike/go-billing/core"
	stripego "github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/checkout/session"
	"github.com/stripe/stripe-go/v80/subscription"
	"github.com/stripe/stripe-go/v80/webhook"
)

// Provider implements core.Provider for Stripe.
type Provider struct {
	secretKey     string
	webhookSecret string
}

// New creates a Stripe billing provider.
func New(cfg interface {
	GetSecretKey() string
	GetWebhookSecret() string
}) (core.Provider, error) {
	return &Provider{
		secretKey:     cfg.GetSecretKey(),
		webhookSecret: cfg.GetWebhookSecret(),
	}, nil
}

// NewFromFactory is the factory.ProviderFactory-compatible constructor.
func NewFromFactory(cfg any) (core.Provider, error) {
	type factoryCfg struct {
		SecretKey     string
		WebhookSecret string
	}
	data, _ := json.Marshal(cfg)
	var fc factoryCfg
	_ = json.Unmarshal(data, &fc)
	return &Provider{secretKey: fc.SecretKey, webhookSecret: fc.WebhookSecret}, nil
}

func (p *Provider) Name() string { return "stripe" }

func (p *Provider) CreateCheckout(_ context.Context, req core.CheckoutRequest) (*core.CheckoutSession, error) {
	stripego.Key = p.secretKey

	params := &stripego.CheckoutSessionParams{
		Mode: stripego.String(string(stripego.CheckoutSessionModeSubscription)),
		LineItems: []*stripego.CheckoutSessionLineItemParams{
			{
				Price:    stripego.String(req.PriceID),
				Quantity: stripego.Int64(1),
			},
		},
		SuccessURL: stripego.String(req.SuccessURL),
		CancelURL:  stripego.String(req.CancelURL),
		Metadata: map[string]string{
			"tenant_id":  req.TenantID,
			"user_id":    req.UserID,
			"product_id": req.ProductID,
		},
	}
	if req.TrialDays > 0 {
		params.SubscriptionData = &stripego.CheckoutSessionSubscriptionDataParams{
			TrialPeriodDays: stripego.Int64(int64(req.TrialDays)),
		}
	}

	sess, err := session.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe checkout: %w", err)
	}

	return &core.CheckoutSession{
		SessionID: sess.ID,
		URL:       sess.URL,
		ExpiresAt: time.Unix(sess.ExpiresAt, 0),
	}, nil
}

func (p *Provider) HandleWebhook(_ context.Context, req core.WebhookRequest) (*core.SubscriptionEvent, error) {
	stripego.Key = p.secretKey

	sig := req.Headers["Stripe-Signature"]
	evt, err := webhook.ConstructEvent(req.Body, sig, p.webhookSecret)
	if err != nil {
		return nil, fmt.Errorf("webhook signature: %w", err)
	}

	switch evt.Type {
	case "customer.subscription.created",
		"customer.subscription.updated",
		"customer.subscription.deleted":
		var sub stripego.Subscription
		if err := json.Unmarshal(evt.Data.Raw, &sub); err != nil {
			return nil, fmt.Errorf("unmarshal subscription: %w", err)
		}
		out := subscriptionToEvent(string(evt.Type), &sub, p.Name())
		out.EventID = evt.ID
		return out, nil
	}
	return nil, nil // unhandled but valid webhook
}

func (p *Provider) GetSubscription(_ context.Context, subID string) (*core.Subscription, error) {
	stripego.Key = p.secretKey
	sub, err := subscription.Get(subID, nil)
	if err != nil {
		return nil, fmt.Errorf("get subscription %s: %w", subID, err)
	}
	return stripeSubToCore(sub, p.Name()), nil
}

func (p *Provider) CancelSubscription(_ context.Context, subID string) error {
	stripego.Key = p.secretKey
	_, err := subscription.Cancel(subID, &stripego.SubscriptionCancelParams{
		InvoiceNow: stripego.Bool(false),
		Prorate:    stripego.Bool(true),
	})
	return err
}

// ─────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────

func subscriptionToEvent(evtType string, sub *stripego.Subscription, provider string) *core.SubscriptionEvent {
	evt := &core.SubscriptionEvent{
		SubscriptionID: sub.ID,
		Provider:       provider,
		Plan:           sub.Items.Data[0].Price.ID,
		Status:         stripeStatusToCore(string(sub.Status)),
		PeriodStart:    time.Unix(sub.CurrentPeriodStart, 0),
		PeriodEnd:      time.Unix(sub.CurrentPeriodEnd, 0),
	}
	if sub.Metadata != nil {
		evt.TenantID = sub.Metadata["tenant_id"]
		evt.UserID = sub.Metadata["user_id"]
		evt.ProductID = sub.Metadata["product_id"]
	}
	if sub.TrialEnd != 0 {
		t := time.Unix(sub.TrialEnd, 0)
		evt.TrialEnd = &t
	}
	if sub.CanceledAt != 0 {
		t := time.Unix(sub.CanceledAt, 0)
		evt.CanceledAt = &t
	}

	switch evtType {
	case "customer.subscription.created":
		evt.Type = core.EventCreated
	case "customer.subscription.deleted":
		evt.Type = core.EventCanceled
	default:
		evt.Type = core.EventUpdated
	}
	return evt
}

func stripeSubToCore(sub *stripego.Subscription, provider string) *core.Subscription {
	s := &core.Subscription{
		ID:          sub.ID,
		Provider:    provider,
		Plan:        sub.Items.Data[0].Price.ID,
		Status:      stripeStatusToCore(string(sub.Status)),
		PeriodStart: time.Unix(sub.CurrentPeriodStart, 0),
		PeriodEnd:   time.Unix(sub.CurrentPeriodEnd, 0),
		CreatedAt:   time.Unix(sub.Created, 0),
	}
	if sub.Metadata != nil {
		s.TenantID = sub.Metadata["tenant_id"]
		s.UserID = sub.Metadata["user_id"]
		s.ProductID = sub.Metadata["product_id"]
	}
	if sub.TrialEnd != 0 {
		t := time.Unix(sub.TrialEnd, 0)
		s.TrialEnd = &t
	}
	if sub.CanceledAt != 0 {
		t := time.Unix(sub.CanceledAt, 0)
		s.CanceledAt = &t
	}
	return s
}

func stripeStatusToCore(s string) core.SubscriptionStatus {
	switch s {
	case "active":
		return core.StatusActive
	case "trialing":
		return core.StatusTrialing
	case "past_due", "unpaid":
		return core.StatusPastDue
	case "canceled":
		return core.StatusCanceled
	default:
		return core.StatusExpired
	}
}
