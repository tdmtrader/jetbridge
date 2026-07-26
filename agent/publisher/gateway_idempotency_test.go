package publisher_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/contracttest"
)

func TestGatewayRetriesCarryBytewiseIdenticalIdempotencyKeyWhenLookupLagsThePublish(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{
		CurrentBase: testBaseSHA,
		Fault: contracttest.Fault{
			// The write side has landed the publish; the read side has not
			// caught up, and the first attempt never learns its own answer.
			BlindLookup:           true,
			FailAfterFirstPublish: http.StatusServiceUnavailable,
		},
	})
	fixture := newGatewaySnapshotFixture(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	executor, err := publisher.NewGatewayExecutor(
		store, fixture.metadata, fixture.content,
		gatewayTestConfig(t, reference.Server, gatewayGitPolicy),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayGitRequest(fixture.ref)
	if _, err := executor.Execute(context.Background(), request); err == nil {
		t.Fatal("a 503 after the write landed must surface as a retryable error")
	}
	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	pending, found, err := store.Get(context.Background(), key)
	if err != nil || !found || pending.Status != publisher.StatusPending {
		t.Fatalf("first attempt = (%+v, %t, %v), want a pending row", pending, found, err)
	}

	now = now.Add(2 * time.Minute)
	publication, err := executor.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusSucceeded || publication.Attempt != 2 {
		t.Fatalf("reclaimed attempt = (%+v, %v)", publication, err)
	}

	keys := reference.IdempotencyKeys("/v1/git/publish")
	if len(keys) != 2 {
		t.Fatalf("publish idempotency keys = %v, want two attempts", keys)
	}
	if keys[0] != keys[1] || keys[0] != key {
		t.Fatalf("retried publish changed its idempotency key: %q then %q (operation key %q)", keys[0], keys[1], key)
	}
	if got := reference.PublishRequests(); got != 2 {
		t.Fatalf("publish requests = %d, want 2 — the blind lookup cannot prevent the second write", got)
	}
	// Exactly one external effect, and it came from the gateway's durable
	// (publisher, operation_key) map, not from anything ATC could see.
	if got := reference.ExternalEffects(); got != 1 {
		t.Fatalf("external effects = %d, want 1", got)
	}
	if want := []string{
		"/v1/publications/lookup", "/v1/git/current-base", "/v1/git/publish",
		"/v1/publications/lookup", "/v1/git/current-base", "/v1/git/publish",
	}; !slices.Equal(reference.Paths(), want) {
		t.Fatalf("gateway calls = %v, want %v", reference.Paths(), want)
	}
}
