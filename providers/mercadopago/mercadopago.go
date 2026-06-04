// Package mercadopago implements the billing core.Provider interface for
// Mercado Pago subscription checkouts (preapproval).
package mercadopago

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/madmike/go-billing/core"
)

var (
	errMissingSignature      = errors.New("mercadopago: missing x-signature header")
	errMissingWebhookSecret  = errors.New("mercadopago: webhook secret not configured")
	errInvalidSignature      = errors.New("mercadopago: invalid webhook signature")
)

const defaultAPIBaseURL = "https://api.mercadopago.com"

// Provider implements core.Provider for Mercado Pago.
type Provider struct {
	accessToken   string
	webhookSecret string
	apiBaseURL    string
	httpClient    *http.Client
}

// New creates a Mercado Pago billing provider.
func New(cfg interface {
	GetAccessToken() string
	GetWebhookSecret() string
	GetAPIBaseURL() string
}) (core.Provider, error) {
	accessToken := strings.TrimSpace(cfg.GetAccessToken())
	if accessToken == "" {
		return nil, fmt.Errorf("mercadopago: access token is required")
	}
	apiBaseURL := strings.TrimSpace(cfg.GetAPIBaseURL())
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	return &Provider{
		accessToken:   accessToken,
		webhookSecret: strings.TrimSpace(cfg.GetWebhookSecret()),
		apiBaseURL:    strings.TrimRight(apiBaseURL, "/"),
		httpClient:    &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *Provider) Name() string { return "mercadopago" }

func (p *Provider) CreateCheckout(ctx context.Context, req core.CheckoutRequest) (*core.CheckoutSession, error) {
	payload := map[string]any{
		"reason":             defaultString(req.Metadata["plan_name"], req.ProductID),
		"external_reference": encodeExternalRef(req.TenantID, req.UserID, req.ProductID),
		"back_url":           req.SuccessURL,
		"status":             "pending",
	}
	if req.PriceID != "" {
		payload["preapproval_plan_id"] = req.PriceID
	} else {
		currency := strings.ToUpper(strings.TrimSpace(req.Currency))
		if currency == "" || req.AmountMinor <= 0 {
			return nil, fmt.Errorf("mercadopago checkout: preapproval_plan_id or (currency and amount_minor) is required")
		}
		payload["auto_recurring"] = map[string]any{
			"frequency":          1,
			"frequency_type":     "months",
			"transaction_amount": float64(req.AmountMinor) / 100.0,
			"currency_id":        currency,
		}
	}
	if email := strings.TrimSpace(req.Metadata["payer_email"]); email != "" {
		payload["payer_email"] = email
	}

	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiBaseURL+"/preapproval", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mercadopago checkout request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.accessToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mercadopago checkout call: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	var out struct {
		ID           string `json:"id"`
		InitPoint    string `json:"init_point"`
		SandboxPoint string `json:"sandbox_init_point"`
	}
	if len(bodyBytes) > 0 {
		_ = json.Unmarshal(bodyBytes, &out)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mercadopago checkout failed: status=%d id=%s body=%s", resp.StatusCode, out.ID, trimForLog(string(bodyBytes), 512))
	}
	if len(bodyBytes) == 0 {
		return nil, fmt.Errorf("mercadopago checkout failed: empty response body")
	}

	url := strings.TrimSpace(out.InitPoint)
	if url == "" {
		url = strings.TrimSpace(out.SandboxPoint)
	}
	if url == "" {
		return nil, fmt.Errorf("mercadopago checkout failed: missing init_point")
	}

	return &core.CheckoutSession{
		SessionID: out.ID,
		URL:       url,
		ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
	}, nil
}

// verifySignature validates the MercadoPago x-signature header using HMAC-SHA256.
// The signature format is: ts=<timestamp> v1=<signature>
// The signing message is: "id:<data.id>;ts:<ts>;"
func (p *Provider) verifySignature(req core.WebhookRequest, dataID string) error {
	if p.webhookSecret == "" {
		return errMissingWebhookSecret
	}

	sigHeader := req.Headers["x-signature"]
	if sigHeader == "" {
		return errMissingSignature
	}

	// Parse ts and v1 from header
	var ts, v1 string
	for _, part := range strings.Split(sigHeader, " ") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "ts=") {
			ts = strings.TrimPrefix(part, "ts=")
		} else if strings.HasPrefix(part, "v1=") {
			v1 = strings.TrimPrefix(part, "v1=")
		}
	}
	if ts == "" || v1 == "" {
		return errInvalidSignature
	}

	// Build signing string: id:<data.id>;ts:<ts>;
	signingString := fmt.Sprintf("id:%s;ts:%s;", dataID, ts)

	// Compute expected signature
	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write([]byte(signingString))
	expected := hex.EncodeToString(mac.Sum(nil))

	// Constant-time comparison
	if !hmac.Equal([]byte(expected), []byte(v1)) {
		return errInvalidSignature
	}
	return nil
}

