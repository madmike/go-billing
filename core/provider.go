// Package core defines the protocol-agnostic billing interfaces. Every payment
// provider (Stripe, Telegram Stars, future) implements these so billing-service
// can route without knowing which provider handles which tenant.
package core

import (
	"context"
	"time"
)

// Provider is the base interface every billing backend implements.
type Provider interface {
	Name() string // "stripe" | "mercadopago" | "telegram_stars"

	// CreateCheckout initiates a payment session. Returns an opaque URL or
	// payload that the caller forwards to the user.
	CreateCheckout(ctx context.Context, req CheckoutRequest) (*CheckoutSession, error)

	// HandleWebhook processes a raw inbound webhook. The provider validates the
	// signature and returns the resulting SubscriptionEvent.
	HandleWebhook(ctx context.Context, req WebhookRequest) (*SubscriptionEvent, error)

	// GetSubscription fetches the current state of a subscription.
	GetSubscription(ctx context.Context, subscriptionID string) (*Subscription, error)

	// CancelSubscription cancels a subscription at period end.
	CancelSubscription(ctx context.Context, subscriptionID string) error
}

// CheckoutRequest describes what to charge for.
type CheckoutRequest struct {
	TenantID    string
	UserID      string
	ProductID   string // e.g. course ID or plan identifier
	PriceID     string // provider-side price/plan ID
	Currency    string // ISO-4217 currency code (e.g. USD, EUR, RUB)
	AmountMinor int64  // monthly amount in minor units (cents/kopeks)
	SuccessURL  string
	CancelURL   string
	TrialDays   int
	Metadata    map[string]string
}

// CheckoutSession is returned after initiating a checkout flow.
type CheckoutSession struct {
	SessionID string // provider-side session ID
	URL       string // redirect URL (Stripe) or payment URL (Stars)
	ExpiresAt time.Time
}

// WebhookRequest carries the raw HTTP webhook payload for the provider to validate.
type WebhookRequest struct {
	Headers map[string]string
	Body    []byte
}

// SubscriptionEvent is the normalised output of HandleWebhook.
// A single webhook may carry multiple events; providers return the most
// significant one. Nil means the webhook was valid but not actionable.
type SubscriptionEvent struct {
	// EventID is the provider-side unique identifier of the webhook event used
	// for idempotency. Stripe: evt.ID. Telegram Stars: TelegramPaymentChargeID.
	// Callers MUST treat this as the dedup key and skip already-processed events.
	EventID        string
	Type           SubscriptionEventType
	SubscriptionID string
	UserID         string
	TenantID       string
	ProductID      string
	Plan           string
	Status         SubscriptionStatus
	Provider       string
	PeriodStart    time.Time
	PeriodEnd      time.Time
	TrialEnd       *time.Time
	CanceledAt     *time.Time
	RawPayload     map[string]any
}

// SubscriptionEventType enumerates the lifecycle transitions.
type SubscriptionEventType string

const (
	EventCreated  SubscriptionEventType = "created"
	EventRenewed  SubscriptionEventType = "renewed"
	EventUpdated  SubscriptionEventType = "updated"
	EventCanceled SubscriptionEventType = "canceled"
	EventExpired  SubscriptionEventType = "expired"
	EventPastDue  SubscriptionEventType = "past_due"
	EventTrialEnd SubscriptionEventType = "trial_end"
)

// SubscriptionStatus is the current entitlement state.
type SubscriptionStatus string

const (
	StatusActive   SubscriptionStatus = "active"
	StatusTrialing SubscriptionStatus = "trialing"
	StatusPastDue  SubscriptionStatus = "past_due"
	StatusCanceled SubscriptionStatus = "canceled"
	StatusExpired  SubscriptionStatus = "expired"
)

// Subscription is the canonical subscription record.
type Subscription struct {
	ID          string
	Provider    string
	UserID      string
	TenantID    string
	ProductID   string
	Plan        string
	Status      SubscriptionStatus
	PeriodStart time.Time
	PeriodEnd   time.Time
	TrialEnd    *time.Time
	CanceledAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IsEntitled returns true when the subscription grants active access.
func (s Subscription) IsEntitled() bool {
	return s.Status == StatusActive || s.Status == StatusTrialing
}
