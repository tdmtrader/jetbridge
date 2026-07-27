package workflowwaits_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/workflowwaits"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/agent/workflowwait/workflowwaittest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const largeWaitRunID snapshot.WorkflowRunID = 9007199254740993

var stableIdentity = workflowwaits.RequestIdentity{
	Actor:       "subject:sha256:" + strings.Repeat("a", 64),
	DisplayName: "Alice Example",
}

type runStoreStub struct {
	run   db.AgentWorkflowRun
	found bool
	err   error
}

func (store runStoreStub) Get(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return store.run, store.found, store.err
}

type manifestStoreStub struct {
	values map[snapshot.SnapshotID]snapshot.Snapshot
	err    error
}

func (store manifestStoreStub) GetAuthorized(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
	if store.err != nil {
		return snapshot.Snapshot{}, false, store.err
	}
	value, found := store.values[id]
	return value, found, nil
}

type contentStoreStub struct {
	values map[snapshot.SnapshotID][]byte
	err    error
}

func (store contentStoreStub) Open(_ context.Context, manifest snapshot.Snapshot) (io.ReadCloser, error) {
	if store.err != nil {
		return nil, store.err
	}
	value, found := store.values[manifest.ID]
	if !found {
		return nil, errors.New("missing content")
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}

type creatorStub struct {
	mu       sync.Mutex
	result   snapshot.Snapshot
	err      error
	requests []snapshot.UploadRequest
	archives [][]byte
}

func (creator *creatorStub) Upload(ctx context.Context, request snapshot.UploadRequest) (snapshot.Snapshot, error) {
	if creator.err != nil {
		return snapshot.Snapshot{}, creator.err
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
	creator.mu.Lock()
	defer creator.mu.Unlock()
	creator.requests = append(creator.requests, request.Clone())
	creator.archives = append(creator.archives, archive)
	return creator.result, nil
}

func (creator *creatorStub) count() int {
	creator.mu.Lock()
	defer creator.mu.Unlock()
	return len(creator.requests)
}

func TestHandlerListsExactSealedQuestionWithoutExecutionAuthority(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	fixture := seededWait(t, now)
	handler := fixture.handler(t, func(*http.Request) (workflowwaits.RequestIdentity, error) {
		return stableIdentity, nil
	})

	response := httptest.NewRecorder()
	handler.List(response, waitRequest(http.MethodGet, fixture.wait.ID.String(), ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
	var listed workflowwaits.ListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.WorkflowRunID != largeWaitRunID || len(listed.Waits) != 1 {
		t.Fatalf("list = %+v", listed)
	}
	visible := listed.Waits[0]
	if visible.ID != fixture.wait.ID || visible.Question != fixture.wait.Question ||
		visible.Prompt != "Ship this change?" || visible.Context != "Production deploy" ||
		len(visible.Options) != 2 || visible.Options[0] != "approve" ||
		visible.ExpectedType != "human-answer/v1" || visible.Status != workflowwait.StatusWaiting {
		t.Fatalf("visible wait = %+v", visible)
	}
	for _, forbidden := range []string{"build_id", "plan_id", "attempt", "workflow_definition_id", "default_snapshot_id"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response disclosed %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestHandlerOwnsAnswerSnapshotAndUsesStableActorWithDisplayAudit(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 123000000, time.UTC)
	fixture := seededWait(t, now)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	handler := fixture.handler(t, func(*http.Request) (workflowwaits.RequestIdentity, error) {
		return stableIdentity, nil
	})

	response := httptest.NewRecorder()
	handler.Resolve(response, waitRequest(http.MethodPut, fixture.wait.ID.String(), `{"answer":"approve"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
	var visible workflowwaits.PublicWait
	if err := json.Unmarshal(response.Body.Bytes(), &visible); err != nil {
		t.Fatal(err)
	}
	if visible.Status != workflowwait.StatusResolved || visible.Answer == nil ||
		visible.ResolvedBy != stableIdentity.Actor ||
		visible.ResolvedByDisplayName != stableIdentity.DisplayName {
		t.Fatalf("resolved = %+v", visible)
	}
	if fixture.creator.count() != 1 {
		t.Fatalf("uploads = %d", fixture.creator.count())
	}
	request := fixture.creator.requests[0]
	if request.Actor != stableIdentity.Actor || request.UploadedBy != stableIdentity.DisplayName ||
		request.IdempotencyKey != "workflow-wait-answer:7:9007199254740993:1" {
		t.Fatalf("upload authority = %+v", request)
	}
	document := decodeTarDocument[contracts.HumanAnswerDocument](t, fixture.creator.archives[0], "human-answer.json")
	if document.Answer != "approve" || document.AnsweredBy != stableIdentity.Actor ||
		document.AnsweredAt != now.Format(time.RFC3339Nano) || document.TimedOut {
		t.Fatalf("answer document = %+v", document)
	}

	// A transport retry reuses the durable intent and terminal answer; it does
	// not mint a second snapshot with a different timestamp.
	replay := httptest.NewRecorder()
	handler.Resolve(replay, waitRequest(http.MethodPut, fixture.wait.ID.String(), `{"answer":"approve"}`))
	if replay.Code != http.StatusOK || fixture.creator.count() != 1 {
		t.Fatalf("replay = %d/%s, uploads %d", replay.Code, replay.Body.String(), fixture.creator.count())
	}
}

func TestAcceptedAnswerIsRestartCompletedAfterEagerUploadFailure(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 123000000, time.UTC)
	fixture := seededWait(t, now)
	fixture.creator.err = errors.New("snapshot service restarted")
	handler := fixture.handler(t, func(*http.Request) (workflowwaits.RequestIdentity, error) {
		return stableIdentity, nil
	})

	response := httptest.NewRecorder()
	handler.Resolve(response, waitRequest(http.MethodPut, fixture.wait.ID.String(), `{"answer":"approve"}`))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial status/body = %d/%s", response.Code, response.Body.String())
	}
	pending, err := fixture.store.PendingResolutions(context.Background(), 7, largeWaitRunID, 100)
	if err != nil || len(pending) != 1 || pending[0].Intent.AnswerValue != "approve" ||
		pending[0].Intent.Actor != stableIdentity.Actor {
		t.Fatalf("durable accepted intent = (%+v, %v)", pending, err)
	}

	fixture.creator.err = nil
	completer, err := workflowwait.NewResolutionCompleter(fixture.store, fixture.creator)
	if err != nil {
		t.Fatal(err)
	}
	if err := completer.ReconcilePending(context.Background(), 7, atc.DefaultTeamName, largeWaitRunID); err != nil {
		t.Fatalf("restart completion: %v", err)
	}

	retry := httptest.NewRecorder()
	handler.Resolve(retry, waitRequest(http.MethodPut, fixture.wait.ID.String(), `{"answer":"approve"}`))
	if retry.Code != http.StatusOK || fixture.creator.count() != 1 {
		t.Fatalf("retry status/body/uploads = %d/%s/%d", retry.Code, retry.Body.String(), fixture.creator.count())
	}
	var visible workflowwaits.PublicWait
	if err := json.Unmarshal(retry.Body.Bytes(), &visible); err != nil {
		t.Fatal(err)
	}
	if visible.Status != workflowwait.StatusResolved || visible.ResolvedBy != stableIdentity.Actor {
		t.Fatalf("restart-completed wait = %+v", visible)
	}
}

func TestHandlerKeepsLegacySnapshotCompatibilityOnlyForStableMatchingActor(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for name, testCase := range map[string]struct {
		actor  string
		status int
	}{
		"matching stable actor": {actor: stableIdentity.Actor, status: http.StatusOK},
		"display name forgery":  {actor: stableIdentity.DisplayName, status: http.StatusBadRequest},
		"other subject":         {actor: "subject:sha256:" + strings.Repeat("b", 64), status: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := seededWait(t, now)
			answerManifest, answerArchive := sealedDocument(
				t,
				40,
				"human-answer/v1",
				"human-answer.json",
				contracts.HumanAnswerDocument{
					SchemaVersion: "1.0.0",
					Answer:        "approve",
					AnsweredBy:    testCase.actor,
					AnsweredAt:    now.Format(time.RFC3339),
				},
				now,
			)
			fixture.manifests.values[answerManifest.ID] = answerManifest
			fixture.content.values[answerManifest.ID] = answerArchive
			handler := fixture.handler(t, func(*http.Request) (workflowwaits.RequestIdentity, error) {
				return stableIdentity, nil
			})
			response := httptest.NewRecorder()
			handler.Resolve(
				response,
				waitRequest(http.MethodPut, fixture.wait.ID.String(), `{"snapshot_id":"40"}`),
			)
			if response.Code != testCase.status {
				t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
			}
			if testCase.status == http.StatusOK && fixture.creator.count() != 1 {
				t.Fatalf("legacy answer did not materialize one server-owned snapshot: %d", fixture.creator.count())
			}
		})
	}
}

func TestHandlerRejectsAnswersOutsideQuestionAndUntrustedBodies(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for name, body := range map[string]string{
		"unknown option": `{"answer":"merge"}`,
		"missing":        `{}`,
		"both forms":     `{"answer":"approve","snapshot_id":"40"}`,
		"unknown field":  `{"actor":"mallory"}`,
		"duplicate":      `{"answer":"approve","answer":"approve"}`,
		"trailing":       `{"answer":"approve"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := seededWait(t, now)
			handler := fixture.handler(t, func(*http.Request) (workflowwaits.RequestIdentity, error) {
				return stableIdentity, nil
			})
			response := httptest.NewRecorder()
			handler.Resolve(response, waitRequest(http.MethodPut, fixture.wait.ID.String(), body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHandlerBoundsDependencyErrorsAndRequiresVerifiedIdentity(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	fixture := seededWait(t, now)
	fixture.content.err = errors.New("object credentials: swordfish")
	handler := fixture.handler(t, func(*http.Request) (workflowwaits.RequestIdentity, error) {
		return stableIdentity, nil
	})
	response := httptest.NewRecorder()
	handler.Resolve(response, waitRequest(http.MethodPut, fixture.wait.ID.String(), `{"answer":"approve"}`))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "swordfish") {
		t.Fatalf("dependency status/body = %d/%s", response.Code, response.Body.String())
	}

	fixture = seededWait(t, now)
	handler = fixture.handler(t, func(*http.Request) (workflowwaits.RequestIdentity, error) {
		return workflowwaits.RequestIdentity{}, errors.New("no identity")
	})
	response = httptest.NewRecorder()
	handler.Resolve(response, waitRequest(http.MethodPut, fixture.wait.ID.String(), `{"answer":"approve"}`))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("identity status/body = %d/%s", response.Code, response.Body.String())
	}
}

type waitFixture struct {
	store     *workflowwaittest.MemoryStore
	wait      workflowwait.Wait
	manifests *manifestStoreStub
	content   *contentStoreStub
	creator   *creatorStub
}

func seededWait(t *testing.T, now time.Time) waitFixture {
	t.Helper()
	questionManifest, questionArchive := sealedDocument(
		t,
		38,
		"question/v1",
		"question.json",
		contracts.QuestionDocument{
			SchemaVersion: "1.0.0",
			Prompt:        "Ship this change?",
			Context:       "Production deploy",
			Options:       []string{"approve", "reject"},
			Default:       "reject",
		},
		now,
	)
	store := workflowwaittest.NewMemoryStore(func() time.Time { return now })
	defaultValue := snapshot.SnapshotRef{ID: 39, Type: "human-answer/v1", Digest: digest('d')}
	wait, _, err := store.CreateOrGet(context.Background(), workflowwait.CreateRequest{
		Key: workflowwait.ExecutionKey{
			TeamID:        7,
			WorkflowRunID: largeWaitRunID,
			BuildID:       23,
			PlanID:        "private-plan",
			Attempt:       "2.1",
			OutputName:    "answer",
		},
		QuestionName: "question",
		Question: snapshot.SnapshotRef{
			ID:     questionManifest.ID,
			Type:   questionManifest.Type,
			Digest: questionManifest.Digest,
		},
		ExpectedType:         "human-answer/v1",
		Deadline:             now.Add(time.Hour),
		TimeoutPolicy:        workflowwait.TimeoutDefault,
		Default:              &defaultValue,
		WorkflowPort:         "approval",
		WorkflowDefinitionID: 29,
	})
	if err != nil {
		t.Fatal(err)
	}
	return waitFixture{
		store: store,
		wait:  wait,
		manifests: &manifestStoreStub{
			values: map[snapshot.SnapshotID]snapshot.Snapshot{questionManifest.ID: questionManifest},
		},
		content: &contentStoreStub{
			values: map[snapshot.SnapshotID][]byte{questionManifest.ID: questionArchive},
		},
		creator: &creatorStub{
			result: manifest(50, "human-answer/v1", 'f', snapshot.ContentStateAvailable, now),
		},
	}
}

func (fixture waitFixture) handler(
	t *testing.T,
	identity workflowwaits.IdentityFunc,
) *workflowwaits.Handler {
	t.Helper()
	handler, err := workflowwaits.NewHandler(workflowwaits.Config{
		Team:          workflowwaits.TrustedTeam{ID: 7, Name: atc.DefaultTeamName},
		Identity:      identity,
		Runs:          runStoreStub{run: validRun(), found: true},
		Waits:         fixture.store,
		Manifests:     fixture.manifests,
		Content:       fixture.content,
		Creator:       fixture.creator,
		ArchiveLimits: snapshot.ArchiveLimits{MaxEntries: 16, MaxContentBytes: 2 << 20},
		TempDir:       t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func sealedDocument(
	t *testing.T,
	id snapshot.SnapshotID,
	typ snapshot.TypeRef,
	name string,
	document any,
	now time.Time,
) (snapshot.Snapshot, []byte) {
	t.Helper()
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	tree, err := (snapshot.Canonicalizer{MaxEntries: 16, MaxContentBytes: 2 << 20}).Capture(
		context.Background(),
		bytes.NewReader(raw.Bytes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tree.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	canonical, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Snapshot{
		ID:             id,
		Type:           typ,
		Digest:         tree.Digest,
		ByteSize:       tree.ByteSize,
		FileCount:      tree.FileCount,
		Representation: "application/x-tar",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt:      now,
	}, canonical
}

func decodeTarDocument[T any](t *testing.T, archive []byte, name string) T {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			t.Fatalf("%s not found", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != name {
			continue
		}
		var result T
		if err := json.NewDecoder(reader).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
}

func waitRequest(method, waitID, body string) *http.Request {
	request := httptest.NewRequest(method, "/", strings.NewReader(body))
	if method == http.MethodPut {
		request.Header.Set("Content-Type", "application/json")
	}
	request.URL.RawQuery = url.Values{
		":workflow_name":    {"deploy"},
		":workflow_run_id":  {largeWaitRunID.String()},
		":workflow_wait_id": {waitID},
	}.Encode()
	return request
}

func validRun() db.AgentWorkflowRun {
	return db.AgentWorkflowRun{
		ID:           largeWaitRunID,
		TeamID:       7,
		TeamName:     atc.DefaultTeamName,
		WorkflowName: "deploy",
	}
}

func manifest(
	id snapshot.SnapshotID,
	typ snapshot.TypeRef,
	byteValue byte,
	state snapshot.ContentState,
	now time.Time,
) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID:             id,
		Type:           typ,
		Digest:         digest(byteValue),
		ByteSize:       1,
		FileCount:      1,
		Representation: "application/x-tar",
		ContentState:   state,
		CreatedAt:      now,
	}
}

func digest(value byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(string(value), 64))
}
