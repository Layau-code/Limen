package semantic

import (
	"context"
	"sync"
	"time"

	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/decision"
)

// ShadowEvaluation contains only finite classification metadata and counterfactual routing output.
type ShadowEvaluation struct {
	TenantID                string
	DecisionID              string
	StateHash               string
	StateLength             int
	Truncated               bool
	Mode                    string
	Status                  string
	Reason                  string
	ModelVersion            string
	TemplateVersion         string
	MappingVersion          string
	Language                string
	TaskType                string
	TaskProbabilities       map[string]float64
	TaskConfidence          float64
	Complexity              string
	ComplexityProbabilities map[string]float64
	ComplexityConfidence    float64
	InputTokens             int64
	OutputTokens            int64
	CostNanoUSD             int64
	CostKnown               bool
	Duration                time.Duration
	CounterfactualTargets   []string
	CounterfactualPlanHash  string
	CreatedAt               time.Time
}

// ShadowStore persists shadow-only evaluation metadata.
type ShadowStore interface {
	SaveShadowEvaluation(context.Context, ShadowEvaluation) error
}

// ShadowJob carries request text only in bounded process memory and is never serialized.
type ShadowJob struct {
	TenantID   string
	DecisionID string
	State      State
	Config     config.SemanticRouting
	Input      decision.Input
}

// ShadowWorker drops work when its bounded in-memory queue is full.
type ShadowWorker struct {
	queue    chan ShadowJob
	assessor Assessor
	store    ShadowStore
	planner  func(decision.Input) (decision.ExecutionPlan, error)
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.RWMutex
	closed   bool
	observer func(ShadowEvaluation)
}

// MemoryShadowStore keeps prompt-free shadow records for single-process deployments.
type MemoryShadowStore struct {
	mu      sync.Mutex
	records []ShadowEvaluation
}

// NewMemoryShadowStore 创建进程内的影子结果存储。
func NewMemoryShadowStore() *MemoryShadowStore { return &MemoryShadowStore{} }

// SaveShadowEvaluation 追加不含原始文本的影子结果。
func (store *MemoryShadowStore) SaveShadowEvaluation(ctx context.Context, evaluation ShadowEvaluation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	evaluation.CounterfactualTargets = append([]string(nil), evaluation.CounterfactualTargets...)
	evaluation.TaskProbabilities = cloneProbabilities(evaluation.TaskProbabilities)
	evaluation.ComplexityProbabilities = cloneProbabilities(evaluation.ComplexityProbabilities)
	store.records = append(store.records, evaluation)
	return nil
}

