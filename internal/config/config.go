package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Model 定义一个对外模型及其上游映射。
type Model struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	UpstreamModel string `json:"upstream_model"`
	DisplayName   string `json:"display_name,omitempty"`
}

type Config struct {
	Addr             string
	LimenAPIKey      string
	OpenAIAPIKey     string
	OpenAIBaseURL    string
	AnthropicAPIKey  string
	AnthropicBaseURL string
	Models           []Model
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
	if cfg.LimenAPIKey == "" {
		return Config{}, errors.New("LIMEN_API_KEY is required")
	}
	modelsFile := os.Getenv("LIMEN_MODELS_FILE")
	if modelsFile != "" {
		models, err := loadModels(modelsFile)
		if err != nil {
			return Config{}, err
		}
		cfg.Models = models
	}
	if err := validateProviderKeys(cfg); err != nil {
		return Config{}, err
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

// loadModels 读取并校验模型注册表文件。
func loadModels(path string) ([]Model, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read models file: %w", err)
	}
	var document struct {
		Models []Model `json:"models"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		return nil, fmt.Errorf("decode models file: %w", err)
	}
	if len(document.Models) == 0 {
		return nil, errors.New("models file must contain at least one model")
	}
	seen := make(map[string]struct{}, len(document.Models))
	for index, model := range document.Models {
		if strings.TrimSpace(model.ID) == "" || strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.UpstreamModel) == "" {
			return nil, fmt.Errorf("model at index %d requires id, provider, and upstream_model", index)
		}
		if model.Provider != "openai" && model.Provider != "anthropic" {
			return nil, fmt.Errorf("model %q uses unsupported provider %q", model.ID, model.Provider)
		}
		if _, exists := seen[model.ID]; exists {
			return nil, fmt.Errorf("duplicate model id %q", model.ID)
		}
		seen[model.ID] = struct{}{}
	}
	return document.Models, nil
}

// validateProviderKeys 校验兼容模式或模型注册表实际使用的 Provider 密钥。
func validateProviderKeys(cfg Config) error {
	usesOpenAI := len(cfg.Models) == 0
	usesAnthropic := len(cfg.Models) == 0
	for _, model := range cfg.Models {
		usesOpenAI = usesOpenAI || model.Provider == "openai"
		usesAnthropic = usesAnthropic || model.Provider == "anthropic"
	}
	if usesOpenAI && cfg.OpenAIAPIKey == "" {
		return errors.New("OPENAI_API_KEY is required by the model registry")
	}
	if usesAnthropic && cfg.AnthropicAPIKey == "" {
		return errors.New("ANTHROPIC_API_KEY is required by the model registry")
	}
	return nil
}

// valueOrDefault 返回环境变量值；变量为空时使用默认值。
func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
