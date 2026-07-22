package atccmd

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

type compositionContentStore struct{}

func (*compositionContentStore) Put(context.Context, snapshot.Digest, io.Reader) ([]snapshot.Location, error) {
	return nil, nil
}
func (*compositionContentStore) Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
	return nil, nil
}
func (*compositionContentStore) Exists(context.Context, snapshot.Location) (bool, error) {
	return false, nil
}
func (*compositionContentStore) DeleteLocation(context.Context, snapshot.Location) error { return nil }
func (*compositionContentStore) DeleteAll(context.Context, snapshot.Digest) error        { return nil }

type compositionOutputSealer struct{}

func (*compositionOutputSealer) Seal(context.Context, snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	return map[string]snapshot.SealedOutput{}, nil
}

func TestAgentSnapshotComponentsAreComposedOnceWithExplicitConnection(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.MaxBytes = 17
	command.AgentSnapshots.MaxFiles = 3
	wantDaemon := &jetbridge.DaemonClient{}
	wantStore := &compositionContentStore{}
	wantSealer := &compositionOutputSealer{}
	var storageCalls, sealerCalls int
	var gotConnection db.DbConn
	var storageMetadata db.AgentSnapshotsFactory
	var storageLimits snapshot.ArchiveLimits
	command.agentSnapshotComposer = func(connection db.DbConn, metadata db.AgentSnapshotsFactory, limits snapshot.ArchiveLimits) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		storageCalls++
		gotConnection = connection
		storageMetadata = metadata
		storageLimits = limits
		return wantDaemon, wantStore, nil
	}
	var gotCanonicalizer snapshot.Canonicalizer
	var gotRegistry snapshot.ValidatorRegistry
	var gotSealerMetadata snapshot.MetadataStore
	var gotSealerContent snapshot.ContentStore
	var gotLocker snapshot.DigestLockManager
	var gotStageTTL time.Duration
	command.agentSnapshotSealerComposer = func(
		canonicalizer snapshot.Canonicalizer,
		registry snapshot.ValidatorRegistry,
		metadata snapshot.MetadataStore,
		content snapshot.ContentStore,
		locker snapshot.DigestLockManager,
		stageTTL time.Duration,
	) (snapshot.OutputSealer, error) {
		sealerCalls++
		gotCanonicalizer = canonicalizer
		gotRegistry = registry
		gotSealerMetadata = metadata
		gotSealerContent = content
		gotLocker = locker
		gotStageTTL = stageTTL
		return wantSealer, nil
	}

	var explicitConnection db.DbConn = &dbfakes.FakeDbConn{}
	if err := command.composeAgentSnapshots(explicitConnection); err != nil {
		t.Fatal(err)
	}
	if err := command.composeAgentSnapshots(nil); err != nil {
		t.Fatal(err)
	}
	if storageCalls != 1 || sealerCalls != 1 || gotConnection != explicitConnection {
		t.Fatalf("composer calls/connection = storage:%d sealer:%d/%#v, want once with explicit connection", storageCalls, sealerCalls, gotConnection)
	}
	if command.agentSnapshotDaemonClient != wantDaemon || command.agentSnapshotContentStore != wantStore {
		t.Fatal("composition did not retain exact daemon/store identity")
	}
	if command.agentSnapshotMetadataStore == nil || command.agentSnapshotDigestLocker == nil || command.agentSnapshotValidatorRegistry == nil || command.agentSnapshotOutputSealer != wantSealer {
		t.Fatal("composition did not retain the complete command-scoped sealing graph")
	}
	if storageMetadata != command.agentSnapshotMetadataStore || gotSealerMetadata != command.agentSnapshotMetadataStore {
		t.Fatal("content store and sealer did not reuse the exact metadata factory")
	}
	if gotSealerContent != wantStore || gotLocker != command.agentSnapshotDigestLocker || gotRegistry != command.agentSnapshotValidatorRegistry {
		t.Fatal("sealer did not reuse the exact command-scoped content, locker, and registry instances")
	}
	if _, err := gotRegistry.Lookup(snapshot.TypeRef("review/v1")); err != nil {
		t.Fatalf("composition registry is not the authoritative built-in registry: %v", err)
	}
	if command.agentSnapshotArchiveLimits != (snapshot.ArchiveLimits{MaxContentBytes: 17, MaxEntries: 3}) {
		t.Fatalf("retained archive limits = %#v", command.agentSnapshotArchiveLimits)
	}
	if storageLimits != command.agentSnapshotArchiveLimits || gotCanonicalizer.MaxContentBytes != 17 || gotCanonicalizer.MaxEntries != 3 {
		t.Fatalf("logical archive limits were not reused exactly: storage=%#v canonicalizer=%#v", storageLimits, gotCanonicalizer)
	}
	if gotStageTTL != time.Hour {
		t.Fatalf("stage TTL = %s, want 1h", gotStageTTL)
	}
	if option, ok := command.agentSnapshotCoreStepFactoryOption(); !ok || option == nil {
		t.Fatal("enabled composition did not expose the output-sealer engine option")
	}
}