// NewShadowWorker 启动具有有界队列的后台工作协程。
func NewShadowWorker(assessor Assessor, store ShadowStore, planner func(decision.Input) (decision.ExecutionPlan, error), workers, queueSize int) *ShadowWorker {
	if workers < 1 {
		workers = 1
	}
	if queueSize < 1 {
		queueSize = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	worker := &ShadowWorker{queue: make(chan ShadowJob, queueSize), assessor: assessor, store: store, planner: planner, ctx: ctx, cancel: cancel}
	for i := 0; i < workers; i++ {
		worker.wg.Add(1)
		go worker.run()
	}
	return worker
}

// Submit 非阻塞地提交一项影子评估。
func (worker *ShadowWorker) Submit(job ShadowJob) bool {
	if worker == nil {
		return false
	}
	worker.mu.RLock()
	defer worker.mu.RUnlock()
	if worker.closed {
		return false
	}
	select {
	case worker.queue <- job:
		return true
	default:
		return false
	}
}

// Close 停止后台工作并清除剩余的内存任务。
func (worker *ShadowWorker) Close() {
	if worker == nil {
		return
	}
	worker.mu.Lock()
	if !worker.closed {
		worker.closed = true
		worker.cancel()
	}
	worker.mu.Unlock()
	worker.wg.Wait()
	for {
		select {
		case job := <-worker.queue:
			job.State.Text = ""
		default:
			return
		}
	}
}

// SetObserver 设置影子任务完成后的低基数指标回调。
func (worker *ShadowWorker) SetObserver(observer func(ShadowEvaluation)) {
	if worker == nil {
		return
	}
	worker.mu.Lock()
	worker.observer = observer
	worker.mu.Unlock()
}

// run 从有界队列读取任务并由工作协程处理。
func (worker *ShadowWorker) run() {
	defer worker.wg.Done()
	for {
		select {
		case job := <-worker.queue:
			if worker.ctx.Err() != nil {
				return
			}
			worker.evaluate(job)
		case <-worker.ctx.Done():
			return
		}
	}
}

// evaluate 执行影子评估、反事实路由并仅保存元数据。
func (worker *ShadowWorker) evaluate(job ShadowJob) {
	defer func() { job.State.Text = "" }()
	created := time.Now().UTC()
	evaluation := ShadowEvaluation{TenantID: job.TenantID, DecisionID: job.DecisionID, StateHash: job.State.Hash, StateLength: job.State.Length, Truncated: job.State.Truncated, Mode: "shadow", Status: "provider_error", ModelVersion: job.Config.ModelVersion, TemplateVersion: job.Config.QuestionTemplateVersion, MappingVersion: job.Config.MappingVersion, Language: job.State.Language, CreatedAt: created}
	worker.mu.RLock()
	observer := worker.observer
	worker.mu.RUnlock()
	defer func() {
		if observer != nil {
			observer(evaluation)
		}
	}()
	if worker.assessor == nil || worker.store == nil {
		return
	}
	timeout := job.Config.Timeout
	if timeout <= 0 {
		timeout = 200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(worker.ctx, timeout)
	defer cancel()
	result, err := worker.assessor.Assess(ctx, job.TenantID, job.State, job.Config.ModelVersion, job.Config.QuestionTemplateVersion)
	if err != nil {
		evaluation.Status = StatusForError(err)
		evaluation.Reason = evaluation.Status
		worker.persist(&evaluation)
		return
	}
	assessment := Map(job.Config, job.State, result)
	assessment.Mode = "active"
	evaluation.Status, evaluation.Reason = assessment.Status, assessment.Reason
	evaluation.ModelVersion = result.ModelVersion
	evaluation.TaskType, evaluation.TaskProbabilities, evaluation.TaskConfidence = result.TaskType, result.TaskProbabilities, result.TaskConfidence
	evaluation.Complexity, evaluation.ComplexityProbabilities, evaluation.ComplexityConfidence = result.Complexity, result.ComplexityProbabilities, result.ComplexityConfidence
	evaluation.InputTokens, evaluation.OutputTokens, evaluation.Duration = result.InputTokens, result.OutputTokens, result.Duration
	evaluation.CostNanoUSD, evaluation.CostKnown = CostNanoUSD(job.Config, result)
	if assessment.Status == "assessed" && worker.planner != nil {
		input := job.Input
		input.SchemaVersion, input.AlgorithmVersion = decision.SchemaVersionV2, decision.AlgorithmVersionV3
		input.SemanticAssessment = assessment
		plan, planErr := worker.planner(input)
		if planErr == nil {
			evaluation.CounterfactualPlanHash = plan.PlanHash
			for _, target := range plan.Targets {
				evaluation.CounterfactualTargets = append(evaluation.CounterfactualTargets, target.ModelID+":"+target.Target.ID)
			}
		} else {
			evaluation.Status = "counterfactual_error"
			evaluation.Reason = evaluation.Status
		}
	}
	worker.persist(&evaluation)
}

// persist 将持久化失败计入缺样原因，避免把未保存的影子结果报告为成功。
func (worker *ShadowWorker) persist(evaluation *ShadowEvaluation) {
	storeCtx, storeCancel := context.WithTimeout(context.WithoutCancel(worker.ctx), time.Second)
	defer storeCancel()
	if err := worker.store.SaveShadowEvaluation(storeCtx, *evaluation); err != nil {
		evaluation.Status, evaluation.Reason = "storage_error", "storage_error"
	}
}
