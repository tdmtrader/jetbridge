package atccmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

func TestAgentSnapshotStreamErrorReporterLogsOnlyBoundedCategory(t *testing.T) {
	logger := lagertest.NewTestLogger("atc-test")
	report := agentSnapshotStreamErrorReporter(logger)
	report(context.Background(), "snapshot_content_stream_failed")
	report(context.Background(), "secret-node="+strings.Repeat("x", 8192))

	logs := logger.Logs()
	if len(logs) != 2 {
		t.Fatalf("log count = %d, want 2", len(logs))
	}
	if logs[0].Message != "atc-test.agent-snapshot-api.content-stream-failed" || logs[0].Data["category"] != "snapshot_content_stream_failed" {
		t.Fatalf("known stream log = %#v", logs[0])
	}
	if logs[1].Data["category"] != "unknown" {
		t.Fatalf("unrecognized category was not bounded: %#v", logs[1].Data)
	}
	for _, log := range logs {
		if strings.Contains(string(log.ToJSON()), "secret-node") {
			t.Fatalf("untrusted reporter input leaked into production log: %s", log.ToJSON())
		}
	}
}

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
func (*compositionContentStore) RepairReplicas(context.Context, snapshot.Snapshot, []snapshot.Location) (snapshot.ReplicaRepairResult, error) {
	return snapshot.ReplicaRepairResult{}, nil
}

type compositionOutputSealer struct{}

func (*compositionOutputSealer) Seal(context.Context, snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	return map[string]snapshot.SealedOutput{}, nil
}
func (*compositionOutputSealer) Upload(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error) {
	return snapshot.Snapshot{}, nil
}

type compositionPublisher struct{}

func (*compositionPublisher) Execute(context.Context, publisher.Request) (publisher.Publication, error) {
	return publisher.Publication{}, nil
}

type compositionLifecycle struct {
	collectCalls  int
	repairCalls   int
	collectReport snapshot.LifecycleReport
	repairReport  snapshot.LifecycleReport
	collectErr    error
	repairErr     error
}

func (l *compositionLifecycle) Collect(context.Context) (snapshot.LifecycleReport, error) {
	l.collectCalls++
	return l.collectReport, l.collectErr
}

func (l *compositionLifecycle) Repair(context.Context) (snapshot.LifecycleReport, error) {
	l.repairCalls++
	return l.repairReport, l.repairErr
}

