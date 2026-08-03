package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

// metadataLosingDaemon models the live failure and its repair: the durable
// object's bytes survived a restart but its custom metadata did not, so reads
// fail closed until the repair route restores the derivable vocabulary.
type metadataLosingDaemon struct {
	mu            sync.Mutex
	content       []byte
	metadataLost  bool
	repairOutcome int
	requests      []string
}

func (daemon *metadataLosingDaemon) roundTrip(request *http.Request) (*http.Response, error) {
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	daemon.requests = append(daemon.requests, request.Method+" "+request.URL.Path)
	if strings.HasPrefix(request.URL.Path, snapshotMetadataRepairRoute) {
		if request.Method != http.MethodPost {
			return nil, fmt.Errorf("repair must be a POST, got %s", request.Method)
		}
		outcome := daemon.repairOutcome
		if outcome == 0 {
			outcome = http.StatusOK
		}
		if outcome == http.StatusOK {
			daemon.metadataLost = false
		}
		return response(outcome, nil), nil
	}
	if daemon.metadataLost {
		// The daemon cannot serve a snapshot it cannot verify against the
		// durable object, so the cache read fails.
		return response(http.StatusBadGateway, nil), nil
	}
	return response(http.StatusOK, daemon.content), nil
}

func (daemon *metadataLosingDaemon) calls() []string {
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	return append([]string(nil), daemon.requests...)
}

func hangarRepairFixture(t *testing.T, daemon *metadataLosingDaemon) (
	*SnapshotContentStore,
	snapshot.Snapshot,
	snapshot.Location,
) {
	t.Helper()
	value := snapshotFor(daemon.content)
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(daemon.roundTrip))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)
	if err != nil {
		t.Fatal(err)
	}
	return store, value, hangarSnapshotLocation(value.Digest)
}

func TestHangarRepairRestoresLostDurableMetadataAndThenVerifies(t *testing.T) {
	daemon := &metadataLosingDaemon{content: []byte("durable bytes outlived their metadata"), metadataLost: true}
	store, value, location := hangarRepairFixture(t, daemon)

	result, err := store.RepairReplicas(context.Background(), value, []snapshot.Location{location})
	if err != nil {
		t.Fatalf("repair pass failed to self-heal: %v", err)
	}
	if result.Verified != 1 || result.Desired != 1 {
		t.Fatalf("repair result = %#v, want one verified durable authority", result)
	}

	calls := daemon.calls()
	if len(calls) != 3 {
		t.Fatalf("repair calls = %v, want verify, repair, re-verify", calls)
	}
	if !strings.HasPrefix(calls[1], "POST "+snapshotMetadataRepairRoute) {
		t.Fatalf("repair call = %q, want a POST to the repair route", calls[1])
	}
	if !strings.HasSuffix(calls[1], strings.TrimPrefix(value.Digest.String(), "sha256:")) {
		t.Fatalf("repair call = %q, want the snapshot's own digest", calls[1])
	}
}

func TestHangarRepairDoesNotRequestRepairForAVerifiableObject(t *testing.T) {
	daemon := &metadataLosingDaemon{content: []byte("healthy durable object")}
	store, value, location := hangarRepairFixture(t, daemon)

	result, err := store.RepairReplicas(context.Background(), value, []snapshot.Location{location})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified != 1 {
		t.Fatalf("repair result = %#v", result)
	}
	// Proving costs a full download and decompression on the daemon. A durable
	// object that already verifies must never trigger it.
	for _, call := range daemon.calls() {
		if strings.Contains(call, snapshotMetadataRepairRoute) {
			t.Fatalf("healthy object triggered a repair request: %v", daemon.calls())
		}
	}
}

func TestHangarRepairSurfacesUnprovableContentAsItsOwnCondition(t *testing.T) {
	daemon := &metadataLosingDaemon{
		content:       []byte("bytes that cannot prove themselves"),
		metadataLost:  true,
		repairOutcome: http.StatusUnprocessableEntity,
	}
	store, value, location := hangarRepairFixture(t, daemon)

	result, err := store.RepairReplicas(context.Background(), value, []snapshot.Location{location})
	if !errors.Is(err, errDurableMetadataUnrepairable) {
		t.Fatalf("repair error = %v, want an unrepairable-content report", err)
	}
	if !errors.Is(err, snapshot.ErrNoReadableReplica) {
		t.Fatalf("repair error = %v, want the digest still reported unreadable", err)
	}
	if result.Verified != 0 {
		t.Fatalf("unprovable content reported %d verified replicas", result.Verified)
	}
	// The object stays exactly as it is: one probe, one refused repair, and no
	// deletion or rewrite of any kind.
	for _, call := range daemon.calls() {
		if strings.HasPrefix(call, http.MethodDelete) || strings.HasPrefix(call, http.MethodPut) {
			t.Fatalf("unprovable object was mutated: %v", daemon.calls())
		}
	}
	if len(daemon.calls()) != 2 {
		t.Fatalf("calls = %v, want probe then refused repair with no re-verify", daemon.calls())
	}
}

