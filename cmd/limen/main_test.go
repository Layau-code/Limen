package main

import (
	"context"
	"testing"

	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/credentialstore"
	"github.com/huz/limen/internal/gateway"
)

func TestGatewayTargetPreservesPricing(t *testing.T) {
	source := config.Target{Provider: "openai", UpstreamModel: "gpt-test", Pricing: &config.Pricing{InputPerMillionNanoUSD: 11}}
	target := gatewayTarget(source)
	if target.Provider != source.Provider || target.UpstreamModel != source.UpstreamModel || target.Pricing == nil || target.Pricing.InputPerMillionNanoUSD != 11 {
		t.Fatalf("target = %+v", target)
	}
}

type testKeySetter struct{ key string }

func (setter *testKeySetter) SetAPIKey(key string) error {
	setter.key = key
	return nil
}

func TestLoadStoredCredentialDoesNotExposeMissingRecordAsSuccess(t *testing.T) {
	vault, err := credentialstore.NewVault([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	setter := &testKeySetter{}
	loaded, err := loadStoredCredential(context.Background(), credentialstore.NewMemoryStore(vault), "tenant", "openai", "endpoint", setter)
	if err != nil || loaded || setter.key != "" {
		t.Fatalf("loaded=%t key=%q err=%v", loaded, setter.key, err)
	}
}

func TestValidateRuntimeProviderKeysUsesActiveRegistry(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "openai-only", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{OpenAIAPIKey: "openai-secret"}
	if err := validateRuntimeProviderKeys(registry, cfg); err != nil {
		t.Fatalf("unexpected key validation error: %v", err)
	}
	cfg.OpenAIAPIKey = ""
	if err := validateRuntimeProviderKeys(registry, cfg); err == nil {
		t.Fatal("expected missing active provider key")
	}
}
