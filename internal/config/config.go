package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxTargetsPerModel = 4

// Target 定义一个逻辑模型可以调用的上游目标。
type Target struct {
	ID                string   `json:"id"`
	Provider          string   `json:"provider"`
	UpstreamModel     string   `json:"upstream_model"`
	Capabilities      []string `json:"capabilities"`
	SupportsStreaming *bool    `json:"supports_streaming"`
	QualityTier       int      `json:"quality_tier"`
	CostTier          int      `json:"cost_tier"`
	ContextWindow     int64    `json:"context_window"`
	DataClasses       []string `json:"data_classes"`
	Pricing           *Pricing `json:"pricing,omitempty"`
}

// Model 定义一个对外逻辑模型及其有序上游目标。
type Model struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name,omitempty"`
	Targets     []Target `json:"targets"`
}

// Routing 定义 Fallback 单次尝试和熔断策略。
type Routing struct {
	AttemptTimeout   time.Duration
	FailureThreshold int
	Cooldown         time.Duration
}

// Config 保存 Limen 启动后使用的不可变配置。
type Config struct {
	Addr             string
	LimenAPIKey      string
	OpenAIAPIKey     string
	OpenAIBaseURL    string
	AnthropicAPIKey  string
	AnthropicBaseURL string
	Models           []Model
	Routing          Routing
	RequestTimeout   time.Duration
	ConfigVersion    string
}

type modelsDocument struct {
	Routing routingDocument `json:"routing"`
	Models  []Model         `json:"models"`
}

type routingDocument struct {
	AttemptTimeout   string `json:"attempt_timeout"`
	FailureThreshold *int   `json:"failure_threshold"`
	Cooldown         string `json:"cooldown"`
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
		Routing: Routing{
			AttemptTimeout:   15 * time.Second,
			FailureThreshold: 3,
			Cooldown:         30 * time.Second,
		},
		RequestTimeout: 60 * time.Second,
		ConfigVersion:  "compatibility-v1",
	}
	if cfg.LimenAPIKey == "" {
		return Config{}, errors.New("LIMEN_API_KEY is required")
	}
	if err := validateBaseURL("OPENAI_BASE_URL", cfg.OpenAIBaseURL); err != nil {
		return Config{}, err
	}
	if err := validateBaseURL("ANTHROPIC_BASE_URL", cfg.AnthropicBaseURL); err != nil {
		return Config{}, err
	}
	if raw := os.Getenv("LIMEN_REQUEST_TIMEOUT"); raw != "" {
		timeout, err := parsePositiveDuration("LIMEN_REQUEST_TIMEOUT", raw)
		if err != nil {
			return Config{}, err
		}
		cfg.RequestTimeout = timeout
	}
	modelsFile := os.Getenv("LIMEN_MODELS_FILE")
	if modelsFile != "" {
		models, routing, version, err := loadModels(modelsFile, cfg.Routing)
		if err != nil {
			return Config{}, err
		}
		cfg.Models = models
		cfg.Routing = routing
		cfg.ConfigVersion = version
	}
	if err := validateProviderKeys(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadModels 读取并校验模型注册表与路由策略。
func loadModels(path string, defaults Routing) ([]Model, Routing, string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, Routing{}, "", fmt.Errorf("read models file: %w", err)
	}
	document, err := decodeModelsDocument(contents)
	if err != nil {
		return nil, Routing{}, "", err
	}
	routing, err := parseRouting(document.Routing, defaults)
	if err != nil {
		return nil, Routing{}, "", err
	}
	for modelIndex := range document.Models {
		for targetIndex := range document.Models[modelIndex].Targets {
			applyTargetDefaults(&document.Models[modelIndex].Targets[targetIndex])
		}
	}
	if err := validateModels(document.Models); err != nil {
		return nil, Routing{}, "", err
	}
	version, err := hashModelsDocument(document)
	if err != nil {
		return nil, Routing{}, "", err
	}
	return document.Models, routing, version, nil
}

