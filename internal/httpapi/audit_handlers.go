package httpapi

import (
	"net/http"
	"strconv"

	"github.com/huz/limen/internal/audit"
	"github.com/huz/limen/internal/auth"
)

// auditResponse 是控制面返回的租户审计摘要列表。
type auditResponse struct {
	Object string        `json:"object"`
	Data   []audit.Event `json:"data"`
}

// listAudit 鉴权并返回当前租户最近的管理操作摘要。
func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeAdmin) {
		return
	}
	if h.audit == nil {
		writeError(w, http.StatusServiceUnavailable, "audit store unavailable", "api_error", "audit_store_unavailable")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100", "invalid_request_error", "invalid_limit")
			return
		}
		limit = parsed
	}
	events, err := h.audit.List(r.Context(), h.requestTenantID(r), limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, "audit store unavailable", "api_error", "audit_store_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, auditResponse{Object: "list", Data: events})
}
