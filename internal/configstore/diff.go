package configstore

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/huz/limen/internal/config"
)

// Change 描述两个配置版本之间的结构变化，不携带密钥或配置值。
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// Diff 按稳定路径比较两个配置版本，供 Explain 和控制面审计使用。
func Diff(before, after Record) []Change {
	changes := make([]Change, 0)
	beforeModels := modelMap(before.Models)
	afterModels := modelMap(after.Models)
	for id := range beforeModels {
		if _, ok := afterModels[id]; !ok {
			changes = append(changes, Change{Path: modelPath(id), Kind: "removed"})
		}
	}
	for id, model := range afterModels {
		previous, ok := beforeModels[id]
		if !ok {
			changes = append(changes, Change{Path: modelPath(id), Kind: "added"})
			continue
		}
		if model.DisplayName != previous.DisplayName {
			changes = append(changes, Change{Path: modelPath(id) + ".display_name", Kind: "changed"})
		}
		changes = append(changes, diffTargets(id, previous.Targets, model.Targets)...)
	}
	if before.Routing != after.Routing {
		changes = append(changes, Change{Path: "routing", Kind: "changed"})
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

// diffTargets 比较同一逻辑模型下的目标集合和目标字段。
func diffTargets(modelID string, before, after []config.Target) []Change {
	changes := make([]Change, 0)
	beforeTargets := targetMap(before)
	afterTargets := targetMap(after)
	for id := range beforeTargets {
		if _, ok := afterTargets[id]; !ok {
			changes = append(changes, Change{Path: targetPath(modelID, id), Kind: "removed"})
		}
	}
	for id, target := range afterTargets {
		previous, ok := beforeTargets[id]
		if !ok {
			changes = append(changes, Change{Path: targetPath(modelID, id), Kind: "added"})
			continue
		}
		if target.Provider != previous.Provider {
			changes = append(changes, Change{Path: targetPath(modelID, id) + ".provider", Kind: "changed"})
		}
		if target.UpstreamModel != previous.UpstreamModel {
			changes = append(changes, Change{Path: targetPath(modelID, id) + ".upstream_model", Kind: "changed"})
		}
		if target.EndpointID != previous.EndpointID {
			changes = append(changes, Change{Path: targetPath(modelID, id) + ".endpoint_id", Kind: "changed"})
		}
		if !reflect.DeepEqual(target.Capabilities, previous.Capabilities) {
			changes = append(changes, Change{Path: targetPath(modelID, id) + ".capabilities", Kind: "changed"})
		}
		if targetPolicyChanged(previous, target) {
			changes = append(changes, Change{Path: targetPath(modelID, id) + ".policy", Kind: "changed"})
		}
	}
	return changes
}

// targetPolicyChanged 判断目标能力和费用策略是否发生变化。
func targetPolicyChanged(before, after config.Target) bool {
	return !reflect.DeepEqual(before.SupportsStreaming, after.SupportsStreaming) ||
		before.QualityTier != after.QualityTier ||
		before.CostTier != after.CostTier ||
		before.ContextWindow != after.ContextWindow ||
		!reflect.DeepEqual(before.DataClasses, after.DataClasses) ||
		!reflect.DeepEqual(before.Pricing, after.Pricing)
}

func modelMap(models []config.Model) map[string]config.Model {
	result := make(map[string]config.Model, len(models))
	for _, model := range models {
		result[model.ID] = model
	}
	return result
}

func targetMap(targets []config.Target) map[string]config.Target {
	result := make(map[string]config.Target, len(targets))
	for _, target := range targets {
		result[target.ID] = target
	}
	return result
}

func modelPath(id string) string {
	return fmt.Sprintf("models[%s]", id)
}

func targetPath(modelID, targetID string) string {
	return modelPath(modelID) + fmt.Sprintf(".targets[%s]", targetID)
}
