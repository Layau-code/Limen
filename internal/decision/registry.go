package decision

import (
	"errors"
	"strings"
	"sync"
)

// AlgorithmRegistry 保存可用于 Replay 的确定性决策算法版本。
type AlgorithmRegistry struct {
	mu      sync.RWMutex
	engines map[string]Engine
}

// NewAlgorithmRegistry 创建包含当前稳定算法版本的只读注册表。
func NewAlgorithmRegistry() *AlgorithmRegistry {
	return &AlgorithmRegistry{engines: map[string]Engine{AlgorithmVersionV1: NewEngine()}}
}

// Resolve 按版本获取决策算法；未知版本不会回退到新算法。
func (registry *AlgorithmRegistry) Resolve(version string) (Engine, bool) {
	if registry == nil {
		return Engine{}, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	engine, ok := registry.engines[version]
	return engine, ok
}

// Register 注册一个算法版本，重复版本或空版本会被拒绝。
func (registry *AlgorithmRegistry) Register(version string, engine Engine) error {
	version = strings.TrimSpace(version)
	if registry == nil || version == "" {
		return errors.New("algorithm version is required")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.engines == nil {
		registry.engines = make(map[string]Engine)
	}
	if _, exists := registry.engines[version]; exists {
		return errors.New("algorithm version already registered")
	}
	registry.engines[version] = engine
	return nil
}
