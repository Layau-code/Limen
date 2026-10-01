package httpapi

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/semantic"
	"github.com/huz/limen/internal/telemetry"
)

// prepareSemantic 在授权和数据分类检查后执行或采样语义评估。
func (h *Handler) prepareSemantic(r *http.Request, envelope parsedChatRequest) (*decision.SemanticAssessment, semantic.State, bool) {
	cfg := envelope.SemanticRouting
	if cfg.Mode != "active" && cfg.Mode != "shadow" {
		return nil, semantic.State{}, false
	}
	if envelope.Request.Model != "auto" || h.router == nil {
		return nil, semantic.State{}, false
	}
	registry := envelope.Registry
	if registry == nil {
		registry, _, _, _ = h.router.RoutingSnapshot()
	}
	if registry == nil || registry.IsCompatibility() {
		return nil, semantic.State{}, false
	}
	if envelope.SemanticSkip {
		if cfg.Mode == "active" {
			return h.semanticSkip("bypassed", cfg, semantic.State{}), semantic.State{}, false
		}
		return nil, semantic.State{}, false
	}
	if !cfg.ExternalEnabled {
		if cfg.Mode == "active" {
			return h.semanticSkip("not_authorized", cfg, semantic.State{}), semantic.State{}, false
		}
		return nil, semantic.State{}, false
	}
	principal, hasPrincipal := r.Context().Value(principalContextKey{}).(auth.Principal)
	_, semanticScope := principal.Scopes[auth.ScopeSemanticExternal]
	if !hasPrincipal || !semanticScope {
		if cfg.Mode == "active" {
			return h.semanticSkip("not_authorized", cfg, semantic.State{}), semantic.State{}, false
		}
		return nil, semantic.State{}, false
	}
	if envelope.Contract.DataClass != "public" || !containsDataClass(cfg.AllowedDataClasses, "public") {
		if cfg.Mode == "active" {
			return h.semanticSkip("data_class_denied", cfg, semantic.State{}), semantic.State{}, false
		}
		return nil, semantic.State{}, false
	}
	state, usable := semantic.BuildState(envelope.Request.Messages)
	if !usable {
		if cfg.Mode == "active" {
			return h.semanticSkip("no_usable_state", cfg, semantic.State{}), semantic.State{}, false
		}
		return nil, semantic.State{}, false
	}
	if cfg.Mode == "shadow" {
		if h.semanticAssessor == nil || !sampleSemanticState(state, cfg.SamplePercent) {
			return nil, semantic.State{}, false
		}
		return nil, state, true
	}
	if h.semanticAssessor == nil {
		assessment := h.semanticSkip("provider_error", cfg, state)
		return assessment, semantic.State{}, false
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 200 * time.Millisecond
	}
	if deadline, ok := r.Context().Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			assessment := h.semanticSkip("provider_timeout", cfg, state)
			return assessment, semantic.State{}, false
		}
		budget := remaining / 10
		if budget <= 0 {
			assessment := h.semanticSkip("provider_timeout", cfg, state)
			return assessment, semantic.State{}, false
		}
		if budget < timeout {
			timeout = budget
		}
	}
	assessmentContext, cancel := context.WithTimeout(r.Context(), timeout)
	result, err := h.semanticAssessor.Assess(assessmentContext, h.requestTenantID(r), state, cfg.ModelVersion, cfg.QuestionTemplateVersion)
	cancel()
	if err != nil {
		status := semantic.StatusForError(err)
		assessment := h.semanticSkip(status, cfg, state)
		return assessment, semantic.State{}, false
	}
	decisionAssessment := semantic.Map(cfg, state, result)
	h.metrics.Inc(telemetry.SemanticAssessmentsTotal, telemetry.Labels{Endpoint: "/v1/chat/completions", Reason: decisionAssessment.Status})
	h.metrics.Observe(telemetry.SemanticAssessmentDurationSeconds, telemetry.Labels{Endpoint: "/v1/chat/completions"}, result.Duration.Seconds())
	h.metrics.Add(telemetry.SemanticInputTokensTotal, telemetry.Labels{Endpoint: "/v1/chat/completions"}, uint64(max64(result.InputTokens, 0)))
	h.metrics.Add(telemetry.SemanticOutputTokensTotal, telemetry.Labels{Endpoint: "/v1/chat/completions"}, uint64(max64(result.OutputTokens, 0)))
	if costNanoUSD, known := semantic.CostNanoUSD(cfg, result); known {
		h.metrics.Add(telemetry.SemanticCostNanoUSDTotal, telemetry.Labels{Endpoint: "/v1/chat/completions"}, uint64(costNanoUSD))
	} else {
		h.metrics.Inc(telemetry.SemanticCostUnknownTotal, telemetry.Labels{Endpoint: "/v1/chat/completions"})
	}
	return decisionAssessment, semantic.State{}, false
}

