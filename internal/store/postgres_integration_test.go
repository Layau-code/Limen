package store

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/journal"
	"github.com/huz/limen/internal/run"
	"github.com/lib/pq"
)

var (
	integrationSetupOnce sync.Once
	integrationAdminDB   *sql.DB
	integrationAppDB     *sql.DB
	integrationSetupErr  error
	integrationSequence  atomic.Uint64
)

// TestPostgresIntegrationRLSUsesDatabasePolicy 验证受限角色无法绕过租户行策略。
func TestPostgresIntegrationRLSUsesDatabasePolicy(t *testing.T) {
	adminDB, appDB, _ := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantA, tenantB := integrationID("tenant-a"), integrationID("tenant-b")
	ensureIntegrationTenant(t, adminDB, tenantA)
	ensureIntegrationTenant(t, adminDB, tenantB)

	store := NewPostgresStore(appDB)
	runA := integrationRun(integrationID("run-a"), 1)
	runB := integrationRun(integrationID("run-b"), 1)
	if err := store.CreateRun(ctx, tenantA, runA); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(ctx, tenantB, runB); err != nil {
		t.Fatal(err)
	}

	tx, err := appDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := setTenantTx(ctx, tx, tenantA); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE id=$1`, runB.ID).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Fatalf("cross-tenant rows visible = %d", visible)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET strategy='economy' WHERE id=$1`, runB.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	if updated != 0 {
		t.Fatalf("cross-tenant rows updated = %d", updated)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	stored, err := store.GetRun(ctx, tenantB, runB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Strategy != "balanced" {
		t.Fatalf("cross-tenant update changed strategy = %q", stored.Strategy)
	}
}

// TestPostgresIntegrationDecisionJournalRoundTripsPricing 验证带价格的决策快照可从 JSONB 还原并 Replay。
func TestPostgresIntegrationDecisionJournalRoundTripsPricing(t *testing.T) {
	adminDB, appDB, _ := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantID := integrationID("tenant-journal-pricing")
	ensureIntegrationTenant(t, adminDB, tenantID)
	pricing := &cost.Pricing{InputPerMillionNanoUSD: 250_000_001, OutputPerMillionNanoUSD: 2_000_000_009}
	input := decision.Input{
		SchemaVersion: decision.SchemaVersionV1, AlgorithmVersion: decision.AlgorithmVersionV1,
		ConfigVersion: "journal-config-v1", EvaluatedAtUnixMS: 1,
		Request: decision.Request{Model: "auto", Contract: decision.Contract{Active: true, Strategy: decision.StrategyBalanced}},
		Candidates: []decision.Candidate{{ModelID: "model", Enabled: true, SecurityAllowed: true, Target: catalog.Target{
			ID: "target", Provider: "openai", UpstreamModel: "gpt-test", Capabilities: []string{"text"}, SupportsStreaming: true,
			QualityTier: 1, CostTier: 1, ContextWindow: 1024, DataClasses: []string{"public"}, Pricing: pricing,
		}}},
	}
	plan, err := decision.NewEngine().Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	store := NewDecisionJournal(appDB)
	record := journal.Record{ID: integrationID("decision-pricing"), TenantID: tenantID, Input: input, Plan: plan, CreatedAt: time.Now().UTC()}
	if err := store.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, tenantID, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Input, record.Input) || !reflect.DeepEqual(got.Plan, record.Plan) {
		t.Fatalf("journal round trip changed decision: got=%+v want=%+v", got, record)
	}
}

// TestPostgresIntegrationConfigPublishNotifiesMetadata 验证配置发布只广播租户和版本哈希。
func TestPostgresIntegrationConfigPublishNotifiesMetadata(t *testing.T) {
	adminDB, appDB, appURL := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantID := integrationID("tenant-config-notify")
	ensureIntegrationTenant(t, adminDB, tenantID)
	listener := pq.NewListener(appURL, 100*time.Millisecond, time.Second, nil)
	defer listener.Close()
	if err := listener.Listen(configChangeChannel); err != nil {
		t.Fatal(err)
	}
	configs := NewPostgresConfigStore(appDB)
	record, err := configs.Create(ctx, tenantID, []byte(`{"models":[{"id":"notify-model","targets":[{"id":"notify-target","provider":"openai","upstream_model":"gpt-secret-upstream"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	mutation := configstore.Mutation{Key: "publish-config-1", Hash: "sha256:publish-config"}
	if _, err := configs.PublishWithMutation(ctx, tenantID, record.Version, mutation); err != nil {
		t.Fatal(err)
	}
	repeated, err := configs.PublishWithMutation(ctx, tenantID, record.Version, mutation)
	if err != nil || repeated.Version != record.Version {
		t.Fatalf("repeated config publish = %+v, err=%v", repeated, err)
	}
	if _, err := configs.PublishWithMutation(ctx, tenantID, record.Version, configstore.Mutation{Key: mutation.Key, Hash: "sha256:other"}); !errors.Is(err, configstore.ErrIdempotencyConflict) {
		t.Fatalf("config publish conflict = %v", err)
	}
	select {
	case notification := <-listener.Notify:
		change, err := parseConfigChange(notification.Extra)
		if err != nil {
			t.Fatal(err)
		}
		if change.TenantID != tenantID || change.Version != record.Version || strings.Contains(notification.Extra, "gpt-secret-upstream") {
			t.Fatalf("config notification=%q change=%+v", notification.Extra, change)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("config notification timed out")
	}
}

// TestPostgresIntegrationConcurrentAdmissionBound 验证数据库行锁严格限制 Run 并发名额。
func TestPostgresIntegrationConcurrentAdmissionBound(t *testing.T) {
	adminDB, appDB, _ := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantID := integrationID("tenant-admission")
	ensureIntegrationTenant(t, adminDB, tenantID)
	store := NewPostgresStore(appDB)
	item := integrationRun(integrationID("run-admission"), 8)
	if err := store.CreateRun(ctx, tenantID, item); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsFound := make(chan error, 100)
	var admitted atomic.Int64
	var rejected atomic.Int64
	var group sync.WaitGroup
	for index := range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			key := fmt.Sprintf("request-%d", index)
			_, err := store.AdmitRequest(ctx, tenantID, item.ID, AdmissionInput{
				Request: run.Request{ID: integrationID(key), Endpoint: "/v1/chat/completions", IdempotencyKey: key, RequestHash: "hash-" + key},
				Now:     time.Now().UTC(),
			})
			switch {
			case err == nil:
				admitted.Add(1)
			case errors.Is(err, run.ErrRunConcurrencyExceeded):
				rejected.Add(1)
			default:
				errorsFound <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("unexpected admission error: %v", err)
	}
	if admitted.Load() != 8 || rejected.Load() != 92 {
		t.Fatalf("admitted=%d rejected=%d", admitted.Load(), rejected.Load())
	}
	stored, err := store.GetRun(ctx, tenantID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.InFlight != 8 {
		t.Fatalf("in_flight = %d", stored.InFlight)
	}
}

// TestPostgresIntegrationConcurrentIdempotency 验证并发重试只创建一个 Request。
func TestPostgresIntegrationConcurrentIdempotency(t *testing.T) {
	adminDB, appDB, _ := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantID := integrationID("tenant-idempotency")
	ensureIntegrationTenant(t, adminDB, tenantID)
	store := NewPostgresStore(appDB)
	item := integrationRun(integrationID("run-idempotency"), 100)
	if err := store.CreateRun(ctx, tenantID, item); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsFound := make(chan error, 100)
	var admitted atomic.Int64
	var duplicate atomic.Int64
	var group sync.WaitGroup
	for index := range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := store.AdmitRequest(ctx, tenantID, item.ID, AdmissionInput{
				Request: run.Request{ID: integrationID(fmt.Sprintf("duplicate-%d", index)), Endpoint: "/v1/chat/completions", IdempotencyKey: "same-key", RequestHash: "same-hash"},
				Now:     time.Now().UTC(),
			})
			switch {
			case err == nil:
				admitted.Add(1)
			case errors.Is(err, run.ErrRequestInProgress):
				duplicate.Add(1)
			default:
				errorsFound <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("unexpected idempotency error: %v", err)
	}
	if admitted.Load() != 1 || duplicate.Load() != 99 {
		t.Fatalf("admitted=%d duplicate=%d", admitted.Load(), duplicate.Load())
	}
	if count := tenantRowCount(t, appDB, tenantID, `SELECT count(*) FROM run_requests WHERE endpoint=$1 AND idempotency_key=$2`, "/v1/chat/completions", "same-key"); count != 1 {
		t.Fatalf("request rows = %d", count)
	}
	_, err := store.AdmitRequest(ctx, tenantID, item.ID, AdmissionInput{
		Request: run.Request{ID: integrationID("conflict"), Endpoint: "/v1/chat/completions", IdempotencyKey: "same-key", RequestHash: "different-hash"},
		Now:     time.Now().UTC(),
	})
	if !errors.Is(err, run.ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

// TestPostgresIntegrationConcurrentSettlement 验证同一费用只能进入账本一次。
func TestPostgresIntegrationConcurrentSettlement(t *testing.T) {
	adminDB, appDB, _ := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantID := integrationID("tenant-settlement")
	ensureIntegrationTenant(t, adminDB, tenantID)
	store := NewPostgresStore(appDB)
	item := integrationRun(integrationID("run-settlement"), 1)
	if err := store.CreateRun(ctx, tenantID, item); err != nil {
		t.Fatal(err)
	}
	request, err := store.AdmitRequest(ctx, tenantID, item.ID, AdmissionInput{
		Request: run.Request{ID: integrationID("request-settlement"), Endpoint: "/v1/chat/completions", IdempotencyKey: "settlement-key", RequestHash: "settlement-hash"},
		Now:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSettlement(ctx, tenantID, request.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	cost := int64(125_000)
	start := make(chan struct{})
	errorsFound := make(chan error, 100)
	var group sync.WaitGroup
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if _, err := store.SettleRequest(ctx, tenantID, request.ID, &cost, time.Now().UTC()); err != nil {
				errorsFound <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("unexpected settlement error: %v", err)
	}
	if count := tenantRowCount(t, appDB, tenantID, `SELECT count(*) FROM ledger_entries WHERE request_id=$1`, request.ID); count != 1 {
		t.Fatalf("ledger rows = %d", count)
	}
	stored, err := store.GetRun(ctx, tenantID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SettledCostNanoUSD != cost || stored.InFlight != 0 {
		t.Fatalf("settled_cost=%d in_flight=%d", stored.SettledCostNanoUSD, stored.InFlight)
	}
}

// TestPostgresIntegrationLeaseRecoveryCompetition 验证多个实例不会重复恢复同一过期请求。
func TestPostgresIntegrationLeaseRecoveryCompetition(t *testing.T) {
	adminDB, appDB, appURL := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantID := integrationID("tenant-recovery")
	ensureIntegrationTenant(t, adminDB, tenantID)
	first := NewPostgresStore(appDB)
	secondDB, err := OpenPostgres(appURL, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	second := NewPostgresStore(secondDB)
	item := integrationRun(integrationID("run-recovery"), 1)
	if err := first.CreateRun(ctx, tenantID, item); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Minute)
	request, err := first.AdmitRequest(ctx, tenantID, item.ID, AdmissionInput{
		Request:    run.Request{ID: integrationID("request-recovery"), Endpoint: "/v1/chat/completions", IdempotencyKey: "recovery-key", RequestHash: "recovery-hash"},
		Now:        base,
		LeaseOwner: "instance-a",
		LeaseTTL:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	counts := make(chan int, 2)
	errorsFound := make(chan error, 2)
	var group sync.WaitGroup
	for _, current := range []*PostgresStore{first, second} {
		group.Add(1)
		go func(candidate *PostgresStore) {
			defer group.Done()
			<-start
			recovered, err := candidate.RecoverExpiredRequests(ctx, tenantID, time.Now().UTC(), 10)
			if err != nil {
				errorsFound <- err
				return
			}
			counts <- len(recovered)
		}(current)
	}
	close(start)
	group.Wait()
	close(counts)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	total := 0
	for count := range counts {
		total += count
	}
	if total != 1 {
		t.Fatalf("recovered requests = %d", total)
	}
	storedRequest, err := first.GetRequest(ctx, tenantID, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedRun, err := first.GetRun(ctx, tenantID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRequest.State != run.RequestAbandoned || storedRequest.SettlementStatus != "pending" {
		t.Fatalf("request state=%s settlement=%s", storedRequest.State, storedRequest.SettlementStatus)
	}
	if storedRun.State != run.StateSuspendedAccounting || storedRun.InFlight != 0 {
		t.Fatalf("run state=%s in_flight=%d", storedRun.State, storedRun.InFlight)
	}
}

// TestPostgresIntegrationCrashRecoveryAbandonsAttempt 验证崩溃实例遗留的调用证据会进入明确终态。
func TestPostgresIntegrationCrashRecoveryAbandonsAttempt(t *testing.T) {
	adminDB, appDB, appURL := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantID := integrationID("tenant-crash")
	runID := integrationID("run-crash")
	requestID := integrationID("request-crash")
	attemptID := integrationID("attempt-crash")
	ensureIntegrationTenant(t, adminDB, tenantID)
	store := NewPostgresStore(appDB)
	if err := store.CreateRun(ctx, tenantID, integrationRun(runID, 1)); err != nil {
		t.Fatal(err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestPostgresIntegrationCrashHelper$")
	command.Env = append(os.Environ(),
		"LIMEN_FAULT_HELPER=crash",
		"LIMEN_FAULT_TENANT_ID="+tenantID,
		"LIMEN_FAULT_RUN_ID="+runID,
		"LIMEN_FAULT_REQUEST_ID="+requestID,
		"LIMEN_FAULT_ATTEMPT_ID="+attemptID,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	t.Cleanup(func() {
		if !killed && command.Process != nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
			return
		}
		ready <- ""
	}()
	select {
	case marker := <-ready:
		if marker != "READY" {
			t.Fatalf("crash helper marker=%q stderr=%s", marker, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("crash helper did not become ready: %s", stderr.String())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	killed = true
	if err := command.Wait(); err == nil {
		t.Fatal("crash helper exited without forced termination")
	}

	request, err := store.GetRequest(ctx, tenantID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(request.LeaseExpiresAt.Add(20 * time.Millisecond)); wait > 0 {
		timer := time.NewTimer(wait)
		<-timer.C
	}
	secondDB, err := OpenPostgres(appURL, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	second := NewPostgresStore(secondDB)
	recovered, err := second.RecoverExpiredRequests(ctx, tenantID, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].ID != requestID {
		t.Fatalf("recovered requests = %+v", recovered)
	}
	if state := tenantAttemptState(t, appDB, tenantID, attemptID); state != run.AttemptAbandoned {
		t.Fatalf("attempt state = %s", state)
	}
	if count := tenantRowCount(t, appDB, tenantID, `SELECT count(*) FROM ledger_entries WHERE request_id=$1`, requestID); count != 0 {
		t.Fatalf("ledger rows = %d", count)
	}
	storedRun, err := store.GetRun(ctx, tenantID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRun.State != run.StateSuspendedAccounting || storedRun.InFlight != 0 {
		t.Fatalf("run state=%s in_flight=%d", storedRun.State, storedRun.InFlight)
	}
}

// TestPostgresIntegrationCrashHelper 模拟持久化调用证据后仍在执行的独立实例。
func TestPostgresIntegrationCrashHelper(t *testing.T) {
	if os.Getenv("LIMEN_FAULT_HELPER") != "crash" {
		return
	}
	db, err := OpenPostgres(os.Getenv("LIMEN_TEST_DATABASE_URL"), 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db)
	now := time.Now().UTC()
	tenantID := os.Getenv("LIMEN_FAULT_TENANT_ID")
	requestID := os.Getenv("LIMEN_FAULT_REQUEST_ID")
	request, err := store.AdmitRequest(context.Background(), tenantID, os.Getenv("LIMEN_FAULT_RUN_ID"), AdmissionInput{
		Request:    run.Request{ID: requestID, Endpoint: "/v1/chat/completions", IdempotencyKey: "crash-key", RequestHash: "crash-hash"},
		Now:        now,
		LeaseOwner: "crash-helper",
		LeaseTTL:   500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAttemptStarted(context.Background(), tenantID, run.Attempt{
		ID:            os.Getenv("LIMEN_FAULT_ATTEMPT_ID"),
		RequestID:     request.ID,
		TargetID:      "primary",
		Provider:      "openai",
		UpstreamModel: "gpt-test",
		StartedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "READY"); err != nil {
		t.Fatal(err)
	}
	if err := os.Stdout.Sync(); err != nil {
		t.Fatal(err)
	}
	select {}
}

// TestPostgresIntegrationSettlementSurvivesDatabasePause 验证短暂断连不会丢失已持久化结算任务。
func TestPostgresIntegrationSettlementSurvivesDatabasePause(t *testing.T) {
	containerName := os.Getenv("LIMEN_TEST_DATABASE_CONTAINER")
	if containerName == "" {
		t.Skip("the external PostgreSQL lifecycle is not controlled by this test")
	}
	adminDB, appDB, _ := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantID := integrationID("tenant-db-pause")
	ensureIntegrationTenant(t, adminDB, tenantID)
	store := NewPostgresStore(appDB)
	item := integrationRun(integrationID("run-db-pause"), 1)
	if err := store.CreateRun(ctx, tenantID, item); err != nil {
		t.Fatal(err)
	}
	request, err := store.AdmitRequest(ctx, tenantID, item.ID, AdmissionInput{
		Request: run.Request{ID: integrationID("request-db-pause"), Endpoint: "/v1/chat/completions", IdempotencyKey: "db-pause-key", RequestHash: "db-pause-hash"},
		Now:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cost := int64(250_000)
	if err := store.QueueSettlement(ctx, tenantID, request.ID, &cost, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("docker", "pause", containerName).CombinedOutput(); err != nil {
		t.Fatalf("pause PostgreSQL: %v: %s", err, output)
	}
	paused := true
	t.Cleanup(func() {
		if paused {
			_ = exec.Command("docker", "unpause", containerName).Run()
		}
	})

	outageContext, cancelOutage := context.WithTimeout(ctx, 300*time.Millisecond)
	processed, outageErr := run.ProcessSettlementJobs(outageContext, store, tenantID, "worker-during-outage", time.Now().UTC(), 10)
	cancelOutage()
	if outageErr == nil || processed != 0 {
		t.Fatalf("outage processed=%d error=%v", processed, outageErr)
	}
	if output, err := exec.Command("docker", "unpause", containerName).CombinedOutput(); err != nil {
		t.Fatalf("unpause PostgreSQL: %v: %s", err, output)
	}
	paused = false
	waitForIntegrationDatabase(t, appDB, 5*time.Second)

	processed, err = run.ProcessSettlementJobs(ctx, store, tenantID, "worker-after-recovery", time.Now().UTC().Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("recovered settlements = %d", processed)
	}
	if count := tenantRowCount(t, appDB, tenantID, `SELECT count(*) FROM ledger_entries WHERE request_id=$1`, request.ID); count != 1 {
		t.Fatalf("ledger rows = %d", count)
	}
	storedRun, err := store.GetRun(ctx, tenantID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRun.SettledCostNanoUSD != cost || storedRun.InFlight != 0 {
		t.Fatalf("settled_cost=%d in_flight=%d", storedRun.SettledCostNanoUSD, storedRun.InFlight)
	}
}

// TestPostgresIntegrationCancellationNotificationAndPolling 验证通知和事件表形成快慢两条取消路径。
func TestPostgresIntegrationCancellationNotificationAndPolling(t *testing.T) {
	adminDB, appDB, appURL := postgresIntegrationDatabases(t)
	ctx := context.Background()
	tenantID := integrationID("tenant-cancel")
	ensureIntegrationTenant(t, adminDB, tenantID)
	store := NewPostgresStore(appDB)
	item := integrationRun(integrationID("run-cancel"), 1)
	if err := store.CreateRun(ctx, tenantID, item); err != nil {
		t.Fatal(err)
	}

	listener := pq.NewListener(appURL, 100*time.Millisecond, time.Second, nil)
	defer listener.Close()
	if err := listener.Listen(cancellationEventChannel); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelRunWithMutation(ctx, tenantID, item.ID, run.Mutation{Key: "cancel-key", Hash: "cancel-hash"}); err != nil {
		t.Fatal(err)
	}
	select {
	case notification := <-listener.Notify:
		if notification == nil {
			t.Fatal("received empty cancellation notification")
		}
		event, err := parseCancellationEvent(notification.Extra)
		if err != nil {
			t.Fatal(err)
		}
		if event.TenantID != tenantID || event.RunID != item.ID {
			t.Fatalf("notification = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation notification timed out")
	}
	events, err := store.PollCancellationEvents(ctx, tenantID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].RunID != item.ID {
		t.Fatalf("polled events = %+v", events)
	}
}

// postgresIntegrationDatabases 初始化一次迁移，并返回管理员与受限应用连接。
func postgresIntegrationDatabases(t *testing.T) (*sql.DB, *sql.DB, string) {
	t.Helper()
	adminURL := os.Getenv("LIMEN_TEST_DATABASE_ADMIN_URL")
	appURL := os.Getenv("LIMEN_TEST_DATABASE_URL")
	role := os.Getenv("LIMEN_TEST_DATABASE_ROLE")
	if adminURL == "" || appURL == "" || role == "" {
		t.Skip("PostgreSQL integration environment is not configured")
	}
	integrationSetupOnce.Do(func() {
		if !regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`).MatchString(role) {
			integrationSetupErr = fmt.Errorf("invalid integration role %q", role)
			return
		}
		integrationAdminDB, integrationSetupErr = OpenPostgres(adminURL, 2*time.Second)
		if integrationSetupErr != nil {
			return
		}
		if integrationSetupErr = integrationAdminDB.Ping(); integrationSetupErr != nil {
			return
		}
		integrationAdminDB.SetMaxOpenConns(4)
		if integrationSetupErr = ApplyMigrations(context.Background(), integrationAdminDB); integrationSetupErr != nil {
			return
		}
		quotedRole := pq.QuoteIdentifier(role)
		for _, statement := range []string{
			"GRANT USAGE ON SCHEMA public TO " + quotedRole,
			"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO " + quotedRole,
			"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO " + quotedRole,
		} {
			if _, integrationSetupErr = integrationAdminDB.Exec(statement); integrationSetupErr != nil {
				return
			}
		}
		integrationAppDB, integrationSetupErr = OpenPostgres(appURL, 500*time.Millisecond)
		if integrationSetupErr != nil {
			return
		}
		integrationAppDB.SetMaxOpenConns(24)
		integrationSetupErr = integrationAppDB.Ping()
	})
	if integrationSetupErr != nil {
		t.Fatal(integrationSetupErr)
	}
	return integrationAdminDB, integrationAppDB, appURL
}

