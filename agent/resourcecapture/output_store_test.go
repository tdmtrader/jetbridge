package resourcecapture_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
)

type captureOutputFinderStub struct {
	value snapshot.Snapshot
	found bool
	err   error
	calls []resourcecapture.OutputRequest
}

func (stub *captureOutputFinderStub) FindResourceCaptureOutput(_ context.Context, teamID int, runID int64, operationKey, outputPort string, expectedType snapshot.TypeRef) (snapshot.Snapshot, bool, error) {
	stub.calls = append(stub.calls, resourcecapture.OutputRequest{TeamID: teamID, PipelineRunID: runID, OperationKey: operationKey, OutputPort: outputPort, ExpectedType: expectedType})
	return stub.value, stub.found, stub.err
}

type capturePinnerStub struct {
	calls  int
	team   int
	actor  string
	ref    snapshot.SnapshotRef
	reason string
	err    error
}

func (stub *capturePinnerStub) Pin(_ context.Context, _ snapshot.DigestLease, teamID int, actor string, ref snapshot.SnapshotRef, reason string) (snapshot.RetentionClaim, error) {
	stub.calls++
	stub.team, stub.actor, stub.ref, stub.reason = teamID, actor, ref, reason
	return snapshot.RetentionClaim{}, stub.err
}

type digestLeaseStub struct {
	digest snapshot.Digest
	closed int
}

func (lease *digestLeaseStub) Covers(digest snapshot.Digest) bool { return digest == lease.digest }
func (lease *digestLeaseStub) Close() error                       { lease.closed++; return nil }

type digestLockerStub struct {
	lease *digestLeaseStub
	err   error
}

func (stub *digestLockerStub) AcquireMany(context.Context, []snapshot.Digest) (snapshot.DigestLease, error) {
	return stub.lease, stub.err
}

func TestOutputStoreAuthorizesExactProductionAndPinsItDurably(t *testing.T) {
	digest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	manifest := snapshot.Snapshot{
		ID: 71, Type: "repository/v1", Digest: digest, ByteSize: 3, FileCount: 1,
		Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
	}
	finder := &captureOutputFinderStub{value: manifest, found: true}
	pinner := &capturePinnerStub{}
	lease := &digestLeaseStub{digest: digest}
	store, err := resourcecapture.NewOutputStore(finder, pinner, &digestLockerStub{lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	request := resourcecapture.OutputRequest{
		TeamID: 7, TeamName: "main", PipelineRunID: 51, OperationKey: strings.Repeat("b", 64),
		OutputPort: "snapshot", ExpectedType: "repository/v1", Actor: "github:subject-1",
	}
	got, pinned, err := store.Finalize(context.Background(), request)
	if err != nil || !pinned || got.ID != manifest.ID {
		t.Fatalf("Finalize() = %#v, %v, %v", got, pinned, err)
	}
	if pinner.calls != 1 || pinner.team != 7 || pinner.actor != "github:subject-1" || pinner.ref.ID != manifest.ID ||
		pinner.reason != "resource capture "+request.OperationKey || lease.closed != 1 {
		t.Fatalf("pin = calls %d team %d actor %q ref %#v reason %q close %d", pinner.calls, pinner.team, pinner.actor, pinner.ref, pinner.reason, lease.closed)
	}
}

func TestOutputStoreFailsClosedWhenProductionIsMissingOrCanceled(t *testing.T) {
	finder := &captureOutputFinderStub{}
	pinner := &capturePinnerStub{}
	store, err := resourcecapture.NewOutputStore(finder, pinner, &digestLockerStub{})
	if err != nil {
		t.Fatal(err)
	}
	request := resourcecapture.OutputRequest{
		TeamID: 7, TeamName: "main", PipelineRunID: 51, OperationKey: strings.Repeat("b", 64),
		OutputPort: "snapshot", ExpectedType: "repository/v1", Actor: "github:subject-1",
	}
	if _, _, err := store.Finalize(context.Background(), request); !errors.Is(err, resourcecapture.ErrOutputUnavailable) {
		t.Fatalf("missing output error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.Finalize(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled output error = %v", err)
	}
	if pinner.calls != 0 {
		t.Fatal("missing/canceled output was pinned")
	}
}
