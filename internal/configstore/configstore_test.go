package configstore

import (
	"context"
	"testing"
)

const testConfig = `{"models":[{"id":"smart","display_name":"Smart","targets":[{"id":"smart-target","provider":"openai","upstream_model":"gpt-4.1"}]}]}`

func TestMemoryStoreCreatePublishAndTenantIsolation(t *testing.T) {
	store := NewMemoryStore()
	first, err := store.Create(context.Background(), "tenant-a", []byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.Create(context.Background(), "tenant-a", []byte(testConfig))
	if err != nil || repeated.Version != first.Version {
		t.Fatalf("repeat = %+v, err=%v", repeated, err)
	}
	if _, err := store.Get(context.Background(), "tenant-b", first.Version); err != ErrNotFound {
		t.Fatalf("cross-tenant get error = %v", err)
	}
	published, err := store.Publish(context.Background(), "tenant-a", first.Version)
	if err != nil || published.State != StatePublished || published.PublishedAt == nil {
		t.Fatalf("publish = %+v, err=%v", published, err)
	}
	active, err := store.GetPublished(context.Background(), "tenant-a")
	if err != nil || active.Version != first.Version {
		t.Fatalf("active = %+v, err=%v", active, err)
	}
	list, err := store.List(context.Background(), "tenant-a")
	if err != nil || len(list) != 1 || list[0].State != StatePublished {
		t.Fatalf("list = %+v, err=%v", list, err)
	}
}

func TestMemoryStoreRejectsInvalidDocument(t *testing.T) {
	if _, err := NewMemoryStore().Create(context.Background(), "tenant-a", []byte(`{"models":[]}`)); err == nil {
		t.Fatal("expected invalid config error")
	}
}

func TestMemoryStorePublishWithMutationIsIdempotent(t *testing.T) {
	store := NewMemoryStore()
	first, err := store.Create(context.Background(), "tenant-a", []byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	mutation := Mutation{Key: "publish-1", Hash: "sha256:request"}
	published, err := store.PublishWithMutation(context.Background(), "tenant-a", first.Version, mutation)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.PublishWithMutation(context.Background(), "tenant-a", first.Version, mutation)
	if err != nil || repeated.PublishedAt == nil || published.PublishedAt == nil || !repeated.PublishedAt.Equal(*published.PublishedAt) {
		t.Fatalf("repeat = %+v, err=%v", repeated, err)
	}
	if _, err := store.PublishWithMutation(context.Background(), "tenant-a", first.Version, Mutation{Key: mutation.Key, Hash: "sha256:other"}); err != ErrIdempotencyConflict {
		t.Fatalf("conflict error = %v", err)
	}
}
