package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/credentialstore"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/journal"
	"github.com/huz/limen/internal/provider"
	"github.com/huz/limen/internal/run"
	"github.com/huz/limen/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const maxRequestBytes = 4 << 20

var errDecisionJournal = errors.New("decision journal unavailable")

var settlementTrailerNames = []string{
	"X-Limen-Settlement-Status",
	"X-Limen-Input-Tokens",
	"X-Limen-Output-Tokens",
	"X-Limen-Total-Tokens",
	"X-Limen-Cost-USD",
}

type Handler struct {
	authenticator       auth.Authenticator
	router              *gateway.Router
	runs                run.Service
	tenantID            string
	leaseOwner          string
	decisions           journal.Store
	configs             configstore.Store
	metrics             *telemetry.Registry
	credentials         credentialstore.Store
	credentialSetters   map[string]ProviderCredentialSetter
	credentialEndpoints map[string]string
	cancellations       *run.CancellationHub
	credentialMu        sync.Mutex
}

const runTenantID = "local"

type principalContextKey struct{}

// New 创建 Limen 的 HTTP 路由和请求处理器。
func New(apiKey string, router *gateway.Router) http.Handler {
	return NewWithHealth(apiKey, router, nil)
}

// NewWithHealth 创建带健康检查端点的 Limen HTTP 处理器。
func NewWithHealth(apiKey string, router *gateway.Router, health *Health) http.Handler {
	return NewWithHealthAndRuns(apiKey, router, health, nil)
}

// NewWithRuns 创建带 Run 控制面的 HTTP 处理器。
func NewWithRuns(apiKey string, router *gateway.Router, runs run.Service) http.Handler {
	return NewWithHealthAndRuns(apiKey, router, nil, runs)
}

// NewWithHealthAndRuns 创建同时支持健康检查和 Run 控制面的 HTTP 处理器。
func NewWithHealthAndRuns(apiKey string, router *gateway.Router, health *Health, runs run.Service) http.Handler {
	return NewWithHealthAndRunsForTenant(apiKey, router, health, runTenantID, runs)
}

// NewWithHealthAndRunsForTenant 创建绑定到指定租户的健康和 Run 控制面。
func NewWithHealthAndRunsForTenant(apiKey string, router *gateway.Router, health *Health, tenantID string, runs run.Service) http.Handler {
	return NewWithHealthAndRunsForTenantScopes(apiKey, router, health, tenantID, nil, runs)
}

// NewWithHealthAndRunsForTenantScopes 创建带租户和 Scope 限制的 HTTP 处理器。
func NewWithHealthAndRunsForTenantScopes(apiKey string, router *gateway.Router, health *Health, tenantID string, scopes []auth.Scope, runs run.Service) http.Handler {
	return NewWithHealthAndRunsForTenantScopesAndJournal(apiKey, router, health, tenantID, scopes, journal.NewMemoryStore(), runs)
}

// NewWithHealthAndRunsForTenantScopesAndJournal 创建带决策日志的完整 HTTP 处理器。
func NewWithHealthAndRunsForTenantScopesAndJournal(apiKey string, router *gateway.Router, health *Health, tenantID string, scopes []auth.Scope, decisions journal.Store, runs run.Service) http.Handler {
	return NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(auth.NewStaticAuthenticator(apiKey, tenantID, scopes), router, health, tenantID, decisions, configstore.NewMemoryStore(), runs)
}

// NewWithHealthAndRunsForTenantAuthenticatorAndJournal 创建使用可替换鉴权器的 HTTP 处理器。
func NewWithHealthAndRunsForTenantAuthenticatorAndJournal(authenticator auth.Authenticator, router *gateway.Router, health *Health, tenantID string, decisions journal.Store, runs run.Service) http.Handler {
	return NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(authenticator, router, health, tenantID, decisions, configstore.NewMemoryStore(), runs)
}

// NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig 创建包含决策和配置控制面的完整处理器。
func NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(authenticator auth.Authenticator, router *gateway.Router, health *Health, tenantID string, decisions journal.Store, configs configstore.Store, runs run.Service) http.Handler {
	return NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentials(authenticator, router, health, tenantID, decisions, configs, nil, nil, nil, runs)
}

// ProviderCredentialSetter 定义 Provider 密钥的原子替换和撤销操作。
type ProviderCredentialSetter interface {
	SetAPIKey(string) error
	ClearAPIKey()
}

// NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentials 创建完整数据面和凭据控制面。
func NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentials(authenticator auth.Authenticator, router *gateway.Router, health *Health, tenantID string, decisions journal.Store, configs configstore.Store, credentials credentialstore.Store, setters map[string]ProviderCredentialSetter, endpoints map[string]string, runs run.Service, cancellationHubs ...*run.CancellationHub) http.Handler {
	if health == nil {
		health = NewHealth()
		health.SetReady(true)
	}
	if strings.TrimSpace(tenantID) == "" {
		tenantID = runTenantID
	}
	if decisions == nil {
		decisions = journal.NewMemoryStore()
	}
	if configs == nil {
		configs = configstore.NewMemoryStore()
	}
	var cancellationHub *run.CancellationHub
	if len(cancellationHubs) > 0 {
		cancellationHub = cancellationHubs[0]
	}
	handler := &Handler{
		authenticator:       authenticator,
		router:              router,
		runs:                runs,
		tenantID:            tenantID,
		leaseOwner:          newLeaseOwner(),
		decisions:           decisions,
		configs:             configs,
		metrics:             telemetry.NewRegistry(),
		credentials:         credentials,
		credentialSetters:   setters,
		credentialEndpoints: endpoints,
		cancellations:       cancellationHub,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", handler.chatCompletions)
	mux.HandleFunc("POST /v1/limen/decisions/dry-run", handler.dryRun)
	mux.HandleFunc("GET /v1/limen/decisions/{decision_id}", handler.getDecision)
	mux.HandleFunc("POST /v1/limen/decisions/{decision_id}/replay", handler.replayDecision)
	mux.HandleFunc("GET /v1/limen/configs", handler.listConfigs)
	mux.HandleFunc("GET /v1/limen/configs/{version}/diff/{base_version}", handler.diffConfig)
	mux.HandleFunc("POST /v1/limen/configs", handler.createConfig)
	mux.HandleFunc("POST /v1/limen/configs/{version}/publish", handler.publishConfig)
	mux.HandleFunc("POST /v1/limen/credentials/{provider}", handler.rotateCredential)
	mux.HandleFunc("POST /v1/limen/credentials/{provider}/revoke", handler.revokeCredential)
	mux.HandleFunc("POST /v1/limen/runs", handler.createRun)
	mux.HandleFunc("GET /v1/limen/runs/{run_id}", handler.getRun)
	mux.HandleFunc("POST /v1/limen/runs/{run_id}/complete", handler.completeRun)
	mux.HandleFunc("POST /v1/limen/runs/{run_id}/cancel", handler.cancelRun)
	mux.HandleFunc("GET /v1/limen/runs/{run_id}/requests/{request_id}", handler.getRunRequest)
	mux.HandleFunc("POST /v1/limen/runs/{run_id}/requests/{request_id}/accounting", handler.resolveAccounting)
	mux.HandleFunc("GET /v1/models", handler.models)
	mux.HandleFunc("GET /metrics", handler.metricsEndpoint)
	mux.HandleFunc("GET /livez", health.Live)
	mux.HandleFunc("GET /readyz", health.Ready)
	return mux
}

// dryRun 鉴权并返回不访问 Provider 的确定性决策计划。
func (h *Handler) dryRun(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeInference, auth.ScopeDecisions) {
		return
	}
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
	input, plan, err := h.router.Explain(envelope.Request, envelope.Contract)
	if err != nil {
		var unsupported *gateway.UnsupportedModelError
		if errors.As(err, &unsupported) {
			writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "unsupported_model")
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(plan)
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
	writeJSON(w, http.StatusOK, record)
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
		"original_plan": record.Plan,
		"replay_plan":   replay,
		"match":         len(differences) == 0,
		"differences":   differences,
	})
}