// hashModelsDocument 为规范化后的模型配置生成不可变版本标识。
func hashModelsDocument(document modelsDocument) (string, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode models version: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// decodeModelsDocument 严格解析模型文件并拒绝未知字段和多个 JSON 值。
func decodeModelsDocument(contents []byte) (modelsDocument, error) {
	var document modelsDocument
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return modelsDocument{}, fmt.Errorf("decode models file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return modelsDocument{}, errors.New("decode models file: multiple JSON values")
	}
	return document, nil
}

// parseRouting 合并模型文件中的路由参数和默认值。
func parseRouting(document routingDocument, defaults Routing) (Routing, error) {
	routing := defaults
	if document.AttemptTimeout != "" {
		attemptTimeout, err := parsePositiveDuration("routing.attempt_timeout", document.AttemptTimeout)
		if err != nil {
			return Routing{}, err
		}
		routing.AttemptTimeout = attemptTimeout
	}
	if document.FailureThreshold != nil {
		routing.FailureThreshold = *document.FailureThreshold
		if routing.FailureThreshold <= 0 {
			return Routing{}, errors.New("routing.failure_threshold must be positive")
		}
	}
	if document.Cooldown != "" {
		cooldown, err := parsePositiveDuration("routing.cooldown", document.Cooldown)
		if err != nil {
			return Routing{}, err
		}
		routing.Cooldown = cooldown
	}
	return routing, nil
}

// validateModels 校验逻辑模型、目标数量和唯一性。
func validateModels(models []Model) error {
	if len(models) == 0 {
		return errors.New("models file must contain at least one model")
	}
	seen := make(map[string]struct{}, len(models))
	for index, model := range models {
		if strings.TrimSpace(model.ID) == "" || len(model.Targets) == 0 {
			return fmt.Errorf("model at index %d requires id and targets", index)
		}
		if len(model.Targets) > maxTargetsPerModel {
			return fmt.Errorf("model %q supports at most %d targets", model.ID, maxTargetsPerModel)
		}
		targets := make(map[string]struct{}, len(model.Targets))
		upstreamTargets := make(map[string]struct{}, len(model.Targets))
		for targetIndex, target := range model.Targets {
			if strings.TrimSpace(target.Provider) == "" || strings.TrimSpace(target.UpstreamModel) == "" {
				return fmt.Errorf("target at index %d for model %q requires provider and upstream_model", targetIndex, model.ID)
			}
			if strings.TrimSpace(target.ID) == "" {
				return fmt.Errorf("target at index %d for model %q requires id", targetIndex, model.ID)
			}
			if target.Provider != "openai" && target.Provider != "anthropic" {
				return fmt.Errorf("model %q uses unsupported provider %q", model.ID, target.Provider)
			}
			if target.QualityTier < 0 || target.QualityTier > 5 {
				return fmt.Errorf("target %q for model %q has invalid quality_tier", target.ID, model.ID)
			}
			if target.CostTier < 0 {
				return fmt.Errorf("target %q for model %q has invalid cost_tier", target.ID, model.ID)
			}
			if target.ContextWindow < 0 {
				return fmt.Errorf("target %q for model %q has invalid context_window", target.ID, model.ID)
			}
			if _, exists := targets[target.ID]; exists {
				return fmt.Errorf("model %q contains duplicate target id %q", model.ID, target.ID)
			}
			targets[target.ID] = struct{}{}
			providerKey := target.Provider + "\x00" + target.UpstreamModel
			if _, exists := upstreamTargets[providerKey]; exists {
				return fmt.Errorf("model %q contains duplicate target %q", model.ID, target.UpstreamModel)
			}
			upstreamTargets[providerKey] = struct{}{}
			if err := validateTargetCapabilities(target); err != nil {
				return fmt.Errorf("target %q for model %q: %w", target.ID, model.ID, err)
			}
		}
		if _, exists := seen[model.ID]; exists {
			return fmt.Errorf("duplicate model id %q", model.ID)
		}
		seen[model.ID] = struct{}{}
	}
	return nil
}

// validateTargetCapabilities 校验目标能力和允许处理的数据等级。
func validateTargetCapabilities(target Target) error {
	for _, capability := range target.Capabilities {
		if capability != "text" {
			return fmt.Errorf("unsupported capability %q", capability)
		}
	}
	for _, dataClass := range target.DataClasses {
		if !validDataClass(dataClass) {
			return fmt.Errorf("unsupported data class %q", dataClass)
		}
	}
	return nil
}

// validDataClass 判断数据等级是否属于当前版本的受控集合。
func validDataClass(dataClass string) bool {
	switch dataClass {
	case "public", "internal", "confidential", "restricted":
		return true
	default:
		return false
	}
}

// applyTargetDefaults 补全旧模型配置缺失的基础文本能力字段。
func applyTargetDefaults(target *Target) {
	if strings.TrimSpace(target.ID) == "" {
		target.ID = target.Provider + ":" + target.UpstreamModel
	}
	if len(target.Capabilities) == 0 {
		target.Capabilities = []string{"text"}
	}
	if target.SupportsStreaming == nil {
		streaming := true
		target.SupportsStreaming = &streaming
	}
	if target.QualityTier == 0 {
		target.QualityTier = 1
	}
	if target.CostTier == 0 {
		target.CostTier = 1
	}
	if target.ContextWindow == 0 {
		target.ContextWindow = math.MaxInt64
	}
	if len(target.DataClasses) == 0 {
		target.DataClasses = []string{"public", "internal", "confidential", "restricted"}
	}
}

// parsePositiveDuration 将配置中的持续时间解析为正数。
func parsePositiveDuration(name, raw string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return duration, nil
}

// validateBaseURL 校验 Provider 地址能够安全地作为 HTTP API 根地址使用。
func validateBaseURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", name)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not contain credentials", name)
	}
	return nil
}

// validateProviderKeys 校验兼容模式或模型注册表实际使用的 Provider 密钥。
func validateProviderKeys(cfg Config) error {
	usesOpenAI := len(cfg.Models) == 0
	usesAnthropic := len(cfg.Models) == 0
	for _, model := range cfg.Models {
		for _, target := range model.Targets {
			usesOpenAI = usesOpenAI || target.Provider == "openai"
			usesAnthropic = usesAnthropic || target.Provider == "anthropic"
		}
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
