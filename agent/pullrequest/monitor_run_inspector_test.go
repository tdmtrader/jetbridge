package pullrequest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestDurableMonitorRunInspectorDoesNotInferSuccessFromRunStatus(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*MonitorRunEvidence)
		outcome MonitorOutcome
	}{
		{
			name: "review batch requires response",
			mutate: func(evidence *MonitorRunEvidence) {
				evidence.Publications = evidence.Publications[:2]
			},
			outcome: MonitorOutcomeAmbiguous,
		},
		{
			name: "conflict forbids response",
			mutate: func(evidence *MonitorRunEvidence) {
				evidence.ActionKind = ActionConflict
			},
			outcome: MonitorOutcomeAmbiguous,
		},
		{
			name: "freshness accepts branch and status",
			mutate: func(evidence *MonitorRunEvidence) {
				evidence.ActionKind = ActionFreshness
				evidence.Publications = evidence.Publications[:2]
			},
			outcome: MonitorOutcomePublished,
		},
		{
			name: "same head is an exact validated no-op",
			mutate: func(evidence *MonitorRunEvidence) {
				branch := evidence.Publications[0].PRAction.Branch
				branch.NewSourceSHA = branch.ExpectedSource.SHA
				evidence.Publications[0] = monitorPublication(
					t,
					*evidence.Publications[0].PRAction,
					publisher.Result{
						Status:     publisher.StatusSucceeded,
						ExternalID: branch.SourceRef,
						HeadSHA:    branch.NewSourceSHA,
						BaseSHA:    branch.ExpectedTargetSHA,
					},
				)
				status := evidence.Publications[1].PRAction.Status
				status.SourceSHA = branch.NewSourceSHA
				evidence.Publications[1] = monitorPublication(
					t,
					*evidence.Publications[1].PRAction,
					publisher.Result{
						Status:     publisher.StatusSucceeded,
						ExternalID: "status-1",
						URL:        "https://github.example/acme/widget/status/1",
						HeadSHA:    status.SourceSHA,
					},
				)
			},
			outcome: MonitorOutcomeValidatedNoop,
		},
		{
			name: "successful runtime without publications is ambiguous",
			mutate: func(evidence *MonitorRunEvidence) {
				evidence.Publications = nil
			},
			outcome: MonitorOutcomeAmbiguous,
		},
		{
			name: "duplicate publication is ambiguous",
			mutate: func(evidence *MonitorRunEvidence) {
				evidence.Publications = append(
					evidence.Publications,
					evidence.Publications[0].Clone(),
				)
			},
			outcome: MonitorOutcomeAmbiguous,
		},
		{
			name: "overflow beyond exact publication prefix is ambiguous",
			mutate: func(evidence *MonitorRunEvidence) {
				evidence.PublicationProofOverflow = true
			},
			outcome: MonitorOutcomeAmbiguous,
		},
		{
			name: "mismatched status head is ambiguous",
			mutate: func(evidence *MonitorRunEvidence) {
				status := evidence.Publications[1].PRAction.Status
				status.SourceSHA = monitorObjectID('d')
				evidence.Publications[1] = monitorPublication(
					t,
					*evidence.Publications[1].PRAction,
					publisher.Result{
						Status:     publisher.StatusSucceeded,
						ExternalID: "status-1",
						URL:        "https://github.example/acme/widget/status/1",
						HeadSHA:    status.SourceSHA,
					},
				)
			},
			outcome: MonitorOutcomeAmbiguous,
		},
		{
			name: "mismatched occurrence workflow is ambiguous",
			mutate: func(evidence *MonitorRunEvidence) {
				evidence.Publications[1].PRAction.Status.Authority.WorkflowRunID++
			},
			outcome: MonitorOutcomeAmbiguous,
		},
		{
			name: "failed branch proof is ambiguous",
			mutate: func(evidence *MonitorRunEvidence) {
				branch := evidence.Publications[0].PRAction.Branch
				evidence.Publications[0] = monitorPublication(
					t,
					*evidence.Publications[0].PRAction,
					publisher.Result{
						Status:  publisher.StatusFailed,
						HeadSHA: branch.NewSourceSHA,
						BaseSHA: branch.ExpectedTargetSHA,
						Detail:  "provider rejected the update",
					},
				)
			},
			outcome: MonitorOutcomeAmbiguous,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := monitorSucceededEvidence(t)
			test.mutate(&evidence)
			inspector, err := newTestDurableMonitorRunInspector(evidence)
			if err != nil {
				t.Fatal(err)
			}

			result, terminal, err := inspector.InspectMonitorRun(
				context.Background(), evidence.TeamID, evidence.WorkflowRunID,
			)
			if err != nil {
				t.Fatalf("InspectMonitorRun() error = %v", err)
			}
			if !terminal {
				t.Fatal("successful run was not terminal")
			}
			if result.Outcome != test.outcome {
				t.Fatalf("outcome = %q, want %q", result.Outcome, test.outcome)
			}
			if test.outcome == MonitorOutcomeAmbiguous &&
				result.AttentionReason == "" {
				t.Fatal("ambiguous proof has no bounded attention reason")
			}
		})
	}
}