func TestAgentSnapshotComponentsAreComposedOnceWithExplicitConnection(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.MaxBytes = 17
	command.AgentSnapshots.MaxFiles = 3
	command.AgentSnapshots.OrphanGracePeriod = 2 * time.Hour
	command.AgentSnapshots.BindingRetention = 14 * 24 * time.Hour
	command.AgentSnapshots.TempDir = t.TempDir()
	wantDaemon := &jetbridge.DaemonClient{}
	wantStore := &compositionContentStore{}
	wantSealer := &compositionOutputSealer{}
	wantPublisher := &compositionPublisher{}
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
	var gotStageTTL, gotBindingRetention time.Duration
	command.agentSnapshotSealerComposer = func(
		canonicalizer snapshot.Canonicalizer,
		registry snapshot.ValidatorRegistry,
		metadata snapshot.MetadataStore,
		content snapshot.ContentStore,
		locker snapshot.DigestLockManager,
		stageTTL time.Duration,
		bindingRetention time.Duration,
	) (snapshot.SnapshotCreator, error) {
		sealerCalls++
		gotCanonicalizer = canonicalizer
		gotRegistry = registry
		gotSealerMetadata = metadata
		gotSealerContent = content
		gotLocker = locker
		gotStageTTL = stageTTL
		gotBindingRetention = bindingRetention
		return wantSealer, nil
	}
	var gotPublicationStore publisher.Store
	var publisherMetadata snapshot.MetadataStore
	var publisherContent snapshot.ContentStore
	command.agentSnapshotPublisherComposer = func(
		store publisher.Store,
		metadata snapshot.MetadataStore,
		content snapshot.ContentStore,
	) (publisher.Executor, error) {
		gotPublicationStore = store
		publisherMetadata = metadata
		publisherContent = content
		return wantPublisher, nil
	}
	wantLifecycle := &compositionLifecycle{}
	var lifecycleMetadata snapshot.MetadataStore
	var lifecycleContent snapshot.ContentStore
	var lifecycleReplicas snapshot.ReplicaRepairer
	var lifecycleLocker snapshot.DigestLockManager
	command.agentSnapshotLifecycleComposer = func(
		metadata snapshot.MetadataStore,
		content snapshot.ContentStore,
		replicas snapshot.ReplicaRepairer,
		locker snapshot.DigestLockManager,
	) (snapshotLifecycle, error) {
		lifecycleMetadata = metadata
		lifecycleContent = content
		lifecycleReplicas = replicas
		lifecycleLocker = locker
		return wantLifecycle, nil
	}

	var explicitConnection db.DbConn = openTestDB(t)
	logger := lagertest.NewTestLogger("snapshot-composition")
	if err := command.composeAgentSnapshots(explicitConnection, logger); err != nil {
		t.Fatal(err)
	}
	firstWorkflowVerifier := command.agentSnapshotWorkflowRuns
	if err := command.composeAgentSnapshots(nil, logger); err != nil {
		t.Fatal(err)
	}
	if storageCalls != 1 || sealerCalls != 1 || gotConnection != explicitConnection {
		t.Fatalf("composer calls/connection = storage:%d sealer:%d/%#v, want once with explicit connection", storageCalls, sealerCalls, gotConnection)
	}
	if command.agentSnapshotDaemonClient != wantDaemon || command.agentSnapshotContentStore != wantStore {
		t.Fatal("composition did not retain exact daemon/store identity")
	}
	if command.agentSnapshotMetadataStore == nil || command.agentSnapshotDigestLocker == nil || command.agentSnapshotValidatorRegistry == nil || command.agentSnapshotOutputSealer == nil || command.agentSnapshotCreator == nil || command.agentSnapshotOutputSealer != command.agentSnapshotCreator || command.agentSnapshotLifecycle != wantLifecycle || command.agentSnapshotPublisher != wantPublisher || command.agentSnapshotProjectionRegistry == nil || command.agentResourceCaptureFinalizer == nil || command.agentSnapshotHandlerFactory == nil {
		t.Fatal("composition did not retain the complete command-scoped sealing graph")
	}
	if gotPublicationStore == nil || publisherMetadata != command.agentSnapshotMetadataStore || publisherContent != wantStore {
		t.Fatal("publisher did not receive the durable audit and exact command-scoped snapshot stores")
	}
	if _, ok := command.agentSnapshotCreator.(*projection.ProjectingCreator); !ok {
		t.Fatalf("snapshot creator = %T, want post-seal projection decorator", command.agentSnapshotCreator)
	}
	if command.agentSnapshotWorkflowRuns == nil {
		t.Fatal("composition did not retain one command-scoped workflow input verifier")
	}
	if command.agentSnapshotWorkflowRuns != firstWorkflowVerifier {
		t.Fatal("composition rebuilt the workflow input verifier")
	}
	if storageMetadata != command.agentSnapshotMetadataStore || gotSealerMetadata != command.agentSnapshotMetadataStore {
		t.Fatal("content store and sealer did not reuse the exact metadata factory")
	}
	if gotSealerContent != wantStore || gotLocker != command.agentSnapshotDigestLocker || gotRegistry != command.agentSnapshotValidatorRegistry {
		t.Fatal("sealer did not reuse the exact command-scoped content, locker, and registry instances")
	}
	if lifecycleMetadata != command.agentSnapshotMetadataStore || lifecycleContent != wantStore || lifecycleReplicas != wantStore || lifecycleLocker != command.agentSnapshotDigestLocker {
		t.Fatal("lifecycle did not reuse the exact command-scoped metadata, content, repair, and locker instances")
	}
	if _, err := gotRegistry.Lookup(snapshot.TypeRef("review/v1")); err != nil {
		t.Fatalf("composition registry is not the authoritative built-in registry: %v", err)
	}
	if command.agentSnapshotArchiveLimits != (snapshot.ArchiveLimits{MaxContentBytes: 17, MaxEntries: 3}) {
		t.Fatalf("retained archive limits = %#v", command.agentSnapshotArchiveLimits)
	}
	if storageLimits != command.agentSnapshotArchiveLimits || gotCanonicalizer.MaxContentBytes != 17 ||
		gotCanonicalizer.MaxEntries != 3 || gotCanonicalizer.TempDir != command.AgentSnapshots.TempDir {
		t.Fatalf("logical archive limits were not reused exactly: storage=%#v canonicalizer=%#v", storageLimits, gotCanonicalizer)
	}
	if gotStageTTL != 2*time.Hour {
		t.Fatalf("stage TTL = %s, want 2h", gotStageTTL)
	}
	if gotBindingRetention != 14*24*time.Hour {
		t.Fatalf("binding retention = %s, want 14d", gotBindingRetention)
	}
	if options, ok := command.agentSnapshotCoreStepFactoryOptions(); !ok || len(options) != 4 {
		t.Fatalf("enabled composition exposed %d engine options, want sealer, canonicalizer, loader, and publisher", len(options))
	}
}

