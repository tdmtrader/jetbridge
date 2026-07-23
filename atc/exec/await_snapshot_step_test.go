package exec_test

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

type waitStoreStub struct {
	mu         sync.Mutex
	create     func(context.Context, workflowwait.CreateRequest) (workflowwait.Wait, bool, error)
	expire     func(context.Context, workflowwait.ExecutionKey, time.Time) (workflowwait.Wait, bool, error)
	createCall int
	expireCall int
}

func (stub *waitStoreStub) CreateOrGet(ctx context.Context, request workflowwait.CreateRequest) (workflowwait.Wait, bool, error) {
	stub.mu.Lock()
	stub.createCall++
	stub.mu.Unlock()
	return stub.create(ctx, request)
}

func (stub *waitStoreStub) Expire(ctx context.Context, key workflowwait.ExecutionKey, now time.Time) (workflowwait.Wait, bool, error) {
	stub.mu.Lock()
	stub.expireCall++
	stub.mu.Unlock()
	return stub.expire(ctx, key, now)
}

func TestAwaitSnapshotStepCreatesExactWaitAndPublishesResolvedAnswer(t *testing.T) {
	question := awaitManifest(21, "question/v1", 'a')
	answer := awaitManifest(22, "human-answer/v1", 'b')
	defaultAnswer := awaitManifest(23, "human-answer/v1", 'c')
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedCalls(func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		if id == question.ID {
			return question, true, nil
		}
		if id == defaultAnswer.ID {
			return defaultAnswer, true, nil
		}
		if id == answer.ID {
			return answer, true, nil
		}
		return snapshot.Snapshot{}, false, nil
	})
	content := new(snapshotfakes.FakeContentStore)
	content.OpenReturns(io.NopCloser(strings.NewReader("unused")), nil)
	repository, state, delegates := awaitHarness(t, question, content)

	var captured workflowwait.CreateRequest
	store := &waitStoreStub{}
	store.create = func(_ context.Context, request workflowwait.CreateRequest) (workflowwait.Wait, bool, error) {
		captured = request
		return waitFromRequest(31, request, workflowwait.StatusWaiting, nil), true, nil
	}
	store.expire = func(_ context.Context, _ workflowwait.ExecutionKey, _ time.Time) (workflowwait.Wait, bool, error) {
		ref := awaitRef(answer)
		wait := waitFromRequest(31, captured, workflowwait.StatusResolved, &ref)
		return wait, true, nil
	}

	plan := atc.AwaitSnapshotPlan{
		Name: "answer", Question: "question", Type: "human-answer/v1",
		OnTimeout: atc.AwaitSnapshotOnTimeoutDefault, DefaultSnapshotID: defaultAnswer.ID.String(),
		WorkflowPort: "approval", WorkflowDefinitionID: 7, WorkflowRunID: "19",
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	deadline, _ := ctx.Deadline()
	step := exec.NewAwaitSnapshotStep("plan-1", []int{2, 3}, plan,
		exec.StepMetadata{TeamID: 17, BuildID: 29}, delegates, store, nil, metadata, content, time.Millisecond)
	ok, err := step.Run(ctx, state)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, workflowwait.ExecutionKey{TeamID: 17, WorkflowRunID: 19, BuildID: 29, PlanID: "plan-1", Attempt: "2.3", OutputName: "answer"}, captured.Key)
	require.Equal(t, awaitRef(question), captured.Question)
	require.Equal(t, "question", captured.QuestionName)
	require.Equal(t, awaitRef(defaultAnswer), *captured.Default)
	require.WithinDuration(t, deadline, captured.Deadline, time.Millisecond)
	require.Equal(t, "approval", captured.WorkflowPort)
	require.Equal(t, 7, captured.WorkflowDefinitionID)
	entry, found := repository.ArtifactEntryFor("answer")
	require.True(t, found)
	require.Equal(t, awaitRef(answer), *entry.Snapshot)
	require.IsType(t, &runtime.SnapshotArtifact{}, entry.Artifact)
}