func TestDurableMonitorRunInspectorKeepsObservedLeaseAndReportsExactReconciledHead(
	t *testing.T,
) {
	for _, test := range []struct {
		name               string
		publishedSourceSHA string
		wantOutcome        MonitorOutcome
		wantReconciledSHA  string
	}{
		{
			name:               "changed published head",
			publishedSourceSHA: monitorObjectID('c'),
			wantOutcome:        MonitorOutcomePublished,
			wantReconciledSHA:  monitorObjectID('c'),
		},
		{
			name:               "exact no-op head",
			publishedSourceSHA: monitorObjectID('a'),
			wantOutcome:        MonitorOutcomeValidatedNoop,
			wantReconciledSHA:  monitorObjectID('a'),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := monitorSucceededEvidence(t)
			evidence.ActionKind = ActionFreshness
			evidence.Publications = evidence.Publications[:2]
			branch := evidence.Publications[0].PRAction.Branch
			branch.NewSourceSHA = test.publishedSourceSHA
			evidence.Publications[0] = monitorPublication(
				t,
				*evidence.Publications[0].PRAction,
				publisher.Result{
					Status:     publisher.StatusSucceeded,
					ExternalID: branch.SourceRef,
					HeadSHA:    test.publishedSourceSHA,
					BaseSHA:    branch.ExpectedTargetSHA,
				},
			)
			status := evidence.Publications[1].PRAction.Status
			status.SourceSHA = test.publishedSourceSHA
			evidence.Publications[1] = monitorPublication(
				t,
				*evidence.Publications[1].PRAction,
				publisher.Result{
					Status:     publisher.StatusSucceeded,
					ExternalID: "status-1",
					URL:        "https://github.example/acme/widget/status/1",
					HeadSHA:    test.publishedSourceSHA,
				},
			)
			inspector, err := newTestDurableMonitorRunInspector(evidence)
			if err != nil {
				t.Fatal(err)
			}

			result, terminal, err := inspector.InspectMonitorRun(
				context.Background(), evidence.TeamID, evidence.WorkflowRunID,
			)
			if err != nil || !terminal {
				t.Fatalf("InspectMonitorRun() = (%+v, %t, %v)", result, terminal, err)
			}
			if result.Outcome != test.wantOutcome {
				t.Fatalf("outcome = %q, want %q", result.Outcome, test.wantOutcome)
			}
			if result.SourceSHA != monitorObjectID('a') {
				t.Fatalf(
					"lease source sha = %q, want observed %q",
					result.SourceSHA, monitorObjectID('a'),
				)
			}
			if result.ReconciledSourceSHA != test.wantReconciledSHA {
				t.Fatalf(
					"reconciled source sha = %q, want %q",
					result.ReconciledSourceSHA, test.wantReconciledSHA,
				)
			}
		})
	}
}

