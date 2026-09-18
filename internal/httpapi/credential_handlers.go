package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/huz/limen/internal/audit"
	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/credentialstore"
)

type credentialRequest struct {
	EndpointID string `json:"endpoint_id"`
	Secret     string `json:"secret"`
}

// rotateCredential 写入加密凭据并立即原子替换对应 Provider 密钥。
func (h *Handler) rotateCredential(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeAdmin) {
		return
	}
	providerName := strings.TrimSpace(r.PathValue("provider"))
	setter, endpointID, ok := h.credentialBinding(providerName)
	if !ok || h.credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credential control is unavailable", "api_error", "credential_control_unavailable")
		return
	}
	body, err := readRequestBody(w, r)
	if err != nil {
		return
	}
	var incoming credentialRequest
	if err := decodeStrictJSON(body, &incoming); err != nil || strings.TrimSpace(incoming.EndpointID) == "" || incoming.Secret == "" {
		writeError(w, http.StatusBadRequest, "endpoint_id and secret are required", "invalid_request_error", "invalid_credential")
		return
	}
	if incoming.EndpointID != endpointID {
		writeError(w, http.StatusBadRequest, "credential endpoint is not allowed", "invalid_request_error", "credential_endpoint_mismatch")
		return
	}
	h.credentialMu.Lock()
	defer h.credentialMu.Unlock()
	record, err := h.credentials.Rotate(r.Context(), h.requestTenantID(r), providerName, endpointID, incoming.Secret)
	if err != nil {
		writeCredentialStoreError(w, err)
		return
	}
	if err := setter.SetAPIKey(incoming.Secret); err != nil {
		writeError(w, http.StatusServiceUnavailable, "credential activation failed", "api_error", "credential_activation_failed")
		return
	}
	h.appendAudit(r.Context(), h.requestTenantID(r), audit.ActionCredentialRotate, "credential", record.ID, "success", "")
	writeJSON(w, http.StatusOK, record)
}

// revokeCredential 撤销当前凭据并清除 Provider 内存中的密钥。
func (h *Handler) revokeCredential(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeAdmin) {
		return
	}
	providerName := strings.TrimSpace(r.PathValue("provider"))
	setter, endpointID, ok := h.credentialBinding(providerName)
	if !ok || h.credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credential control is unavailable", "api_error", "credential_control_unavailable")
		return
	}
	body, err := readRequestBody(w, r)
	if err != nil {
		return
	}
	var incoming credentialRequest
	if err := decodeStrictJSON(body, &incoming); err != nil || incoming.EndpointID != endpointID {
		writeError(w, http.StatusBadRequest, "endpoint_id is required and must match the configured endpoint", "invalid_request_error", "credential_endpoint_mismatch")
		return
	}
	h.credentialMu.Lock()
	defer h.credentialMu.Unlock()
	if err := h.credentials.Revoke(r.Context(), h.requestTenantID(r), providerName, endpointID); err != nil {
		writeCredentialStoreError(w, err)
		return
	}
	setter.ClearAPIKey()
	h.appendAudit(r.Context(), h.requestTenantID(r), audit.ActionCredentialRevoke, "credential", providerName+":"+endpointID, "success", "")
	writeJSON(w, http.StatusOK, map[string]string{"provider": providerName, "endpoint_id": endpointID, "state": "revoked"})
}

// credentialBinding 返回 Provider 的固定 endpoint 和密钥操作器。
func (h *Handler) credentialBinding(providerName string) (ProviderCredentialSetter, string, bool) {
	setter, setterOK := h.credentialSetters[providerName]
	endpointID, endpointOK := h.credentialEndpoints[providerName]
	return setter, endpointID, setterOK && endpointOK && setter != nil && endpointID != ""
}

// writeCredentialStoreError 将凭据存储错误转换为不泄露内容的 API 错误。
func writeCredentialStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, credentialstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "provider credential not found", "invalid_request_error", "credential_not_found")
		return
	}
	writeError(w, http.StatusBadGateway, "credential store unavailable", "api_error", "credential_store_unavailable")
}
