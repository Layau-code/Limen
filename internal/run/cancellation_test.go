package run

import (
	"testing"
	"time"
)

func TestCancellationHubPublishesOnlyMatchingRun(t *testing.T) {
	hub := NewCancellationHub()
	matched, unsubscribeMatched := hub.Subscribe("tenant-a", "run-a")
	defer unsubscribeMatched()
	other, unsubscribeOther := hub.Subscribe("tenant-a", "run-b")
	defer unsubscribeOther()
	hub.Publish(CancellationEvent{TenantID: "tenant-a", RunID: "run-a", CreatedAt: time.Now()})
	select {
	case <-matched:
	case <-time.After(time.Second):
		t.Fatal("matching subscription was not notified")
	}
	select {
	case <-other:
		t.Fatal("unrelated subscription was notified")
	default:
	}
}
