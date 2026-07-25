package publisher_test

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

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/stretchr/testify/require"
)

type waitListerFunc func(context.Context, int, snapshot.WorkflowRunID) ([]workflowwait.Wait, error)

func (function waitListerFunc) List(ctx context.Context, teamID int, runID snapshot.WorkflowRunID) ([]workflowwait.Wait, error) {
	return function(ctx, teamID, runID)
}

func TestDurableApprovalVerifierBindsExactAffirmativeHumanDecision(t *testing.T) {
	fixture := newApprovalFixture(t, nil, nil)
	t.Setenv("TMPDIR", t.TempDir()+"/missing")

	evidence, err := fixture.verifier.Verify(context.Background(), fixture.request)
	require.NoError(t, err)
	require.Equal(t, publisher.ApprovalEvidence{
		WaitID: fixture.wait.ID, Question: fixture.wait.Question, Answer: fixture.request.Approval,
		ResolvedBy: fixture.wait.ResolvedBy, ResolvedAt: fixture.wait.ResolvedAt.UTC(),
	}, evidence)
	require.Equal(t, 2, fixture.metadata.GetAuthorizedCallCount())
	require.Equal(t, 2, fixture.content.OpenCallCount())
}

func TestDurableApprovalVerifierRejectsDecisionThatDoesNotBindPublication(t *testing.T) {
	tests := map[string]func(*approvalFixture){
		"destination": func(fixture *approvalFixture) { fixture.request.Destination = "git.example/other/repo" },
		"base": func(fixture *approvalFixture) {
			fixture.request.ExpectedBaseSHA = strings.Repeat("e", 40)
			fixture.request.Parameters["expected_base_sha"] = strings.Repeat("e", 40)
		},
		// The assertion is server-derived, so a durable request always carries
		// it in both the field and the parameter map, and they always agree.
		// Verification refuses anything else rather than treating an absent
		// assertion as "unspecified".
		"missing derived base": func(fixture *approvalFixture) {
			fixture.request.ExpectedBaseSHA = ""
			delete(fixture.request.Parameters, "expected_base_sha")
		},
		"derived base disagrees with its parameter": func(fixture *approvalFixture) {
			fixture.request.Parameters["expected_base_sha"] = strings.Repeat("e", 40)
		},
		"policy": func(fixture *approvalFixture) { fixture.request.ApprovalPolicyVersion = "engineering/v2" },
		"target branch": func(fixture *approvalFixture) {
			fixture.request.Parameters["target_branch"] = "release"
		},
		"extra parameter": func(fixture *approvalFixture) {
			fixture.request.Parameters["provider_semantics"] = "changed"
		},
		"input": func(fixture *approvalFixture) {
			fixture.request.Input.Digest = snapshot.Digest("sha256:" + strings.Repeat("f", 64))
		},
		"workflow run": func(fixture *approvalFixture) { fixture.request.WorkflowRunID = 18 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newApprovalFixture(t, nil, nil)
			mutate(fixture)
			_, err := fixture.verifier.Verify(context.Background(), fixture.request)
			require.ErrorIs(t, err, publisher.ErrInvalidRequest)
		})
	}
}