func TestDurableMonitorRunInspectorClassifiesExactRebaseAndLifecycleEvidence(
	t *testing.T,
) {
	for _, test := range []struct {
		name       string
		actionKind ActionKind
		configure  func(*MonitorRunEvidence)
		outcome    MonitorOutcome
	}{
		{
			name:       "rebase required",
			actionKind: ActionFreshness,
			configure: func(evidence *MonitorRunEvidence) {
				branch := evidence.Publications[0].PRAction.Branch
				evidence.Publications = []publisher.Publication{
					monitorPublication(
						t,
						*evidence.Publications[0].PRAction,
						publisher.Result{
							Status:  publisher.StatusRebaseRequired,
							HeadSHA: branch.NewSourceSHA,
							BaseSHA: branch.ExpectedTargetSHA,
							Detail:  "target moved before publication",
						},
					),
				}
			},
			outcome: MonitorOutcomeStale,
		},
		{
			name:       "completed",
			actionKind: ActionCompleted,
			configure: func(evidence *MonitorRunEvidence) {
				evidence.Publications = nil
			},
			outcome: MonitorOutcomeCompleted,
		},
		{
			name:       "abandoned",
			actionKind: ActionAbandoned,
			configure: func(evidence *MonitorRunEvidence) {
				evidence.Publications = nil
			},
			outcome: MonitorOutcomeAbandoned,
		},
		{
			name:       "terminal observation with mutation is ambiguous",
			actionKind: ActionCompleted,
			configure:  func(*MonitorRunEvidence) {},
			outcome:    MonitorOutcomeAmbiguous,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := monitorSucceededEvidence(t)
			evidence.ActionKind = test.actionKind
			test.configure(&evidence)
			inspector, err := newTestDurableMonitorRunInspector(evidence)
			if err != nil {
				t.Fatal(err)
			}

			result, terminal, err := inspector.InspectMonitorRun(
				context.Background(), evidence.TeamID, evidence.WorkflowRunID,
			)
			if err != nil || !terminal || result.Outcome != test.outcome {
				t.Fatalf(
					"InspectMonitorRun() = (%+v, %t, %v), want outcome %q",
					result, terminal, err, test.outcome,
				)
			}
		})
	}
}

func TestDurableMonitorRunInspectorRequiresExactSealedTerminalObservation(
	t *testing.T,
) {
	evidence := monitorSucceededEvidence(t)
	evidence.ActionKind = ActionCompleted
	evidence.Publications = nil
	body := monitorObservationBody(evidence)
	body.State = contracts.PullRequestActive
	body.Trigger = contracts.PullRequestFreshnessTrigger
	inspector, err := NewDurableMonitorRunInspector(
		&monitorRunEvidenceReader{evidence: evidence, found: true},
		&monitorObservationInspector{body: body},
	)
	if err != nil {
		t.Fatal(err)
	}

	result, terminal, err := inspector.InspectMonitorRun(
		context.Background(), evidence.TeamID, evidence.WorkflowRunID,
	)
	if err != nil || !terminal ||
		result.Outcome != MonitorOutcomeAmbiguous {
		t.Fatalf(
			"mismatched terminal observation = (%+v, %t, %v)",
			result, terminal, err,
		)
	}
}

func TestDurableMonitorRunInspectorRejectsAmbiguousReviewBatchProof(
	t *testing.T,
) {
	evidence := monitorSucceededEvidence(t)
	observation := monitorObservationBody(evidence)
	second := observation.ReviewBatches[0]
	second.ID = "batch-2"
	second.ReviewID = "review-2"
	observation.ReviewBatches = append(
		observation.ReviewBatches, second,
	)
	response := evidence.Publications[2].PRAction.Response
	response.Batch.ID = second.ID
	response.Batch.ReviewID = second.ReviewID
	response.Response.BatchID = second.ID
	evidence.Publications[2] = monitorPublication(
		t,
		*evidence.Publications[2].PRAction,
		publisher.Result{
			Status:     publisher.StatusSucceeded,
			ExternalID: "response-2",
			URL:        evidence.URL,
			HeadSHA:    response.Batch.CommitSHA,
		},
	)
	inspector, err := NewDurableMonitorRunInspector(
		&monitorRunEvidenceReader{evidence: evidence, found: true},
		&monitorObservationInspector{body: observation},
	)
	if err != nil {
		t.Fatal(err)
	}

	result, terminal, err := inspector.InspectMonitorRun(
		context.Background(), evidence.TeamID, evidence.WorkflowRunID,
	)
	if err != nil || !terminal ||
		result.Outcome != MonitorOutcomeAmbiguous {
		t.Fatalf(
			"ambiguous review batch proof = (%+v, %t, %v)",
			result, terminal, err,
		)
	}
}

