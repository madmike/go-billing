package factory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFactoryCreate instantiates each provider from the registry.
func TestFactoryCreate(t *testing.T) {
	tests := []struct {
		name          string
		config        Config
		shouldErr     bool
		expectedName  string
	}{
		{
			"stripe provider",
			Config{Preset: "stripe", SecretKey: "sk_test_123", WebhookSecret: "whsec_123"},
			false,
			"stripe",
		},
		{
			"telegram_stars provider",
			Config{Preset: "telegram_stars", SecretKey: "bot_token_123", BotToken: "bot_token_123"},
			false,
			"telegram_stars",
		},
		{
			"mercadopago provider",
			Config{Preset: "mercadopago", AccessToken: "APP_USR_test_123"},
			false,
			"mercadopago",
		},
		{
			"unknown preset",
			Config{Preset: "unknown", SecretKey: "key"},
			true,
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := Create(tt.config)
			if tt.shouldErr {
				require.Error(t, err)
				require.Nil(t, provider)
				require.Contains(t, err.Error(), "unknown billing preset")
			} else {
				require.NoError(t, err)
				require.NotNil(t, provider)
				require.Equal(t, tt.expectedName, provider.Name())
			}
		})
	}
}

// TestRegistryContainsStripe verifies Stripe is registered.
func TestRegistryContainsStripe(t *testing.T) {
	_, ok := Registry["stripe"]
	require.True(t, ok, "stripe not in registry")
}

// TestRegistryContainsTelegramStars verifies Telegram Stars is registered.
func TestRegistryContainsTelegramStars(t *testing.T) {
	_, ok := Registry["telegram_stars"]
	require.True(t, ok, "telegram_stars not in registry")
}

// TestRegistryContainsMercadoPago verifies Mercado Pago is registered.
func TestRegistryContainsMercadoPago(t *testing.T) {
	_, ok := Registry["mercadopago"]
	require.True(t, ok, "mercadopago not in registry")
}

// TestConfigGetSecretKey tests the Config getter.
func TestConfigGetSecretKey(t *testing.T) {
	cfg := Config{
		SecretKey: "my-secret-key",
	}
	require.Equal(t, "my-secret-key", cfg.GetSecretKey())
}

// TestConfigGetWebhookSecret tests the webhook secret getter.
func TestConfigGetWebhookSecret(t *testing.T) {
	cfg := Config{
		WebhookSecret: "my-webhook-secret",
	}
	require.Equal(t, "my-webhook-secret", cfg.GetWebhookSecret())
}

// TestFactoryErrorMessageOnUnknownPreset verifies helpful error message.
func TestFactoryErrorMessageOnUnknownPreset(t *testing.T) {
	cfg := Config{Preset: "nonexistent"}
	_, err := Create(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "available: stripe, mercadopago, telegram_stars")
}

// TestFactoryDirectRegistryUsage tests the factory lookup pattern.
func TestFactoryDirectRegistryUsage(t *testing.T) {
	factory, ok := Registry["stripe"]
	require.True(t, ok)
	require.NotNil(t, factory)

	// Factory function should be callable
	cfg := Config{Preset: "stripe", SecretKey: "sk_test_123", WebhookSecret: "whsec_123"}
	provider, err := factory(cfg)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, "stripe", provider.Name())
}
