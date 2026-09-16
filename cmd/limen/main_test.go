package main

import (
	"testing"

	"github.com/huz/limen/internal/config"
)

func TestGatewayTargetPreservesPricing(t *testing.T) {
	source := config.Target{Provider: "openai", UpstreamModel: "gpt-test", Pricing: &config.Pricing{InputPerMillionNanoUSD: 11}}
	target := gatewayTarget(source)
	if target.Provider != source.Provider || target.UpstreamModel != source.UpstreamModel || target.Pricing == nil || target.Pricing.InputPerMillionNanoUSD != 11 {
		t.Fatalf("target = %+v", target)
	}
}