// semanticPreview is the explicit, authorized endpoint for a one-off Jev request.
// semanticPreview 为获准调用方提供不写入决策日志的一次性评估。
func (h *Handler) semanticPreview(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeInference, auth.ScopeDecisions, auth.ScopeSemanticExternal) {
		return
	}
	principal, ok := r.Context().Value(principalContextKey{}).(auth.Principal)
	if !ok {
		writeError(w, http.StatusForbidden, "semantic scope is required", "permission_error", "insufficient_scope")
		return
	}
	if _, ok := principal.Scopes[auth.ScopeSemanticExternal]; !ok {
		writeError(w, http.StatusForbidden, "semantic scope is required", "permission_error", "insufficient_scope")
		return
	}
	if h.router == nil || h.semanticAssessor == nil {
		writeError(w, http.StatusServiceUnavailable, "semantic preview is unavailable", "api_error", "semantic_unavailable")
		return
	}
	registry, _, _, cfg := h.router.RoutingSnapshot()
	if !cfg.ExternalEnabled || !containsDataClass(cfg.AllowedDataClasses, "public") {
		writeError(w, http.StatusForbidden, "external semantic assessment is not enabled for public data", "permission_error", "semantic_not_enabled")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error", "invalid_body")
		return
	}
	envelope, err := parseChatRequestEnvelope(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_chat_request")
		return
	}
	if envelope.Request.Model != "auto" || registry == nil || registry.IsCompatibility() {
		writeError(w, http.StatusBadRequest, "semantic preview requires model=auto", "invalid_request_error", "semantic_preview_requires_auto")
		return
	}
	if envelope.Contract.DataClass != "public" {
		writeError(w, http.StatusForbidden, "semantic preview requires data_class=public", "permission_error", "data_class_denied")
		return
	}
	state, usable := semantic.BuildState(envelope.Request.Messages)
	if !usable {
		writeError(w, http.StatusBadRequest, "no usable user message", "invalid_request_error", "no_usable_state")
		return
	}
	timeout := cfg.Timeout
	if timeout <= 0 || timeout > 200*time.Millisecond {
		timeout = 200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	result, err := h.semanticAssessor.Assess(ctx, h.requestTenantID(r), state, cfg.ModelVersion, cfg.QuestionTemplateVersion)
	if err != nil {
		status := semantic.StatusForError(err)
		h.metrics.Inc(telemetry.SemanticAssessmentsTotal, telemetry.Labels{Endpoint: "/v1/limen/decisions/semantic-preview", Reason: status})
		writeJSON(w, http.StatusOK, map[string]any{"semantic_status": status, "state_hash": state.Hash, "state_length": state.Length, "truncated": state.Truncated, "language": state.Language})
		return
	}
	assessment := semantic.Map(cfg, state, result)
	assessment.Mode = "preview"
	h.metrics.Inc(telemetry.SemanticAssessmentsTotal, telemetry.Labels{Endpoint: "/v1/limen/decisions/semantic-preview", Reason: assessment.Status})
	h.metrics.Observe(telemetry.SemanticAssessmentDurationSeconds, telemetry.Labels{Endpoint: "/v1/limen/decisions/semantic-preview"}, result.Duration.Seconds())
	h.metrics.Add(telemetry.SemanticInputTokensTotal, telemetry.Labels{Endpoint: "/v1/limen/decisions/semantic-preview"}, uint64(max64(result.InputTokens, 0)))
	h.metrics.Add(telemetry.SemanticOutputTokensTotal, telemetry.Labels{Endpoint: "/v1/limen/decisions/semantic-preview"}, uint64(max64(result.OutputTokens, 0)))
	costNanoUSD, costKnown := semantic.CostNanoUSD(cfg, result)
	if costKnown {
		h.metrics.Add(telemetry.SemanticCostNanoUSDTotal, telemetry.Labels{Endpoint: "/v1/limen/decisions/semantic-preview"}, uint64(costNanoUSD))
	} else {
		h.metrics.Inc(telemetry.SemanticCostUnknownTotal, telemetry.Labels{Endpoint: "/v1/limen/decisions/semantic-preview"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"semantic_assessment": publicSemanticAssessment(assessment), "input_tokens": result.InputTokens, "output_tokens": result.OutputTokens, "duration_ms": result.Duration.Milliseconds(), "estimated_cost_nano_usd": costNanoUSD, "cost_known": costKnown})
}

// semanticSkip 创建不应用路由信号的安全评估结果。
func (h *Handler) semanticSkip(status string, cfg config.SemanticRouting, state semantic.State) *decision.SemanticAssessment {
	assessment := semantic.Snapshot("active", status, status, state, cfg)
	assessment.ModelVersion = cfg.ModelVersion
	h.metrics.Inc(telemetry.SemanticAssessmentsTotal, telemetry.Labels{Endpoint: "/v1/chat/completions", Reason: status})
	return assessment
}

// containsDataClass 判断数据分类是否在显式允许列表中。
func containsDataClass(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// sampleSemanticState 使用状态哈希执行稳定的百分比抽样。
func sampleSemanticState(state semantic.State, percent int) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	encoded := state.Hash
	if len(encoded) < len("sha256:")+8 {
		return false
	}
	b, err := hex.DecodeString(encoded[len("sha256:") : len("sha256:")+8])
	return err == nil && len(b) == 4 && uint64(binary.BigEndian.Uint32(b))*100>>32 < uint64(percent)
}

// max64 将有符号计数限制到指定下界。
func max64(value, lower int64) int64 {
	if value < lower {
		return lower
	}
	return value
}