func TestDurableApprovalVerifierRejectsAnythingExceptExactFailClosedHumanApprove(t *testing.T) {
	tests := []struct {
		name     string
		question func(*contracts.QuestionDocument)
		answer   func(*contracts.HumanAnswerDocument)
		wait     func(*workflowwait.Wait)
	}{
		{name: "rejection", answer: func(document *contracts.HumanAnswerDocument) { document.Answer = "reject" }},
		{name: "timeout", answer: func(document *contracts.HumanAnswerDocument) { document.TimedOut = true; document.Answer = "" }},
		{name: "actor mismatch", answer: func(document *contracts.HumanAnswerDocument) { document.AnsweredBy = "other" }},
		{name: "approve default", question: func(document *contracts.QuestionDocument) { document.Default = "approve" }},
		{name: "missing reject option", question: func(document *contracts.QuestionDocument) { document.Options = []string{"approve"} }},
		{name: "non-human resolution", wait: func(wait *workflowwait.Wait) { wait.ResolutionSource = "timeout" }},
		{name: "unresolved wait", wait: func(wait *workflowwait.Wait) { wait.Status = workflowwait.StatusWaiting }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalFixture(t, test.question, test.answer)
			if test.wait != nil {
				test.wait(&fixture.wait)
				fixture.setWaits([]workflowwait.Wait{fixture.wait})
			}
			_, err := fixture.verifier.Verify(context.Background(), fixture.request)
			require.ErrorIs(t, err, publisher.ErrInvalidRequest)
		})
	}
}

func TestDurableApprovalVerifierRejectsAmbiguousOrCorruptEvidence(t *testing.T) {
	t.Run("ambiguous", func(t *testing.T) {
		fixture := newApprovalFixture(t, nil, nil)
		duplicate := fixture.wait
		duplicate.ID++
		fixture.setWaits([]workflowwait.Wait{fixture.wait, duplicate})
		_, err := fixture.verifier.Verify(context.Background(), fixture.request)
		require.ErrorIs(t, err, publisher.ErrInvalidRequest)
	})

	t.Run("content does not match manifest", func(t *testing.T) {
		fixture := newApprovalFixture(t, nil, nil)
		fixture.archives[fixture.wait.Question.ID] = fixture.archives[fixture.request.Approval.ID]
		_, err := fixture.verifier.Verify(context.Background(), fixture.request)
		require.ErrorIs(t, err, publisher.ErrInvalidRequest)
	})
}

type approvalFixture struct {
	verifier *publisher.DurableApprovalVerifier
	request  publisher.MergeApprovalRequest
	wait     workflowwait.Wait
	metadata *snapshotfakes.FakeMetadataStore
	content  *snapshotfakes.FakeContentStore
	archives map[snapshot.SnapshotID][]byte
	waits    *[]workflowwait.Wait
}

func (fixture *approvalFixture) setWaits(waits []workflowwait.Wait) {
	*fixture.waits = waits
}