// comparePlans 返回 Replay 与原计划之间的稳定差异码。
func comparePlans(original, replay decision.ExecutionPlan) []string {
	if original.PlanHash == replay.PlanHash {
		return nil
	}
	return []string{"plan_hash_mismatch"}
}

// writeDecisionLookupError 将决策日志查询错误映射为稳定 API 错误。
func writeDecisionLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, journal.ErrNotFound) {
		writeError(w, http.StatusNotFound, "decision not found", "invalid_request_error", "decision_not_found")
		return
	}
	writeError(w, http.StatusBadGateway, "decision journal unavailable", "api_error", "decision_journal_unavailable")
}

type createRunRequest struct {
	SoftBudgetUSD  string `json:"soft_budget_usd"`
	Deadline       string `json:"deadline"`
	MaxParallelism int    `json:"max_parallelism"`
	Strategy       string `json:"strategy"`
}

// createRun 创建一个带软预算和截止时间的受治理 Run。
func (h *Handler) createRun(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeRunsWrite) {
		return
	}
	control, ok := h.runs.(run.ControlService)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "run control is unavailable", "api_error", "run_unavailable")
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
	var incoming createRunRequest
	if err := decodeStrictJSON(body, &incoming); err != nil {
		writeError(w, http.StatusBadRequest, "invalid Run request", "invalid_request_error", "invalid_run_request")
		return
	}
	budget, err := cost.ParseUSD(incoming.SoftBudgetUSD)
	if err != nil || budget < 0 {
		writeError(w, http.StatusBadRequest, "soft_budget_usd must be a non-negative decimal", "invalid_request_error", "invalid_run_budget")
		return
	}
	deadline := time.Time{}
	if incoming.Deadline != "" {
		deadline, err = time.Parse(time.RFC3339, incoming.Deadline)
		if err != nil {
			writeError(w, http.StatusBadRequest, "deadline must be RFC3339", "invalid_request_error", "invalid_run_deadline")
			return
		}
	}
	if incoming.MaxParallelism <= 0 {
		writeError(w, http.StatusBadRequest, "max_parallelism must be positive", "invalid_request_error", "invalid_run_concurrency")
		return
	}
	strategy := incoming.Strategy
	if strategy == "" {
		strategy = "balanced"
	}
	if strategy != "balanced" && strategy != "economy" {
		writeError(w, http.StatusBadRequest, "unsupported Run strategy", "invalid_request_error", "strategy_conflict")
		return
	}
	now := time.Now().UTC()
	id, err := run.NewID("run")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to create Run", "api_error", "run_id_error")
		return
	}
	tenantID := h.requestTenantID(r)
	hash, err := run.HashRequest(tenantID, r.URL.Path, key, body, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid idempotency request", "invalid_request_error", "invalid_idempotency_request")
		return
	}
	configVersion := "runtime"
	if h.router != nil && h.router.ConfigVersion() != "" {
		configVersion = h.router.ConfigVersion()
	}
	item, err := control.CreateRunWithMutation(r.Context(), tenantID, run.Run{ID: id, State: run.StateActive, SoftBudgetNanoUSD: budget, Deadline: deadline, MaxParallelism: incoming.MaxParallelism, Strategy: strategy, ConfigVersion: configVersion, CreatedAt: now, UpdatedAt: now}, run.Mutation{Key: key, Hash: hash})
	if err != nil {
		writeRunMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// getRun 返回不含正文的 Run 当前状态。
func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeRunsRead) {
		return
	}
	if h.runs == nil {
		writeError(w, http.StatusServiceUnavailable, "run control is unavailable", "api_error", "run_unavailable")
		return
	}
	item, err := h.runs.GetRun(r.Context(), h.requestTenantID(r), r.PathValue("run_id"))
	if err != nil {
		writeRunLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// completeRun 请求停止新准入，并在在途请求结束后完成 Run。
func (h *Handler) completeRun(w http.ResponseWriter, r *http.Request) {
	h.mutateRun(w, r, false)
}

// cancelRun 立即标记 Run 为取消并阻止后续准入。
func (h *Handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	h.mutateRun(w, r, true)
}

// mutateRun 执行带幂等键的 Run 完成或取消操作。
func (h *Handler) mutateRun(w http.ResponseWriter, r *http.Request, cancel bool) {
	if !h.authenticateScopes(w, r, auth.ScopeRunsWrite) {
		return
	}
	control, ok := h.runs.(run.ControlService)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "run control is unavailable", "api_error", "run_unavailable")
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
	mutation := run.Mutation{Key: key, Hash: hash}
	var item run.Run
	if cancel {
		item, err = control.CancelRunWithMutation(r.Context(), tenantID, r.PathValue("run_id"), mutation)
	} else {
		item, err = control.CompleteRunWithMutation(r.Context(), tenantID, r.PathValue("run_id"), mutation)
	}
	if err != nil {
		writeRunMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// getRunRequest 返回请求状态和结算状态，不返回 Prompt 或 Response。
func (h *Handler) getRunRequest(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeRunsRead) {
		return
	}
	if h.runs == nil {
		writeError(w, http.StatusServiceUnavailable, "run control is unavailable", "api_error", "run_unavailable")
		return
	}
	item, err := h.runs.GetRequest(r.Context(), h.requestTenantID(r), r.PathValue("request_id"))
	if err != nil {
		writeRunLookupError(w, err)
		return
	}
	if item.RunID != r.PathValue("run_id") {
		writeError(w, http.StatusNotFound, "run request not found", "invalid_request_error", "run_request_not_found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type accountingResolutionRequest struct {
	Mode    string `json:"mode"`
	CostUSD string `json:"cost_usd"`
}

// resolveAccounting 由管理员完成未知费用的补记或明确接受未知费用。
func (h *Handler) resolveAccounting(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeAdmin) {
		return
	}
	service, ok := h.runs.(run.AccountingService)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "accounting control is unavailable", "api_error", "accounting_control_unavailable")
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
	var incoming accountingResolutionRequest
	if err := decodeStrictJSON(body, &incoming); err != nil {
		writeError(w, http.StatusBadRequest, "invalid accounting resolution", "invalid_request_error", "invalid_accounting_resolution")
		return
	}
	resolution, err := parseAccountingResolution(incoming)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_accounting_resolution")
		return
	}
	tenantID := h.requestTenantID(r)
	hash, err := run.HashRequest(tenantID, r.URL.Path, key, body, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid idempotency request", "invalid_request_error", "invalid_idempotency_request")
		return
	}
	request, err := service.ResolveAccountingWithMutation(r.Context(), tenantID, r.PathValue("run_id"), r.PathValue("request_id"), resolution, run.Mutation{Key: key, Hash: hash}, time.Now().UTC())
	if err != nil {
		writeAccountingResolutionError(w, err, r.PathValue("request_id"))
		return
	}
	writeJSON(w, http.StatusOK, request)
}

