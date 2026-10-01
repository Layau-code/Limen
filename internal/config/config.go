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

	"github.com/huz/limen/internal/auth"
)

const maxTargetsPerModel = 4

// Target 定义一个逻辑模型可以调用的上游目标。
type Target struct {
	ID                string   `json:"id"`
	Provider          string   `json:"provider"`
	UpstreamModel     string   `json:"upstream_model"`
	EndpointID        string   `json:"endpoint_id,omitempty"`
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
	AttemptTimeout          time.Duration
	FailureThreshold        int
	Cooldown                time.Duration
	EconomyThresholdPercent int
	MinimumAttemptWindow    time.Duration
	Semantic                SemanticRouting
}

// SemanticRouting 定义版本化的 Jev 语义信号策略。默认关闭，且默认不授权正文出站。
type SemanticRouting struct {
	Mode                    string         `json:"mode"`
	ExternalEnabled         bool           `json:"external_enabled,omitempty"`
	Provider                string         `json:"provider,omitempty"`
	ModelVersion            string         `json:"model_version,omitempty"`
	StateBuilderVersion     string         `json:"state_builder_version,omitempty"`
	QuestionTemplateVersion string         `json:"question_template_version,omitempty"`
	MappingVersion          string         `json:"mapping_version,omitempty"`
	AllowedDataClasses      []string       `json:"allowed_data_classes,omitempty"`
	Timeout                 time.Duration  `json:"timeout,omitempty"`
	SamplePercent           int            `json:"sample_percent,omitempty"`
	AssessmentPricing       *Pricing       `json:"assessment_pricing,omitempty"`
	Rules                   []SemanticRule `json:"rules,omitempty"`
}

// SemanticRule 将可信的封闭语义类别映射到更高质量下限或首选目标集合。
type SemanticRule struct {
	TaskType                    string   `json:"task_type"`
	Complexity                  string   `json:"complexity"`
	Language                    string   `json:"language,omitempty"`
	MinimumTaskConfidence       float64  `json:"minimum_task_confidence"`
	MinimumComplexityConfidence float64  `json:"minimum_complexity_confidence"`
	MinimumProbabilityMargin    float64  `json:"minimum_probability_margin"`
	MinimumQualityTier          int      `json:"minimum_quality_tier,omitempty"`
	PreferredTargetIDs          []string `json:"preferred_target_ids,omitempty"`
	ThresholdProfile            string   `json:"threshold_profile"`
	EvaluationReport            string   `json:"evaluation_report,omitempty"`
}

// DefaultRouting 返回未指定模型文件参数时使用的路由默认值。
func DefaultRouting() Routing {
	return Routing{
		AttemptTimeout:          15 * time.Second,
		FailureThreshold:        3,
		Cooldown:                30 * time.Second,
		EconomyThresholdPercent: 20,
		MinimumAttemptWindow:    250 * time.Millisecond,
		Semantic:                SemanticRouting{Mode: "off", Provider: "typesafe", ModelVersion: "jev-1.13.0", StateBuilderVersion: "recent-user.v1", QuestionTemplateVersion: "task-complexity.zh.v1", MappingVersion: "task-target.v1", Timeout: 200 * time.Millisecond, SamplePercent: 5},
	}
}

// Config 保存 Limen 启动后使用的不可变配置。
type Config struct {
	Addr                   string
	LimenAPIKey            string
	APIKeyStore            string
	APIKeyHMACSecret       string
	CredentialMasterKey    string
	OpenAIAPIKey           string
	OpenAIBaseURL          string
	AnthropicAPIKey        string
	AnthropicBaseURL       string
	TypeSafeAPIKey         string
	Models                 []Model
	Routing                Routing
	RequestTimeout         time.Duration
	ConfigVersion          string
	ConfigApprovalRequired bool
	DatabaseURL            string
	TenantID               string
	Scopes                 []auth.Scope
}

type modelsDocument struct {
	Routing routingDocument `json:"routing"`
	Models  []Model         `json:"models"`
}

type routingDocument struct {
	AttemptTimeout          string                  `json:"attempt_timeout"`
	FailureThreshold        *int                    `json:"failure_threshold"`
	Cooldown                string                  `json:"cooldown"`
	EconomyThresholdPercent *int                    `json:"economy_threshold_percent"`
	MinimumAttemptWindow    string                  `json:"minimum_attempt_window"`
	Semantic                semanticRoutingDocument `json:"semantic"`
}

