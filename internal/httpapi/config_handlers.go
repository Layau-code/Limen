package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/gateway"
)

// configSummary 是配置控制面返回的安全摘要，不暴露真实上游模型名。
type configSummary struct {
	Version     string               `json:"version"`
	State       string               `json:"state"`
	Models      []configModelSummary `json:"models"`
	CreatedAt   time.Time            `json:"created_at"`
	PublishedAt *time.Time           `json:"published_at,omitempty"`
}

type configModelSummary struct {
	ID          string                `json:"id"`
	DisplayName string                `json:"display_name,omitempty"`
	Targets     []configTargetSummary `json:"targets"`
}

type configTargetSummary struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
}

// listConfigs 返回当前租户的配置版本摘要。
func (h *Handler) listConfigs(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeConfigsRead) {
		return
	}
	if h.configs == nil {
		writeError(w, http.StatusServiceUnavailable, "config store unavailable", "api_error", "config_store_unavailable")
		return
	}
	records, err := h.configs.List(r.Context(), h.requestTenantID(r))
	if err != nil {
		writeError(w, http.StatusBadGateway, "config store unavailable", "api_error", "config_store_unavailable")
		return
	}
	data := make([]configSummary, 0, len(records))
	for _, record := range records {
		data = append(data, summarizeConfig(record))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// diffConfig 返回两个配置版本的稳定结构差异，不返回配置值或上游密钥。
func (h *Handler) diffConfig(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeConfigsRead) {
		return
	}
	if h.configs == nil {
		writeError(w, http.StatusServiceUnavailable, "config store unavailable", "api_error", "config_store_unavailable")
		return
	}
	tenantID := h.requestTenantID(r)
	before, err := h.configs.Get(r.Context(), tenantID, r.PathValue("base_version"))
	if err != nil {
		writeConfigStoreError(w, err)
		return
	}
	after, err := h.configs.Get(r.Context(), tenantID, r.PathValue("version"))
	if err != nil {
		writeConfigStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":       "config_diff",
		"base_version": before.Version,
		"version":      after.Version,
		"changes":      configstore.Diff(before, after),
	})
}

// createConfig 校验并保存一个不可变的配置草稿。
func (h *Handler) createConfig(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeConfigsWrite) {
		return
	}
	if h.configs == nil {
		writeError(w, http.StatusServiceUnavailable, "config store unavailable", "api_error", "config_store_unavailable")
		return
	}
	body, err := readRequestBody(w, r)
	if err != nil {
		return
	}
	record, err := h.configs.Create(r.Context(), h.requestTenantID(r), body)
	if err != nil {
		if errors.Is(err, configstore.ErrConflict) {
			writeError(w, http.StatusConflict, "config version conflict", "invalid_request_error", "config_version_conflict")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid config document", "invalid_request_error", "invalid_config")
		return
	}
	writeJSON(w, http.StatusCreated, summarizeConfig(record))
}

// publishConfig 发布配置版本并原子替换当前进程的 Router 目录。
func (h *Handler) publishConfig(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeConfigsWrite) {
		return
	}
	if h.configs == nil || h.router == nil {
		writeError(w, http.StatusServiceUnavailable, "config control is unavailable", "api_error", "config_control_unavailable")
		return
	}
	record, err := h.configs.Get(r.Context(), h.requestTenantID(r), r.PathValue("version"))
	if err != nil {
		writeConfigStoreError(w, err)
		return
	}
	registry, err := registryFromConfig(record.Models)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config document", "invalid_request_error", "invalid_config")
		return
	}
	record, err = h.configs.Publish(r.Context(), h.requestTenantID(r), record.Version)
	if err != nil {
		writeConfigStoreError(w, err)
		return
	}
	policy := h.router.Policy()
	policy.AttemptTimeout = record.Routing.AttemptTimeout
	policy.FailureThreshold = record.Routing.FailureThreshold
	policy.Cooldown = record.Routing.Cooldown
	if err := h.router.ReplaceRegistryWithPolicy(registry, record.Version, policy); err != nil {
		writeError(w, http.StatusConflict, "config could not be activated", "api_error", "config_activation_failed")
		return
	}
	writeJSON(w, http.StatusOK, summarizeConfig(record))
}

// summarizeConfig 将内部配置转换为不包含真实上游模型名的摘要。
func summarizeConfig(record configstore.Record) configSummary {
	result := configSummary{Version: record.Version, State: record.State, CreatedAt: record.CreatedAt, PublishedAt: record.PublishedAt}
	result.Models = make([]configModelSummary, 0, len(record.Models))
	for _, model := range record.Models {
		summary := configModelSummary{ID: model.ID, DisplayName: model.DisplayName, Targets: make([]configTargetSummary, 0, len(model.Targets))}
		for _, target := range model.Targets {
			summary.Targets = append(summary.Targets, configTargetSummary{ID: target.ID, Provider: target.Provider})
		}
		result.Models = append(result.Models, summary)
	}
	return result
}

// registryFromConfig 将已校验的配置模型转换为只读 Router 目录。
func registryFromConfig(models []config.Model) (*gateway.ModelRegistry, error) {
	converted := make([]gateway.Model, 0, len(models))
	for _, model := range models {
		targets := make([]gateway.Target, 0, len(model.Targets))
		for _, target := range model.Targets {
			streaming := true
			if target.SupportsStreaming != nil {
				streaming = *target.SupportsStreaming
			}
			targets = append(targets, gateway.Target{ID: target.ID, Provider: target.Provider, UpstreamModel: target.UpstreamModel, Capabilities: append([]string(nil), target.Capabilities...), SupportsStreaming: streaming, QualityTier: target.QualityTier, CostTier: target.CostTier, ContextWindow: target.ContextWindow, DataClasses: append([]string(nil), target.DataClasses...), Pricing: target.Pricing})
		}
		converted = append(converted, gateway.Model{ID: model.ID, DisplayName: model.DisplayName, Targets: targets})
	}
	return gateway.NewModelRegistry(converted)
}

// writeConfigStoreError 将配置存储错误映射为稳定 API 错误。
func writeConfigStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, configstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "config version not found", "invalid_request_error", "config_not_found")
		return
	}
	writeError(w, http.StatusBadGateway, "config store unavailable", "api_error", "config_store_unavailable")
}
