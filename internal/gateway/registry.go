package gateway

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/huz/limen/internal/cost"
)

const maxTargetsPerModel = 4

// Target 描述一个 Provider 及其真实上游模型。
type Target struct {
	Provider      string
	UpstreamModel string
	Pricing       *cost.Pricing
}

// Model 描述客户端可见模型及其有序上游目标。
type Model struct {
	ID            string
	DisplayName   string
	Targets       []Target
	Compatibility bool
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
		if strings.TrimSpace(model.ID) == "" || len(model.Targets) == 0 {
			return nil, errors.New("model registry model requires id and targets")
		}
		if len(model.Targets) > maxTargetsPerModel {
			return nil, fmt.Errorf("model %q supports at most %d targets", model.ID, maxTargetsPerModel)
		}
		targets := make(map[string]struct{}, len(model.Targets))
		for _, target := range model.Targets {
			if target.Provider != "openai" && target.Provider != "anthropic" {
				return nil, fmt.Errorf("model %q uses unsupported provider %q", model.ID, target.Provider)
			}
			if strings.TrimSpace(target.UpstreamModel) == "" {
				return nil, fmt.Errorf("model %q target requires upstream model", model.ID)
			}
			key := target.Provider + "\x00" + target.UpstreamModel
			if _, exists := targets[key]; exists {
				return nil, fmt.Errorf("model %q contains duplicate target", model.ID)
			}
			targets[key] = struct{}{}
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
func NewCompatibilityRegistry() *ModelRegistry {
	registry := &ModelRegistry{
		compatibility: true,
		models: []Model{
			{ID: "claude-*", Targets: []Target{{Provider: "anthropic", UpstreamModel: "*"}}, Compatibility: true},
			{ID: "gpt-*", Targets: []Target{{Provider: "openai", UpstreamModel: "*"}}, Compatibility: true},
			{ID: "o1-*", Targets: []Target{{Provider: "openai", UpstreamModel: "*"}}, Compatibility: true},
			{ID: "o3-*", Targets: []Target{{Provider: "openai", UpstreamModel: "*"}}, Compatibility: true},
		},
	}
	registry.sortModels()
	return registry
}

// Resolve 查找客户端模型并返回对应的 Provider 和上游模型。
func (r *ModelRegistry) Resolve(modelID string) (Model, bool) {
	if !r.compatibility {
		model, found := r.byID[modelID]
		return cloneModel(model), found
	}
	for _, model := range r.models {
		prefix := strings.TrimSuffix(model.ID, "*")
		if strings.HasPrefix(modelID, prefix) {
			model.Targets = []Target{{Provider: model.Targets[0].Provider, UpstreamModel: modelID}}
			return model, true
		}
	}
	return Model{}, false
}

// List 返回按 ID 排序的模型列表副本。
func (r *ModelRegistry) List() []Model {
	models := make([]Model, len(r.models))
	for index, model := range r.models {
		models[index] = cloneModel(model)
	}
	return models
}

// cloneModel 深复制模型，避免调用方修改只读注册表。
func cloneModel(model Model) Model {
	model.Targets = append([]Target(nil), model.Targets...)
	for index := range model.Targets {
		if model.Targets[index].Pricing != nil {
			pricing := *model.Targets[index].Pricing
			model.Targets[index].Pricing = &pricing
		}
	}
	return model
}

// sortModels 固定模型列表顺序，保证 API 输出稳定。
func (r *ModelRegistry) sortModels() {
	sort.Slice(r.models, func(i, j int) bool {
		return r.models[i].ID < r.models[j].ID
	})
}