func TestDurableMonitorRunInspectorAcceptsReviewOfAnEarlierPRHead(
	t *testing.T,
) {
	evidence := monitorSucceededEvidence(t)
	observation := monitorObservationBody(evidence)
	reviewedSHA := monitorObjectID('e')
	observation.ReviewBatches[0].CommitSHA = reviewedSHA
	response := evidence.Publications[2].PRAction.Response
	response.Batch.CommitSHA = reviewedSHA
	evidence.Publications[2] = monitorPublication(
		t,
		*evidence.Publications[2].PRAction,
		publisher.Result{
			Status:     publisher.StatusSucceeded,
			ExternalID: "response-1",
			URL:        evidence.URL,
			HeadSHA:    reviewedSHA,
		},
	)
	inspector, err := NewDurableMonitorRunInspector(
		&monitorRunEvidenceReader{evidence: evidence, found: true},
		&monitorObservationInspector{body: observation},
	)
	if err != nil {
		t.Fatal(err)
	}

	result, terminal, err := inspector.InspectMonitorRun(
		context.Background(), evidence.TeamID, evidence.WorkflowRunID,
	)
	if err != nil || !terminal ||
		result.Outcome != MonitorOutcomePublished {
		t.Fatalf(
			"earlier reviewed head = (%+v, %t, %v), want published",
			result, terminal, err,
		)
	}
}

func TestDurableMonitorRunInspectorUsesExactTerminalRunStatus(t *testing.T) {
	for _, test := range []struct {
		status   MonitorEvidenceRunStatus
		terminal bool
		want     MonitorRunStatus
	}{
		{status: MonitorEvidenceRunAdmitting},
		{status: MonitorEvidenceRunRunning},
		{status: MonitorEvidenceRunCanceling},
		{
			status: MonitorEvidenceRunFailed, terminal: true,
			want: MonitorRunFailed,
		},
		{
			status: MonitorEvidenceRunErrored, terminal: true,
			want: MonitorRunErrored,
		},
		{
			status: MonitorEvidenceRunAborted, terminal: true,
			want: MonitorRunAborted,
		},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			evidence := monitorSucceededEvidence(t)
			evidence.RunStatus = test.status
			inspector, err := newTestDurableMonitorRunInspector(evidence)
			if err != nil {
				t.Fatal(err)
			}

			result, terminal, err := inspector.InspectMonitorRun(
				context.Background(), evidence.TeamID, evidence.WorkflowRunID,
			)
			if err != nil || terminal != test.terminal {
				t.Fatalf(
					"InspectMonitorRun() = (%+v, %t, %v), want terminal %t",
					result, terminal, err, test.terminal,
				)
			}
			if terminal &&
				(result.RunStatus != test.want || result.Outcome != "") {
				t.Fatalf("terminal result = %+v, want status %q", result, test.want)
			}
		})
	}
}