type semanticRoutingDocument struct {
	Mode                    string         `json:"mode"`
	ExternalEnabled         bool           `json:"external_enabled"`
	Provider                string         `json:"provider"`
	ModelVersion            string         `json:"model_version"`
	StateBuilderVersion     string         `json:"state_builder_version"`
	QuestionTemplateVersion string         `json:"question_template_version"`
	MappingVersion          string         `json:"mapping_version"`
	AllowedDataClasses      []string       `json:"allowed_data_classes"`
	Timeout                 string         `json:"timeout"`
	SamplePercent           *int           `json:"sample_percent"`
	AssessmentPricing       *Pricing       `json:"assessment_pricing"`
	Rules                   []SemanticRule `json:"rules"`
}

// Load 从环境变量读取配置，并校验启动所需的密钥。
func Load() (Config, error) {
	limenAPIKey, err := loadSecret("LIMEN_API_KEY", "LIMEN_API_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	openAIAPIKey, err := loadSecret("OPENAI_API_KEY", "OPENAI_API_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	typeSafeAPIKey, err := loadSecret("TYPESAFE_API_KEY", "TYPESAFE_API_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	anthropicAPIKey, err := loadSecret("ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Addr:                valueOrDefault("LIMEN_ADDR", ":8080"),
		LimenAPIKey:         limenAPIKey,
		APIKeyStore:         valueOrDefault("LIMEN_API_KEY_STORE", "static"),
		APIKeyHMACSecret:    os.Getenv("LIMEN_API_KEY_HMAC_SECRET"),
		CredentialMasterKey: os.Getenv("LIMEN_CREDENTIAL_MASTER_KEY"),
		OpenAIAPIKey:        openAIAPIKey,
		OpenAIBaseURL:       valueOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		AnthropicAPIKey:     anthropicAPIKey,
		AnthropicBaseURL:    valueOrDefault("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
		TypeSafeAPIKey:      typeSafeAPIKey,
		Routing:             DefaultRouting(),
		RequestTimeout:      60 * time.Second,
		ConfigVersion:       "compatibility-v1",
		DatabaseURL:         os.Getenv("LIMEN_DATABASE_URL"),
		TenantID:            valueOrDefault("LIMEN_TENANT_ID", "local"),
		Scopes:              auth.AllScopes(),
	}
	if cfg.APIKeyStore != "static" && cfg.APIKeyStore != "postgres" {
		return Config{}, errors.New("LIMEN_API_KEY_STORE must be static or postgres")
	}
	if cfg.APIKeyStore == "postgres" && strings.TrimSpace(os.Getenv("LIMEN_API_KEY_FILE")) != "" {
		return Config{}, errors.New("LIMEN_API_KEY_FILE is only supported in static key mode")
	}
	if cfg.APIKeyStore == "static" && cfg.LimenAPIKey == "" {
		return Config{}, errors.New("LIMEN_API_KEY is required in static key mode")
	}
	if cfg.APIKeyStore == "postgres" && strings.TrimSpace(cfg.APIKeyHMACSecret) == "" {
		return Config{}, errors.New("LIMEN_API_KEY_HMAC_SECRET is required in postgres key mode")
	}
	if cfg.DatabaseURL != "" {
		if err := validateDatabaseURL(cfg.DatabaseURL); err != nil {
			return Config{}, err
		}
	}
	if cfg.APIKeyStore == "postgres" && cfg.DatabaseURL == "" {
		return Config{}, errors.New("LIMEN_DATABASE_URL is required in postgres key mode")
	}
	if strings.TrimSpace(cfg.TenantID) == "" {
		return Config{}, errors.New("LIMEN_TENANT_ID must not be empty")
	}
	if raw := os.Getenv("LIMEN_API_SCOPES"); raw != "" {
		scopes, err := parseScopes(raw)
		if err != nil {
			return Config{}, err
		}
		cfg.Scopes = scopes
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
	if raw := strings.TrimSpace(os.Getenv("LIMEN_CONFIG_APPROVAL_REQUIRED")); raw != "" {
		approvalRequired, err := parseConfigApprovalFlag(raw)
		if err != nil {
			return Config{}, err
		}
		cfg.ConfigApprovalRequired = approvalRequired
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

// loadSecret 从环境变量或只读文件读取密钥，并拒绝同时配置两个来源。
func loadSecret(valueName, fileName string) (string, error) {
	value := os.Getenv(valueName)
	path := strings.TrimSpace(os.Getenv(fileName))
	if value != "" && path != "" {
		return "", fmt.Errorf("%s and %s must not both be set", valueName, fileName)
	}
	if path == "" {
		return value, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("unable to read %s: %w", fileName, err)
	}
	secret := strings.TrimSpace(string(contents))
	if secret == "" {
		return "", fmt.Errorf("%s must contain a non-empty secret", fileName)
	}
	return secret, nil
}

// parseConfigApprovalFlag 只接受明确的 true 或 false，避免启动配置产生歧义。
func parseConfigApprovalFlag(raw string) (bool, error) {
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("LIMEN_CONFIG_APPROVAL_REQUIRED must be true or false")
	}
}

// parseScopes 解析并校验静态 API Key 的 Scope 列表。
func parseScopes(raw string) ([]auth.Scope, error) {
	allowed := make(map[auth.Scope]struct{})
	for _, scope := range auth.AllScopes() {
		allowed[scope] = struct{}{}
	}
	seen := make(map[auth.Scope]struct{})
	parts := strings.Split(raw, ",")
	if len(parts) == 0 {
		return nil, errors.New("LIMEN_API_SCOPES must not be empty")
	}
	result := make([]auth.Scope, 0, len(parts))
	for _, part := range parts {
		scope := auth.Scope(strings.TrimSpace(part))
		if scope == "" {
			return nil, errors.New("LIMEN_API_SCOPES contains an empty scope")
		}
		if _, ok := allowed[scope]; !ok {
			return nil, fmt.Errorf("LIMEN_API_SCOPES contains unsupported scope %q", scope)
		}
		if _, ok := seen[scope]; ok {
			return nil, fmt.Errorf("LIMEN_API_SCOPES contains duplicate scope %q", scope)
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	return result, nil
}

// loadModels 读取并校验模型注册表与路由策略。
func loadModels(path string, defaults Routing) ([]Model, Routing, string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, Routing{}, "", fmt.Errorf("read models file: %w", err)
	}
	return ParseModels(contents, defaults)
}

// ParseModels 严格解析模型配置并返回规范版本、路由参数和模型目录。
func ParseModels(contents []byte, defaults Routing) ([]Model, Routing, string, error) {
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
	if err := validateSemanticTargets(routing.Semantic, document.Models); err != nil {
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
	if document.EconomyThresholdPercent != nil {
		if *document.EconomyThresholdPercent < 0 || *document.EconomyThresholdPercent > 100 {
			return Routing{}, errors.New("routing.economy_threshold_percent must be between 0 and 100")
		}
		routing.EconomyThresholdPercent = *document.EconomyThresholdPercent
	}
	if document.MinimumAttemptWindow != "" {
		minimumWindow, err := parsePositiveDuration("routing.minimum_attempt_window", document.MinimumAttemptWindow)
		if err != nil {
			return Routing{}, err
		}
		routing.MinimumAttemptWindow = minimumWindow
	}
	semantic, err := parseSemanticRouting(document.Semantic)
	if err != nil {
		return Routing{}, err
	}
	routing.Semantic = semantic
	return routing, nil
}

// parseSemanticRouting 解析并校验语义路由配置。
func parseSemanticRouting(document semanticRoutingDocument) (SemanticRouting, error) {
	semantic := SemanticRouting{Mode: "off", Provider: "typesafe", ModelVersion: "jev-1.13.0", StateBuilderVersion: "recent-user.v1", QuestionTemplateVersion: "task-complexity.zh.v1", MappingVersion: "task-target.v1", Timeout: 200 * time.Millisecond, SamplePercent: 5}
	if document.Mode != "" {
		semantic.Mode = document.Mode
	}
	if semantic.Mode != "off" && semantic.Mode != "shadow" && semantic.Mode != "active" {
		return SemanticRouting{}, errors.New("routing.semantic.mode must be off, shadow, or active")
	}
	if document.ExternalEnabled {
		semantic.ExternalEnabled = true
	}
	if document.Provider != "" {
		semantic.Provider = document.Provider
	}
	if document.ModelVersion != "" {
		semantic.ModelVersion = document.ModelVersion
	}
	if document.StateBuilderVersion != "" {
		semantic.StateBuilderVersion = document.StateBuilderVersion
	}
	if document.QuestionTemplateVersion != "" {
		semantic.QuestionTemplateVersion = document.QuestionTemplateVersion
	}
	if document.MappingVersion != "" {
		semantic.MappingVersion = document.MappingVersion
	}
	if document.AllowedDataClasses != nil {
		semantic.AllowedDataClasses = append([]string(nil), document.AllowedDataClasses...)
	}
	if document.Timeout != "" {
		value, err := parsePositiveDuration("routing.semantic.timeout", document.Timeout)
		if err != nil || value > 5*time.Second {
			return SemanticRouting{}, errors.New("routing.semantic.timeout must be between 1ns and 5s")
		}
		semantic.Timeout = value
	}
	if document.SamplePercent != nil {
		if *document.SamplePercent < 0 || *document.SamplePercent > 100 {
			return SemanticRouting{}, errors.New("routing.semantic.sample_percent must be between 0 and 100")
		}
		semantic.SamplePercent = *document.SamplePercent
	}
	if document.AssessmentPricing != nil {
		pricing := *document.AssessmentPricing
		semantic.AssessmentPricing = &pricing
	}
	semantic.Rules = append([]SemanticRule(nil), document.Rules...)
	for i := range semantic.Rules {
		semantic.Rules[i].PreferredTargetIDs = append([]string(nil), semantic.Rules[i].PreferredTargetIDs...)
	}
	if err := validateSemanticRouting(semantic); err != nil {
		return SemanticRouting{}, err
	}
	return semantic, nil
}

// validateSemanticRouting 检查语义配置的固定版本、白名单和启用条件。
func validateSemanticRouting(semantic SemanticRouting) error {
	if semantic.Provider != "typesafe" {
		return errors.New("routing.semantic.provider must be typesafe")
	}
	if !validJevVersion(semantic.ModelVersion) {
		return errors.New("routing.semantic.model_version must be a fixed Jev version")
	}
	if semantic.StateBuilderVersion != "recent-user.v1" {
		return errors.New("unsupported routing.semantic.state_builder_version")
	}
	if semantic.QuestionTemplateVersion != "task-complexity.zh.v1" && semantic.QuestionTemplateVersion != "task-complexity.en.v1" && semantic.QuestionTemplateVersion != "task-complexity.auto.v1" {
		return errors.New("unsupported routing.semantic.question_template_version")
	}
	if semantic.MappingVersion != "task-target.v1" {
		return errors.New("unsupported routing.semantic.mapping_version")
	}
	seenClass := map[string]bool{}
	for _, class := range semantic.AllowedDataClasses {
		if class != "public" || seenClass[class] {
			return errors.New("routing.semantic.allowed_data_classes may contain only unique public")
		}
		seenClass[class] = true
	}
	seen := map[string]bool{}
	for _, rule := range semantic.Rules {
		if !validSemanticTask(rule.TaskType) || !validSemanticComplexity(rule.Complexity) || (rule.Language != "" && rule.Language != "zh" && rule.Language != "en") {
			return errors.New("routing.semantic.rules contains an unsupported task, complexity, or language")
		}
		key := rule.TaskType + "\x00" + rule.Complexity + "\x00" + rule.Language
		if seen[key] {
			return errors.New("routing.semantic.rules contains a duplicate rule")
		}
		seen[key] = true
		if rule.MinimumTaskConfidence < 0.5 || rule.MinimumTaskConfidence > 1 || rule.MinimumComplexityConfidence < 0.5 || rule.MinimumComplexityConfidence > 1 || rule.MinimumProbabilityMargin <= 0 || rule.MinimumProbabilityMargin > 1 {
			return errors.New("routing.semantic rule thresholds must be between 0 and 1")
		}
		if rule.MinimumQualityTier < 0 || rule.MinimumQualityTier > 5 || rule.ThresholdProfile == "" {
			return errors.New("routing.semantic rule requires a threshold profile and valid quality tier")
		}
		if rule.MinimumQualityTier == 0 && len(rule.PreferredTargetIDs) == 0 {
			return errors.New("routing.semantic rule must define a quality tier or preferred targets")
		}
		if semantic.Mode == "active" && strings.TrimSpace(rule.EvaluationReport) == "" {
			return errors.New("active semantic rules require evaluation_report")
		}
		targets := map[string]bool{}
		for _, id := range rule.PreferredTargetIDs {
			if strings.TrimSpace(id) == "" || targets[id] {
				return errors.New("routing.semantic rule has an invalid preferred target")
			}
			targets[id] = true
		}
	}
	if semantic.Mode == "active" && (!semantic.ExternalEnabled || len(semantic.Rules) == 0 || len(semantic.AllowedDataClasses) == 0) {
		return errors.New("active semantic routing requires external_enabled, allowed_data_classes, and rules")
	}
	return nil
}

// validateSemanticTargets 确认语义偏好目标在模型目录中唯一存在。
func validateSemanticTargets(semantic SemanticRouting, models []Model) error {
	known := map[string]int{}
	for _, model := range models {
		for _, target := range model.Targets {
			known[target.ID]++
		}
	}
	for _, rule := range semantic.Rules {
		for _, id := range rule.PreferredTargetIDs {
			if known[id] == 0 {
				return fmt.Errorf("routing.semantic preferred target %q is not in the model directory", id)
			}
			if known[id] > 1 {
				return fmt.Errorf("routing.semantic preferred target %q is ambiguous", id)
			}
		}
	}
	return nil
}

// validJevVersion 只接受由数字组成的固定 Jev 三段版本号。
func validJevVersion(value string) bool {
	version, ok := strings.CutPrefix(value, "jev-")
	if !ok {
		return false
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

// validSemanticTask 判断任务类型是否属于支持的固定枚举。
func validSemanticTask(value string) bool {
	return value == "extraction" || value == "transformation" || value == "writing" || value == "code" || value == "analysis" || value == "other"
}

// validSemanticComplexity 判断复杂度是否属于支持的固定枚举。
func validSemanticComplexity(value string) bool {
	return value == "simple" || value == "standard" || value == "complex"
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
			if err := validateEndpointID(target.EndpointID); err != nil {
				return fmt.Errorf("target %q for model %q: %w", target.ID, model.ID, err)
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
			providerKey := target.Provider + "\x00" + target.EndpointID + "\x00" + target.UpstreamModel
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

// validateEndpointID 校验可选的 Provider endpoint 绑定标识，避免配置携带地址或任意字符串。
func validateEndpointID(endpointID string) error {
	if endpointID == "" {
		return nil
	}
	if !strings.HasPrefix(endpointID, "endpoint:") || len(endpointID) != len("endpoint:")+24 {
		return errors.New("endpoint_id must be endpoint: followed by 24 lowercase hex characters")
	}
	for _, char := range endpointID[len("endpoint:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return errors.New("endpoint_id must be endpoint: followed by 24 lowercase hex characters")
		}
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

// ValidateProviderBaseURL 校验 Provider 地址使用 HTTPS 且不携带凭据或动态查询参数。
func ValidateProviderBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "https" {
		return errors.New("provider base URL must be an absolute HTTPS URL")
	}
	if parsed.User != nil {
		return errors.New("provider base URL must not contain credentials")
	}
	return nil
}

// validateBaseURL 为环境变量校验保留字段名，便于启动错误直接定位配置项。
func validateBaseURL(name, raw string) error {
	if err := ValidateProviderBaseURL(raw); err != nil {
		if strings.Contains(err.Error(), "credentials") {
			return fmt.Errorf("%s must not contain credentials", name)
		}
		return fmt.Errorf("%s must be an absolute HTTPS URL", name)
	}
	return nil
}

// validateDatabaseURL 校验 PostgreSQL DSN 的协议和主机，不记录其中的凭据。
func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return errors.New("LIMEN_DATABASE_URL must be a PostgreSQL URL")
	}
	return nil
}

// validateProviderKeys 校验兼容模式或模型注册表实际使用的 Provider 密钥。
func validateProviderKeys(cfg Config) error {
	// 配置可能来自数据库中尚未加载的已发布版本，启动装配完成后再按实际目录校验。
	if len(cfg.Models) == 0 && cfg.DatabaseURL != "" {
		return nil
	}
	storedCredentialsAvailable := cfg.DatabaseURL != "" && strings.TrimSpace(cfg.CredentialMasterKey) != ""
	usesOpenAI := len(cfg.Models) == 0
	usesAnthropic := len(cfg.Models) == 0
	for _, model := range cfg.Models {
		for _, target := range model.Targets {
			usesOpenAI = usesOpenAI || target.Provider == "openai"
			usesAnthropic = usesAnthropic || target.Provider == "anthropic"
		}
	}
	if usesOpenAI && cfg.OpenAIAPIKey == "" && !storedCredentialsAvailable {
		return errors.New("OPENAI_API_KEY is required by the model registry")
	}
	if usesAnthropic && cfg.AnthropicAPIKey == "" && !storedCredentialsAvailable {
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
