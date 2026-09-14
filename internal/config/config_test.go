package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("LIMEN_MODELS_FILE", "")
	t.Setenv("LIMEN_ADDR", ":9090")
	t.Setenv("OPENAI_BASE_URL", "http://provider.example/v1")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_BASE_URL", "http://anthropic.example")
	t.Setenv("LIMEN_REQUEST_TIMEOUT", "45s")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9090" || cfg.RequestTimeout != 45*time.Second || cfg.AnthropicAPIKey != "anthropic-secret" || cfg.AnthropicBaseURL != "http://anthropic.example" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadModelsFileRequiresOnlyUsedProviderKey(t *testing.T) {
	modelsFile := filepath.Join(t.TempDir(), "models.json")
	contents := `{"models":[{"id":"fast-model","provider":"openai","upstream_model":"gpt-test","display_name":"Fast Model"}]}`
	if err := os.WriteFile(modelsFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LIMEN_MODELS_FILE", modelsFile)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "fast-model" {
		t.Fatalf("models = %+v", cfg.Models)
	}
}

func TestLoadModelsFileRequiresReferencedProviderKey(t *testing.T) {
	modelsFile := filepath.Join(t.TempDir(), "models.json")
	contents := `{"models":[{"id":"smart-model","provider":"anthropic","upstream_model":"claude-test"}]}`
	if err := os.WriteFile(modelsFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LIMEN_MODELS_FILE", modelsFile)

	if _, err := Load(); err == nil {
		t.Fatal("expected missing Anthropic key error")
	}
}

func TestLoadRejectsInvalidModelsFile(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{"invalid json", `{`},
		{"empty models", `{"models":[]}`},
		{"unknown provider", `{"models":[{"id":"model","provider":"other","upstream_model":"real"}]}`},
		{"duplicate id", `{"models":[{"id":"model","provider":"openai","upstream_model":"one"},{"id":"model","provider":"openai","upstream_model":"two"}]}`},
		{"missing field", `{"models":[{"id":"model","provider":"openai"}]}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modelsFile := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(modelsFile, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LIMEN_API_KEY", "limen-secret")
			t.Setenv("OPENAI_API_KEY", "openai-secret")
			t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
			t.Setenv("LIMEN_MODELS_FILE", modelsFile)
			if _, err := Load(); err == nil {
				t.Fatal("expected models file error")
			}
		})
	}
}

func TestLoadRequiresSecrets(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LIMEN_MODELS_FILE", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing secret error")
	}
}
