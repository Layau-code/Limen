package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "limen-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("LIMEN_ADDR", ":9090")
	t.Setenv("OPENAI_BASE_URL", "http://provider.example/v1")
	t.Setenv("LIMEN_REQUEST_TIMEOUT", "45s")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9090" || cfg.RequestTimeout != 45*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRequiresSecrets(t *testing.T) {
	t.Setenv("LIMEN_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing secret error")
	}
}