func newApprovalFixture(
	t *testing.T,
	mutateQuestion func(*contracts.QuestionDocument),
	mutateAnswer func(*contracts.HumanAnswerDocument),
) *approvalFixture {
	t.Helper()
	input := snapshot.SnapshotRef{
		ID: 41, Type: snapshot.TypeRef("repository-change/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}
	request := publisher.MergeApprovalRequest{
		TeamID: 9, WorkflowRunID: 17, BuildID: 12, Input: input,
		Publisher: publisher.GitPublisher, Mode: publisher.ModeMerge,
		Destination: "git.example/acme/widget",
		// expected_base_sha is not authored anywhere: the exec steps stamp it
		// from Input's sealed base_sha. It appears here as the value a server
		// would have derived.
		Parameters: map[string]string{
			"target_branch": "main", "expected_base_sha": strings.Repeat("b", 40),
		},
		ExpectedBaseSHA: strings.Repeat("b", 40), ApprovalPolicyVersion: "engineering/v1",
	}
	approvalEnvelope, err := publisher.BuildMergeApprovalContext(request)
	require.NoError(t, err)
	approvalContext, err := json.Marshal(approvalEnvelope)
	require.NoError(t, err)
	question := contracts.QuestionDocument{
		SchemaVersion: "1.0.0", Prompt: "Merge this exact change?", Context: string(approvalContext),
		Options: []string{"approve", "reject"}, Default: "reject",
	}
	answer := contracts.HumanAnswerDocument{
		SchemaVersion: "1.0.0", Answer: "approve", AnsweredBy: "reviewer",
		AnsweredAt: "2026-07-22T12:00:00Z",
	}
	if mutateQuestion != nil {
		mutateQuestion(&question)
	}
	if mutateAnswer != nil {
		mutateAnswer(&answer)
	}
	questionManifest, questionArchive := approvalDocumentSnapshot(t, 42, "question/v1", "question.json", question)
	answerManifest, answerArchive := approvalDocumentSnapshot(t, 43, "human-answer/v1", "human-answer.json", answer)
	questionRef := snapshot.SnapshotRef{ID: questionManifest.ID, Type: questionManifest.Type, Digest: questionManifest.Digest}
	answerRef := snapshot.SnapshotRef{ID: answerManifest.ID, Type: answerManifest.Type, Digest: answerManifest.Digest}
	request.Approval = answerRef
	resolvedAt := time.Date(2026, 7, 22, 12, 0, 1, 0, time.UTC)
	wait := workflowwait.Wait{
		ID: 7,
		Key: workflowwait.ExecutionKey{
			TeamID: 9, WorkflowRunID: 17, BuildID: 12, PlanID: "approval", Attempt: "1", OutputName: "approval",
		},
		QuestionName: "merge-approval", Question: questionRef, ExpectedType: "human-answer/v1",
		Deadline: resolvedAt.Add(time.Hour), TimeoutPolicy: workflowwait.TimeoutFail,
		Status: workflowwait.StatusResolved, Answer: &answerRef, ResolvedBy: "reviewer", ResolutionSource: "human",
		CreatedAt: resolvedAt.Add(-time.Minute), UpdatedAt: resolvedAt, ResolvedAt: &resolvedAt,
	}
	waits := []workflowwait.Wait{wait}
	lister := waitListerFunc(func(_ context.Context, teamID int, runID snapshot.WorkflowRunID) ([]workflowwait.Wait, error) {
		if teamID != 9 || runID != 17 {
			return nil, nil
		}
		return append([]workflowwait.Wait(nil), waits...), nil
	})
	manifests := map[snapshot.SnapshotID]snapshot.Snapshot{
		questionManifest.ID: questionManifest,
		answerManifest.ID:   answerManifest,
	}
	metadata := &snapshotfakes.FakeMetadataStore{}
	metadata.GetAuthorizedCalls(func(_ context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		require.Equal(t, 9, teamID)
		manifest, found := manifests[id]
		return manifest, found, nil
	})
	archives := map[snapshot.SnapshotID][]byte{
		questionManifest.ID: questionArchive,
		answerManifest.ID:   answerArchive,
	}
	content := &snapshotfakes.FakeContentStore{}
	content.OpenStub = func(_ context.Context, manifest snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archives[manifest.ID])), nil
	}
	verifier, err := publisher.NewDurableApprovalVerifier(
		lister,
		metadata,
		content,
		publisher.WithApprovalCanonicalizer(snapshot.Canonicalizer{TempDir: t.TempDir()}),
	)
	require.NoError(t, err)
	return &approvalFixture{
		verifier: verifier,
		request:  request,
		wait:     wait, metadata: metadata, content: content, archives: archives, waits: &waits,
	}
}

func approvalDocumentSnapshot(t *testing.T, id snapshot.SnapshotID, typeRef snapshot.TypeRef, name string, document any) (snapshot.Snapshot, []byte) {
	t.Helper()
	payload, err := json.Marshal(document)
	require.NoError(t, err)
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	require.NoError(t, writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(payload)), Typeflag: tar.TypeReg}))
	_, err = writer.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	tree, err := (snapshot.Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw.Bytes()))
	require.NoError(t, err)
	defer tree.Close()
	archive, err := os.ReadFile(tree.ArchivePath)
	require.NoError(t, err)
	return snapshot.Snapshot{
		ID: id, Type: typeRef, Digest: tree.Digest, ByteSize: tree.ByteSize, FileCount: tree.FileCount,
		Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
		CreatedAt: time.Now().UTC(),
	}, archive
}
