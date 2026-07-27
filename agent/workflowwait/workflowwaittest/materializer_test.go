package workflowwaittest_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/agent/workflowwait/workflowwaittest"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type answerCreatorStub struct {
	mu       sync.Mutex
	manifest snapshot.Snapshot
	failures int
	requests []snapshot.UploadRequest
	archives [][]byte
}

func (creator *answerCreatorStub) Upload(ctx context.Context, request snapshot.UploadRequest) (snapshot.Snapshot, error) {
	creator.mu.Lock()
	defer creator.mu.Unlock()
	if creator.failures > 0 {
		creator.failures--
		return snapshot.Snapshot{}, errors.New("upload unavailable")
	}
	reader, err := request.OpenTar(ctx)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	archive, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return snapshot.Snapshot{}, err
	}
	creator.requests = append(creator.requests, request.Clone())
	creator.archives = append(creator.archives, archive)
	return creator.manifest, nil
}

func TestResolutionCompleterRecoversAcceptedIntentAfterFailureAndDeadline(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 123000000, time.UTC)
	store := workflowwaittest.NewMemoryStore(func() time.Time { return now })
	wait, _, err := store.CreateOrGet(context.Background(), validCreateRequest(now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	_, intent, found, err := store.ReserveResolution(context.Background(), workflowwait.ReserveResolutionRequest{
		TeamID: wait.Key.TeamID, WorkflowRunID: wait.Key.WorkflowRunID, WaitID: wait.ID,
		AnswerValue: "approve", Actor: "subject:sha256:alice", DisplayName: "Alice",
	})
	if err != nil || !found {
		t.Fatalf("reserve = (found %t, err %v)", found, err)
	}
	now = wait.Deadline.Add(time.Hour)
	creator := &answerCreatorStub{
		manifest: validAnswerManifest(81, now),
		failures: 1,
	}
	completer, err := workflowwait.NewResolutionCompleter(store, creator)
	if err != nil {
		t.Fatal(err)
	}

	if err := completer.ReconcilePending(context.Background(), 17, "main", 19); err == nil {
		t.Fatal("first upload failure was hidden")
	}
	stillWaiting, _, _ := store.Get(context.Background(), 17, 19, wait.ID)
	if stillWaiting.Status != workflowwait.StatusWaiting {
		t.Fatalf("failed materialization changed wait to %s", stillWaiting.Status)
	}
	if err := completer.ReconcilePending(context.Background(), 17, "main", 19); err != nil {
		t.Fatalf("restart completion: %v", err)
	}
	resolved, found, err := store.Get(context.Background(), 17, 19, wait.ID)
	if err != nil || !found || resolved.Status != workflowwait.StatusResolved || resolved.Answer == nil ||
		resolved.ResolvedBy != intent.Actor || resolved.ResolvedByDisplayName != intent.DisplayName {
		t.Fatalf("resolved = (%+v, %t, %v)", resolved, found, err)
	}
	if len(creator.requests) != 1 ||
		creator.requests[0].IdempotencyKey != "workflow-wait-answer:17:19:"+wait.ID.String() ||
		creator.requests[0].Actor != intent.Actor ||
		creator.requests[0].UploadedBy != intent.DisplayName {
		t.Fatalf("upload requests = %+v", creator.requests)
	}
	document := answerDocumentFromTar(t, creator.archives[0])
	if document.Answer != intent.AnswerValue || document.AnsweredBy != intent.Actor ||
		document.AnsweredAt != intent.ReservedAt.Format(time.RFC3339Nano) {
		t.Fatalf("document = %+v", document)
	}
	if err := completer.ReconcilePending(context.Background(), 17, "main", 19); err != nil {
		t.Fatalf("terminal replay: %v", err)
	}
	if len(creator.requests) != 1 {
		t.Fatalf("terminal replay uploaded %d times", len(creator.requests))
	}
}

func validAnswerManifest(id snapshot.SnapshotID, now time.Time) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID: id, Type: "human-answer/v1",
		Digest:         snapshot.Digest("sha256:" + repeatByte('a', 64)),
		ByteSize:       1,
		FileCount:      1,
		Representation: "application/x-tar",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt:      now,
	}
}

func answerDocumentFromTar(t *testing.T, archive []byte) contracts.HumanAnswerDocument {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(archive))
	header, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "human-answer.json" {
		t.Fatalf("archive entry = %q", header.Name)
	}
	var document contracts.HumanAnswerDocument
	if err := json.NewDecoder(reader).Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}