// parseAccountingResolution 将管理员请求转换为纳美元定点处置参数。
func parseAccountingResolution(incoming accountingResolutionRequest) (run.AccountingResolution, error) {
	switch strings.TrimSpace(incoming.Mode) {
	case "accept_unknown":
		if incoming.CostUSD != "" {
			return run.AccountingResolution{}, errors.New("cost_usd is not allowed when mode is accept_unknown")
		}
		return run.AccountingResolution{AcceptUnknown: true}, nil
	case "cost":
		if strings.TrimSpace(incoming.CostUSD) == "" {
			return run.AccountingResolution{}, errors.New("cost_usd is required when mode is cost")
		}
		value, err := cost.ParseUSD(incoming.CostUSD)
		if err != nil {
			return run.AccountingResolution{}, errors.New("cost_usd must be a non-negative decimal")
		}
		return run.AccountingResolution{CostNanoUSD: &value}, nil
	default:
		return run.AccountingResolution{}, errors.New("mode must be cost or accept_unknown")
	}
}

// writeAccountingResolutionError 将未知费用处置错误映射为稳定 API 错误。
func writeAccountingResolutionError(w http.ResponseWriter, err error, requestID string) {
	if requestID != "" {
		w.Header().Set("X-Limen-Request-ID", requestID)
	}
	switch {
	case errors.Is(err, run.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency key conflict", "invalid_request_error", "idempotency_conflict")
	case errors.Is(err, run.ErrRequestAlreadyProcessed):
		writeError(w, http.StatusConflict, "request already processed", "invalid_request_error", "request_already_processed")
	case errors.Is(err, run.ErrRequestNotSettleable):
		writeError(w, http.StatusConflict, "request is not awaiting accounting resolution", "invalid_request_error", "request_not_settleable")
	case errors.Is(err, run.ErrAccountingSuspended):
		writeError(w, http.StatusConflict, "Run is not awaiting accounting resolution", "invalid_request_error", "run_accounting_not_suspended")
	case errors.Is(err, run.ErrResourceNotFound):
		writeError(w, http.StatusNotFound, "run request not found", "invalid_request_error", "run_request_not_found")
	case errors.Is(err, run.ErrInvalidAccountingResolution):
		writeError(w, http.StatusBadRequest, "invalid accounting resolution", "invalid_request_error", "invalid_accounting_resolution")
	default:
		writeError(w, http.StatusBadGateway, "accounting control unavailable", "api_error", "accounting_control_error")
	}
}

// idempotencyKey 读取并规范化控制面幂等键。
func idempotencyKey(r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	return key, key != ""
}

// readRequestBody 以有界缓冲读取控制面请求正文。
func readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error", "invalid_body")
		return nil, err
	}
	return body, nil
}

// decodeStrictJSON 严格解析单个 JSON 值并拒绝未知字段。
func decodeStrictJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

// ensureJSONEOF 确认正文中不存在第二个 JSON 值。
func ensureJSONEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

// writeJSON 编码不含敏感正文的 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeRunLookupError 将 Store 查询错误映射为稳定 API 错误。
func writeRunLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, run.ErrResourceNotFound) {
		writeError(w, http.StatusNotFound, "run resource not found", "invalid_request_error", "run_resource_not_found")
		return
	}
	writeError(w, http.StatusBadGateway, "run store unavailable", "api_error", "run_store_error")
}

// writeRunMutationError 将控制面状态变更错误映射为稳定 API 错误。
func writeRunMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, run.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency key conflict", "invalid_request_error", "idempotency_conflict")
	case errors.Is(err, run.ErrInvalidRunTransition), errors.Is(err, run.ErrRunNotActive):
		writeError(w, http.StatusConflict, "run is not active", "invalid_request_error", "run_not_active")
	case errors.Is(err, run.ErrResourceNotFound):
		writeError(w, http.StatusNotFound, "run resource not found", "invalid_request_error", "run_resource_not_found")
	default:
		writeError(w, http.StatusBadRequest, "invalid Run mutation", "invalid_request_error", "run_mutation_error")
	}
}

