package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoadParsesAPIScopes(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("LIMEN_MODELS_FILE", "")
	t.Setenv("LIMEN_API_SCOPES", "inference, runs:read")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scopes) != 2 || cfg.Scopes[0] != "inference" || cfg.Scopes[1] != "runs:read" {
		t.Fatalf("scopes = %v", cfg.Scopes)
	}
}

func TestLoadRejectsUnknownAPIScope(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("LIMEN_MODELS_FILE", "")
	t.Setenv("LIMEN_API_SCOPES", "inference,unknown")
	if _, err := Load(); err == nil {
		t.Fatal("expected unsupported scope error")
	}
}

func TestLoadDatabaseConfiguration(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("LIMEN_MODELS_FILE", "")
	t.Setenv("LIMEN_DATABASE_URL", "postgresql://limen:secret@db.example/limen?sslmode=require")
	t.Setenv("LIMEN_TENANT_ID", "tenant-1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL == "" || cfg.TenantID != "tenant-1" {
		t.Fatalf("database config = %+v", cfg)
	}
}

func TestLoadPostgresAPIKeyMode(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "")
	t.Setenv("LIMEN_API_KEY_STORE", "postgres")
	t.Setenv("LIMEN_API_KEY_HMAC_SECRET", "hmac-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("LIMEN_MODELS_FILE", "")
	t.Setenv("LIMEN_DATABASE_URL", "postgresql://limen:secret@db.example/limen?sslmode=require")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKeyStore != "postgres" || cfg.APIKeyHMACSecret != "hmac-secret" || cfg.LimenAPIKey != "" {
		t.Fatalf("key config = %+v", cfg)
	}
}

func TestLoadRejectsPostgresAPIKeyModeWithoutDatabase(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "")
	t.Setenv("LIMEN_API_KEY_STORE", "postgres")
	t.Setenv("LIMEN_API_KEY_HMAC_SECRET", "hmac-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("LIMEN_MODELS_FILE", "")
	t.Setenv("LIMEN_DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected database requirement")
	}
}

func TestLoadRejectsInvalidDatabaseURL(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("LIMEN_MODELS_FILE", "")
	t.Setenv("LIMEN_DATABASE_URL", "https://not-postgres.example")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid database URL error")
	}
}

func TestLoadUsesRoutingDefaults(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("LIMEN_MODELS_FILE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.AttemptTimeout != 15*time.Second || cfg.Routing.FailureThreshold != 3 || cfg.Routing.Cooldown != 30*time.Second {
		t.Fatalf("routing = %+v", cfg.Routing)
	}
}

func TestLoadModelsFileRequiresOnlyUsedProviderKey(t *testing.T) {
	modelsFile := filepath.Join(t.TempDir(), "models.json")
	contents := `{"routing":{"attempt_timeout":"8s","failure_threshold":2,"cooldown":"20s"},"models":[{"id":"fast-model","display_name":"Fast Model","targets":[{"provider":"openai","upstream_model":"gpt-test"}]}]}`
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
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "fast-model" || len(cfg.Models[0].Targets) != 1 {
		t.Fatalf("models = %+v", cfg.Models)
	}
	if !strings.HasPrefix(cfg.ConfigVersion, "sha256:") {
		t.Fatalf("config version = %q", cfg.ConfigVersion)
	}
	if cfg.Models[0].Targets[0].UpstreamModel != "gpt-test" {
		t.Fatalf("target = %+v", cfg.Models[0].Targets[0])
	}
	if cfg.Routing.AttemptTimeout != 8*time.Second || cfg.Routing.FailureThreshold != 2 || cfg.Routing.Cooldown != 20*time.Second {
		t.Fatalf("routing = %+v", cfg.Routing)
	}
}

func TestLoadParsesTargetPricing(t *testing.T) {
	modelsFile := filepath.Join(t.TempDir(), "models.json")
	contents := `{"models":[{"id":"fast-model","targets":[{"provider":"openai","upstream_model":"gpt-test","pricing":{"input_per_million_usd":"0.250000","output_per_million_usd":"2"}}]}]}`
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
	pricing := cfg.Models[0].Targets[0].Pricing
	if pricing == nil || pricing.InputPerMillionNanoUSD != 250_000_000 || pricing.OutputPerMillionNanoUSD != 2_000_000_000 {
		t.Fatalf("pricing = %+v", pricing)
	}
}

func TestLoadParsesTargetCapabilities(t *testing.T) {
	modelsFile := filepath.Join(t.TempDir(), "models.json")
	contents := `{"models":[{"id":"smart-model","targets":[{"id":"primary","provider":"openai","upstream_model":"gpt-test","capabilities":["text"],"supports_streaming":false,"quality_tier":4,"cost_tier":2,"context_window":8192,"data_classes":["public","internal"]}]}]}`
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
	target := cfg.Models[0].Targets[0]
	if target.ID != "primary" || target.SupportsStreaming == nil || *target.SupportsStreaming || target.QualityTier != 4 || target.CostTier != 2 || target.ContextWindow != 8192 {
		t.Fatalf("target = %+v", target)
	}
}

