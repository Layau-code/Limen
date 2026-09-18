package configstore

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/huz/limen/internal/catalog"
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

// PublicDiff 返回隐藏内部目标标识后的安全结构差异，供控制面和离线工具复用。
func PublicDiff(before, after Record) []Change {
	changes := Diff(before, after)
	for index := range changes {
		changes[index].Path = publicPath(changes[index].Path)
	}
	return changes
}

// publicPath 将目标标识替换为稳定 opaque 引用，避免暴露上游命名。
func publicPath(path string) string {
	const marker = ".targets["
	start := strings.Index(path, marker)
	if start < 0 {
		return path
	}
	valueStart := start + len(marker)
	close := strings.LastIndex(path[valueStart:], "]")
	if close < 0 {
		return path
	}
	close += valueStart
	return path[:valueStart] + catalog.OpaqueTargetID(path[valueStart:close]) + path[close:]
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

// modelMap 将模型切片转换为按逻辑 ID 索引的映射。
func modelMap(models []config.Model) map[string]config.Model {
	result := make(map[string]config.Model, len(models))
	for _, model := range models {
		result[model.ID] = model
	}
	return result
}

// targetMap 将目标切片转换为按目标 ID 索引的映射。
func targetMap(targets []config.Target) map[string]config.Target {
	result := make(map[string]config.Target, len(targets))
	for _, target := range targets {
		result[target.ID] = target
	}
	return result
}

// modelPath 生成安全的逻辑模型差异路径。
func modelPath(id string) string {
	return fmt.Sprintf("models[%s]", id)
}

// targetPath 生成安全的模型目标差异路径。
func targetPath(modelID, targetID string) string {
	return modelPath(modelID) + fmt.Sprintf(".targets[%s]", targetID)
}