func TestAwaitSnapshotStepSynthesizesExactServerBoundMergeQuestion(t *testing.T) {
	subject := awaitManifest(21, "repository-change/v1", 'a')
	question := awaitManifest(22, "question/v1", 'b')
	answer := awaitManifest(23, "human-answer/v1", 'c')
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedCalls(func(_ context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		require.Equal(t, 17, teamID)
		switch id {
		case subject.ID:
			return subject, true, nil
		case answer.ID:
			return answer, true, nil
		default:
			return snapshot.Snapshot{}, false, nil
		}
	})
	content := new(snapshotfakes.FakeContentStore)
	repository, state, delegates := awaitHarness(t, subject, content)
	subjectRef := awaitRef(subject)
	subjectArtifact, err := runtime.NewSnapshotArtifact(subject, content)
	require.NoError(t, err)
	require.NoError(t, repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		"change": {Artifact: subjectArtifact, Snapshot: &subjectRef},
	}))

	parameters := map[string]string{"target_branch": "main", "expected_base_sha": strings.Repeat("d", 40)}
	plan := atc.AwaitSnapshotPlan{
		Name: "approval", Type: "human-answer/v1", OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
		MergeApproval: &atc.MergeApprovalIntent{
			Input: "change", Publisher: publisher.GitPublisher, Destination: "git.example/acme/widget",
			Parameters: parameters, ApprovalPolicyVersion: "engineering/v1", Prompt: "Merge this exact change?",
		},
		WorkflowDefinitionID: 7, WorkflowRunID: "19",
	}
	var sealedRequest snapshot.SealRequest
	sealer := &recordingOutputSealer{stub: func(_ context.Context, request snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
		sealedRequest = request.Clone()
		return map[string]snapshot.SealedOutput{
			"question": {Port: snapshot.Port{Name: "question", Type: "question/v1"}, Snapshot: awaitRef(question)},
		}, nil
	}}
	var waitRequest workflowwait.CreateRequest
	store := &waitStoreStub{}
	store.create = func(_ context.Context, request workflowwait.CreateRequest) (workflowwait.Wait, bool, error) {
		waitRequest = request
		answerRef := awaitRef(answer)
		return waitFromRequest(31, request, workflowwait.StatusResolved, &answerRef), true, nil
	}
	store.expire = func(context.Context, workflowwait.ExecutionKey, time.Time) (workflowwait.Wait, bool, error) {
		return workflowwait.Wait{}, false, errors.New("resolved wait must not poll")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	step := exec.NewAwaitSnapshotStep("plan-1", []int{2}, plan, exec.StepMetadata{
		TeamID: 17, TeamName: "main", BuildID: 29, SnapshotCreatedBy: "build:29",
	}, delegates, store, sealer, metadata, content, time.Millisecond)
	ok, err := step.Run(ctx, state)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 29, sealedRequest.BuildID)
	require.Equal(t, "await_snapshot", sealedRequest.StepKind)
	require.Equal(t, "approval", sealedRequest.StepName)
	require.Equal(t, []string{"change"}, sealedRequest.InputOrder)
	require.Equal(t, subjectRef, sealedRequest.Inputs["change"])
	require.Equal(t, 7, *sealedRequest.WorkflowDefinitionID)
	require.Equal(t, snapshot.WorkflowRunID(19), *sealedRequest.WorkflowRunID)
	require.Equal(t, awaitRef(question), waitRequest.Question)
	require.Equal(t, "approval-merge-question", waitRequest.QuestionName)

	stream, err := sealedRequest.Outputs[0].OpenTar(context.Background())
	require.NoError(t, err)
	defer stream.Close()
	archive := tar.NewReader(stream)
	header, err := archive.Next()
	require.NoError(t, err)
	require.Equal(t, "question.json", header.Name)
	var document contracts.QuestionDocument
	require.NoError(t, json.NewDecoder(archive).Decode(&document))
	require.Equal(t, []string{"approve", "reject"}, document.Options)
	require.Equal(t, "reject", document.Default)
	expectedContext, err := publisher.BuildMergeApprovalContext(publisher.MergeApprovalRequest{
		TeamID: 17, WorkflowRunID: 19, BuildID: 29, Input: subjectRef,
		Publisher: publisher.GitPublisher, Mode: publisher.ModeMerge,
		Destination: "git.example/acme/widget", Parameters: parameters,
		ExpectedBaseSHA: strings.Repeat("d", 40), ApprovalPolicyVersion: "engineering/v1",
	})
	require.NoError(t, err)
	var actualContext publisher.MergeApprovalContext
	require.NoError(t, json.Unmarshal([]byte(document.Context), &actualContext))
	require.Equal(t, expectedContext, actualContext)
}

