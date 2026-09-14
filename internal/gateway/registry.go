package gateway

import (
	"errors"
	"sort"
	"strings"
)

// Model 描述客户端可见模型及其上游映射。
type Model struct {
	ID            string
	Provider      string
	UpstreamModel string
	DisplayName   string
}

// ModelRegistry 保存启动时构建的只读模型映射。
type ModelRegistry struct {
	models        []Model
	byID          map[string]Model
	compatibility bool
}

// NewModelRegistry 根据显式模型配置创建只读注册表。
func NewModelRegistry(models []Model) (*ModelRegistry, error) {
	if len(models) == 0 {
		return nil, errors.New("model registry requires at least one model")
	}
	registry := &ModelRegistry{byID: make(map[string]Model, len(models))}
	for _, model := range models {
		if _, exists := registry.byID[model.ID]; exists {
			return nil, errors.New("model registry contains duplicate id")
		}
		registry.byID[model.ID] = model
		registry.models = append(registry.models, model)
	}
	registry.sortModels()
	return registry, nil
}

// NewCompatibilityRegistry 创建保持原有模型前缀行为的注册表。
func NewCompatibilityRegistry() *ModelRegistry {
	registry := &ModelRegistry{
		compatibility: true,
		models: []Model{
			{ID: "claude-*", Provider: "anthropic", UpstreamModel: "*"},
			{ID: "gpt-*", Provider: "openai", UpstreamModel: "*"},
			{ID: "o1-*", Provider: "openai", UpstreamModel: "*"},
			{ID: "o3-*", Provider: "openai", UpstreamModel: "*"},
		},
	}
	registry.sortModels()
	return registry
}

// Resolve 查找客户端模型并返回对应的 Provider 和上游模型。
func (r *ModelRegistry) Resolve(modelID string) (Model, bool) {
	if !r.compatibility {
		model, found := r.byID[modelID]
		return model, found
	}
	for _, model := range r.models {
		prefix := strings.TrimSuffix(model.ID, "*")
		if strings.HasPrefix(modelID, prefix) {
			model.ID = modelID
			model.UpstreamModel = modelID
			return model, true
		}
	}
	return Model{}, false
}

// List 返回按 ID 排序的模型列表副本。
func (r *ModelRegistry) List() []Model {
	models := make([]Model, len(r.models))
	copy(models, r.models)
	return models
}

// sortModels 固定模型列表顺序，保证 API 输出稳定。
func (r *ModelRegistry) sortModels() {
	sort.Slice(r.models, func(i, j int) bool {
		return r.models[i].ID < r.models[j].ID
	})
}