// ensureIntegrationTenant 创建测试专用租户。
func ensureIntegrationTenant(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	if err := EnsureTenant(context.Background(), db, tenantID); err != nil {
		t.Fatal(err)
	}
}

// integrationRun 创建只含治理测试所需字段的 Run。
func integrationRun(id string, maxParallelism int) run.Run {
	now := time.Now().UTC()
	return run.Run{
		ID:                id,
		State:             run.StateActive,
		SoftBudgetNanoUSD: 10_000_000_000,
		MaxParallelism:    maxParallelism,
		Strategy:          "balanced",
		ConfigVersion:     "integration-v1",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

// integrationID 生成不会在共享测试数据库中冲突的标识。
func integrationID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), integrationSequence.Add(1))
}

// tenantRowCount 在真实 RLS 会话中统计指定租户的数据行。
func tenantRowCount(t *testing.T, db *sql.DB, tenantID, query string, args ...any) int {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := setTenantTx(context.Background(), tx, tenantID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return count
}

// tenantAttemptState 在租户会话内读取一次 Attempt 的状态。
func tenantAttemptState(t *testing.T, db *sql.DB, tenantID, attemptID string) run.AttemptState {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := setTenantTx(context.Background(), tx, tenantID); err != nil {
		t.Fatal(err)
	}
	var state run.AttemptState
	if err := tx.QueryRowContext(context.Background(), `SELECT state FROM attempts WHERE id=$1`, attemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return state
}

// waitForIntegrationDatabase 等待暂停后的测试数据库重新接受查询。
func waitForIntegrationDatabase(t *testing.T, db *sql.DB, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		pingContext, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		err := db.PingContext(pingContext)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("database did not recover: %v", err)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		<-timer.C
	}
}