func TestAgentSnapshotCompositionDisabledRemainsNil(t *testing.T) {
	command := &RunCommand{}
	command.agentSnapshotComposer = func(db.DbConn, db.AgentSnapshotsFactory, snapshot.ArchiveLimits) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		t.Fatal("disabled composition invoked constructor")
		return nil, nil, nil
	}
	command.agentSnapshotSealerComposer = func(snapshot.Canonicalizer, snapshot.ValidatorRegistry, snapshot.MetadataStore, snapshot.ContentStore, snapshot.DigestLockManager, time.Duration, time.Duration) (snapshot.SnapshotCreator, error) {
		t.Fatal("disabled composition invoked sealer constructor")
		return nil, nil
	}
	if err := command.composeAgentSnapshots(nil, nil); err != nil {
		t.Fatal(err)
	}
	if command.agentSnapshotDaemonClient != nil || command.agentSnapshotContentStore != nil || command.agentSnapshotMetadataStore != nil || command.agentSnapshotWorkflowRuns != nil || command.agentSnapshotDigestLocker != nil || command.agentSnapshotValidatorRegistry != nil || command.agentSnapshotOutputSealer != nil || command.agentSnapshotCreator != nil || command.agentSnapshotLifecycle != nil || command.agentSnapshotPublisher != nil || command.agentSnapshotHandlerFactory != nil {
		t.Fatal("disabled snapshot composition must remain nil")
	}
	if options, ok := command.agentSnapshotCoreStepFactoryOptions(); ok || options != nil {
		t.Fatal("disabled composition exposed snapshot engine options")
	}
}

func TestAgentSnapshotPublisherIsAbsentWithoutExplicitDeploymentComposer(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.agentSnapshotOutputSealer = &compositionOutputSealer{}
	connection := openTestDB(t)
	command.agentSnapshotMetadataStore = db.NewAgentSnapshotsFactory(connection)
	command.agentSnapshotContentStore = &compositionContentStore{}
	command.agentSnapshotWorkflowRuns = db.NewAgentWorkflowRunsFactory(connection)

	options, ok := command.agentSnapshotCoreStepFactoryOptions()
	if !ok {
		t.Fatal("complete snapshot composition was not exposed to the engine")
	}
	if command.agentSnapshotPublisherComposer != nil || command.agentSnapshotPublisher != nil {
		t.Fatal("zero-value command unexpectedly configured an external publisher")
	}
	if len(options) != 3 {
		t.Fatalf("engine options = %d, want sealer, canonicalizer, and loader without a deployment publisher", len(options))
	}
}

