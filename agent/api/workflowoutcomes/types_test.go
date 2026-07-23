package workflowoutcomes_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/snapshot"
)

const (
	largeRunID      snapshot.WorkflowRunID = 9007199254740993
	largeSnapshotID snapshot.SnapshotID    = 9007199254740995
)

func TestOutcomeValidatesGenericRunOutputIdentity(t *testing.T) {
	outcome := validOutcome()
	if err := outcome.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	payload, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(
		string(payload),
		`"workflow_run_id":"9007199254740993"`,
		`"output_snapshot_id":"9007199254740995"`,
		`"publication_id":"9007199254740999"`,
	) {
		t.Fatalf("IDs lost JSON precision: %s", payload)
	}

	invalid := []func(*workflowoutcomes.Outcome){
		func(value *workflowoutcomes.Outcome) { value.WorkflowRunID = 0 },
		func(value *workflowoutcomes.Outcome) { value.OutputSnapshotID = 0 },
		func(value *workflowoutcomes.Outcome) { value.Disposition = "maybe" },
		func(value *workflowoutcomes.Outcome) { value.PublicationState = "unknown" },
		func(value *workflowoutcomes.Outcome) {
			value.PublicationState = workflowoutcomes.PublicationPublished
			value.PublicationID = nil
		},
		func(value *workflowoutcomes.Outcome) {
			value.PublicationState = workflowoutcomes.PublicationNotRequested
		},
		func(value *workflowoutcomes.Outcome) { value.InterventionCount = -1 },
		func(value *workflowoutcomes.Outcome) { value.Actor = " " },
		func(value *workflowoutcomes.Outcome) { value.AuditedAt = time.Time{} },
		func(value *workflowoutcomes.Outcome) { value.Labels = []string{"quality", "quality"} },
		func(value *workflowoutcomes.Outcome) {
			id := snapshot.SnapshotID(8)
			value.ModificationSnapshotID = &id
			value.HumanModified = false
		},
	}
	for index, mutate := range invalid {
		candidate := outcome.Clone()
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid case %d accepted: %+v", index, candidate)
		}
	}
}

func TestMemoryStoreSupportsMultipleOutputsAndIdempotentIngestion(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := workflowoutcomes.NewMemoryStore(func() time.Time { return now })
	first := workflowoutcomes.RecordRequest{
		WorkflowRunID: largeRunID, OutputSnapshotID: largeSnapshotID,
		Disposition: workflowoutcomes.DispositionAccepted, PublicationState: workflowoutcomes.PublicationNotRequested,
		Labels: []string{"dogfood", "quality"}, Actor: "alice",
	}
	created, wasCreated, err := store.Record(context.Background(), 7, first)
	if err != nil || !wasCreated {
		t.Fatalf("first Record = (%+v, %t, %v)", created, wasCreated, err)
	}
	replayed, wasCreated, err := store.Record(context.Background(), 7, first)
	if err != nil || wasCreated || !reflect.DeepEqual(created, replayed) {
		t.Fatalf("replayed Record = (%+v, %t, %v), want exact existing", replayed, wasCreated, err)
	}

	other := first
	other.OutputSnapshotID = largeSnapshotID + 1
	other.Disposition = workflowoutcomes.DispositionRejected
	if _, wasCreated, err := store.Record(context.Background(), 7, other); err != nil || !wasCreated {
		t.Fatalf("other output Record = (%t, %v)", wasCreated, err)
	}
	listed, err := store.ListByRun(context.Background(), 7, largeRunID)
	if err != nil || len(listed) != 2 || listed[0].OutputSnapshotID != largeSnapshotID {
		t.Fatalf("ListByRun = (%+v, %v)", listed, err)
	}
	if _, found, err := store.Get(context.Background(), 8, largeRunID, largeSnapshotID); err != nil || found {
		t.Fatalf("cross-team Get = (%t, %v)", found, err)
	}
}

func TestMemoryStoreUpdatesAuditOnlyForSemanticChange(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := workflowoutcomes.NewMemoryStore(func() time.Time { return now })
	request := workflowoutcomes.RecordRequest{
		WorkflowRunID: 11, OutputSnapshotID: 12,
		Disposition: workflowoutcomes.DispositionAccepted, PublicationState: workflowoutcomes.PublicationNotRequested,
		Actor: "watcher",
	}
	first, _, err := store.Record(context.Background(), 3, request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	replayed, created, err := store.Record(context.Background(), 3, request)
	if err != nil || created || replayed.AuditedAt != first.AuditedAt {
		t.Fatalf("idempotent audit = (%+v, %t, %v)", replayed, created, err)
	}
	modification := snapshot.SnapshotID(13)
	request.Disposition = workflowoutcomes.DispositionMerged
	request.HumanModified = true
	request.ModificationSnapshotID = &modification
	request.InterventionCount = 2
	request.Actor = "bob"
	updated, created, err := store.Record(context.Background(), 3, request)
	if err != nil || created || updated.Revision != first.Revision+1 || updated.AuditedAt != now || updated.Actor != "bob" {
		t.Fatalf("updated Record = (%+v, %t, %v)", updated, created, err)
	}
}

func TestMemoryStoreRejectsCallerAuthoredPublicationEvidence(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(time.Now)
	publicationID := snapshot.DatabaseID(13)
	_, _, err := store.Record(context.Background(), 3, workflowoutcomes.RecordRequest{
		WorkflowRunID: 11, OutputSnapshotID: 12,
		Disposition: workflowoutcomes.DispositionMerged, PublicationState: workflowoutcomes.PublicationPublished,
		PublicationID: &publicationID, Actor: "member",
	})
	if !errors.Is(err, workflowoutcomes.ErrInvalidOutcome) {
		t.Fatalf("Record publication evidence = %v", err)
	}
}

func TestMemoryStoreConcurrentFirstWriteConverges(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(time.Now)
	request := workflowoutcomes.RecordRequest{
		WorkflowRunID: 21, OutputSnapshotID: 22,
		Disposition:      workflowoutcomes.DispositionAbandoned,
		PublicationState: workflowoutcomes.PublicationNotRequested, Actor: "alice",
	}
	const writers = 16
	var wait sync.WaitGroup
	wait.Add(writers)
	created := make(chan bool, writers)
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wait.Done()
			_, wasCreated, err := store.Record(context.Background(), 1, request)
			created <- wasCreated
			errs <- err
		}()
	}
	wait.Wait()
	close(created)
	close(errs)
	createdCount := 0
	for value := range created {
		if value {
			createdCount++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}
}

func TestMemoryStoreHonorsCancellationAndRejectsInvalidRequests(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(time.Now)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.Record(canceled, 1, workflowoutcomes.RecordRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Record = %v", err)
	}
	if _, _, err := store.Record(context.Background(), 0, workflowoutcomes.RecordRequest{}); !errors.Is(err, workflowoutcomes.ErrInvalidOutcome) {
		t.Fatalf("invalid Record = %v", err)
	}
}

func validOutcome() workflowoutcomes.Outcome {
	modification := snapshot.SnapshotID(9007199254740997)
	publication := snapshot.DatabaseID(9007199254740999)
	return workflowoutcomes.Outcome{
		WorkflowRunID: largeRunID, OutputSnapshotID: largeSnapshotID,
		Disposition: workflowoutcomes.DispositionMerged, PublicationState: workflowoutcomes.PublicationPublished,
		PublicationID: &publication, HumanModified: true, ModificationSnapshotID: &modification,
		InterventionCount: 1, Labels: []string{"quality"}, Actor: "alice", Revision: 2,
		AuditedAt: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
