package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/huz/limen/internal/approval"
	"github.com/huz/limen/internal/audit"
	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/run"
)

type createApprovalRequest struct {
	PublishIdempotencyKey string `json:"publish_idempotency_key"`
}

// createApproval 为指定配置发布创建一个有时效的待审批记录。
func (h *Handler) createApproval(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeConfigsWrite) {
		return
	}
	if h.approvals == nil || h.configs == nil {
		writeError(w, http.StatusServiceUnavailable, "approval control is unavailable", "api_error", "approval_control_unavailable")
		return
	}
	key, ok := idempotencyKey(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is required", "invalid_request_error", "idempotency_key_required")
		return
	}
	body, err := readRequestBody(w, r)
	if err != nil {
		return
	}
	var incoming createApprovalRequest
	if err := decodeStrictJSON(body, &incoming); err != nil || strings.TrimSpace(incoming.PublishIdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, "publish_idempotency_key is required", "invalid_request_error", "invalid_approval_request")
		return
	}
	tenantID := h.requestTenantID(r)
	version := r.PathValue("version")
	if _, err := h.configs.Get(r.Context(), tenantID, version); err != nil {
		writeConfigStoreError(w, err)
		return
	}
	publishHash, err := configPublishRequestHash(tenantID, version, incoming.PublishIdempotencyKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid approval binding", "invalid_request_error", "invalid_approval_request")
		return
	}
	operationHash, err := run.HashRequest(tenantID, r.URL.Path, key, body, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid idempotency request", "invalid_request_error", "invalid_idempotency_request")
		return
	}
	record, err := h.approvals.Create(r.Context(), tenantID, version, incoming.PublishIdempotencyKey, publishHash, requestActorID(r.Context()), approval.Mutation{Key: key, Hash: operationHash})
	if err != nil {
		writeApprovalError(w, err)
		return
	}
	h.appendAudit(r.Context(), tenantID, audit.ActionApprovalRequested, "config_approval", record.ID, "success", operationHash)
	writeJSON(w, http.StatusCreated, record)
}

// getApproval 返回当前租户指定配置发布审批的安全状态摘要。
func (h *Handler) getApproval(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeConfigsRead) {
		return
	}
	if h.approvals == nil {
		writeError(w, http.StatusServiceUnavailable, "approval control is unavailable", "api_error", "approval_control_unavailable")
		return
	}
	record, err := h.approvals.Get(r.Context(), h.requestTenantID(r), r.PathValue("version"), r.PathValue("approval_id"))
	if err != nil {
		writeApprovalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

// approveConfig 执行与申请者身份分离的配置发布批准操作。
func (h *Handler) approveConfig(w http.ResponseWriter, r *http.Request) {
	h.mutateApproval(w, r, true)
}

// rejectConfig 执行与申请者身份分离的配置发布拒绝操作。
func (h *Handler) rejectConfig(w http.ResponseWriter, r *http.Request) {
	h.mutateApproval(w, r, false)
}

// mutateApproval 统一处理批准和拒绝的严格空对象请求与幂等语义。
func (h *Handler) mutateApproval(w http.ResponseWriter, r *http.Request, approve bool) {
	if !h.authenticateScopes(w, r, auth.ScopeConfigsWrite) {
		return
	}
	if h.approvals == nil {
		writeError(w, http.StatusServiceUnavailable, "approval control is unavailable", "api_error", "approval_control_unavailable")
		return
	}
	key, ok := idempotencyKey(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is required", "invalid_request_error", "idempotency_key_required")
		return
	}
	body, err := readRequestBody(w, r)
	if err != nil {
		return
	}
	var empty struct{}
	if err := decodeStrictJSON(body, &empty); err != nil {
		writeError(w, http.StatusBadRequest, "approval mutation body must be an empty JSON object", "invalid_request_error", "invalid_approval_request")
		return
	}
	tenantID := h.requestTenantID(r)
	hash, err := run.HashRequest(tenantID, r.URL.Path, key, body, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid idempotency request", "invalid_request_error", "invalid_idempotency_request")
		return
	}
	version, approvalID, actorID := r.PathValue("version"), r.PathValue("approval_id"), requestActorID(r.Context())
	mutation := approval.Mutation{Key: key, Hash: hash}
	var record approval.Record
	if approve {
		record, err = h.approvals.Approve(r.Context(), tenantID, version, approvalID, actorID, mutation)
	} else {
		record, err = h.approvals.Reject(r.Context(), tenantID, version, approvalID, actorID, mutation)
	}
	if err != nil {
		writeApprovalError(w, err)
		return
	}
	action := audit.ActionApprovalRejected
	if approve {
		action = audit.ActionApprovalApproved
	}
	h.appendAudit(r.Context(), tenantID, action, "config_approval", record.ID, "success", hash)
	writeJSON(w, http.StatusOK, record)
}

// writeApprovalError 将审批状态机错误映射为稳定的控制面响应。
func writeApprovalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, configstore.ErrApprovalNotFound):
		writeError(w, http.StatusNotFound, "approval not found", "invalid_request_error", "approval_not_found")
	case errors.Is(err, configstore.ErrApprovalExpired):
		writeError(w, http.StatusConflict, "approval has expired", "invalid_request_error", "approval_expired")
	case errors.Is(err, configstore.ErrApprovalStateConflict):
		writeError(w, http.StatusConflict, "approval state does not allow this operation", "invalid_request_error", "approval_state_conflict")
	case errors.Is(err, configstore.ErrApprovalBindingConflict):
		writeError(w, http.StatusConflict, "approval binding conflict", "invalid_request_error", "approval_binding_conflict")
	case errors.Is(err, configstore.ErrApprovalActorNotDistinct):
		writeError(w, http.StatusConflict, "approval actor must differ from requester", "invalid_request_error", "approval_actor_not_distinct")
	case errors.Is(err, approval.ErrNotFound):
		writeError(w, http.StatusNotFound, "approval not found", "invalid_request_error", "approval_not_found")
	case errors.Is(err, approval.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "approval idempotency key conflict", "invalid_request_error", "idempotency_conflict")
	case errors.Is(err, approval.ErrBindingConflict):
		writeError(w, http.StatusConflict, "approval binding conflict", "invalid_request_error", "approval_binding_conflict")
	case errors.Is(err, approval.ErrActorNotDistinct):
		writeError(w, http.StatusConflict, "approval actor must differ from requester", "invalid_request_error", "approval_actor_not_distinct")
	case errors.Is(err, approval.ErrExpired):
		writeError(w, http.StatusConflict, "approval has expired", "invalid_request_error", "approval_expired")
	case errors.Is(err, approval.ErrStateConflict):
		writeError(w, http.StatusConflict, "approval state does not allow this operation", "invalid_request_error", "approval_state_conflict")
	case errors.Is(err, approval.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid approval", "invalid_request_error", "invalid_approval_request")
	default:
		writeError(w, http.StatusBadGateway, "approval control unavailable", "api_error", "approval_control_error")
	}
}
