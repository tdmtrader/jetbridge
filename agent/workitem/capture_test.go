package workitem_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workitem"
)

func completeRevision() workitem.Revision {
	return workitem.Revision{
		TicketID: 42, Revision: 8, UpdatedAt: time.Date(2026, 7, 22, 12, 34, 56, 123000000, time.UTC),
		Adapter: "jetbridge", ExternalID: "JIRA-42", Title: "Upgrade PostgreSQL", Body: "Move to 18.",
	}
}

func TestMarshalRevisionCapturesStrictCompleteWorkItem(t *testing.T) {
	captured, err := workitem.MarshalRevision(completeRevision())
	if err != nil {
		t.Fatal(err)
	}
	if captured.TicketID != 42 || captured.Revision != 8 || captured.CapturedAt != completeRevision().UpdatedAt {
		t.Fatalf("capture metadata = %+v", captured)
	}

	var document contracts.WorkItemDocument
	decoder := json.NewDecoder(bytes.NewReader(captured.Document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("strict work-item/v1 validation: %v", err)
	}
	if document.Revision != "8" || document.Body != "Move to 18." {
		t.Fatalf("document identity = %+v", document)
	}
	// The captured value is the authored content and nothing else: no lifecycle
	// state, no workflow selection, no spec/plan sub-document, no comments. State
	// and workflow belong to the durable run, which records which function ran
	// over which snapshot; spec/plan and comments mirrored tables that no longer
	// exist, and a work item's prose lives only in its body. DisallowUnknownFields
	// above already proves a re-read would reject each key, so this checks the
	// bytes the capture actually emitted.
	for _, key := range []string{`"state"`, `"workflow"`, `"spec"`, `"plan"`, `"comments"`} {
		if bytes.Contains(captured.Document, []byte(key)) {
			t.Fatalf("captured document carries %s: %s", key, captured.Document)
		}
	}
}

func TestCapturedRevisionRejectsMetadataThatDisagreesWithDocument(t *testing.T) {
	captured, err := workitem.MarshalRevision(completeRevision())
	if err != nil {
		t.Fatal(err)
	}
	captured.CapturedAt = captured.CapturedAt.Add(time.Second)
	if err := captured.Validate(); !errors.Is(err, workitem.ErrInvalidRevision) {
		t.Fatalf("Validate() = %v, want ErrInvalidRevision", err)
	}
}

type sourceStub struct {
	revision workitem.CapturedRevision
	found    bool
	err      error
}

func (source sourceStub) CaptureRevision(context.Context, int) (workitem.CapturedRevision, bool, error) {
	return source.revision.Clone(), source.found, source.err
}

type uploadStub struct {
	mu       sync.Mutex
	requests []snapshot.UploadRequest
	byKey    map[string]snapshot.Snapshot
}

func (stub *uploadStub) Upload(ctx context.Context, request snapshot.UploadRequest) (snapshot.Snapshot, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.requests = append(stub.requests, request.Clone())
	if existing, found := stub.byKey[request.IdempotencyKey]; found {
		return existing, nil
	}
	reader, err := request.OpenTar(ctx)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	header, err := tarReader.Next()
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	if header.Name != "work-item.json" || header.Typeflag != tar.TypeReg {
		return snapshot.Snapshot{}, io.ErrUnexpectedEOF
	}
	document, err := io.ReadAll(tarReader)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	var strict contracts.WorkItemDocument
	if err := json.Unmarshal(document, &strict); err != nil {
		return snapshot.Snapshot{}, err
	}
	manifest := snapshot.Snapshot{
		ID: 17, Type: "work-item/v1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ByteSize: int64(len(document)), FileCount: 1, Representation: "application/x-tar",
		ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now(),
	}
	stub.byKey[request.IdempotencyKey] = manifest
	return manifest, nil
}

func TestCapturerSealsThroughStandardUploadIdempotentlyByRevision(t *testing.T) {
	revision, err := workitem.MarshalRevision(completeRevision())
	if err != nil {
		t.Fatal(err)
	}
	uploader := &uploadStub{byKey: map[string]snapshot.Snapshot{}}
	capturer, err := workitem.NewCapturer(sourceStub{revision: revision, found: true}, uploader, workitem.Authority{
		TeamID: 1, TeamName: "main", Actor: "ticket-adapter", DisplayName: "ticket-adapter",
	})
	if err != nil {
		t.Fatal(err)
	}

	first, found, err := capturer.CaptureRevision(context.Background(), 42)
	if err != nil || !found {
		t.Fatalf("first capture = (%+v, %t, %v)", first, found, err)
	}
	second, found, err := capturer.CaptureRevision(context.Background(), 42)
	if err != nil || !found || second.Snapshot.ID != first.Snapshot.ID || second.Revision != 8 {
		t.Fatalf("replayed capture = (%+v, %t, %v)", second, found, err)
	}
	if len(uploader.requests) != 2 {
		t.Fatalf("upload calls = %d", len(uploader.requests))
	}
	for _, request := range uploader.requests {
		if request.IdempotencyKey != "work-item:42:revision:8" || request.Type != "work-item/v1" ||
			request.TeamID != 1 || request.TeamName != "main" || request.Actor != "ticket-adapter" {
			t.Fatalf("upload request = %+v", request)
		}
		var metadata map[string]any
		if err := json.Unmarshal(request.SourceMetadata, &metadata); err != nil || metadata["revision"] != float64(8) ||
			metadata["adapter"] != "jetbridge" || metadata["external_id"] != "JIRA-42" {
			t.Fatalf("source metadata = %s, %v", request.SourceMetadata, err)
		}
	}
}

func TestCapturerPreservesSourceCancellation(t *testing.T) {
	uploader := &uploadStub{byKey: map[string]snapshot.Snapshot{}}
	capturer, err := workitem.NewCapturer(sourceStub{err: context.Canceled}, uploader, workitem.Authority{
		TeamID: 1, TeamName: "main", Actor: "ticket-adapter", DisplayName: "ticket-adapter",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = capturer.CaptureRevision(context.Background(), 42)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, workitem.ErrCaptureFailed) {
		t.Fatalf("CaptureRevision() = %v, want cancellation joined with ErrCaptureFailed", err)
	}
}

func TestMemoryCaptureNeverTearsConcurrentTicketMutation(t *testing.T) {
	store := tickets.NewMemoryStore()
	id, err := store.Create(&tickets.Ticket{Title: "work", Body: "old-body", Repo: "repo", WorkflowName: "old-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	newBody, newTitle := "new-body", "new-title"
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		// Two captured fields mutated by one update: a capture that observed one
		// and not the other would be torn. The workflow selection used to serve
		// this role and no longer can — it is not captured any more.
		done <- store.Update(id, tickets.Update{Body: &newBody, Title: &newTitle})
	}()
	close(start)

	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		captured, found, err := store.CaptureRevision(context.Background(), id)
		if err != nil || !found {
			t.Fatalf("capture = (%+v, %t, %v)", captured, found, err)
		}
		var document contracts.WorkItemDocument
		if err := json.Unmarshal(captured.Document, &document); err != nil {
			t.Fatal(err)
		}
		pair := []string{document.Body, document.Title}
		if !reflect.DeepEqual(pair, []string{"old-body", "work"}) &&
			!reflect.DeepEqual(pair, []string{"new-body", "new-title"}) {
			t.Fatalf("torn capture at revision %d: %v", captured.Revision, pair)
		}
		seen[pair[0]] = true
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, _, err := store.CaptureRevision(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var document contracts.WorkItemDocument
	if err := json.Unmarshal(final.Document, &document); err != nil {
		t.Fatal(err)
	}
	if document.Body != "new-body" || document.Title != "new-title" || final.Revision != 2 {
		t.Fatalf("final revision = %+v, document = %+v, seen = %v", final, document, seen)
	}
}
