package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/journal"
	"github.com/huz/limen/internal/run"
)

// dryRun 鉴权并返回不访问 Provider 的确定性决策计划。
func (h *Handler) dryRun(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeInference, auth.ScopeDecisions) {
		return
	}
	h.dryRunWithRegistry(w, r, nil, "")
}

// dryRunWithRegistry 解析请求、记录决策快照并返回安全计划视图。
func (h *Handler) dryRunWithRegistry(w http.ResponseWriter, r *http.Request, registry *gateway.ModelRegistry, configVersion string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error", "invalid_body")
		return
	}
	envelope, err := parseChatRequestEnvelope(body)
	if err != nil {
		code := "invalid_chat_request"
		var unsupported *unsupportedFieldError
		if errors.As(err, &unsupported) {
			code = "unsupported_field"
		}
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", code)
		return
	}
	if h.router == nil {
		writeError(w, http.StatusBadGateway, "provider unavailable", "api_error", "provider_unavailable")
		return
	}
	var input decision.Input
	var plan decision.ExecutionPlan
	if registry == nil {
		input, plan, err = h.router.Explain(envelope.Request, envelope.Contract)
	} else {
		input, plan, err = h.router.ExplainWithRegistry(envelope.Request, envelope.Contract, registry, configVersion)
	}
	writePlanHashHeader(w, plan)
	if err != nil {
		var endpointBinding *gateway.EndpointBindingError
		if errors.As(err, &endpointBinding) {
			writeError(w, http.StatusBadRequest, "config endpoint binding is invalid", "invalid_request_error", "endpoint_binding_mismatch")
			return
		}
		var unsupported *gateway.UnsupportedModelError
		if errors.As(err, &unsupported) {
			writeError(w, http.StatusBadRequest, "unsupported model", "invalid_request_error", "unsupported_model")
			return
		}
		var noEligible *gateway.NoEligibleTargetError
		if errors.As(err, &noEligible) {
			writeError(w, http.StatusServiceUnavailable, err.Error(), "api_error", "no_eligible_target")
			return
		}
		var decisionErr *decision.DecisionError
		if errors.As(err, &decisionErr) {
			writeError(w, http.StatusBadRequest, "invalid capability contract", "invalid_request_error", decisionErr.Code)
			return
		}
		writeError(w, http.StatusBadRequest, "unable to generate decision plan", "invalid_request_error", "decision_error")
		return
	}
	decisionID, err := h.recordDecision(r.Context(), h.requestTenantID(r), input, plan)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "decision journal unavailable", "api_error", "decision_journal_unavailable")
		return
	}
	w.Header().Set("X-Limen-Decision-ID", decisionID)
	if plan.ConfigVersion != "" {
		w.Header().Set("X-Limen-Config-Version", plan.ConfigVersion)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(publicExecutionPlan(plan))
}

// recordDecision 为当前租户保存一次不含敏感正文的决策快照。
func (h *Handler) recordDecision(ctx context.Context, tenantID string, input decision.Input, plan decision.ExecutionPlan) (string, error) {
	if h.decisions == nil {
		return "", errors.New("decision journal is unavailable")
	}
	id, err := run.NewID("decision")
	if err != nil {
		return "", err
	}
	if err := h.decisions.Save(ctx, journal.Record{ID: id, TenantID: tenantID, Input: input, Plan: plan, CreatedAt: time.Now().UTC()}); err != nil {
		return "", fmt.Errorf("%w: %v", errDecisionJournal, err)
	}
	return id, nil
}

// getDecision 返回当前租户可审计的决策输入和执行计划。
func (h *Handler) getDecision(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeDecisions) {
		return
	}
	if h.decisions == nil {
		writeError(w, http.StatusServiceUnavailable, "decision journal unavailable", "api_error", "decision_journal_unavailable")
		return
	}
	record, err := h.decisions.Get(r.Context(), h.requestTenantID(r), r.PathValue("decision_id"))
	if err != nil {
		writeDecisionLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicDecisionRecord(record))
}

