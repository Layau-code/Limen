package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huz/limen/internal/audit"
	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/run"
)

type createAPIKeyRequest struct {
	Scopes    []auth.Scope `json:"scopes"`
	ExpiresAt *time.Time   `json:"expires_at,omitempty"`
}

type apiKeyResponse struct {
	PublicPrefix string       `json:"public_prefix"`
	Scopes       []auth.Scope `json:"scopes"`
	Active       bool         `json:"active"`
	ExpiresAt    *time.Time   `json:"expires_at,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	Key          string       `json:"key,omitempty"`
}

// createAPIKey 创建租户 API Key，并只在首次成功响应中返回明文。
func (h *Handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeAdmin) {
		return
	}
	if h.apiKeys == nil {
		writeError(w, http.StatusServiceUnavailable, "api key control is unavailable", "api_error", "api_key_control_unavailable")
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
	var incoming createAPIKeyRequest
	if err := decodeStrictJSON(body, &incoming); err != nil || auth.ValidateAPIKeyScopes(incoming.Scopes) != nil {
		writeError(w, http.StatusBadRequest, "scopes must contain known unique values", "invalid_request_error", "invalid_api_key_scopes")
		return
	}
	if incoming.ExpiresAt != nil && !incoming.ExpiresAt.After(time.Now().UTC()) {
		writeError(w, http.StatusBadRequest, "expires_at must be in the future", "invalid_request_error", "invalid_api_key_expiry")
		return
	}
	tenantID := h.requestTenantID(r)
	hash, err := run.HashRequest(tenantID, "POST "+r.URL.Path, key, body, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid idempotency request", "invalid_request_error", "invalid_idempotency_request")
		return
	}
	record, plaintext, err := h.apiKeys.Create(r.Context(), tenantID, incoming.Scopes, incoming.ExpiresAt, auth.APIKeyMutation{Key: key, Hash: hash})
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	h.appendAudit(r.Context(), tenantID, audit.ActionAPIKeyCreate, "api_key", record.PublicPrefix, "success", hash)
	writeJSON(w, http.StatusCreated, apiKeyResponseFrom(record, plaintext))
}

// listAPIKeys 返回当前租户的 API Key 元数据，不返回摘要或明文。
func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeAdmin) {
		return
	}
	if h.apiKeys == nil {
		writeError(w, http.StatusServiceUnavailable, "api key control is unavailable", "api_error", "api_key_control_unavailable")
		return
	}
	records, err := h.apiKeys.List(r.Context(), h.requestTenantID(r))
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	data := make([]apiKeyResponse, 0, len(records))
	for _, record := range records {
		data = append(data, apiKeyResponseFrom(record, ""))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// revokeAPIKey 停用指定租户 API Key，并支持幂等重试。
func (h *Handler) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeAdmin) {
		return
	}
	if h.apiKeys == nil {
		writeError(w, http.StatusServiceUnavailable, "api key control is unavailable", "api_error", "api_key_control_unavailable")
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
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	tenantID := h.requestTenantID(r)
	hash, err := run.HashRequest(tenantID, r.URL.Path, key, body, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid idempotency request", "invalid_request_error", "invalid_idempotency_request")
		return
	}
	prefix := strings.TrimSpace(r.PathValue("public_prefix"))
	if err := h.apiKeys.Revoke(r.Context(), tenantID, prefix, auth.APIKeyMutation{Key: key, Hash: hash}); err != nil {
		writeAPIKeyError(w, err)
		return
	}
	h.appendAudit(r.Context(), tenantID, audit.ActionAPIKeyRevoke, "api_key", prefix, "success", hash)
	writeJSON(w, http.StatusOK, map[string]string{"public_prefix": prefix, "state": "revoked"})
}

// apiKeyResponseFrom 将内部元数据转换为不泄露租户和摘要的 API 响应。
func apiKeyResponseFrom(record auth.APIKeyRecord, plaintext string) apiKeyResponse {
	return apiKeyResponse{PublicPrefix: record.PublicPrefix, Scopes: append([]auth.Scope(nil), record.Scopes...), Active: record.Active, ExpiresAt: record.ExpiresAt, CreatedAt: record.CreatedAt, Key: plaintext}
}

// writeAPIKeyError 将 API Key 存储错误映射为稳定错误码。
func writeAPIKeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrAPIKeyConflict):
		writeError(w, http.StatusConflict, "idempotency key conflict", "invalid_request_error", "idempotency_conflict")
	case errors.Is(err, auth.ErrAPIKeyNotFound):
		writeError(w, http.StatusNotFound, "api key not found", "invalid_request_error", "api_key_not_found")
	case errors.Is(err, auth.ErrInvalidAPIKeyOperation):
		writeError(w, http.StatusBadRequest, "invalid api key operation", "invalid_request_error", "invalid_api_key_operation")
	default:
		writeError(w, http.StatusBadGateway, "api key store unavailable", "api_error", "api_key_store_unavailable")
	}
}