func TestLoadRejectsUnknownCapabilityAndDataClass(t *testing.T) {
	tests := []string{
		`{"models":[{"id":"m","targets":[{"provider":"openai","upstream_model":"gpt","capabilities":["magic"]}]}]}`,
		`{"models":[{"id":"m","targets":[{"provider":"openai","upstream_model":"gpt","data_classes":["secret"]}]}]}`,
	}
	for _, contents := range tests {
		t.Run(contents, func(t *testing.T) {
			modelsFile := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(modelsFile, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LIMEN_API_KEY", "limen-secret")
			t.Setenv("OPENAI_API_KEY", "openai-secret")
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("LIMEN_MODELS_FILE", modelsFile)
			if _, err := Load(); err == nil {
				t.Fatal("expected capability validation error")
			}
		})
	}
}

func TestLoadModelsFileRequiresReferencedProviderKey(t *testing.T) {
	modelsFile := filepath.Join(t.TempDir(), "models.json")
	contents := `{"models":[{"id":"smart-model","targets":[{"provider":"anthropic","upstream_model":"claude-test"}]}]}`
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

func TestLoadAnthropicOnlyModelsWithoutOpenAIKey(t *testing.T) {
	modelsFile := filepath.Join(t.TempDir(), "models.json")
	contents := `{"models":[{"id":"smart-model","targets":[{"provider":"anthropic","upstream_model":"claude-test"}]}]}`
	if err := os.WriteFile(modelsFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("LIMEN_MODELS_FILE", modelsFile)

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsInvalidModelsFile(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{"invalid json", `{`},
		{"empty models", `{"models":[]}`},
		{"unknown field", `{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real","extra":true}]}]}`},
		{"legacy single target", `{"models":[{"id":"model","provider":"openai","upstream_model":"real"}]}`},
		{"unknown provider", `{"models":[{"id":"model","targets":[{"provider":"other","upstream_model":"real"}]}]}`},
		{"duplicate id", `{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"one"}]},{"id":"model","targets":[{"provider":"openai","upstream_model":"two"}]}]}`},
		{"missing target field", `{"models":[{"id":"model","targets":[{"provider":"openai"}]}]}`},
		{"too many targets", `{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"one"},{"provider":"openai","upstream_model":"two"},{"provider":"openai","upstream_model":"three"},{"provider":"openai","upstream_model":"four"},{"provider":"openai","upstream_model":"five"}]}]}`},
		{"duplicate target", `{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"same"},{"provider":"openai","upstream_model":"same"}]}]}`},
		{"missing input price", `{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real","pricing":{"output_per_million_usd":"1"}}]}]}`},
		{"missing output price", `{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real","pricing":{"input_per_million_usd":"1"}}]}]}`},
		{"negative price", `{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real","pricing":{"input_per_million_usd":"-1","output_per_million_usd":"1"}}]}]}`},
		{"exponent price", `{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real","pricing":{"input_per_million_usd":"1e-3","output_per_million_usd":"1"}}]}]}`},
		{"too precise price", `{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real","pricing":{"input_per_million_usd":"0.0000000001","output_per_million_usd":"1"}}]}]}`},
		{"invalid attempt timeout", `{"routing":{"attempt_timeout":"invalid"},"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real"}]}]}`},
		{"zero attempt timeout", `{"routing":{"attempt_timeout":"0s"},"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real"}]}]}`},
		{"zero failure threshold", `{"routing":{"failure_threshold":0},"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real"}]}]}`},
		{"negative cooldown", `{"routing":{"cooldown":"-1s"},"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"real"}]}]}`},
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

func TestLoadRejectsInvalidProviderBaseURL(t *testing.T) {
	tests := []struct {
		name string
		env  string
		url  string
	}{
		{"relative URL", "OPENAI_BASE_URL", "/v1"},
		{"unsupported scheme", "OPENAI_BASE_URL", "ftp://provider.example/v1"},
		{"embedded credentials", "OPENAI_BASE_URL", "https://user:pass@provider.example/v1"},
		{"query in URL", "OPENAI_BASE_URL", "https://provider.example/v1?token=unsafe"},
		{"invalid Anthropic URL", "ANTHROPIC_BASE_URL", "/v1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LIMEN_API_KEY", "limen-secret")
			t.Setenv("OPENAI_API_KEY", "openai-secret")
			t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
			t.Setenv(test.env, test.url)
			t.Setenv("LIMEN_MODELS_FILE", "")
			if _, err := Load(); err == nil {
				t.Fatal("expected invalid Provider base URL error")
			}
		})
	}
}
