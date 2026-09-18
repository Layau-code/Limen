package decision

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// AlgorithmRegistry 保存可用于 Replay 的确定性决策算法版本。
type AlgorithmRegistry struct {
	mu      sync.RWMutex
	engines map[string]algorithmEntry
}

type algorithmEntry struct {
	engine      Engine
	retainUntil time.Time
}

// NewAlgorithmRegistry 创建包含当前稳定算法版本的只读注册表。
func NewAlgorithmRegistry() *AlgorithmRegistry {
	engine := NewEngine()
	return &AlgorithmRegistry{engines: map[string]algorithmEntry{
		AlgorithmVersionV1: {engine: engine},
		AlgorithmVersionV2: {engine: engine},
	}}
}

// Resolve 按版本获取决策算法；未知版本不会回退到新算法。
func (registry *AlgorithmRegistry) Resolve(version string) (Engine, bool) {
	return registry.ResolveAt(version, time.Now().UTC())
}

// ResolveAt 按指定时间获取仍在保留窗口内的决策算法。
func (registry *AlgorithmRegistry) ResolveAt(version string, at time.Time) (Engine, bool) {
	if registry == nil {
		return Engine{}, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	entry, ok := registry.engines[version]
	if !ok || (!entry.retainUntil.IsZero() && !at.Before(entry.retainUntil)) {
		return Engine{}, false
	}
	return entry.engine, true
}

// Register 注册一个算法版本，重复版本或空版本会被拒绝。
func (registry *AlgorithmRegistry) Register(version string, engine Engine) error {
	return registry.RegisterWithRetention(version, engine, time.Time{})
}

// RegisterWithRetention 注册带明确 Replay 保留截止时间的算法版本。
func (registry *AlgorithmRegistry) RegisterWithRetention(version string, engine Engine, retainUntil time.Time) error {
	version = strings.TrimSpace(version)
	if registry == nil || version == "" {
		return errors.New("algorithm version is required")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.engines == nil {
		registry.engines = make(map[string]algorithmEntry)
	}
	if _, exists := registry.engines[version]; exists {
		return errors.New("algorithm version already registered")
	}
	registry.engines[version] = algorithmEntry{engine: engine, retainUntil: retainUntil.UTC()}
	return nil
}