func (p *Provider) HandleWebhook(ctx context.Context, req core.WebhookRequest) (*core.SubscriptionEvent, error) {
	var payload struct {
		Type   string `json:"type"`
		Action string `json:"action"`
		Data   struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(req.Body, &payload)

	// Verify HMAC signature before processing
	if err := p.verifySignature(req, payload.Data.ID); err != nil {
		return nil, fmt.Errorf("mercadopago webhook: %w", err)
	}

	subID := strings.TrimSpace(payload.Data.ID)
	if subID == "" {
		return nil, nil // valid but not actionable for subscriptions
	}

	sub, err := p.GetSubscription(ctx, subID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, nil
	}

	eventType := core.EventUpdated
	if sub.Status == core.StatusCanceled || sub.Status == core.StatusExpired {
		eventType = core.EventCanceled
	}
	eventID := strings.TrimSpace(req.Headers["x-request-id"])
	if eventID == "" {
		eventID = fmt.Sprintf("%s:%s:%d", sub.ID, sub.Status, sub.PeriodEnd.Unix())
	}

	return &core.SubscriptionEvent{
		EventID:        eventID,
		Type:           eventType,
		SubscriptionID: sub.ID,
		UserID:         sub.UserID,
		TenantID:       sub.TenantID,
		ProductID:      sub.ProductID,
		Plan:           sub.Plan,
		Status:         sub.Status,
		Provider:       p.Name(),
		PeriodStart:    sub.PeriodStart,
		PeriodEnd:      sub.PeriodEnd,
		TrialEnd:       sub.TrialEnd,
		CanceledAt:     sub.CanceledAt,
	}, nil
}

func (p *Provider) GetSubscription(ctx context.Context, subscriptionID string) (*core.Subscription, error) {
	if strings.TrimSpace(subscriptionID) == "" {
		return nil, fmt.Errorf("mercadopago: subscription id is required")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiBaseURL+"/preapproval/"+subscriptionID, nil)
	if err != nil {
		return nil, fmt.Errorf("mercadopago get subscription request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.accessToken)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mercadopago get subscription call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mercadopago get subscription failed: status=%d", resp.StatusCode)
	}

	var out struct {
		ID              string `json:"id"`
		Status          string `json:"status"`
		ExternalRef     string `json:"external_reference"`
		PlanID          string `json:"preapproval_plan_id"`
		DateCreated     string `json:"date_created"`
		NextPaymentDate string `json:"next_payment_date"`
		LastModified    string `json:"last_modified"`
		AutoRecurring   struct {
			Frequency     int     `json:"frequency"`
			FrequencyType string  `json:"frequency_type"`
			Amount        float64 `json:"transaction_amount"`
			CurrencyID    string  `json:"currency_id"`
		} `json:"auto_recurring"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("mercadopago get subscription decode: %w", err)
	}

	tenantID, userID, productID := decodeExternalRef(out.ExternalRef)
	now := time.Now().UTC()
	periodStart := parseTimeOr(out.LastModified, parseTimeOr(out.DateCreated, now))
	periodEnd := parseTimeOr(out.NextPaymentDate, periodStart.AddDate(0, 1, 0))
	if !periodEnd.After(periodStart) {
		periodEnd = periodStart.AddDate(0, 1, 0)
	}

	status := mpStatusToCore(out.Status)
	var canceledAt *time.Time
	if status == core.StatusCanceled || status == core.StatusExpired {
		t := time.Now().UTC()
		canceledAt = &t
	}

	return &core.Subscription{
		ID:          out.ID,
		Provider:    p.Name(),
		UserID:      userID,
		TenantID:    tenantID,
		ProductID:   productID,
		Plan:        firstNonEmpty(productID, out.PlanID),
		Status:      status,
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		CanceledAt:  canceledAt,
		CreatedAt:   parseTimeOr(out.DateCreated, now),
		UpdatedAt:   now,
	}, nil
}

func (p *Provider) CancelSubscription(ctx context.Context, subscriptionID string) error {
	if strings.TrimSpace(subscriptionID) == "" {
		return fmt.Errorf("mercadopago: subscription id is required")
	}
	body := bytes.NewBufferString(`{"status":"cancelled"}`)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, p.apiBaseURL+"/preapproval/"+subscriptionID, body)
	if err != nil {
		return fmt.Errorf("mercadopago cancel request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.accessToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("mercadopago cancel call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("mercadopago cancel failed: status=%d", resp.StatusCode)
	}
	return nil
}

func defaultString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	fallback = strings.TrimSpace(fallback)
	if fallback != "" {
		return fallback
	}
	return "Aulinq Subscription"
}

func encodeExternalRef(tenantID, userID, productID string) string {
	return strings.Join([]string{
		strings.TrimSpace(tenantID),
		strings.TrimSpace(userID),
		strings.TrimSpace(productID),
	}, ":")
}

func decodeExternalRef(ref string) (tenantID, userID, productID string) {
	parts := strings.Split(ref, ":")
	if len(parts) > 0 {
		tenantID = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		userID = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		productID = strings.TrimSpace(parts[2])
	}
	return tenantID, userID, productID
}

func parseTimeOr(value string, fallback time.Time) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts
	}
	return fallback
}

func mpStatusToCore(status string) core.SubscriptionStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "authorized":
		return core.StatusActive
	case "pending":
		return core.StatusTrialing
	case "paused":
		return core.StatusPastDue
	case "cancelled", "canceled":
		return core.StatusCanceled
	default:
		return core.StatusExpired
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func trimForLog(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}
