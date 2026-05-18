// Package factory instantiates billing providers from named presets, mirroring
// the go-ai-providers and go-messengers factory patterns.
package factory

import (
	"fmt"

	"github.com/madmike/go-billing/core"
	"github.com/madmike/go-billing/providers/stars"
	"github.com/madmike/go-billing/providers/stripe"
)

// ProviderFactory constructs a billing Provider from a Config.
type ProviderFactory func(cfg Config) (core.Provider, error)

// Registry maps preset name → factory function.
var Registry = map[string]ProviderFactory{
	"stripe": func(cfg Config) (core.Provider, error) {
		return stripe.New(cfg)
	},
	"telegram_stars": func(cfg Config) (core.Provider, error) {
		return stars.New(cfg)
	},
}

// Config holds the credentials needed to instantiate a billing provider.
// Each provider reads only the fields it needs.
type Config struct {
	Preset    string // "stripe" | "telegram_stars"
	SecretKey string // Stripe secret key or Stars bot token
	// Stripe-specific
	WebhookSecret  string
	PublishableKey string
	// Telegram Stars-specific
	BotToken string
}

func (c Config) GetSecretKey() string     { return c.SecretKey }
func (c Config) GetWebhookSecret() string { return c.WebhookSecret }

// Create instantiates a Provider by preset name.
func Create(cfg Config) (core.Provider, error) {
	factory, ok := Registry[cfg.Preset]
	if !ok {
		return nil, fmt.Errorf("unknown billing preset: %q (available: stripe, telegram_stars)", cfg.Preset)
	}
	return factory(cfg)
}
