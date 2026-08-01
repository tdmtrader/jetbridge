package occurrence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

type frozenSourceFake struct {
	rows      []db.AgentWorkflowRunNodeOccurrence
	requested []int64
	err       error
}

func (fake *frozenSourceFake) ForRun(_ context.Context, runID int64) ([]db.AgentWorkflowRunNodeOccurrence, error) {
	fake.requested = append(fake.requested, runID)
	if fake.err != nil {
		return nil, fake.err
	}
	return fake.rows, nil
}

type readerHarness struct {
	reader      *Reader
	run         db.AgentWorkflowRun
	frozen      *frozenSourceFake
	evidence    *evidenceSourceFake
	definitions *definitionSourceFake
}

func newReaderHarness(t *testing.T, seed string) *readerHarness {
	t.Helper()
	base := newHarness(t, seed)
	frozen := &frozenSourceFake{}
	reader, err := NewReader(frozen, base.evidence, base.definitions)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return &readerHarness{
		reader: reader, run: base.run, frozen: frozen,
		evidence: base.evidence, definitions: base.definitions,
	}
}

func (h *readerHarness) read(t *testing.T) []NodeOccurrence {
	t.Helper()
	occurrences, err := h.reader.OccurrencesForRun(context.Background(), h.run)
	if err != nil {
		t.Fatalf("OccurrencesForRun: %v", err)
	}
	return occurrences
}

func TestNewReaderRequiresEverySource(t *testing.T) {
	frozen := &frozenSourceFake{}
	evidence := &evidenceSourceFake{}
	definitions := &definitionSourceFake{}
	for name, construct := range map[string]func() (*Reader, error){
		"frozen":      func() (*Reader, error) { return NewReader(nil, evidence, definitions) },
		"evidence":    func() (*Reader, error) { return NewReader(frozen, nil, definitions) },
		"definitions": func() (*Reader, error) { return NewReader(frozen, evidence, nil) },
	} {
		if _, err := construct(); err == nil {
			t.Fatalf("%s: expected a construction error", name)
		}
	}
}

// A terminal run is served from immutable history. Deriving it live would lose
// deterministic task steps the moment build events are reclaimed.
func TestReaderServesTerminalRunsFromTheFrozenProjection(t *testing.T) {
	harness := newReaderHarness(t, "small-fix-v3")
	harness.run.Status = db.AgentWorkflowRunStatusSucceeded
	completed := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	started := completed.Add(-time.Minute)
	version := 2
	harness.frozen.rows = []db.AgentWorkflowRunNodeOccurrence{{
		WorkflowRunID: int64(harness.run.ID), NodeID: "implement", RetryAttempt: 2, Attempt: 3,
		TeamID: 1, WorkflowName: harness.run.WorkflowName, WorkflowDefinitionID: 41,
		WorkflowVersion: theRunsOwnVersion, NodeKind: "agent", PlanID: "p1",
		ReusableNodeName: "shared", ReusableNodeVersion: &version,
		Status: string(StatusSucceeded), StartedAt: &started, CompletedAt: &completed,
		DurationSeconds: 60, CostUSD: 1.25,
	}}

	occurrences := harness.read(t)

	if len(occurrences) != 1 {
		t.Fatalf("occurrences = %d, want 1", len(occurrences))
	}
	got := occurrences[0]
	if got.WorkflowRunID != snapshot.WorkflowRunID(harness.run.ID) || got.NodeID != "implement" {
		t.Fatalf("unexpected identity: %+v", got)
	}
	if got.RetryAttempt != 2 || got.Attempt != 3 {
		t.Fatalf("both attempt axes must survive the projection, got %+v", got)
	}
	if got.Status != StatusSucceeded || got.CostUSD != 1.25 || got.DurationSeconds != 60 {
		t.Fatalf("unexpected projection: %+v", got)
	}
	if got.ReusableNodeName != "shared" || got.ReusableNodeVersion != 2 {
		t.Fatalf("unexpected reusable-node provenance: %+v", got)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) ||
		got.CompletedAt == nil || !got.CompletedAt.Equal(completed) {
		t.Fatalf("unexpected timestamps: %+v", got)
	}
	if len(harness.evidence.runs) != 0 {
		t.Fatal("a frozen terminal run must not be re-derived from live evidence")
	}
}

// A run still executing has no frozen history yet, and the page's primary job
// is showing what needs attention NOW.
func TestReaderDerivesActiveRunsLive(t *testing.T) {
	harness := newReaderHarness(t, "small-fix-v3")
	harness.run.Status = db.AgentWorkflowRunStatusRunning
	harness.frozen.rows = []db.AgentWorkflowRunNodeOccurrence{{
		WorkflowRunID: int64(harness.run.ID), NodeID: "stale", RetryAttempt: 1, Attempt: 1,
		NodeKind: "agent", Status: string(StatusSucceeded),
	}}

	occurrences := harness.read(t)

	if len(harness.frozen.requested) != 0 {
		t.Fatal("an active run must not be answered from frozen history")
	}
	if len(harness.evidence.runs) != 1 {
		t.Fatalf("expected one live evidence read, got %d", len(harness.evidence.runs))
	}
	for _, entry := range occurrences {
		if entry.NodeID == "stale" {
			t.Fatal("the frozen row must not leak into a live derivation")
		}
	}
	if len(occurrences) == 0 {
		t.Fatal("expected the run's plan nodes to be projected")
	}
}