// replayDecision 使用历史输入重新计算计划，不访问 Provider 或当前熔断器。
func (h *Handler) replayDecision(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeDecisions) {
		return
	}
	if h.decisions == nil || h.router == nil {
		writeError(w, http.StatusServiceUnavailable, "decision replay unavailable", "api_error", "decision_replay_unavailable")
		return
	}
	record, err := h.decisions.Get(r.Context(), h.requestTenantID(r), r.PathValue("decision_id"))
	if err != nil {
		writeDecisionLookupError(w, err)
		return
	}
	replay, err := h.router.Replay(record.Input)
	if err != nil {
		var decisionErr *decision.DecisionError
		if errors.As(err, &decisionErr) {
			writeError(w, http.StatusConflict, "decision algorithm is unavailable", "invalid_request_error", "algorithm_version_unavailable")
			return
		}
		writeError(w, http.StatusBadGateway, "decision replay failed", "api_error", "decision_replay_error")
		return
	}
	differences := comparePlans(record.Plan, replay)
	writeJSON(w, http.StatusOK, map[string]any{
		"decision_id":   record.ID,
		"original_plan": publicExecutionPlan(record.Plan),
		"replay_plan":   publicExecutionPlan(replay),
		"match":         len(differences) == 0,
		"differences":   differences,
	})
}

// publicDecisionRecord 将内部 Replay 快照转换为不暴露真实上游模型名的响应。
func publicDecisionRecord(record journal.Record) publicDecisionRecordResponse {
	return publicDecisionRecordResponse{
		ID:        record.ID,
		Input:     publicDecisionInput(record.Input),
		Plan:      publicExecutionPlan(record.Plan),
		CreatedAt: record.CreatedAt,
	}
}

// publicDecisionInput 保留决策依据，但移除只供 Provider 使用的上游模型名。
func publicDecisionInput(input decision.Input) publicDecisionInputResponse {
	candidates := make([]publicCandidate, 0, len(input.Candidates))
	for _, candidate := range input.Candidates {
		candidates = append(candidates, publicCandidate{
			ModelID:           candidate.ModelID,
			Compatibility:     candidate.Compatibility,
			TargetID:          publicTargetID(candidate.Target.ID),
			Provider:          candidate.Target.Provider,
			Capabilities:      append([]string(nil), candidate.Target.Capabilities...),
			SupportsStreaming: candidate.Target.SupportsStreaming,
			QualityTier:       candidate.Target.QualityTier,
			CostTier:          candidate.Target.CostTier,
			ContextWindow:     candidate.Target.ContextWindow,
			DataClasses:       append([]string(nil), candidate.Target.DataClasses...),
			Pricing:           candidate.Target.Pricing,
			Enabled:           candidate.Enabled,
			SecurityAllowed:   candidate.SecurityAllowed,
			Health:            candidate.Health,
		})
	}
	return publicDecisionInputResponse{
		SchemaVersion:     input.SchemaVersion,
		AlgorithmVersion:  input.AlgorithmVersion,
		ConfigVersion:     input.ConfigVersion,
		EvaluatedAtUnixMS: input.EvaluatedAtUnixMS,
		Request:           input.Request,
		Run:               input.Run,
		Candidates:        candidates,
	}
}

// publicExecutionPlan 将执行计划压缩为可解释但不含上游模型名的视图。
func publicExecutionPlan(plan decision.ExecutionPlan) publicExecutionPlanResponse {
	candidates := make([]decision.CandidateResult, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		candidate.TargetID = publicTargetID(candidate.TargetID)
		candidates = append(candidates, candidate)
	}
	targets := make([]publicPlanTarget, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		targets = append(targets, publicPlanTarget{ModelID: target.ModelID, Compatibility: target.Compatibility, TargetID: publicTargetID(target.Target.ID), Provider: target.Target.Provider})
	}
	return publicExecutionPlanResponse{
		SchemaVersion:     plan.SchemaVersion,
		AlgorithmVersion:  plan.AlgorithmVersion,
		ConfigVersion:     plan.ConfigVersion,
		InputHash:         plan.InputHash,
		EffectiveStrategy: plan.EffectiveStrategy,
		Reasons:           append([]string(nil), plan.Reasons...),
		Candidates:        candidates,
		Targets:           targets,
		PlanHash:          plan.PlanHash,
	}
}

