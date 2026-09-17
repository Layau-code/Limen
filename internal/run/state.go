package run

import "time"

// Admit 检查 Run 准入条件并为新请求占用一个并发名额。
func (run *Run) Admit(now time.Time) error {
	if run.State == StateSuspendedAccounting {
		return ErrAccountingSuspended
	}
	if run.State != StateActive {
		return ErrRunNotActive
	}
	if !run.Deadline.IsZero() && !now.Before(run.Deadline) {
		run.State = StateDeadlineExceeded
		return ErrRunDeadlineExceeded
	}
	if run.SoftBudgetNanoUSD > 0 && run.SettledCostNanoUSD >= run.SoftBudgetNanoUSD {
		run.State = StateSoftBudgetExhausted
		return ErrRunBudgetExhausted
	}
	if run.MaxParallelism > 0 && run.InFlight >= run.MaxParallelism {
		return ErrRunConcurrencyExceeded
	}
	run.InFlight++
	return nil
}

// Complete 停止新的准入，并在没有在途请求时完成 Run。
func (run *Run) Complete() error {
	if run.State != StateActive {
		return ErrInvalidRunTransition
	}
	run.CompleteRequested = true
	run.State = StateCompleting
	if run.InFlight == 0 {
		run.State = StateCompleted
	}
	return nil
}

// Cancel 立即将 Run 标记为取消，后续请求不能再准入。
func (run *Run) Cancel() error {
	if isTerminal(run.State) {
		return ErrInvalidRunTransition
	}
	run.State = StateCancelled
	return nil
}

// MarkDeadlineExceeded 记录 Run 因截止时间终止。
func (run *Run) MarkDeadlineExceeded() error {
	if isTerminal(run.State) {
		return ErrInvalidRunTransition
	}
	run.State = StateDeadlineExceeded
	return nil
}

// MarkBudgetExhausted 记录 Run 因已结算软预算终止。
func (run *Run) MarkBudgetExhausted() error {
	if isTerminal(run.State) {
		return ErrInvalidRunTransition
	}
	run.State = StateSoftBudgetExhausted
	return nil
}

// SuspendAccounting 暂停后续准入，等待未知费用或账本结果恢复。
func (run *Run) SuspendAccounting() error {
	if isTerminal(run.State) {
		return ErrInvalidRunTransition
	}
	run.State = StateSuspendedAccounting
	return nil
}

// Settle 完成一个在途请求的结算；费用未知时暂停账本而不伪造零成本。
func (run *Run) Settle(costNanoUSD *int64) error {
	if run.InFlight <= 0 {
		return ErrRequestNotSettleable
	}
	if costNanoUSD != nil {
		if *costNanoUSD < 0 {
			return ErrRequestNotSettleable
		}
		run.SettledCostNanoUSD += *costNanoUSD
	} else {
		run.InFlight--
		if !isTerminal(run.State) {
			run.State = StateSuspendedAccounting
		}
		return ErrAccountingSuspended
	}
	run.InFlight--
	if run.State == StateCompleting && run.InFlight == 0 {
		run.State = StateCompleted
	}
	return nil
}

// isTerminal 判断 Run 是否已经进入不可逆终态。
func isTerminal(state RunState) bool {
	switch state {
	case StateCompleted, StateCancelled, StateDeadlineExceeded, StateSoftBudgetExhausted:
		return true
	default:
		return false
	}
}