func TestAgentSnapshotCompositionFailureIsFailClosed(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.agentSnapshotComposer = func(db.DbConn, db.AgentSnapshotsFactory, snapshot.ArchiveLimits) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		return nil, nil, errors.New("invalid mTLS")
	}
	if err := command.composeAgentSnapshots(nil, lagertest.NewTestLogger("snapshot-composition")); err == nil {
		t.Fatal("expected composition failure")
	}
	if command.agentSnapshotDaemonClient != nil || command.agentSnapshotContentStore != nil || command.agentSnapshotMetadataStore != nil || command.agentSnapshotWorkflowRuns != nil || command.agentSnapshotDigestLocker != nil || command.agentSnapshotValidatorRegistry != nil || command.agentSnapshotOutputSealer != nil || command.agentSnapshotCreator != nil || command.agentSnapshotLifecycle != nil || command.agentSnapshotHandlerFactory != nil {
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
	command.agentSnapshotSealerComposer = func(snapshot.Canonicalizer, snapshot.ValidatorRegistry, snapshot.MetadataStore, snapshot.ContentStore, snapshot.DigestLockManager, time.Duration, time.Duration) (snapshot.SnapshotCreator, error) {
		return nil, failure
	}

	if err := command.composeAgentSnapshots(openTestDB(t), lagertest.NewTestLogger("snapshot-composition")); !errors.Is(err, failure) {
		t.Fatalf("composeAgentSnapshots() error = %v", err)
	}
	if command.agentSnapshotDaemonClient != nil || command.agentSnapshotContentStore != nil || command.agentSnapshotMetadataStore != nil || command.agentSnapshotWorkflowRuns != nil || command.agentSnapshotDigestLocker != nil || command.agentSnapshotValidatorRegistry != nil || command.agentSnapshotOutputSealer != nil || command.agentSnapshotCreator != nil || command.agentSnapshotLifecycle != nil || command.agentSnapshotHandlerFactory != nil {
		t.Fatal("failed sealer composition published partial components")
	}
	if command.agentSnapshotArchiveLimits != (snapshot.ArchiveLimits{}) {
		t.Fatal("failed sealer composition published snapshot admission limits")
	}
}

func TestAgentSnapshotTypedNilCompositionFailsClosed(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.MaxBytes = 17
	command.AgentSnapshots.MaxFiles = 3
	command.AgentSnapshots.OrphanGracePeriod = time.Hour
	command.AgentSnapshots.BindingRetention = 7 * 24 * time.Hour
	command.agentSnapshotComposer = func(db.DbConn, db.AgentSnapshotsFactory, snapshot.ArchiveLimits) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		return &jetbridge.DaemonClient{}, &compositionContentStore{}, nil
	}
	command.agentSnapshotSealerComposer = func(snapshot.Canonicalizer, snapshot.ValidatorRegistry, snapshot.MetadataStore, snapshot.ContentStore, snapshot.DigestLockManager, time.Duration, time.Duration) (snapshot.SnapshotCreator, error) {
		var creator *compositionOutputSealer
		return creator, nil
	}

	if err := command.composeAgentSnapshots(openTestDB(t), lagertest.NewTestLogger("snapshot-composition")); err == nil {
		t.Fatal("typed-nil creator was accepted")
	}
	if command.agentSnapshotDaemonClient != nil || command.agentSnapshotContentStore != nil || command.agentSnapshotMetadataStore != nil || command.agentSnapshotCreator != nil || command.agentSnapshotLifecycle != nil || command.agentSnapshotHandlerFactory != nil {
		t.Fatal("typed-nil composition published partial components")
	}
}

