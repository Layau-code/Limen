// Package catalog 保存 Limen 启动后使用的只读模型目录。
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/huz/limen/internal/cost"
)

const maxTargetsPerModel = 4

// Target 描述一个 Provider 及其真实上游模型和声明能力。
type Target struct {
	ID                string        `json:"id"`
	Provider          string        `json:"provider"`
	UpstreamModel     string        `json:"upstream_model"`
	EndpointID        string        `json:"endpoint_id,omitempty"`
	Capabilities      []string      `json:"capabilities"`
	SupportsStreaming bool          `json:"supports_streaming"`
	QualityTier       int           `json:"quality_tier"`
	CostTier          int           `json:"cost_tier"`
	ContextWindow     int64         `json:"context_window"`
	DataClasses       []string      `json:"data_classes"`
	Pricing           *cost.Pricing `json:"pricing,omitempty"`
}

// OpaqueTargetID 将内部目标标识转换为稳定的观测引用，避免泄露配置命名或上游模型。
func OpaqueTargetID(targetID string) string {
	sum := sha256.Sum256([]byte(targetID))
	return "target-" + hex.EncodeToString(sum[:6])
}

// Model 描述客户端可见模型及其有序上游目标。
type Model struct {
	ID            string   `json:"id"`
	DisplayName   string   `json:"display_name,omitempty"`
	Targets       []Target `json:"targets"`
	Compatibility bool     `json:"compatibility,omitempty"`
}

// Registry 保存启动时构建的只读模型映射。
type Registry struct {
	models        []Model
	byID          map[string]Model
	compatibility bool
}

// NewRegistry 根据显式模型配置创建只读注册表。
func NewRegistry(models []Model) (*Registry, error) {
	if len(models) == 0 {
		return nil, errors.New("model registry requires at least one model")
	}
	registry := &Registry{byID: make(map[string]Model, len(models))}
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" || len(model.Targets) == 0 {
			return nil, errors.New("model registry model requires id and targets")
		}
		if len(model.Targets) > maxTargetsPerModel {
			return nil, fmt.Errorf("model %q supports at most %d targets", model.ID, maxTargetsPerModel)
		}
		targets := make(map[string]struct{}, len(model.Targets))
		targetIDs := make(map[string]struct{}, len(model.Targets))
		for index := range model.Targets {
			target := &model.Targets[index]
			if target.Provider != "openai" && target.Provider != "anthropic" {
				return nil, fmt.Errorf("model %q uses unsupported provider %q", model.ID, target.Provider)
			}
			if strings.TrimSpace(target.UpstreamModel) == "" {
				return nil, fmt.Errorf("model %q target requires upstream model", model.ID)
			}
			if strings.TrimSpace(target.ID) == "" {
				target.ID = target.Provider + ":" + target.UpstreamModel
			}
			if _, exists := targetIDs[target.ID]; exists {
				return nil, fmt.Errorf("model %q contains duplicate target id %q", model.ID, target.ID)
			}
			targetIDs[target.ID] = struct{}{}
			key := target.Provider + "\x00" + target.EndpointID + "\x00" + target.UpstreamModel
			if _, exists := targets[key]; exists {
				return nil, fmt.Errorf("model %q contains duplicate target", model.ID)
			}
			targets[key] = struct{}{}
			applyLegacyDefaults(target)
		}
		if _, exists := registry.byID[model.ID]; exists {
			return nil, errors.New("model registry contains duplicate id")
		}
		model = cloneModel(model)
		registry.byID[model.ID] = model
		registry.models = append(registry.models, model)
	}
	registry.sortModels()
	return registry, nil
}

// NewCompatibilityRegistry 创建保持原有模型前缀行为的注册表。
func NewCompatibilityRegistry() *Registry {
	registry := &Registry{
		compatibility: true,
		models: []Model{
			{ID: "claude-*", Targets: []Target{{ID: "anthropic:compat", Provider: "anthropic", UpstreamModel: "*", Capabilities: []string{"text"}, SupportsStreaming: true, QualityTier: 1, CostTier: 1, ContextWindow: math.MaxInt64, DataClasses: allDataClasses()}}, Compatibility: true},
			{ID: "gpt-*", Targets: []Target{{ID: "openai:compat", Provider: "openai", UpstreamModel: "*", Capabilities: []string{"text"}, SupportsStreaming: true, QualityTier: 1, CostTier: 1, ContextWindow: math.MaxInt64, DataClasses: allDataClasses()}}, Compatibility: true},
			{ID: "o1-*", Targets: []Target{{ID: "openai:o1-compat", Provider: "openai", UpstreamModel: "*", Capabilities: []string{"text"}, SupportsStreaming: true, QualityTier: 1, CostTier: 1, ContextWindow: math.MaxInt64, DataClasses: allDataClasses()}}, Compatibility: true},
			{ID: "o3-*", Targets: []Target{{ID: "openai:o3-compat", Provider: "openai", UpstreamModel: "*", Capabilities: []string{"text"}, SupportsStreaming: true, QualityTier: 1, CostTier: 1, ContextWindow: math.MaxInt64, DataClasses: allDataClasses()}}, Compatibility: true},
		},
	}
	registry.sortModels()
	return registry
}

// Resolve 查找客户端模型并返回对应的 Provider 和上游模型。
func (registry *Registry) Resolve(modelID string) (Model, bool) {
	if !registry.compatibility {
		model, found := registry.byID[modelID]
		if !found {
			return Model{}, false
		}
		return cloneModel(model), true
	}
	for _, model := range registry.models {
		prefix := strings.TrimSuffix(model.ID, "*")
		if strings.HasPrefix(modelID, prefix) {
			model.Targets = []Target{model.Targets[0]}
			model.Targets[0].UpstreamModel = modelID
			return model, true
		}
	}
	return Model{}, false
}

// List 返回按 ID 排序的模型列表副本。
func (registry *Registry) List() []Model {
	models := make([]Model, len(registry.models))
	for index, model := range registry.models {
		models[index] = cloneModel(model)
	}
	return models
}

// IsCompatibility 表示注册表是否处于按模型前缀透传的兼容模式。
func (registry *Registry) IsCompatibility() bool {
	return registry.compatibility
}

// cloneModel 深复制模型，避免调用方修改只读注册表。
func cloneModel(model Model) Model {
	model.Targets = append([]Target(nil), model.Targets...)
	for index := range model.Targets {
		model.Targets[index].Capabilities = append([]string(nil), model.Targets[index].Capabilities...)
		model.Targets[index].DataClasses = append([]string(nil), model.Targets[index].DataClasses...)
		if model.Targets[index].Pricing != nil {
			pricing := *model.Targets[index].Pricing
			model.Targets[index].Pricing = &pricing
		}
	}
	return model
}

// sortModels 固定模型列表顺序，保证 API 输出稳定。
func (registry *Registry) sortModels() {
	sort.Slice(registry.models, func(i, j int) bool {
		return registry.models[i].ID < registry.models[j].ID
	})
}

// applyLegacyDefaults 让旧的 Go 构造方式继续使用基础文本能力。
func applyLegacyDefaults(target *Target) {
	if len(target.Capabilities) == 0 {
		target.Capabilities = []string{"text"}
		target.SupportsStreaming = true
		target.ContextWindow = math.MaxInt64
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
		target.DataClasses = allDataClasses()
	}
}

// allDataClasses 返回当前版本允许的数据等级集合。
func allDataClasses() []string {
	return []string{"public", "internal", "confidential", "restricted"}
}