// chatCompletions 鉴权并处理一次 Chat Completions 请求。
func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	metricModel := "unparsed"
	defer func() {
		labels := telemetry.Labels{
			Endpoint: "/v1/chat/completions",
			Status:   metricStatus(recorder.status),
			Model:    metricModel,
			Reason:   recorder.errorCode,
		}
		h.metrics.Inc(telemetry.RequestsTotal, labels)
		h.metrics.Observe(telemetry.RequestDurationSeconds, labels, time.Since(started).Seconds())
		if firstByte := recorder.FirstByteAt(); !firstByte.IsZero() {
			h.metrics.Observe(telemetry.TimeToFirstByteSeconds, labels, firstByte.Sub(started).Seconds())
		}
	}()
	h.handleChatCompletions(recorder, r, &metricModel)
}

// handleChatCompletions 执行聊天请求主流程，并只向指标返回可信模型标识。
func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request, metricModel *string) {
	scopes := []auth.Scope{auth.ScopeInference}
	if strings.TrimSpace(r.Header.Get("X-Limen-Run-ID")) != "" {
		scopes = append(scopes, auth.ScopeRunsWrite)
	}
	if !h.authenticateScopes(w, r, scopes...) {
		return
	}
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
		*metricModel = "unavailable"
	} else {
		*metricModel = h.router.ObservableModelID(envelope.Request.Model)
	}
	if !h.applyRunContract(w, r, &envelope) {
		return
	}
	if h.router == nil {
		writeError(w, http.StatusBadGateway, "provider unavailable", "api_error", "provider_unavailable")
		return
	}
	stopCancellation := h.watchRunCancellation(r, h.requestTenantID(r), strings.TrimSpace(r.Header.Get("X-Limen-Run-ID")))
	defer stopCancellation()
	requestID, releaseLease, admitted := h.admitRunRequest(w, r, body)
	if !admitted {
		return
	}
	defer releaseLease()
	h.forward(w, r, envelope.Request, envelope.Contract, requestID)
}

// applyRunContract 将 Run 固定策略注入请求，并拒绝客户端覆盖治理边界。
func (h *Handler) applyRunContract(w http.ResponseWriter, r *http.Request, envelope *parsedChatRequest) bool {
	runID := strings.TrimSpace(r.Header.Get("X-Limen-Run-ID"))
	if runID == "" || h.runs == nil {
		return true
	}
	runItem, err := h.runs.GetRun(r.Context(), h.requestTenantID(r), runID)
	if err != nil {
		writeRunLookupError(w, err)
		return false
	}
	if envelope.Contract.Strategy != "" && envelope.Contract.Strategy != runItem.Strategy {
		writeError(w, http.StatusBadRequest, "request strategy conflicts with Run strategy", "invalid_request_error", "strategy_conflict")
		return false
	}
	envelope.Contract.Strategy = runItem.Strategy
	envelope.Contract.Active = true
	return true
}

// watchRunCancellation 轮询租户取消事件，并取消当前请求的 Provider Context。
func (h *Handler) watchRunCancellation(r *http.Request, tenantID, runID string) func() {
	if runID == "" || h.runs == nil {
		return func() {}
	}
	service, ok := h.runs.(run.CancellationService)
	if !ok {
		return func() {}
	}
	requestContext, cancel := context.WithCancelCause(r.Context())
	*r = *r.WithContext(requestContext)
	var notified <-chan struct{}
	unsubscribe := func() {}
	if h.cancellations != nil {
		notified, unsubscribe = h.cancellations.Subscribe(tenantID, runID)
	}
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var afterID int64
		for {
			select {
			case <-notified:
				cancel(run.ErrRunCancelled)
				return
			case <-ticker.C:
				events, err := service.PollCancellationEvents(requestContext, tenantID, afterID, 100)
				if err != nil {
					continue
				}
				for _, event := range events {
					if event.ID > afterID {
						afterID = event.ID
					}
					if event.RunID == runID {
						cancel(run.ErrRunCancelled)
						return
					}
				}
			case <-stop:
				return
			case <-requestContext.Done():
				return
			}
		}
	}()
	return func() {
		once.Do(func() {
			unsubscribe()
			close(stop)
			cancel(nil)
		})
	}
}

// admitRunRequest 校验 Run Header、幂等键并占用一次并发准入。
func (h *Handler) admitRunRequest(w http.ResponseWriter, r *http.Request, body []byte) (requestID string, release func(), admitted bool) {
	runID := strings.TrimSpace(r.Header.Get("X-Limen-Run-ID"))
	if runID == "" {
		return "", func() {}, true
	}
	ctx, span := otel.Tracer("github.com/huz/limen/internal/httpapi").Start(r.Context(), "limen.run.admission")
	*r = *r.WithContext(ctx)
	defer func() {
		outcome := "rejected"
		if admitted {
			outcome = "admitted"
		} else {
			span.SetStatus(codes.Error, "")
		}
		if span.IsRecording() {
			if admitted {
				span.SetAttributes(attribute.String("limen.run.id", runID))
			}
			if requestID != "" {
				span.SetAttributes(attribute.String("limen.run.request_id", requestID))
			}
			span.SetAttributes(attribute.String("limen.admission.outcome", outcome))
		}
		span.End()
	}()
	if h.runs == nil {
		writeError(w, http.StatusServiceUnavailable, "run control is unavailable", "api_error", "run_unavailable")
		return "", nil, false
	}
	key, ok := idempotencyKey(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is required for a Run request", "invalid_request_error", "idempotency_key_required")
		return "", nil, false
	}
	tenantID := h.requestTenantID(r)
	hash, err := run.HashRequest(tenantID, r.URL.Path, key, body, map[string]string{"x-limen-run-id": runID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid idempotency request", "invalid_request_error", "invalid_idempotency_request")
		return "", nil, false
	}
	requestID, err = run.NewID("request")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to create request", "api_error", "request_id_error")
		return "", nil, false
	}
	now := time.Now().UTC()
	item, err := h.runs.AdmitRequest(r.Context(), tenantID, runID, run.AdmissionInput{Request: run.Request{ID: requestID, Endpoint: r.URL.Path, IdempotencyKey: key, RequestHash: hash}, Now: now, LeaseOwner: h.leaseOwner, LeaseTTL: run.RequestLeaseDuration})
	if err != nil {
		writeRunAdmissionError(w, err, item.ID)
		return "", nil, false
	}
	w.Header().Set("X-Limen-Request-ID", item.ID)
	release, ok = h.startRequestLease(w, r, tenantID, item.ID, now)
	if !ok {
		_, _ = h.runs.BeginSettlement(r.Context(), tenantID, item.ID, time.Now().UTC())
		_, _ = h.runs.SettleRequest(r.Context(), tenantID, item.ID, nil, time.Now().UTC())
		return "", nil, false
	}
	return item.ID, release, true
}