func TestAwaitSnapshotStepResumesExistingWaitWithoutResettingDeadline(t *testing.T) {
	question := awaitManifest(21, "question/v1", 'a')
	answer := awaitManifest(22, "human-answer/v1", 'b')
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedCalls(func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		if id == question.ID {
			return question, true, nil
		}
		return answer, id == answer.ID, nil
	})
	content := new(snapshotfakes.FakeContentStore)
	repository, state, delegates := awaitHarness(t, question, content)

	originalDeadline := time.Now().UTC().Add(10 * time.Minute)
	store := &waitStoreStub{}
	store.create = func(_ context.Context, request workflowwait.CreateRequest) (workflowwait.Wait, bool, error) {
		request.Deadline = originalDeadline
		return waitFromRequest(31, request, workflowwait.StatusWaiting, nil), false, nil
	}
	store.expire = func(_ context.Context, key workflowwait.ExecutionKey, _ time.Time) (workflowwait.Wait, bool, error) {
		request := validWaitCreate(key, awaitRef(question), originalDeadline)
		ref := awaitRef(answer)
		return waitFromRequest(31, request, workflowwait.StatusResolved, &ref), true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	step := exec.NewAwaitSnapshotStep("plan-1", nil, atc.AwaitSnapshotPlan{
		Name: "answer", Question: "question", Type: "human-answer/v1", OnTimeout: atc.AwaitSnapshotOnTimeoutFail, WorkflowRunID: "19",
	}, exec.StepMetadata{TeamID: 17, BuildID: 29}, delegates, store, nil, metadata, content, time.Millisecond)
	ok, err := step.Run(ctx, state)
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, repository.AsMap(), build.ArtifactName("answer"))
	require.Equal(t, 1, store.createCall)
	require.GreaterOrEqual(t, store.expireCall, 1)
}