func TestHangarRepairBudgetBoundsProvingWorkPerPass(t *testing.T) {
	daemon := &metadataLosingDaemon{
		content:       []byte("permanently unprovable"),
		metadataLost:  true,
		repairOutcome: http.StatusUnprocessableEntity,
	}
	store, value, location := hangarRepairFixture(t, daemon)
	if err := WithSnapshotDurableMetadataRepairBudget(2, time.Hour)(store); err != nil {
		t.Fatal(err)
	}

	for attempt := range 4 {
		_, err := store.RepairReplicas(context.Background(), value, []snapshot.Location{location})
		if err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt)
		}
		if attempt >= 2 && !errors.Is(err, errDurableMetadataRepairDeferred) {
			t.Fatalf("attempt %d error = %v, want the budget to defer it", attempt, err)
		}
	}

	repairs := 0
	for _, call := range daemon.calls() {
		if strings.Contains(call, snapshotMetadataRepairRoute) {
			repairs++
		}
	}
	if repairs != 2 {
		t.Fatalf("repair requests = %d, want the configured budget of 2", repairs)
	}
}

func TestHangarRepairBudgetRefillsOncePerWindow(t *testing.T) {
	budget := newDurableMetadataRepairBudget(1, time.Minute)
	now := time.Unix(1700000000, 0).UTC()
	budget.now = func() time.Time { return now }

	if !budget.take() {
		t.Fatal("first take in a fresh window must be admitted")
	}
	if budget.take() {
		t.Fatal("second take within the window must be refused")
	}
	now = now.Add(59 * time.Second)
	if budget.take() {
		t.Fatal("take before the window closes must still be refused")
	}
	now = now.Add(2 * time.Second)
	if !budget.take() {
		t.Fatal("take after the window closes must be admitted again")
	}
}

func TestSnapshotDurableMetadataRepairBudgetRejectsUnboundedConfiguration(t *testing.T) {
	for name, option := range map[string]SnapshotContentStoreOption{
		"zero limit":      WithSnapshotDurableMetadataRepairBudget(0, time.Minute),
		"negative limit":  WithSnapshotDurableMetadataRepairBudget(-1, time.Minute),
		"zero window":     WithSnapshotDurableMetadataRepairBudget(1, 0),
		"negative window": WithSnapshotDurableMetadataRepairBudget(1, -time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, nil), nil
			}))
			if _, err := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits, option); err == nil {
				t.Fatal("unbounded repair budget was accepted")
			}
		})
	}
}

func TestHangarRepairTreatsABusyDaemonAsDeferredNotFailed(t *testing.T) {
	daemon := &metadataLosingDaemon{
		content:       []byte("daemon already proving another object"),
		metadataLost:  true,
		repairOutcome: http.StatusTooManyRequests,
	}
	store, value, location := hangarRepairFixture(t, daemon)

	_, err := store.RepairReplicas(context.Background(), value, []snapshot.Location{location})
	if !errors.Is(err, errDurableMetadataRepairDeferred) {
		t.Fatalf("repair error = %v, want a deferral", err)
	}
	if errors.Is(err, errDurableMetadataUnrepairable) {
		t.Fatalf("a busy daemon must not be reported as unrepairable content: %v", err)
	}
}

func TestHangarRepairReportsAnObjectStillUnreadableAfterRepair(t *testing.T) {
	// The daemon reports a successful repair but the object still does not
	// verify. Trusting the report would mark a broken digest healthy.
	daemon := &metadataLosingDaemon{content: []byte("never actually recovers"), metadataLost: true}
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasPrefix(request.URL.Path, snapshotMetadataRepairRoute) {
			return response(http.StatusOK, nil), nil
		}
		return response(http.StatusBadGateway, nil), nil
	}))
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)
	if err != nil {
		t.Fatal(err)
	}
	value := snapshotFor(daemon.content)

	result, err := store.RepairReplicas(
		context.Background(),
		value,
		[]snapshot.Location{hangarSnapshotLocation(value.Digest)},
	)
	if err == nil {
		t.Fatal("a still-unreadable object was reported repaired")
	}
	if result.Verified != 0 {
		t.Fatalf("result = %#v, want nothing verified", result)
	}
}
