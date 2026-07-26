package publisher_test

import (
	"context"
	"errors"
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

// failingStore delegates to a real store but fails the first Complete, which
// is crash point 4: the external write landed and the durable completion did
// not. Everything else must behave exactly like the wrapped store.
type failingStore struct {
	publisher.Store
	failures int
}

func (store *failingStore) Complete(
	ctx context.Context,
	operationKey string,
	attempt int,
	result publisher.Result,
) (publisher.Publication, error) {
	if store.failures > 0 {
		store.failures--
		return publisher.Publication{}, errors.New("durable completion failed")
	}
	return store.Store.Complete(ctx, operationKey, attempt, result)
}

func TestGatewayRecoversFromFailedCompletionWithoutRepeatingTheWrite(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{CurrentBase: testBaseSHA})
	fixture := newGatewaySnapshotFixture(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	memory := publisher.NewMemoryStore(func() time.Time { return now })
	store := &failingStore{Store: memory, failures: 1}
	executor, err := publisher.NewGatewayExecutor(
		store, fixture.metadata, fixture.content,
		gatewayTestConfig(t, reference.Server, gatewayGitPolicy),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayGitRequest(fixture.ref)
	if _, err := executor.Execute(context.Background(), request); err == nil {
		t.Fatal("a failed durable completion must not be reported as success")
	}
	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	pending, found, err := memory.Get(context.Background(), key)
	if err != nil || !found || pending.Status != publisher.StatusPending {
		t.Fatalf("after crash point 4 = (%+v, %t, %v), want a pending row", pending, found, err)
	}

	now = now.Add(2 * time.Minute)
	publication, err := executor.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusSucceeded || publication.Result.ExternalID != "publication/1" {
		t.Fatalf("recovery = (%+v, %v)", publication, err)
	}

	if got := reference.PublishRequests(); got != 1 {
		t.Fatalf("publish requests across both attempts = %d, want exactly 1", got)
	}
	if got := reference.ExternalEffects(); got != 1 {
		t.Fatalf("external effects = %d, want exactly 1", got)
	}
	// Pin the ordering, not just the count. If Lookup is moved after the
	// write, the retry becomes current-base → publish → lookup and this
	// sequence assertion fails before the counts do.
	if want := []string{
		"/v1/publications/lookup", "/v1/git/current-base", "/v1/git/publish",
		"/v1/publications/lookup",
	}; !slices.Equal(reference.Paths(), want) {
		t.Fatalf("gateway calls = %v, want %v — Lookup must precede every write on a retry",
			reference.Paths(), want)
	}
}