// newLeaseOwner 为当前进程生成不含业务正文的执行实例标识。
func newLeaseOwner() string {
	owner, err := run.NewID("worker")
	if err != nil {
		return "worker-local"
	}
	return owner
}

// startRequestLease 启动请求租约续期，并返回响应结束时的释放函数。
func (h *Handler) startRequestLease(w http.ResponseWriter, r *http.Request, tenantID, requestID string, now time.Time) (func(), bool) {
	lease, ok := h.runs.(run.LeaseService)
	if !ok {
		return func() {}, true
	}
	if _, err := lease.AcquireRequestLease(r.Context(), tenantID, requestID, h.leaseOwner, now, run.RequestLeaseDuration); err != nil {
		writeRunLeaseError(w, err)
		return nil, false
	}
	requestContext, cancel := context.WithCancel(r.Context())
	*r = *r.WithContext(requestContext)
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(run.RequestLeaseRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := lease.RenewRequestLease(requestContext, tenantID, requestID, h.leaseOwner, time.Now().UTC(), run.RequestLeaseDuration); err != nil {
					cancel()
					return
				}
			case <-stop:
				return
			case <-requestContext.Done():
				return
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(stop)
			cancel()
			releaseContext, releaseCancel := context.WithTimeout(context.Background(), time.Second)
			defer releaseCancel()
			_ = lease.ReleaseRequestLease(releaseContext, tenantID, requestID, h.leaseOwner, time.Now().UTC())
		})
	}, true
}

// writeRunLeaseError 将租约冲突映射为稳定的控制面错误。
func writeRunLeaseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, run.ErrLeaseUnavailable):
		writeError(w, http.StatusConflict, "request lease is held by another worker", "invalid_request_error", "request_in_progress")
	case errors.Is(err, run.ErrRequestAlreadyProcessed):
		writeError(w, http.StatusConflict, "request already processed", "invalid_request_error", "request_already_processed")
	default:
		writeError(w, http.StatusServiceUnavailable, "run store unavailable", "api_error", "run_store_error")
	}
}

// models 鉴权并返回当前可用的 OpenAI 兼容模型列表。
func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeInference) {
		return
	}
	if h.router == nil {
		writeError(w, http.StatusBadGateway, "provider unavailable", "api_error", "provider_unavailable")
		return
	}
	response := modelsResponse{Object: "list"}
	for _, model := range h.router.Models() {
		ownedBy := "limen"
		if model.Compatibility && len(model.Targets) > 0 {
			ownedBy = model.Targets[0].Provider
		}
		response.Data = append(response.Data, modelResponse{
			ID:      model.ID,
			Object:  "model",
			Created: 0,
			OwnedBy: ownedBy,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// metricsEndpoint 鉴权并返回低基数 Prometheus 指标。
func (h *Handler) metricsEndpoint(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateScopes(w, r, auth.ScopeAdmin) {
		return
	}
	h.metrics.ServeHTTP(w, r)
}

// authenticate 校验请求中的 Limen Bearer Key，并在失败时写入统一错误。
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) bool {
	return h.authenticateScopes(w, r)
}

// authenticateScopes 校验身份并确认请求拥有全部指定 Scope。
func (h *Handler) authenticateScopes(w http.ResponseWriter, r *http.Request, scopes ...auth.Scope) bool {
	if h.authenticator == nil {
		writeError(w, http.StatusServiceUnavailable, "authentication unavailable", "api_error", "authentication_unavailable")
		return false
	}
	principal, ok, err := h.authenticator.AuthenticateContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "authentication unavailable", "api_error", "authentication_unavailable")
		return false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid API key", "authentication_error", "invalid_api_key")
		return false
	}
	for _, scope := range scopes {
		if !principal.HasScope(scope) {
			writeError(w, http.StatusForbidden, "insufficient scope", "permission_error", "insufficient_scope")
			return false
		}
	}
	*r = *r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal))
	return true
}

// requestTenantID 返回鉴权 Principal 绑定的租户，兼容未注入身份的内部调用。
func (h *Handler) requestTenantID(r *http.Request) string {
	if principal, ok := r.Context().Value(principalContextKey{}).(auth.Principal); ok && principal.TenantID != "" {
		return principal.TenantID
	}
	return h.tenantID
}

// contextTenantID 返回结算上下文中的租户，缺少身份时回退到进程租户。
func (h *Handler) contextTenantID(ctx context.Context) string {
	if principal, ok := ctx.Value(principalContextKey{}).(auth.Principal); ok && principal.TenantID != "" {
		return principal.TenantID
	}
	return h.tenantID
}

// validBearerToken 使用统一鉴权实现校验 Limen API Key。
func validBearerToken(header, expected string) bool {
	_, ok := auth.NewStaticAuthenticator(expected, "", nil).Authenticate(header)
	return ok
}

type trackedAttempt struct {
	ID string
}

