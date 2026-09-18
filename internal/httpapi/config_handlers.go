package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huz/limen/internal/approval"
	"github.com/huz/limen/internal/audit"
	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/run"
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
		"changes":      publicConfigChanges(configstore.Diff(before, after)),
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
	h.appendAudit(r.Context(), h.requestTenantID(r), audit.ActionConfigCreate, "config", record.Version, "success", record.Version)
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
	key, ok := idempotencyKey(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is required", "invalid_request_error", "idempotency_key_required")
		return
	}
	tenantID := h.requestTenantID(r)
	hash, err := configPublishRequestHash(tenantID, r.PathValue("version"), key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid idempotency request", "invalid_request_error", "invalid_idempotency_request")
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
	mutator, supported := h.configs.(configstore.MutationStore)
	if !supported {
		writeError(w, http.StatusServiceUnavailable, "config idempotency is unavailable", "api_error", "config_control_unavailable")
		return
	}
	mutation := configstore.Mutation{Key: key, Hash: hash}
	if h.configApprovalRequired {
		approvalID := strings.TrimSpace(r.Header.Get("X-Limen-Approval-ID"))
		if approvalID == "" {
			writeError(w, http.StatusBadRequest, "X-Limen-Approval-ID is required", "invalid_request_error", "approval_required")
			return
		}
		if h.approvals == nil {
			writeError(w, http.StatusServiceUnavailable, "approval control is unavailable", "api_error", "approval_control_unavailable")
			return
		}
		binding := approvalBinding(tenantID, record.Version, approvalID, key, hash, requestActorID(r.Context()))
		if _, err := h.approvals.ValidateForPublish(r.Context(), binding); err != nil {
			writeApprovalError(w, err)
			return
		}
		if approvalMutator, atomic := mutator.(configstore.ApprovalMutationStore); atomic {
			record, err = approvalMutator.PublishWithApproval(r.Context(), tenantID, record.Version, mutation, configstore.ApprovalBinding{ApprovalID: approvalID, PublishIdempotencyKey: key, RequestHash: hash, Publisher: requestActorID(r.Context())})
		} else {
			// 内存存储没有跨对象事务，先消费审批；同一发布键重试仍可复用 consumed 状态。
			if _, err = h.approvals.Consume(r.Context(), binding); err == nil {
				record, err = mutator.PublishWithMutation(r.Context(), tenantID, record.Version, mutation)
			}
		}
	} else {
		record, err = mutator.PublishWithMutation(r.Context(), tenantID, record.Version, mutation)
	}
	if err != nil {
		if h.configApprovalRequired {
			if isApprovalStoreError(err) {
				writeApprovalError(w, err)
				return
			}
		}
		writeConfigStoreError(w, err)
		return
	}
	policy := h.router.Policy()
	policy.AttemptTimeout = record.Routing.AttemptTimeout
	policy.FailureThreshold = record.Routing.FailureThreshold
	policy.Cooldown = record.Routing.Cooldown
	policy.EconomyThresholdPercent = record.Routing.EconomyThresholdPercent
	policy.MinimumAttemptWindow = record.Routing.MinimumAttemptWindow
	if err := h.router.ReplaceRegistryWithPolicy(registry, record.Version, policy); err != nil {
		writeError(w, http.StatusConflict, "config could not be activated", "api_error", "config_activation_failed")
		return
	}
	h.appendAudit(r.Context(), tenantID, audit.ActionConfigPublish, "config", record.Version, "success", hash)
	if h.configApprovalRequired {
		h.appendAudit(r.Context(), tenantID, audit.ActionApprovalConsumed, "config_approval", strings.TrimSpace(r.Header.Get("X-Limen-Approval-ID")), "success", hash)
	}
	writeJSON(w, http.StatusOK, summarizeConfig(record))
}

// configPublishRequestHash 生成审批和发布共同使用的配置发布请求哈希。
func configPublishRequestHash(tenantID, version, key string) (string, error) {
	return run.HashRequest(tenantID, "POST /v1/limen/configs/"+version+"/publish", key, nil, map[string]string{"x-limen-config-version": version})
}

