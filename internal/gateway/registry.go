package gateway

import "github.com/huz/limen/internal/catalog"

// Target 保留旧包名，兼容现有 Gateway 调用方。
type Target = catalog.Target

// Model 保留旧包名，兼容现有 Gateway 调用方。
type Model = catalog.Model

// ModelRegistry 保留旧包名，兼容现有 Gateway 调用方。
type ModelRegistry = catalog.Registry

// NewModelRegistry 创建显式模型注册表。
func NewModelRegistry(models []Model) (*ModelRegistry, error) {
	return catalog.NewRegistry(models)
}

// NewCompatibilityRegistry 创建兼容模式注册表。
func NewCompatibilityRegistry() *ModelRegistry {
	return catalog.NewCompatibilityRegistry()
}