type publicDecisionRecordResponse struct {
	ID        string                      `json:"decision_id"`
	Input     publicDecisionInputResponse `json:"input"`
	Plan      publicExecutionPlanResponse `json:"plan"`
	CreatedAt time.Time                   `json:"created_at"`
}

type publicDecisionInputResponse struct {
	SchemaVersion     string               `json:"schema_version"`
	AlgorithmVersion  string               `json:"algorithm_version"`
	ConfigVersion     string               `json:"config_version,omitempty"`
	EvaluatedAtUnixMS int64                `json:"evaluated_at_unix_ms"`
	Request           decision.Request     `json:"request"`
	Run               decision.RunSnapshot `json:"run"`
	Candidates        []publicCandidate    `json:"candidates"`
}

type publicCandidate struct {
	ModelID           string                  `json:"model_id"`
	Compatibility     bool                    `json:"compatibility,omitempty"`
	TargetID          string                  `json:"target_id"`
	Provider          string                  `json:"provider"`
	Capabilities      []string                `json:"capabilities,omitempty"`
	SupportsStreaming bool                    `json:"supports_streaming"`
	QualityTier       int                     `json:"quality_tier"`
	CostTier          int                     `json:"cost_tier"`
	ContextWindow     int64                   `json:"context_window"`
	DataClasses       []string                `json:"data_classes,omitempty"`
	Pricing           *cost.Pricing           `json:"pricing,omitempty"`
	Enabled           bool                    `json:"enabled"`
	SecurityAllowed   bool                    `json:"security_allowed"`
	Health            decision.HealthSnapshot `json:"health"`
}

type publicExecutionPlanResponse struct {
	SchemaVersion     string                     `json:"schema_version"`
	AlgorithmVersion  string                     `json:"algorithm_version"`
	ConfigVersion     string                     `json:"config_version,omitempty"`
	InputHash         string                     `json:"input_hash"`
	EffectiveStrategy string                     `json:"effective_strategy"`
	Reasons           []string                   `json:"reasons"`
	Candidates        []decision.CandidateResult `json:"candidates"`
	Targets           []publicPlanTarget         `json:"targets"`
	PlanHash          string                     `json:"plan_hash"`
}

type publicPlanTarget struct {
	ModelID       string `json:"model_id"`
	Compatibility bool   `json:"compatibility,omitempty"`
	TargetID      string `json:"target_id"`
	Provider      string `json:"provider"`
}

// planDifference 描述 Replay 与原计划之间的安全差异。
type planDifference struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Original string `json:"original,omitempty"`
	Replay   string `json:"replay,omitempty"`
}