func TestAgentSnapshotLifecycleComponentsAreEnabledTogether(t *testing.T) {
	lifecycle := &compositionLifecycle{}
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.GCInterval = 5 * time.Minute
	command.AgentSnapshots.RepairInterval = 10 * time.Minute
	command.agentSnapshotLifecycle = lifecycle

	components, err := command.agentSnapshotLifecycleComponents()
	if err != nil {
		t.Fatal(err)
	}
	command.agentSnapshotProjectionRegistry, _ = projection.NewRegistry(nil)
	if len(components) != 2 {
		t.Fatalf("component count before projection composition = %d, want 2", len(components))
	}

	components, err = command.agentSnapshotLifecycleComponents()
	if err != nil {
		t.Fatal(err)
	}
	if len(components) != 3 {
		t.Fatalf("component count = %d, want 3", len(components))
	}
	if components[0].Name != "agent_snapshot_gc" || components[0].Interval != 5*time.Minute {
		t.Fatalf("GC component = %#v", components[0])
	}
	if components[1].Name != "agent_snapshot_repair" || components[1].Interval != 10*time.Minute {
		t.Fatalf("repair component = %#v", components[1])
	}
	if components[2].Name != "agent_snapshot_projection" || components[2].Interval != time.Minute {
		t.Fatalf("projection component = %#v", components[2])
	}
	if err := components[0].Runnable.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := components[1].Runnable.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := components[2].Runnable.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lifecycle.collectCalls != 1 || lifecycle.repairCalls != 1 {
		t.Fatalf("lifecycle calls = collect:%d repair:%d", lifecycle.collectCalls, lifecycle.repairCalls)
	}

	command.AgentSnapshots.Enabled = false
	components, err = command.agentSnapshotLifecycleComponents()
	if err != nil || components != nil {
		t.Fatalf("disabled components = %#v, %v", components, err)
	}
}

func TestAgentSnapshotLifecycleRegistersResourceCaptureFinalizerAheadOfGC(t *testing.T) {
	finalizeCalls := 0
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.agentSnapshotLifecycle = &compositionLifecycle{}
	command.agentResourceCaptureFinalizer = component.RunFunc(func(context.Context) error {
		finalizeCalls++
		return nil
	})

	components, err := command.agentSnapshotLifecycleComponents()
	if err != nil {
		t.Fatal(err)
	}
	if len(components) != 3 || components[0].Name != "agent_resource_capture_finalizer" || components[0].Interval != 0 {
		t.Fatalf("resource capture finalizer component = %#v", components)
	}
	if err := components[0].Runnable.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if finalizeCalls != 1 {
		t.Fatalf("finalizer calls = %d", finalizeCalls)
	}
}

func TestAgentSnapshotLifecycleComponentsLogOnlyBoundedAggregateReports(t *testing.T) {
	secret := "sha256:" + strings.Repeat("deadbeef", 8) + " node=https://secret.internal"
	lifecycle := &compositionLifecycle{
		collectReport: snapshot.LifecycleReport{
			Scanned: 11, Deferred: 2, Failed: 1, StagesRemoved: 3,
			DigestsExpired: 4, LocationsDeleted: 5, LocationsAdded: 6, StalePruned: 7,
		},
		collectErr: errors.New(secret),
	}
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.agentSnapshotLifecycle = lifecycle

	components, err := command.agentSnapshotLifecycleComponents()
	if err != nil {
		t.Fatal(err)
	}
	logger := lagertest.NewTestLogger("snapshot-lifecycle")
	runErr := components[0].Runnable.Run(lagerctx.NewContext(context.Background(), logger))
	if runErr == nil {
		t.Fatal("failing lifecycle pass returned nil")
	}
	if strings.Contains(runErr.Error(), secret) || runErr.Error() != "agent snapshot lifecycle pass failed" {
		t.Fatalf("component-visible error was not fixed and redacted: %q", runErr)
	}

	logs := logger.Logs()
	if len(logs) != 1 {
		t.Fatalf("log count = %d, want 1", len(logs))
	}
	encoded := string(logs[0].ToJSON())
	if strings.Contains(encoded, secret) || strings.Contains(encoded, "deadbeef") || strings.Contains(encoded, "secret.internal") {
		t.Fatalf("lifecycle detail leaked into aggregate log: %s", encoded)
	}
	if logs[0].Message != "snapshot-lifecycle.agent-snapshot-lifecycle.pass-failed" ||
		logs[0].Data["operation"] != "collect" || fmt.Sprint(logs[0].Data["scanned"]) != "11" ||
		fmt.Sprint(logs[0].Data["failed"]) != "1" || fmt.Sprint(logs[0].Data["locations_added"]) != "6" {
		t.Fatalf("aggregate lifecycle log = %#v", logs[0])
	}
}