// Canceling is still executing. Treating it as terminal would read a frozen
// projection that does not exist yet and report an empty run.
func TestReaderTreatsCancelingAsActive(t *testing.T) {
	harness := newReaderHarness(t, "small-fix-v3")
	harness.run.Status = db.AgentWorkflowRunStatusCanceling

	harness.read(t)

	if len(harness.frozen.requested) != 0 {
		t.Fatal("a canceling run is not terminal")
	}
	if len(harness.evidence.runs) != 1 {
		t.Fatal("a canceling run must be derived live")
	}
}

// Every run sits in 'admitting' with no plan between creation and RecordPlan.
// Erroring there would 500 the whole overview the moment anyone starts a run.
func TestReaderReportsNoOccurrencesForARunWithNoPlanYet(t *testing.T) {
	harness := newReaderHarness(t, "small-fix-v3")
	harness.run.Status = db.AgentWorkflowRunStatusAdmitting
	harness.run.ActualPlan = nil

	occurrences, err := harness.reader.OccurrencesForRun(context.Background(), harness.run)
	if err != nil {
		t.Fatalf("a normally-admitting run must not be an error: %v", err)
	}
	if len(occurrences) != 0 {
		t.Fatalf("expected no occurrences, got %d", len(occurrences))
	}
	if len(harness.evidence.runs) != 0 {
		t.Fatal("there is nothing to gather evidence against")
	}
}

// A terminal run that predates the projection, or whose freeze failed, still
// has whatever live evidence survives. An empty answer would be worse.
func TestReaderFallsBackToLiveDerivationWhenNothingWasFrozen(t *testing.T) {
	harness := newReaderHarness(t, "small-fix-v3")
	harness.run.Status = db.AgentWorkflowRunStatusFailed

	occurrences := harness.read(t)

	if len(harness.frozen.requested) != 1 {
		t.Fatalf("the frozen projection must be consulted first, got %d reads", len(harness.frozen.requested))
	}
	if len(harness.evidence.runs) != 1 {
		t.Fatal("an unfrozen terminal run falls back to live derivation")
	}
	if len(occurrences) == 0 {
		t.Fatal("expected the run's plan nodes to be projected")
	}
}

func TestReaderResolvesTheRunsOwnWorkflowVersion(t *testing.T) {
	harness := newReaderHarness(t, "small-fix-v3")
	harness.run.Status = db.AgentWorkflowRunStatusRunning

	harness.read(t)

	want := definitionKey(harness.run.WorkflowName, theRunsOwnVersion)
	if len(harness.definitions.requested) != 1 || harness.definitions.requested[0] != want {
		t.Fatalf("definition lookups = %v, want exactly [%s]", harness.definitions.requested, want)
	}
}

func TestReaderPropagatesSourceFailures(t *testing.T) {
	t.Run("frozen", func(t *testing.T) {
		harness := newReaderHarness(t, "small-fix-v3")
		harness.run.Status = db.AgentWorkflowRunStatusSucceeded
		harness.frozen.err = errors.New("boom")
		if _, err := harness.reader.OccurrencesForRun(context.Background(), harness.run); err == nil {
			t.Fatal("expected the frozen read failure to propagate")
		}
	})
	t.Run("definitions", func(t *testing.T) {
		harness := newReaderHarness(t, "small-fix-v3")
		harness.run.Status = db.AgentWorkflowRunStatusRunning
		harness.definitions.err = errors.New("boom")
		if _, err := harness.reader.OccurrencesForRun(context.Background(), harness.run); err == nil {
			t.Fatal("expected the definition failure to propagate")
		}
	})
	t.Run("missing version", func(t *testing.T) {
		harness := newReaderHarness(t, "small-fix-v3")
		harness.run.Status = db.AgentWorkflowRunStatusRunning
		harness.run.WorkflowVersion = theRunsOwnVersion + 1
		if _, err := harness.reader.OccurrencesForRun(context.Background(), harness.run); err == nil {
			t.Fatal("expected a missing version to be an error, not an empty projection")
		}
	})
	t.Run("evidence", func(t *testing.T) {
		harness := newReaderHarness(t, "small-fix-v3")
		harness.run.Status = db.AgentWorkflowRunStatusRunning
		harness.evidence.err = errors.New("boom")
		if _, err := harness.reader.OccurrencesForRun(context.Background(), harness.run); err == nil {
			t.Fatal("expected the evidence failure to propagate")
		}
	})
}
