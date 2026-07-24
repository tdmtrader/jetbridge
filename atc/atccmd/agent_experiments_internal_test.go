package atccmd

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc/db"
)

type experimentRunStoreStub struct {
	get       func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
	snapshots func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error)
}

func (store experimentRunStoreStub) Get(ctx context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return store.get(ctx, teamID, id)
}

func (store experimentRunStoreStub) Snapshots(ctx context.Context, id snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
	return store.snapshots(ctx, id)
}

func TestExperimentRunObserverScopesRunsAndReturnsOnlyOutputs(t *testing.T) {
	want := snapshot.SnapshotRef{
		ID: 17, Type: snapshot.TypeRef("review/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}
	store := experimentRunStoreStub{
		get: func(_ context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			if teamID != 7 || id != 11 {
				t.Fatalf("Get scope = %d/%s", teamID, id.String())
			}
			return db.AgentWorkflowRun{ID: id, TeamID: teamID, Status: db.AgentWorkflowRunStatusSucceeded}, true, nil
		},
		snapshots: func(_ context.Context, id snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{
				{WorkflowRunID: id, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "repo", Snapshot: want},
				{WorkflowRunID: id, Direction: db.AgentWorkflowRunSnapshotOutput, PortName: "review", Snapshot: want},
			}, nil
		},
	}
	observation, found, err := (experimentRunObserver{runs: store}).Inspect(context.Background(), 7, 11)
	if err != nil || !found || observation.Status != experiment.ObservedRunSucceeded {
		t.Fatalf("observation = %#v, %v, %v", observation, found, err)
	}
	if len(observation.Outputs) != 1 || observation.Outputs["review"] != want {
		t.Fatalf("outputs = %#v", observation.Outputs)
	}
}

func TestExperimentRunObserverClassifiesOnlyTrustedOutputMismatchAsContractFailure(t *testing.T) {
	message := workflowrun.OutputContractMismatchReason
	store := experimentRunStoreStub{
		get: func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{
				ID: id, TeamID: 7, Status: db.AgentWorkflowRunStatusFailed, ErrorMessage: message,
			}, true, nil
		},
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			t.Fatal("terminal failure should not inspect bindings")
			return nil, nil
		},
	}
	observation, found, err := (experimentRunObserver{runs: store}).Inspect(context.Background(), 7, 11)
	if err != nil || !found || observation.Status != experiment.ObservedRunFailed || observation.Failure != experiment.ObservedContractFailure {
		t.Fatalf("trusted mismatch observation = %#v, %v, %v", observation, found, err)
	}

	message = "selected build failed"
	observation, found, err = (experimentRunObserver{runs: store}).Inspect(context.Background(), 7, 11)
	if err != nil || !found || observation.Failure != experiment.ObservedPlatformFailure {
		t.Fatalf("ordinary failure observation = %#v, %v, %v", observation, found, err)
	}
}

type experimentMetadataStub struct {
	manifest snapshot.Snapshot
}

func (store experimentMetadataStub) GetAuthorized(_ context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
	return store.manifest, teamID == 7 && id == store.manifest.ID, nil
}

type experimentContentStub struct {
	archive []byte
}

func (store experimentContentStub) Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(store.archive)), nil
}

func TestExperimentMeasurementsReaderVerifiesManifestAndStrictContract(t *testing.T) {
	record, err := contracts.NewRecord(
		snapshot.TypeRef("measurements/v1"),
		nil,
		contracts.MeasurementsBody{
			Conclusion: "measured",
			Metrics: []contracts.Measurement{{
				ID: "quality", Value: 0.9, Unit: "ratio", Direction: "higher-is-better",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	archive, manifest := canonicalMeasurementsFixture(t, document)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reader := experimentMeasurementsReader{
		metadata:   experimentMetadataStub{manifest: manifest},
		content:    experimentContentStub{archive: archive},
		validators: registry,
		limits:     snapshot.ArchiveLimits{MaxContentBytes: 2 << 20, MaxEntries: 8},
		tempDir:    t.TempDir(),
	}
	t.Setenv("TMPDIR", t.TempDir()+"/missing")
	got, found, err := reader.ReadMeasurements(context.Background(), 7, manifest.ID)
	if err != nil || !found {
		t.Fatalf("ReadMeasurements = %#v, %v, %v", got, found, err)
	}
	if got.Conclusion != "measured" || len(got.Metrics) != 1 || got.Metrics[0].ID != "quality" {
		t.Fatalf("document = %#v", got)
	}

	badManifest := manifest
	badManifest.Digest = snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	reader.metadata = experimentMetadataStub{manifest: badManifest}
	if _, found, err := reader.ReadMeasurements(context.Background(), 7, badManifest.ID); err != nil || found {
		t.Fatalf("digest mismatch = found:%v err:%v", found, err)
	}
}

func canonicalMeasurementsFixture(t *testing.T, document []byte) ([]byte, snapshot.Snapshot) {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{Name: "record.json", Mode: 0600, Size: int64(len(document))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(document); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	tree, err := (snapshot.Canonicalizer{MaxContentBytes: 2 << 20, MaxEntries: 8}).Capture(context.Background(), bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return archive, snapshot.Snapshot{
		ID: 19, Type: snapshot.TypeRef("measurements/v1"), Digest: tree.Digest,
		ByteSize: tree.ByteSize, FileCount: tree.FileCount,
		Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
		CreatedAt: time.Now().UTC(),
	}
}

func TestValidateAgentExperimentsRequiresPositiveBoundsAndSnapshots(t *testing.T) {
	command := &RunCommand{}
	command.AgentExperiments.Enabled = true
	if err := command.validateAgentExperiments(); err == nil ||
		!strings.Contains(err.Error(), "runner-interval") ||
		!strings.Contains(err.Error(), "max-concurrency") ||
		!strings.Contains(err.Error(), "snapshot-enabled") {
		t.Fatalf("validation error = %v", err)
	}

	command.AgentSnapshots.Enabled = true
	command.AgentExperiments.Interval = 10 * time.Second
	command.AgentExperiments.MaxConcurrency = 4
	if err := command.validateAgentExperiments(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentExperimentBudgetConfigUsesTheCommandGlobalCap(t *testing.T) {
	command := &RunCommand{AgentDailyBudgetUSD: 42.75}

	config := command.agentExperimentBudgetConfig()

	if config.GlobalDailyCapUSD != command.AgentDailyBudgetUSD {
		t.Fatalf("global daily cap = %v, want %v", config.GlobalDailyCapUSD, command.AgentDailyBudgetUSD)
	}
	if config.Location != time.Local {
		t.Fatalf("location = %v, want time.Local", config.Location)
	}
	if config.Now == nil {
		t.Fatal("clock is nil")
	}
	before := time.Now()
	got := config.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("clock returned %v outside [%v, %v]", got, before, after)
	}
}