func TestAgentSnapshotCompositionDisabledRemainsNil(t *testing.T) {
	command := &RunCommand{}
	command.agentSnapshotComposer = func(db.DbConn, db.AgentSnapshotsFactory, snapshot.ArchiveLimits) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		t.Fatal("disabled composition invoked constructor")
		return nil, nil, nil
	}
	command.agentSnapshotSealerComposer = func(snapshot.Canonicalizer, snapshot.ValidatorRegistry, snapshot.MetadataStore, snapshot.ContentStore, snapshot.DigestLockManager, time.Duration) (snapshot.OutputSealer, error) {
		t.Fatal("disabled composition invoked sealer constructor")
		return nil, nil
	}
	if err := command.composeAgentSnapshots(nil); err != nil {
		t.Fatal(err)
	}
	if command.agentSnapshotDaemonClient != nil || command.agentSnapshotContentStore != nil || command.agentSnapshotMetadataStore != nil || command.agentSnapshotDigestLocker != nil || command.agentSnapshotValidatorRegistry != nil || command.agentSnapshotOutputSealer != nil {
		t.Fatal("disabled snapshot composition must remain nil")
	}
	if option, ok := command.agentSnapshotCoreStepFactoryOption(); ok || option != nil {
		t.Fatal("disabled composition exposed an output-sealer engine option")
	}
}

func TestAgentSnapshotCompositionFailureIsFailClosed(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.agentSnapshotComposer = func(db.DbConn, db.AgentSnapshotsFactory, snapshot.ArchiveLimits) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		return nil, nil, errors.New("invalid mTLS")
	}
	if err := command.composeAgentSnapshots(nil); err == nil {
		t.Fatal("expected composition failure")
	}
	if command.agentSnapshotDaemonClient != nil || command.agentSnapshotContentStore != nil || command.agentSnapshotMetadataStore != nil || command.agentSnapshotDigestLocker != nil || command.agentSnapshotValidatorRegistry != nil || command.agentSnapshotOutputSealer != nil {
		t.Fatal("failed composition published partial components")
	}
	if command.agentSnapshotArchiveLimits != (snapshot.ArchiveLimits{}) {
		t.Fatal("failed composition published snapshot admission limits")
	}
}

func TestAgentSnapshotSealerCompositionFailurePublishesNothing(t *testing.T) {
	failure := errors.New("invalid sealer dependency")
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.MaxBytes = 17
	command.AgentSnapshots.MaxFiles = 3
	command.agentSnapshotComposer = func(db.DbConn, db.AgentSnapshotsFactory, snapshot.ArchiveLimits) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		return &jetbridge.DaemonClient{}, &compositionContentStore{}, nil
	}
	command.agentSnapshotSealerComposer = func(snapshot.Canonicalizer, snapshot.ValidatorRegistry, snapshot.MetadataStore, snapshot.ContentStore, snapshot.DigestLockManager, time.Duration) (snapshot.OutputSealer, error) {
		return nil, failure
	}

	if err := command.composeAgentSnapshots(&dbfakes.FakeDbConn{}); !errors.Is(err, failure) {
		t.Fatalf("composeAgentSnapshots() error = %v", err)
	}
	if command.agentSnapshotDaemonClient != nil || command.agentSnapshotContentStore != nil || command.agentSnapshotMetadataStore != nil || command.agentSnapshotDigestLocker != nil || command.agentSnapshotValidatorRegistry != nil || command.agentSnapshotOutputSealer != nil {
		t.Fatal("failed sealer composition published partial components")
	}
	if command.agentSnapshotArchiveLimits != (snapshot.ArchiveLimits{}) {
		t.Fatal("failed sealer composition published snapshot admission limits")
	}
}
