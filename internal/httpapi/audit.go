package httpapi

import (
	"context"
	"time"

	"github.com/huz/limen/internal/audit"
)

// appendAudit 记录控制面操作摘要；审计故障不回滚已经提交的业务变更。
func (h *Handler) appendAudit(ctx context.Context, tenantID, action, resourceType, resourceID, outcome, requestHash string) {
	if h == nil || h.audit == nil {
		return
	}
	event := audit.Event{
		ID:           audit.EventID(tenantID, action, resourceID, requestHash),
		TenantID:     tenantID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Outcome:      outcome,
		RequestHash:  requestHash,
		CreatedAt:    time.Now().UTC(),
	}
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = h.audit.Append(auditContext, event)
}
