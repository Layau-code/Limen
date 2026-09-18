package cost

import (
	"encoding/json"
	"testing"
)

func TestPricingCostUsesFixedPoint(t *testing.T) {
	pricing := Pricing{InputPerMillionNanoUSD: 250_000_000, OutputPerMillionNanoUSD: 2_000_000_000}
	cost, err := pricing.Cost(10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if cost != 8_500 {
		t.Fatalf("cost = %d, want 8500 nano-USD", cost)
	}
	if got := FormatUSD(cost); got != "0.0000085" {
		t.Fatalf("formatted cost = %q", got)
	}
}

func TestPricingCostRoundsHalfUp(t *testing.T) {
	pricing := Pricing{InputPerMillionNanoUSD: 50_000, OutputPerMillionNanoUSD: 0}
	cost, err := pricing.Cost(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cost != 1 {
		t.Fatalf("cost = %d, want 1 nano-USD", cost)
	}
}

func TestPricingCostRejectsNegativeTokens(t *testing.T) {
	pricing := Pricing{InputPerMillionNanoUSD: 1}
	if _, err := pricing.Cost(-1, 0); err == nil {
		t.Fatal("negative input tokens were accepted")
	}
}

func TestPricingJSONRoundTripPreservesFixedPointValues(t *testing.T) {
	want := Pricing{InputPerMillionNanoUSD: 250_000_001, OutputPerMillionNanoUSD: 2_000_000_009}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Pricing
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("pricing = %+v, want %+v", got, want)
	}
}
