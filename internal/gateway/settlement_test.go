package gateway

import (
	"testing"

	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/provider"
)

type fixedUsage struct {
	usage provider.Usage
}

func (usage fixedUsage) Snapshot() provider.Usage {
	return usage.usage
}

func TestSettlementCalculatesCompleteCost(t *testing.T) {
	settlement := NewSettlement()
	settlement.AddAttempt(AttemptSettlement{
		Provider:      "openai",
		UpstreamModel: "gpt-test",
		StatusCode:    200,
		Pricing:       &cost.Pricing{InputPerMillionNanoUSD: 250_000_000, OutputPerMillionNanoUSD: 2_000_000_000},
		Usage:         fixedUsage{usage: provider.Usage{InputTokens: 10, OutputTokens: 3, Complete: true}},
	})

	summary := settlement.Summary()
	if summary.Status != SettlementComplete || summary.InputTokens != 10 || summary.OutputTokens != 3 || !summary.CostAvailable || summary.CostNanoUSD != 8_500 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestSettlementMarksMissingUsagePartial(t *testing.T) {
	settlement := NewSettlement()
	settlement.AddAttempt(AttemptSettlement{
		StatusCode: 200,
		Pricing:    &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1},
		Usage:      fixedUsage{},
	})
	settlement.AddAttempt(AttemptSettlement{
		StatusCode: 200,
		Pricing:    &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1},
		Usage:      fixedUsage{usage: provider.Usage{InputTokens: 2, OutputTokens: 1, Complete: true}},
	})

	summary := settlement.Summary()
	if summary.Status != SettlementPartial || summary.InputTokens != 2 || summary.OutputTokens != 1 || summary.CostAvailable {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestSettlementIgnoresTransportAttemptWithoutResponse(t *testing.T) {
	settlement := NewSettlement()
	settlement.AddAttempt(AttemptSettlement{
		StatusCode: 200,
		Pricing:    &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1},
		Usage:      fixedUsage{usage: provider.Usage{InputTokens: 2, OutputTokens: 1, Complete: true}},
	})
	summary := settlement.Summary()
	if summary.Status != SettlementComplete || !summary.CostAvailable {
		t.Fatalf("summary = %+v", summary)
	}
}