// finishAttemptReports 将 Router 的调用结果写回每个已持久化 Attempt。
func (h *Handler) finishAttemptReports(ctx context.Context, tenantID string, attempts []trackedAttempt, reports []gateway.AttemptReport) error {
	if h.runs == nil {
		return errors.New("run service unavailable")
	}
	for index, attempt := range attempts {
		state := run.AttemptAbandoned
		if index < len(reports) {
			if reports[index].ProviderRequestID != "" {
				if err := h.runs.UpdateAttemptProviderRequestID(ctx, tenantID, attempt.ID, reports[index].ProviderRequestID); err != nil {
					return err
				}
			}
			state = attemptState(reports[index])
		}
		if err := h.runs.FinishAttempt(ctx, tenantID, attempt.ID, state, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

// attemptState 将有限的路由结果映射为持久化 Attempt 状态。
func attemptState(report gateway.AttemptReport) run.AttemptState {
	switch report.Outcome {
	case "canceled", "timeout":
		return run.AttemptCancelled
	case "transport_error":
		return run.AttemptTransientFailed
	case "request_error", "internal_error":
		return run.AttemptDeterministicFail
	}
	if report.StatusCode >= http.StatusOK && report.StatusCode < http.StatusMultipleChoices {
		return run.AttemptSucceeded
	}
	if report.ErrorClass != "" {
		if report.ErrorClass == provider.ErrorClassRetryableTransient {
			return run.AttemptTransientFailed
		}
		return run.AttemptDeterministicFail
	}
	if provider.ClassifyHTTPStatus(report.StatusCode) == provider.ErrorClassRetryableTransient {
		return run.AttemptTransientFailed
	}
	return run.AttemptDeterministicFail
}

// forward 调用路由选中的 Provider，并转发普通内容或 SSE 数据。
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, request provider.ChatRequest, contract decision.Contract, runRequestID string) {
	tenantID := h.requestTenantID(r)
	settledRunRequest := false
	attempts := make([]trackedAttempt, 0)
	attemptReports := make([]gateway.AttemptReport, 0)
	attemptsFinished := false
	defer func() {
		settlementContext, cancelSettlement := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
		defer cancelSettlement()
		if runRequestID != "" && !attemptsFinished {
			_ = h.finishAttemptReports(settlementContext, tenantID, attempts, attemptReports)
		}
		if runRequestID != "" && !settledRunRequest {
			_ = h.settleRunRequest(settlementContext, runRequestID, nil)
		}
	}()
	var beforeAttempt gateway.AttemptStartHook
	if runRequestID != "" {
		beforeAttempt = func(target decision.PlanTarget) error {
			attemptID, err := run.NewID("attempt")
			if err != nil {
				return err
			}
			if err := h.runs.RecordAttemptStarted(r.Context(), tenantID, run.Attempt{ID: attemptID, RequestID: runRequestID, TargetID: target.Target.ID, Provider: target.Target.Provider, UpstreamModel: target.Target.UpstreamModel, State: run.AttemptStarted, StartedAt: time.Now().UTC()}); err != nil {
				return err
			}
			attempts = append(attempts, trackedAttempt{ID: attemptID})
			return nil
		}
	}
	decisionID := ""
	result, err := h.router.ChatWithContractHooks(r.Context(), request, contract, func(input decision.Input, plan decision.ExecutionPlan) error {
		id, recordErr := h.recordDecision(r.Context(), tenantID, input, plan)
		decisionID = id
		return recordErr
	}, beforeAttempt)
	attemptReports = result.Attempts
	metricModel := h.router.ObservableModelID(request.Model)
	for _, attempt := range result.Attempts {
		h.metrics.Inc(telemetry.AttemptsTotal, telemetry.Labels{
			Endpoint: "/v1/chat/completions",
			Status:   metricStatus(attempt.StatusCode),
			Model:    metricModel,
			Provider: attempt.Provider,
			Target:   attempt.TargetID,
			Result:   metricAttemptResult(attempt),
		})
	}
	if result.Plan.ConfigVersion != "" {
		w.Header().Set("X-Limen-Config-Version", result.Plan.ConfigVersion)
	}
	if decisionID != "" {
		w.Header().Set("X-Limen-Decision-ID", decisionID)
	}
	if err != nil {
		if errors.Is(err, errDecisionJournal) {
			writeError(w, http.StatusServiceUnavailable, "decision journal unavailable", "api_error", "decision_journal_unavailable")
			return
		}
		var unsupported *gateway.UnsupportedModelError
		if errors.As(err, &unsupported) {
			writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "unsupported_model")
			return
		}
		var unavailable *gateway.ProviderUnavailableError
		if errors.As(err, &unavailable) {
			writeRouteHeaders(w, unavailable.Decision)
			writeError(w, http.StatusBadGateway, err.Error(), "api_error", "provider_unavailable")
			return
		}
		var noTarget *gateway.NoAvailableTargetError
		if errors.As(err, &noTarget) {
			writeRouteHeaders(w, noTarget.Decision)
			writeError(w, http.StatusServiceUnavailable, err.Error(), "api_error", "no_available_target")
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
		status := http.StatusBadGateway
		code := "provider_error"
		if errors.Is(context.Cause(r.Context()), run.ErrRunCancelled) {
			status = http.StatusConflict
			code = "run_cancelled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
			code = "provider_timeout"
		} else if errors.Is(err, context.Canceled) {
			status = 499
			code = "client_canceled"
		}
		var routeError *gateway.RouteError
		if errors.As(err, &routeError) {
			writeRouteHeaders(w, routeError.Decision)
		}
		writeError(w, status, "provider request failed", "api_error", code)
		return
	}
	response := result.Response
	writeRouteHeaders(w, result.Decision)
	defer response.Body.Close()
	if response.ContentType != "" {
		w.Header().Set("Content-Type", response.ContentType)
	}
	declareSettlementTrailers(w)
	w.WriteHeader(response.StatusCode)
	if strings.HasPrefix(response.ContentType, "text/event-stream") {
		relayStream(w, response.Body)
	} else {
		_, _ = io.Copy(w, response.Body)
	}
	if runRequestID != "" {
		settlementContext, cancelSettlement := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
		defer cancelSettlement()
		if err := h.finishAttemptReports(settlementContext, tenantID, attempts, attemptReports); err != nil {
			writePendingSettlementTrailers(w)
			return
		}
		attemptsFinished = true
		settledRunRequest = true
		if err := h.settleRunRequest(settlementContext, runRequestID, result.Settlement); err != nil {
			writePendingSettlementTrailers(w)
			return
		}
	}
	writeSettlementTrailers(w, result.Settlement)
	settlementStatus := string(gateway.SettlementUnavailable)
	if result.Settlement != nil {
		settlementStatus = string(result.Settlement.Summary().Status)
	}
	h.metrics.Inc(telemetry.SettlementsTotal, telemetry.Labels{Endpoint: "/v1/chat/completions", Status: metricStatus(response.StatusCode), Model: metricModel, Provider: result.Decision.Provider, Result: settlementStatus})
}

// metricStatus 将 HTTP 状态归并为固定类别，避免状态码扩张指标序列。
func metricStatus(status int) string {
	if status <= 0 {
		return "error"
	}
	return strconv.Itoa(status/100) + "xx"
}

// metricAttemptResult 将调用结果归并为成功或稳定 Provider 错误类别。
func metricAttemptResult(attempt gateway.AttemptReport) string {
	if attempt.StatusCode >= http.StatusOK && attempt.StatusCode < http.StatusMultipleChoices {
		return "success"
	}
	if attempt.ErrorClass != "" {
		return string(attempt.ErrorClass)
	}
	if attempt.StatusCode > 0 {
		if class := provider.ClassifyHTTPStatus(attempt.StatusCode); class != "" {
			return string(class)
		}
		return "other_response"
	}
	switch attempt.Outcome {
	case "canceled", "timeout":
		return string(provider.ErrorClassCancelled)
	case "request_error":
		return string(provider.ErrorClassDeterministicRequest)
	case "transport_error":
		return string(provider.ErrorClassRetryableTransient)
	default:
		return string(provider.ErrorClassInternal)
	}
}

// returnRunDecisionError 将计划生成失败映射为安全 API 错误。
func returnRunDecisionError(w http.ResponseWriter, err error) {
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
}

// settleRunRequest 将响应结束后的成本提交到 Run Service。
func (h *Handler) settleRunRequest(ctx context.Context, requestID string, settlement *gateway.Settlement) (err error) {
	ctx, span := otel.Tracer("github.com/huz/limen/internal/httpapi").Start(ctx, "limen.settlement")
	defer func() {
		outcome := "complete"
		if err != nil {
			outcome = "pending"
			span.SetStatus(codes.Error, "")
		}
		if span.IsRecording() {
			span.SetAttributes(attribute.String("limen.settlement.outcome", outcome))
		}
		span.End()
	}()
	if h.runs == nil {
		return errors.New("run service unavailable")
	}
	tenantID := h.contextTenantID(ctx)
	var costNanoUSD *int64
	settlementStatus := string(gateway.SettlementUnavailable)
	if settlement != nil {
		summary := settlement.Summary()
		settlementStatus = string(summary.Status)
		if summary.CostAvailable {
			value := summary.CostNanoUSD
			costNanoUSD = &value
		}
	}
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("limen.run.request_id", requestID),
			attribute.Bool("limen.settlement.cost_known", costNanoUSD != nil),
			attribute.String("limen.settlement.status", settlementStatus),
		)
	}
	err = retrySettlement(ctx, func() error {
		if _, err := h.runs.BeginSettlement(ctx, tenantID, requestID, time.Now().UTC()); err != nil && !errors.Is(err, run.ErrRequestAlreadyProcessed) {
			return err
		}
		_, err := h.runs.SettleRequest(ctx, tenantID, requestID, costNanoUSD, time.Now().UTC())
		return err
	})
	if err != nil && retryableSettlementError(err) {
		if recovery, ok := h.runs.(run.SettlementRecoveryService); ok {
			queueContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			_ = recovery.QueueSettlement(queueContext, tenantID, requestID, costNanoUSD, time.Now().UTC().Add(100*time.Millisecond))
			cancel()
		}
	}
	return err
}