func TestDurableMonitorRunInspectorIsTeamScopedAndPropagatesProjectionErrors(
	t *testing.T,
) {
	evidence := monitorSucceededEvidence(t)
	projectionErr := errors.New("projection unavailable")
	reader := &monitorRunEvidenceReader{
		evidence: evidence, found: true, err: projectionErr,
	}
	inspector, err := NewDurableMonitorRunInspector(
		reader,
		&monitorObservationInspector{
			body: monitorObservationBody(evidence),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := inspector.InspectMonitorRun(
		context.Background(), evidence.TeamID, evidence.WorkflowRunID,
	); !errors.Is(err, projectionErr) {
		t.Fatalf("projection error = %v, want %v", err, projectionErr)
	}

	reader.err = nil
	if _, _, err := inspector.InspectMonitorRun(
		context.Background(), evidence.TeamID+1, evidence.WorkflowRunID,
	); err == nil {
		t.Fatal("cross-team evidence was accepted")
	}
	if _, _, err := inspector.InspectMonitorRun(
		nil, evidence.TeamID, evidence.WorkflowRunID,
	); err == nil {
		t.Fatal("nil context was accepted")
	}
}

type monitorRunEvidenceReader struct {
	evidence MonitorRunEvidence
	found    bool
	err      error
}

type monitorObservationInspector struct {
	body contracts.PullRequestBody
	err  error
}

func (inspector *monitorObservationInspector) InspectMonitorObservation(
	_ context.Context,
	_ int,
	_ snapshot.SnapshotRef,
) (contracts.PullRequestBody, error) {
	return inspector.body, inspector.err
}

func newTestDurableMonitorRunInspector(
	evidence MonitorRunEvidence,
) (MonitorRunInspector, error) {
	return NewDurableMonitorRunInspector(
		&monitorRunEvidenceReader{evidence: evidence, found: true},
		&monitorObservationInspector{
			body: monitorObservationBody(evidence),
		},
	)
}

func monitorObservationBody(
	evidence MonitorRunEvidence,
) contracts.PullRequestBody {
	state := contracts.PullRequestActive
	trigger := contracts.PullRequestTrigger(evidence.ActionKind)
	mergeability := contracts.PullRequestMergeable
	switch evidence.ActionKind {
	case ActionConflict:
		mergeability = contracts.PullRequestConflicted
	case ActionCompleted:
		state = contracts.PullRequestCompleted
	case ActionAbandoned:
		state = contracts.PullRequestAbandoned
	}
	body := contracts.PullRequestBody{
		Provider:   string(evidence.Locator.Provider),
		Repository: evidence.Locator.Repository,
		ExternalID: evidence.Locator.ExternalID,
		URL:        evidence.URL, State: state,
		Mergeability: mergeability,
		SourceRef:    evidence.SourceRef, SourceSHA: evidence.SourceSHA,
		TargetRef: evidence.TargetRef, TargetSHA: evidence.TargetSHA,
		Iteration: "iteration-1", Trigger: trigger,
	}
	if evidence.ActionKind == ActionReviewBatch {
		body.ReviewBatches = []contracts.PullRequestReviewBatch{{
			ID: "batch-1", ReviewID: "review-1",
			CommitSHA: evidence.SourceSHA, Reviewer: "reviewer",
			Ready: true, ThreadIDs: []string{"thread-1"},
		}}
		body.Threads = []contracts.PullRequestThread{{
			ID: "thread-1", Iteration: "iteration-1",
		}}
	}
	return body
}

func (reader *monitorRunEvidenceReader) ReadMonitorRunEvidence(
	_ context.Context,
	_ int,
	_ snapshot.WorkflowRunID,
) (MonitorRunEvidence, bool, error) {
	return reader.evidence, reader.found, reader.err
}

func monitorSucceededEvidence(t *testing.T) MonitorRunEvidence {
	t.Helper()
	authority := publisher.Authority{
		TeamID: 17, TeamName: "engineering", WorkflowRunID: 91,
		BuildID: 42, Actor: "concourse",
	}
	observation := snapshot.SnapshotRef{
		ID: 501, Type: "pull-request/v1",
		Digest: monitorSnapshotDigest('1'),
	}
	candidate := snapshot.SnapshotRef{
		ID: 502, Type: "repository-change/v1",
		Digest: monitorSnapshotDigest('2'),
	}
	validation := snapshot.SnapshotRef{
		ID: 503, Type: "validation/v1",
		Digest: monitorSnapshotDigest('3'),
	}
	impact := snapshot.SnapshotRef{
		ID: 504, Type: "publish-impact/v1",
		Digest: monitorSnapshotDigest('4'),
	}
	response := snapshot.SnapshotRef{
		ID: 505, Type: "pull-request-response/v1",
		Digest: monitorSnapshotDigest('5'),
	}
	publicationEvidence := publisher.PublicationEvidence{
		Kind: publisher.EvidenceAcceptedReview,
		AcceptedReview: &publisher.AcceptedReviewEvidence{
			Review: snapshot.SnapshotRef{
				ID: 401, Type: "review/v1",
				Digest: monitorSnapshotDigest('6'),
			},
			Candidate: snapshot.SnapshotRef{
				ID: 402, Type: "repository/v1",
				Digest: monitorSnapshotDigest('7'),
			},
			Validation: snapshot.SnapshotRef{
				ID: 403, Type: "validation/v1",
				Digest: monitorSnapshotDigest('8'),
			},
			ReviewWorkflowRunID: 81, OutcomeRevision: 3,
			AcceptedBy: "reviewer",
			AcceptedAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
		},
	}
	locator := publisher.PRLocator{
		Provider:   publisher.PRProviderGitHub,
		Repository: "acme/widget", ExternalID: "42",
	}
	sourceSHA := monitorObjectID('a')
	targetSHA := monitorObjectID('b')
	resultSHA := monitorObjectID('c')
	destination := "github.example/acme/widget"
	policy := "engineering/v3"
	sourceRef := "refs/heads/agent/upgrade"
	targetRef := "refs/heads/main"
	branchAction := publisher.PRAction{
		Kind: publisher.OperationPublishPRBranch,
		Branch: &publisher.BranchPublicationRequest{
			Authority: authority, Observation: observation,
			Candidate: candidate, Validation: validation, Impact: impact,
			Evidence: publicationEvidence, Destination: destination,
			ApprovalPolicyVersion: policy, Locator: locator,
			SourceRef: sourceRef, TargetRef: targetRef,
			ExpectedSource: contracts.PullRequestHeadExpectation{
				Exists: true, SHA: sourceSHA,
			},
			ExpectedTargetSHA: targetSHA, NewSourceSHA: resultSHA,
		},
	}
	statusAction := publisher.PRAction{
		Kind: publisher.OperationPublishPRStatus,
		Status: &publisher.StatusPublicationRequest{
			Authority: authority, Observation: observation,
			Validation: validation, Evidence: publicationEvidence,
			Destination: destination, ApprovalPolicyVersion: policy,
			Locator: locator, TargetRef: targetRef, SourceSHA: resultSHA,
			State: "success", Description: "Jetbridge validation passed",
			TargetURL: "https://ci.example/runs/91",
		},
	}
	responseAction := publisher.PRAction{
		Kind: publisher.OperationRespondToReview,
		Response: &publisher.ResponsePublicationRequest{
			Authority: authority, Observation: observation,
			ResponseSnapshot: response, Evidence: publicationEvidence,
			Destination: destination, ApprovalPolicyVersion: policy,
			Locator: locator, TargetRef: targetRef,
			Batch: publisher.PRReviewBatch{
				ID: "batch-1", ReviewID: "review-1", CommitSHA: sourceSHA,
				Reviewer: "reviewer", Ready: true,
				ThreadIDs: []string{"thread-1"},
			},
			Response: contracts.PullRequestResponseBody{
				BatchID: "batch-1", Summary: "Addressed review.",
				Replies: []contracts.PullRequestThreadResponse{{
					ThreadID: "thread-1", Body: "Updated.",
				}},
			},
		},
	}
	return MonitorRunEvidence{
		TeamID: 17, BindingID: 9, BindingRevision: 8,
		WorkflowRunID: 91, ActionDigest: monitorDigest("d"),
		ReservationToken: "reservation-token",
		Observation:      observation, Cursor: "cursor-2",
		SourceSHA: sourceSHA, TargetSHA: targetSHA,
		ActionKind: ActionReviewBatch, RunStatus: MonitorEvidenceRunSucceeded,
		Locator: Locator{
			Provider:   ProviderGitHub,
			Repository: "acme/widget", ExternalID: "42",
		},
		URL:       "https://github.example/acme/widget/pull/42",
		SourceRef: sourceRef, TargetRef: targetRef,
		Destination: destination, ApprovalPolicyVersion: policy,
		Publications: []publisher.Publication{
			monitorPublication(
				t, branchAction,
				publisher.Result{
					Status:     publisher.StatusSucceeded,
					ExternalID: sourceRef, HeadSHA: resultSHA,
					BaseSHA: targetSHA,
				},
			),
			monitorPublication(
				t, statusAction,
				publisher.Result{
					Status:     publisher.StatusSucceeded,
					ExternalID: "status-1",
					URL:        "https://github.example/acme/widget/status/1",
					HeadSHA:    resultSHA,
				},
			),
			monitorPublication(
				t, responseAction,
				publisher.Result{
					Status:     publisher.StatusSucceeded,
					ExternalID: "response-1",
					URL:        "https://github.example/acme/widget/pull/42",
					HeadSHA:    sourceSHA,
				},
			),
		},
	}
}

func monitorPublication(
	t *testing.T,
	action publisher.PRAction,
	result publisher.Result,
) publisher.Publication {
	t.Helper()
	if err := action.ValidatePersisted(); err != nil {
		t.Fatalf("test PR action is invalid: %v", err)
	}
	key, err := action.OperationKey()
	if err != nil {
		t.Fatalf("test PR action key: %v", err)
	}
	now := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	id := snapshot.DatabaseID(1)
	switch action.Kind {
	case publisher.OperationPublishPRStatus:
		id = 2
	case publisher.OperationRespondToReview:
		id = 3
	}
	return publisher.Publication{
		ID: id, OperationKey: key, OperationKind: action.Kind,
		PRAction: &action, Status: result.Status, Attempt: 1, Result: result,
		CreatedAt: now, UpdatedAt: now,
	}
}

func monitorSnapshotDigest(character byte) snapshot.Digest {
	return snapshot.Digest(
		"sha256:" + strings.Repeat(string(character), 64),
	)
}

func monitorObjectID(character byte) string {
	return strings.Repeat(string(character), 40)
}
