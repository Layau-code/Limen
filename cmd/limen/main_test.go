package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/configstore"
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

func TestCredentialResolverKeepsTenantCredentialsSeparate(t *testing.T) {
	vault, err := credentialstore.NewVault([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	credentials := credentialstore.NewMemoryStore(vault)
	if _, err := credentials.Rotate(context.Background(), "tenant-a", "openai", "endpoint", "key-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Rotate(context.Background(), "tenant-b", "openai", "endpoint", "key-b"); err != nil {
		t.Fatal(err)
	}
	resolve := newCredentialResolver(credentials, "openai", "endpoint")
	for tenant, want := range map[string]string{"tenant-a": "key-a", "tenant-b": "key-b"} {
		got, err := resolve(context.Background(), tenant)
		if err != nil || got != want {
			t.Fatalf("tenant=%q credential=%q err=%v", tenant, got, err)
		}
	}
	if _, err := resolve(context.Background(), "tenant-c"); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("missing tenant error=%v", err)
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

func TestRefreshPublishedConfigReplacesRouterSnapshot(t *testing.T) {
	initial, err := gateway.NewModelRegistry([]gateway.Model{{ID: "old", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := gateway.NewRouter(nil, initial, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second})
	configs := configstore.NewMemoryStore()
	record, err := configs.Create(context.Background(), "tenant-a", []byte(`{"models":[{"id":"new","targets":[{"id":"new-target","provider":"anthropic","upstream_model":"claude-new"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configs.Publish(context.Background(), "tenant-a", record.Version); err != nil {
		t.Fatal(err)
	}
	if err := refreshPublishedConfig(context.Background(), "tenant-a", configs, router); err != nil {
		t.Fatal(err)
	}
	if router.ConfigVersion() != record.Version || router.Models()[0].ID != "new" {
		t.Fatalf("router version=%q models=%+v", router.ConfigVersion(), router.Models())
	}
}
