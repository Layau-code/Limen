package run

import (
	"errors"
	"testing"
	"time"
)

func testRun() Run {
	return Run{
		ID:                "run-1",
		TenantID:          "tenant-1",
		State:             StateActive,
		SoftBudgetNanoUSD: 100,
		Deadline:          time.Unix(200, 0),
		MaxParallelism:    2,
		Strategy:          "balanced",
	}
}

func TestRunLifecycleCompletesAfterSettlement(t *testing.T) {
	run := testRun()
	if err := run.Admit(time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := run.Complete(); err != nil {
		t.Fatal(err)
	}
	if run.State != StateCompleting || run.InFlight != 1 {
		t.Fatalf("run after complete = %+v", run)
	}
	cost := int64(40)
	if err := run.Settle(&cost); err != nil {
		t.Fatal(err)
	}
	if run.State != StateCompleted || run.InFlight != 0 || run.SettledCostNanoUSD != 40 {
		t.Fatalf("run after settle = %+v", run)
	}
}

func TestRunAdmissionEnforcesStateDeadlineBudgetAndConcurrency(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Run)
		now   time.Time
		want  error
	}{
		{name: "not active", setup: func(run *Run) { run.State = StateCompleting }, want: ErrRunNotActive},
		{name: "deadline", now: time.Unix(200, 0), want: ErrRunDeadlineExceeded},
		{name: "budget", setup: func(run *Run) { run.SettledCostNanoUSD = 100 }, want: ErrRunBudgetExhausted},
		{name: "concurrency", setup: func(run *Run) { run.InFlight = 2 }, want: ErrRunConcurrencyExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := testRun()
			if test.setup != nil {
				test.setup(&run)
			}
			now := test.now
			if now.IsZero() {
				now = time.Unix(100, 0)
			}
			if err := run.Admit(now); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRunSoftBudgetDoesNotInterruptCurrentRequest(t *testing.T) {
	run := testRun()
	if err := run.Admit(time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	cost := int64(150)
	if err := run.Settle(&cost); err != nil {
		t.Fatal(err)
	}
	if run.State != StateActive || run.SettledCostNanoUSD != 150 {
		t.Fatalf("run after overspend = %+v", run)
	}
	if err := run.Admit(time.Unix(100, 0)); !errors.Is(err, ErrRunBudgetExhausted) || run.State != StateSoftBudgetExhausted {
		t.Fatalf("next admission error=%v state=%s", err, run.State)
	}
}

func TestRunSuspendsWhenSettlementIsUnknown(t *testing.T) {
	run := testRun()
	if err := run.Admit(time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := run.Settle(nil); !errors.Is(err, ErrAccountingSuspended) {
		t.Fatalf("error = %v", err)
	}
	if run.State != StateSuspendedAccounting || run.InFlight != 0 {
		t.Fatalf("run = %+v", run)
	}
	if err := run.Admit(time.Unix(100, 0)); !errors.Is(err, ErrAccountingSuspended) {
		t.Fatalf("admission error = %v", err)
	}
}

func TestUnknownSettlementDoesNotReopenTerminalRun(t *testing.T) {
	for _, state := range []RunState{StateCancelled, StateDeadlineExceeded, StateSoftBudgetExhausted} {
		t.Run(string(state), func(t *testing.T) {
			run := testRun()
			run.State = state
			run.InFlight = 1
			if err := run.Settle(nil); !errors.Is(err, ErrAccountingSuspended) {
				t.Fatalf("error = %v", err)
			}
			if run.State != state || run.InFlight != 0 {
				t.Fatalf("run = %+v", run)
			}
		})
	}
}

func TestRunRejectsRepeatedTerminalTransition(t *testing.T) {
	run := testRun()
	if err := run.Cancel(); err != nil {
		t.Fatal(err)
	}
	if err := run.Complete(); !errors.Is(err, ErrInvalidRunTransition) {
		t.Fatalf("error = %v", err)
	}
}