// approvalBinding 将 HTTP 发布请求转换为审批状态机绑定。
func approvalBinding(tenantID, version, approvalID, publishKey, requestHash, publisher string) approval.Binding {
	return approval.Binding{TenantID: tenantID, ConfigVersion: version, ApprovalID: approvalID, PublishIdempotencyKey: publishKey, RequestHash: requestHash, Publisher: publisher}
}

// isApprovalStoreError 判断持久化配置发布返回的审批领域错误。
func isApprovalStoreError(err error) bool {
	return errors.Is(err, configstore.ErrApprovalNotFound) || errors.Is(err, configstore.ErrApprovalExpired) || errors.Is(err, configstore.ErrApprovalStateConflict) || errors.Is(err, configstore.ErrApprovalBindingConflict) || errors.Is(err, configstore.ErrApprovalActorNotDistinct)
}

// summarizeConfig 将内部配置转换为不包含真实上游模型名的摘要。
func summarizeConfig(record configstore.Record) configSummary {
	result := configSummary{Version: record.Version, State: record.State, CreatedAt: record.CreatedAt, PublishedAt: record.PublishedAt}
	result.Models = make([]configModelSummary, 0, len(record.Models))
	for _, model := range record.Models {
		summary := configModelSummary{ID: model.ID, DisplayName: model.DisplayName, Targets: make([]configTargetSummary, 0, len(model.Targets))}
		for _, target := range model.Targets {
			summary.Targets = append(summary.Targets, configTargetSummary{ID: catalog.OpaqueTargetID(target.ID), Provider: target.Provider})
		}
		result.Models = append(result.Models, summary)
	}
	return result
}

// publicConfigChanges 将配置 diff 中的内部目标标识转换为 opaque 引用。
func publicConfigChanges(changes []configstore.Change) []configstore.Change {
	result := make([]configstore.Change, len(changes))
	for index, change := range changes {
		change.Path = publicConfigPath(change.Path)
		result[index] = change
	}
	return result
}

// publicConfigPath 隐藏 diff 路径中可能由上游模型派生的目标 ID。
func publicConfigPath(path string) string {
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
			targets = append(targets, gateway.Target{ID: target.ID, Provider: target.Provider, UpstreamModel: target.UpstreamModel, EndpointID: target.EndpointID, Capabilities: append([]string(nil), target.Capabilities...), SupportsStreaming: streaming, QualityTier: target.QualityTier, CostTier: target.CostTier, ContextWindow: target.ContextWindow, DataClasses: append([]string(nil), target.DataClasses...), Pricing: target.Pricing})
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
	if errors.Is(err, configstore.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency key conflict", "invalid_request_error", "idempotency_conflict")
		return
	}
	if errors.Is(err, configstore.ErrApprovalNotFound) {
		writeError(w, http.StatusNotFound, "approval not found", "invalid_request_error", "approval_not_found")
		return
	}
	if errors.Is(err, configstore.ErrApprovalExpired) {
		writeError(w, http.StatusConflict, "approval has expired", "invalid_request_error", "approval_expired")
		return
	}
	if errors.Is(err, configstore.ErrApprovalStateConflict) {
		writeError(w, http.StatusConflict, "approval state does not allow this operation", "invalid_request_error", "approval_state_conflict")
		return
	}
	if errors.Is(err, configstore.ErrApprovalBindingConflict) {
		writeError(w, http.StatusConflict, "approval binding conflict", "invalid_request_error", "approval_binding_conflict")
		return
	}
	if errors.Is(err, configstore.ErrApprovalActorNotDistinct) {
		writeError(w, http.StatusConflict, "approval actor must differ from requester", "invalid_request_error", "approval_actor_not_distinct")
		return
	}
	writeError(w, http.StatusBadGateway, "config store unavailable", "api_error", "config_store_unavailable")
}