func TestAwaitSnapshotStepHandlesDurableTimeoutWinner(t *testing.T) {
	question := awaitManifest(21, "question/v1", 'a')
	content := new(snapshotfakes.FakeContentStore)
	_, state, delegates := awaitHarness(t, question, content)
	store := &waitStoreStub{}
	var request workflowwait.CreateRequest
	store.create = func(_ context.Context, value workflowwait.CreateRequest) (workflowwait.Wait, bool, error) {
		request = value
		return waitFromRequest(31, value, workflowwait.StatusWaiting, nil), true, nil
	}
	store.expire = func(_ context.Context, _ workflowwait.ExecutionKey, _ time.Time) (workflowwait.Wait, bool, error) {
		return waitFromRequest(31, request, workflowwait.StatusTimedOut, nil), true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(question, true, nil)
	step := exec.NewAwaitSnapshotStep("plan-1", nil, atc.AwaitSnapshotPlan{
		Name: "answer", Question: "question", Type: "human-answer/v1", OnTimeout: atc.AwaitSnapshotOnTimeoutFail, WorkflowRunID: "19",
	}, exec.StepMetadata{TeamID: 17, BuildID: 29}, delegates, store, nil, metadata, content, time.Millisecond)
	ok, err := step.Run(ctx, state)
	require.False(t, ok)
	var timedOut exec.AwaitSnapshotTimedOutError
	require.ErrorAs(t, err, &timedOut)
}

func TestAwaitSnapshotStepRejectsSpoofedQuestionAndMissingDeadlineBeforePersistence(t *testing.T) {
	question := awaitManifest(21, "question/v1", 'a')
	content := new(snapshotfakes.FakeContentStore)
	repository, state, delegates := awaitHarness(t, question, content)
	entry, _ := repository.ArtifactEntryFor("question")
	repository.RegisterArtifact("question", entry.Artifact, false)
	store := &waitStoreStub{create: func(context.Context, workflowwait.CreateRequest) (workflowwait.Wait, bool, error) {
		return workflowwait.Wait{}, false, errors.New("must not persist")
	}}
	step := exec.NewAwaitSnapshotStep("plan-1", nil, atc.AwaitSnapshotPlan{
		Name: "answer", Question: "question", Type: "human-answer/v1", OnTimeout: atc.AwaitSnapshotOnTimeoutFail, WorkflowRunID: "19",
	}, exec.StepMetadata{TeamID: 17, BuildID: 29}, delegates, store, nil, new(snapshotfakes.FakeMetadataStore), content, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ok, err := step.Run(ctx, state)
	require.False(t, ok)
	require.ErrorContains(t, err, "sealed input snapshot")
	require.Zero(t, store.createCall)
}

func awaitHarness(t *testing.T, question snapshot.Snapshot, content snapshot.ContentStore) (*build.Repository, *execfakes.FakeRunState, *execfakes.FakeBuildStepDelegateFactory) {
	t.Helper()
	repository := build.NewRepository()
	artifact, err := runtime.NewSnapshotArtifact(question, content)
	require.NoError(t, err)
	ref := awaitRef(question)
	require.NoError(t, repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		"question": {Artifact: artifact, Snapshot: &ref},
	}))
	state := new(execfakes.FakeRunState)
	state.ArtifactRepositoryReturns(repository)
	delegate := new(execfakes.FakeBuildStepDelegate)
	delegate.StartSpanCalls(func(ctx context.Context, _ string, _ tracing.Attrs) (context.Context, trace.Span) {
		return ctx, tracing.NoopSpan
	})
	delegates := new(execfakes.FakeBuildStepDelegateFactory)
	delegates.BuildStepDelegateReturns(delegate)
	return repository, state, delegates
}

func awaitManifest(id snapshot.SnapshotID, typ snapshot.TypeRef, digestByte byte) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID: id, Type: typ, Digest: snapshot.Digest("sha256:" + strings.Repeat(string(digestByte), 64)),
		ByteSize: 1, FileCount: 1, Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
		CreatedAt: time.Now().UTC(),
	}
}

func awaitRef(manifest snapshot.Snapshot) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
}

func waitFromRequest(id workflowwait.ID, request workflowwait.CreateRequest, status workflowwait.Status, answer *snapshot.SnapshotRef) workflowwait.Wait {
	now := time.Now().UTC()
	wait := workflowwait.Wait{
		ID: id, Key: request.Key, QuestionName: request.QuestionName, Question: request.Question,
		ExpectedType: request.ExpectedType, Deadline: request.Deadline, TimeoutPolicy: request.TimeoutPolicy,
		Default: request.Default, WorkflowPort: request.WorkflowPort, WorkflowDefinitionID: request.WorkflowDefinitionID,
		Status: status, Answer: answer, CreatedAt: now, UpdatedAt: now,
	}
	if status == workflowwait.StatusResolved {
		wait.ResolvedBy, wait.ResolutionSource, wait.ResolvedAt = "alice", "human", &now
	}
	if status == workflowwait.StatusTimedOut {
		wait.ResolvedBy, wait.ResolutionSource, wait.ResolvedAt = "system:timeout", "timeout", &now
	}
	return wait
}

func validWaitCreate(key workflowwait.ExecutionKey, question snapshot.SnapshotRef, deadline time.Time) workflowwait.CreateRequest {
	return workflowwait.CreateRequest{
		Key: key, QuestionName: "question", Question: question, ExpectedType: "human-answer/v1",
		Deadline: deadline, TimeoutPolicy: workflowwait.TimeoutFail,
	}
}
