package httpapi

import (
	"context"
	"strings"
	"time"

	"github.com/huz/limen/internal/audit"
	"github.com/huz/limen/internal/auth"
)

// appendAudit 记录控制面操作摘要；审计故障不回滚已经提交的业务变更。
func (h *Handler) appendAudit(ctx context.Context, tenantID, action, resourceType, resourceID, outcome, requestHash string) {
	if h == nil || h.audit == nil {
		return
	}
	event := audit.Event{
		ID:           audit.EventIDWithActor(tenantID, requestActorID(ctx), action, resourceID, requestHash),
		TenantID:     tenantID,
		ActorID:      requestActorID(ctx),
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

// requestActorID 返回不包含明文凭据的当前执行者标识。
func requestActorID(ctx context.Context) string {
	if principal, ok := ctx.Value(principalContextKey{}).(auth.Principal); ok && strings.TrimSpace(principal.Subject) != "" {
		return principal.Subject
	}
	return "unknown"
}