// comparePlans 返回不包含上游模型名的结构化计划差异。
func comparePlans(original, replay decision.ExecutionPlan) []planDifference {
	if original.PlanHash == replay.PlanHash {
		return nil
	}
	differences := make([]planDifference, 0)
	appendChange := func(path, originalValue, replayValue string) {
		if originalValue == replayValue {
			return
		}
		kind := "changed"
		switch {
		case originalValue == "":
			kind = "added"
		case replayValue == "":
			kind = "removed"
		}
		differences = append(differences, planDifference{Path: path, Kind: kind, Original: originalValue, Replay: replayValue})
	}
	appendChange("algorithm_version", original.AlgorithmVersion, replay.AlgorithmVersion)
	appendChange("config_version", original.ConfigVersion, replay.ConfigVersion)
	appendChange("effective_strategy", original.EffectiveStrategy, replay.EffectiveStrategy)
	appendChange("input_hash", original.InputHash, replay.InputHash)
	maxTargets := len(original.Targets)
	if len(replay.Targets) > maxTargets {
		maxTargets = len(replay.Targets)
	}
	for index := 0; index < maxTargets; index++ {
		originalValue, replayValue := "", ""
		if index < len(original.Targets) {
			originalValue = safePlanTargetID(original.Targets[index])
		}
		if index < len(replay.Targets) {
			replayValue = safePlanTargetID(replay.Targets[index])
		}
		appendChange(fmt.Sprintf("targets[%d]", index), originalValue, replayValue)
		if index < len(original.Targets) && index < len(replay.Targets) && targetMappingChanged(original.Targets[index], replay.Targets[index]) {
			differences = append(differences, planDifference{Path: fmt.Sprintf("targets[%d]/mapping", index), Kind: "changed"})
		}
		if index < len(original.Targets) && index < len(replay.Targets) && targetPolicyChanged(original.Targets[index], replay.Targets[index]) {
			differences = append(differences, planDifference{Path: fmt.Sprintf("targets[%d]/policy", index), Kind: "changed"})
		}
	}
	maxCandidates := len(original.Candidates)
	if len(replay.Candidates) > maxCandidates {
		maxCandidates = len(replay.Candidates)
	}
	for index := 0; index < maxCandidates; index++ {
		originalValue, replayValue := "", ""
		originalAccepted, replayAccepted := "", ""
		if index < len(original.Candidates) {
			originalValue = original.Candidates[index].Reason
			originalAccepted = strconv.FormatBool(original.Candidates[index].Accepted)
		}
		if index < len(replay.Candidates) {
			replayValue = replay.Candidates[index].Reason
			replayAccepted = strconv.FormatBool(replay.Candidates[index].Accepted)
		}
		appendChange(fmt.Sprintf("candidates[%d]/reason", index), originalValue, replayValue)
		appendChange(fmt.Sprintf("candidates[%d]/accepted", index), originalAccepted, replayAccepted)
	}
	appendChange("plan_hash", original.PlanHash, replay.PlanHash)
	return differences
}

// targetMappingChanged 判断目标的 Provider、上游模型或 endpoint 绑定是否变化，不返回其原始值。
func targetMappingChanged(original, replay decision.PlanTarget) bool {
	return original.Target.Provider != replay.Target.Provider ||
		original.Target.UpstreamModel != replay.Target.UpstreamModel ||
		original.Target.EndpointID != replay.Target.EndpointID
}

// targetPolicyChanged 判断目标能力、限制和价格元数据是否变化，不返回其原始值。
func targetPolicyChanged(original, replay decision.PlanTarget) bool {
	left, right := original.Target, replay.Target
	if !slices.Equal(left.Capabilities, right.Capabilities) ||
		left.SupportsStreaming != right.SupportsStreaming ||
		left.QualityTier != right.QualityTier ||
		left.CostTier != right.CostTier ||
		left.ContextWindow != right.ContextWindow ||
		!slices.Equal(left.DataClasses, right.DataClasses) {
		return true
	}
	if left.Pricing == nil || right.Pricing == nil {
		return left.Pricing != right.Pricing
	}
	return left.Pricing.InputPerMillionNanoUSD != right.Pricing.InputPerMillionNanoUSD ||
		left.Pricing.OutputPerMillionNanoUSD != right.Pricing.OutputPerMillionNanoUSD
}

// safePlanTargetID 返回计划目标的逻辑标识，不暴露真实上游模型名。
func safePlanTargetID(target decision.PlanTarget) string {
	return target.ModelID + ":" + target.Target.Provider + ":" + publicTargetID(target.Target.ID)
}

// publicTargetID 将内部目标标识转换为稳定 opaque 引用，避免配置命名泄露上游模型。
func publicTargetID(targetID string) string {
	return catalog.OpaqueTargetID(targetID)
}

// writeDecisionLookupError 将决策日志查询错误映射为稳定 API 错误。
func writeDecisionLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, journal.ErrNotFound) {
		writeError(w, http.StatusNotFound, "decision not found", "invalid_request_error", "decision_not_found")
		return
	}
	writeError(w, http.StatusBadGateway, "decision journal unavailable", "api_error", "decision_journal_unavailable")
}
