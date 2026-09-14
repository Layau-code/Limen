package config

import (
	"errors"
	"os"
	"time"
)

type Config struct {
	Addr             string
	LimenAPIKey      string
	OpenAIAPIKey     string
	OpenAIBaseURL    string
	AnthropicAPIKey  string
	AnthropicBaseURL string
	RequestTimeout   time.Duration
}

// Load 从环境变量读取配置，并校验启动所需的密钥。
func Load() (Config, error) {
	cfg := Config{
		Addr:             valueOrDefault("LIMEN_ADDR", ":8080"),
		LimenAPIKey:      os.Getenv("LIMEN_API_KEY"),
		OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		OpenAIBaseURL:    valueOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicBaseURL: valueOrDefault("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
		RequestTimeout:   60 * time.Second,
	}
	if cfg.LimenAPIKey == "" || cfg.OpenAIAPIKey == "" || cfg.AnthropicAPIKey == "" {
		return Config{}, errors.New("LIMEN_API_KEY, OPENAI_API_KEY, and ANTHROPIC_API_KEY are required")
	}
	if raw := os.Getenv("LIMEN_REQUEST_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return Config{}, errors.New("LIMEN_REQUEST_TIMEOUT must be a positive duration")
		}
		cfg.RequestTimeout = timeout
	}
	return cfg, nil
}

// valueOrDefault 返回环境变量值；变量为空时使用默认值。
func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