// retrySettlement 对暂时性持久化失败执行短暂、有界且幂等的重试。
func retrySettlement(ctx context.Context, operation func() error) error {
	return retrySettlementWithDelays(ctx, operation, []time.Duration{0, 100 * time.Millisecond, 500 * time.Millisecond})
}

// retrySettlementWithDelays 使用指定退避序列执行结算操作，便于测试恢复边界。
func retrySettlementWithDelays(ctx context.Context, operation func() error, delays []time.Duration) error {
	var lastErr error
	for _, delay := range delays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = operation()
		if lastErr == nil || !retryableSettlementError(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

// retryableSettlementError 判断哪些结算错误值得再次尝试。
func retryableSettlementError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch {
	case errors.Is(err, run.ErrAccountingSuspended), errors.Is(err, run.ErrRequestAlreadyProcessed), errors.Is(err, run.ErrRequestNotSettleable), errors.Is(err, run.ErrResourceNotFound):
		return false
	default:
		return true
	}
}

// writePendingSettlementTrailers 标记数据库未完成的异步结算。
func writePendingSettlementTrailers(w http.ResponseWriter) {
	w.Header().Set("X-Limen-Settlement-Status", "pending")
}

// writeRunAdmissionError 将 Run 准入错误映射为稳定 API 错误。
func writeRunAdmissionError(w http.ResponseWriter, err error, requestID string) {
	if requestID != "" {
		w.Header().Set("X-Limen-Request-ID", requestID)
	}
	switch {
	case errors.Is(err, run.ErrRequestInProgress):
		writeError(w, http.StatusConflict, "request is in progress", "invalid_request_error", "request_in_progress")
	case errors.Is(err, run.ErrRequestAlreadyProcessed):
		writeError(w, http.StatusConflict, "request already processed", "invalid_request_error", "request_already_processed")
	case errors.Is(err, run.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency key conflict", "invalid_request_error", "idempotency_conflict")
	case errors.Is(err, run.ErrRunDeadlineExceeded):
		writeError(w, http.StatusRequestTimeout, "Run deadline exceeded", "invalid_request_error", "run_deadline_exceeded")
	case errors.Is(err, run.ErrRunBudgetExhausted):
		writeError(w, http.StatusTooManyRequests, "Run soft budget exhausted", "invalid_request_error", "run_soft_budget_exhausted")
	case errors.Is(err, run.ErrRunConcurrencyExceeded):
		writeError(w, http.StatusTooManyRequests, "Run concurrency exceeded", "invalid_request_error", "run_concurrency_exceeded")
	case errors.Is(err, run.ErrAccountingSuspended):
		writeError(w, http.StatusConflict, "Run accounting is suspended", "invalid_request_error", "run_accounting_suspended")
	case errors.Is(err, run.ErrRunNotActive):
		writeError(w, http.StatusConflict, "Run is not active", "invalid_request_error", "run_not_active")
	case errors.Is(err, run.ErrResourceNotFound):
		writeError(w, http.StatusNotFound, "Run resource not found", "invalid_request_error", "run_resource_not_found")
	default:
		writeError(w, http.StatusBadGateway, "Run store unavailable", "api_error", "run_store_error")
	}
}

// declareSettlementTrailers 在响应开始前声明请求结束后可用的结算字段。
func declareSettlementTrailers(w http.ResponseWriter) {
	w.Header().Set("Trailer", strings.Join(settlementTrailerNames, ", "))
}

// writeSettlementTrailers 在响应体转发完成后发布结算快照。
func writeSettlementTrailers(w http.ResponseWriter, settlement *gateway.Settlement) {
	if settlement == nil {
		w.Header().Set("X-Limen-Settlement-Status", string(gateway.SettlementUnavailable))
		return
	}
	summary := settlement.Summary()
	w.Header().Set("X-Limen-Settlement-Status", string(summary.Status))
	if summary.Status == gateway.SettlementUnavailable {
		return
	}
	w.Header().Set("X-Limen-Input-Tokens", strconv.FormatInt(summary.InputTokens, 10))
	w.Header().Set("X-Limen-Output-Tokens", strconv.FormatInt(summary.OutputTokens, 10))
	w.Header().Set("X-Limen-Total-Tokens", strconv.FormatInt(summary.TotalTokens, 10))
	if summary.CostAvailable {
		w.Header().Set("X-Limen-Cost-USD", cost.FormatUSD(summary.CostNanoUSD))
	}
}

// writeRouteHeaders 暴露不含模型、密钥和正文的路由摘要。
func writeRouteHeaders(w http.ResponseWriter, decision gateway.Decision) {
	if decision.Provider != "" {
		w.Header().Set("X-Limen-Provider", decision.Provider)
	}
	if decision.Attempts > 0 {
		w.Header().Set("X-Limen-Attempts", strconv.Itoa(decision.Attempts))
	}
	if route := decision.String(); route != "" {
		w.Header().Set("X-Limen-Route", route)
	}
}

type incomingChatRequest struct {
	Model          string            `json:"model"`
	Messages       []incomingMessage `json:"messages"`
	MaxTokens      int               `json:"max_tokens"`
	Temperature    *float64          `json:"temperature"`
	Stream         bool              `json:"stream"`
	Tools          json.RawMessage   `json:"tools"`
	ToolChoice     json.RawMessage   `json:"tool_choice"`
	ResponseFormat json.RawMessage   `json:"response_format"`
	N              *int              `json:"n"`
	Logprobs       *bool             `json:"logprobs"`
	Limen          *incomingLimen    `json:"limen"`
}

type incomingLimen struct {
	RequiredCapabilities  []string `json:"required_capabilities"`
	MinimumQualityTier    int      `json:"minimum_quality_tier"`
	RequiredContextTokens int64    `json:"required_context_tokens"`
	DataClass             string   `json:"data_class"`
	EstimatedInputTokens  int64    `json:"estimated_input_tokens"`
	EstimatedOutputTokens int64    `json:"estimated_output_tokens"`
	Strategy              string   `json:"strategy"`
}

type incomingMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls"`
}

type modelsResponse struct {
	Object string          `json:"object"`
	Data   []modelResponse `json:"data"`
}

type modelResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// parseChatRequest 将 OpenAI 请求解析为内部统一请求，并拒绝暂不支持的内容。
func parseChatRequest(body []byte) (provider.ChatRequest, error) {
	envelope, err := parseChatRequestEnvelope(body)
	if err != nil {
		return provider.ChatRequest{}, err
	}
	return envelope.Request, nil
}

type parsedChatRequest struct {
	Request  provider.ChatRequest
	Contract decision.Contract
}

type unsupportedFieldError struct {
	Field string
}

// Error 返回明确指出暂不支持字段的请求错误。
func (err *unsupportedFieldError) Error() string {
	return err.Field + " is not supported"
}

// parseChatRequestEnvelope 严格解析请求并提取 Limen 能力契约。
func parseChatRequestEnvelope(body []byte) (parsedChatRequest, error) {
	var incoming incomingChatRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&incoming); err != nil {
		return parsedChatRequest{}, errors.New("invalid JSON request")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return parsedChatRequest{}, errors.New("invalid JSON request")
	}
	if strings.TrimSpace(incoming.Model) == "" {
		return parsedChatRequest{}, errors.New("model is required")
	}
	if len(incoming.Messages) == 0 {
		return parsedChatRequest{}, errors.New("messages are required")
	}
	if len(incoming.Tools) > 0 || len(incoming.ToolChoice) > 0 {
		return parsedChatRequest{}, &unsupportedFieldError{Field: "tools/tool_choice"}
	}
	if len(incoming.ResponseFormat) > 0 {
		return parsedChatRequest{}, &unsupportedFieldError{Field: "response_format"}
	}
	if incoming.N != nil {
		return parsedChatRequest{}, &unsupportedFieldError{Field: "n"}
	}
	if incoming.Logprobs != nil {
		return parsedChatRequest{}, &unsupportedFieldError{Field: "logprobs"}
	}
	request := provider.ChatRequest{Model: incoming.Model, MaxTokens: incoming.MaxTokens, Temperature: incoming.Temperature, Stream: incoming.Stream}
	for _, message := range incoming.Messages {
		if len(message.ToolCalls) > 0 {
			return parsedChatRequest{}, &unsupportedFieldError{Field: "messages.tool_calls"}
		}
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
			return parsedChatRequest{}, fmt.Errorf("unsupported message role: %s", message.Role)
		}
		var content string
		if err := json.Unmarshal(message.Content, &content); err != nil {
			return parsedChatRequest{}, errors.New("message content must be text")
		}
		request.Messages = append(request.Messages, provider.Message{Role: message.Role, Content: content})
	}
	contract := decision.Contract{Active: incoming.Model == "auto" || incoming.Limen != nil}
	if incoming.Limen != nil {
		contract.RequiredCapabilities = append([]string(nil), incoming.Limen.RequiredCapabilities...)
		contract.MinimumQualityTier = incoming.Limen.MinimumQualityTier
		contract.RequiredContextTokens = incoming.Limen.RequiredContextTokens
		contract.DataClass = incoming.Limen.DataClass
		contract.EstimatedInputTokens = incoming.Limen.EstimatedInputTokens
		contract.EstimatedOutputTokens = incoming.Limen.EstimatedOutputTokens
		contract.Strategy = incoming.Limen.Strategy
	}
	return parsedChatRequest{Request: request, Contract: contract}, nil
}

// relayStream 逐块转发 SSE 数据，并在每块写入后刷新客户端。
func relayStream(w http.ResponseWriter, source io.Reader) {
	flusher, canFlush := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		count, err := source.Read(buffer)
		if count > 0 {
			if _, writeErr := w.Write(buffer[:count]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
