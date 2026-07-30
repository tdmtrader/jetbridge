package pullrequest

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const monitorAmbiguousPublicationReason = "monitor run lacks exact durable PR publication evidence"

// MonitorEvidenceRunStatus is the exact durable workflow-run lifecycle read
// by MonitorRunEvidenceReader. It is deliberately wider than MonitorRunStatus:
// nonterminal states must remain distinguishable from a missing run.
type MonitorEvidenceRunStatus string

const (
	MonitorEvidenceRunAdmitting MonitorEvidenceRunStatus = "admitting"
	MonitorEvidenceRunRunning   MonitorEvidenceRunStatus = "running"
	MonitorEvidenceRunCanceling MonitorEvidenceRunStatus = "canceling"
	MonitorEvidenceRunSucceeded MonitorEvidenceRunStatus = "succeeded"
	MonitorEvidenceRunFailed    MonitorEvidenceRunStatus = "failed"
	MonitorEvidenceRunErrored   MonitorEvidenceRunStatus = "errored"
	MonitorEvidenceRunAborted   MonitorEvidenceRunStatus = "aborted"
)

// MonitorRunEvidence is one transactionally consistent, team-scoped
// projection of a binding-owned monitor run and every publication occurrence
// owned by that run. Publications remain the existing durable publisher
// records; this type does not create a second effect ledger.
type MonitorRunEvidence struct {
	TeamID           int
	BindingID        int64
	BindingRevision  int64
	WorkflowRunID    snapshot.WorkflowRunID
	ActionDigest     string
	ReservationToken string

	Observation snapshot.SnapshotRef
	Cursor      Cursor
	SourceSHA   string
	TargetSHA   string
	ActionKind  ActionKind
	RunStatus   MonitorEvidenceRunStatus

	Locator               Locator
	URL                   string
	SourceRef             string
	TargetRef             string
	Destination           string
	ApprovalPolicyVersion string

	Publications             []publisher.Publication
	PublicationProofOverflow bool
}

// MonitorRunEvidenceReader must return evidence for exactly one team and
// workflow run from one consistent durable view.
type MonitorRunEvidenceReader interface {
	ReadMonitorRunEvidence(
		context.Context,
		int,
		snapshot.WorkflowRunID,
	) (MonitorRunEvidence, bool, error)
}

// MonitorObservationInspector reopens and semantically validates the exact
// sealed pull-request/v1 snapshot before a successful run can claim any safe
// provider or publication outcome.
type MonitorObservationInspector interface {
	InspectMonitorObservation(
		context.Context,
		int,
		snapshot.SnapshotRef,
	) (contracts.PullRequestBody, error)
}

type durableMonitorRunInspector struct {
	reader       MonitorRunEvidenceReader
	observations MonitorObservationInspector
}

func NewDurableMonitorRunInspector(
	reader MonitorRunEvidenceReader,
	observations MonitorObservationInspector,
) (MonitorRunInspector, error) {
	if nilMonitorRunEvidenceReader(reader) ||
		nilMonitorObservationInspector(observations) {
		return nil, fmt.Errorf(
			"pullrequest: monitor run evidence and observation inspectors are required",
		)
	}
	return &durableMonitorRunInspector{
		reader: reader, observations: observations,
	}, nil
}

func (inspector *durableMonitorRunInspector) InspectMonitorRun(
	ctx context.Context,
	teamID int,
	workflowRunID snapshot.WorkflowRunID,
) (MonitorRunResult, bool, error) {
	if ctx == nil || teamID <= 0 || workflowRunID.Validate() != nil {
		return MonitorRunResult{}, false, fmt.Errorf(
			"pullrequest: monitor run inspection requires context, team, and workflow run",
		)
	}
	if err := ctx.Err(); err != nil {
		return MonitorRunResult{}, false, err
	}
	evidence, found, err := inspector.reader.ReadMonitorRunEvidence(
		ctx, teamID, workflowRunID,
	)
	if err != nil || !found {
		return MonitorRunResult{}, false, err
	}
	if err := validateMonitorRunEvidenceAuthority(
		evidence, teamID, workflowRunID,
	); err != nil {
		return MonitorRunResult{}, false, err
	}

	switch evidence.RunStatus {
	case MonitorEvidenceRunAdmitting, MonitorEvidenceRunRunning,
		MonitorEvidenceRunCanceling:
		return MonitorRunResult{}, false, nil
	case MonitorEvidenceRunFailed:
		return terminalMonitorRunResult(evidence, MonitorRunFailed, ""), true, nil
	case MonitorEvidenceRunErrored:
		return terminalMonitorRunResult(evidence, MonitorRunErrored, ""), true, nil
	case MonitorEvidenceRunAborted:
		return terminalMonitorRunResult(evidence, MonitorRunAborted, ""), true, nil
	case MonitorEvidenceRunSucceeded:
	default:
		return MonitorRunResult{}, false, fmt.Errorf(
			"pullrequest: monitor run evidence status %q is invalid",
			evidence.RunStatus,
		)
	}

	observation, err := inspector.observations.InspectMonitorObservation(
		ctx, evidence.TeamID, evidence.Observation,
	)
	if err != nil {
		return MonitorRunResult{}, false, err
	}
	outcome := MonitorOutcomeAmbiguous
	reconciledSourceSHA := evidence.SourceSHA
	if exactMonitorObservation(evidence, observation) {
		outcome, reconciledSourceSHA = classifySucceededMonitorRun(
			evidence, observation,
		)
	}
	result := terminalMonitorRunResult(
		evidence, MonitorRunSucceeded, outcome,
	)
	result.ReconciledSourceSHA = reconciledSourceSHA
	if outcome == MonitorOutcomeAmbiguous {
		result.AttentionReason = monitorAmbiguousPublicationReason
	}
	return result, true, nil
}

func validateMonitorRunEvidenceAuthority(
	evidence MonitorRunEvidence,
	teamID int,
	workflowRunID snapshot.WorkflowRunID,
) error {
	if evidence.TeamID != teamID || evidence.WorkflowRunID != workflowRunID ||
		evidence.BindingID <= 0 || evidence.BindingRevision <= 0 ||
		evidence.Observation.Validate() != nil ||
		evidence.Observation.Type != snapshot.TypeRef("pull-request/v1") ||
		!isDigest(evidence.ActionDigest) ||
		evidence.Cursor == "" || evidence.Cursor.Validate() != nil ||
		validateObjectID("monitor evidence source sha", evidence.SourceSHA) != nil ||
		validateObjectID("monitor evidence target sha", evidence.TargetSHA) != nil {
		return fmt.Errorf(
			"pullrequest: monitor run evidence authority is invalid",
		)
	}
	if evidence.Locator.validate(contracts.PullRequestActive) != nil ||
		validateURL(evidence.URL) != nil ||
		validateRef("monitor evidence source ref", evidence.SourceRef) != nil ||
		validateRef("monitor evidence target ref", evidence.TargetRef) != nil ||
		evidence.SourceRef == evidence.TargetRef ||
		validateBoundedText(
			"monitor evidence destination",
			evidence.Destination,
			maxURLBytes,
		) != nil ||
		validateBoundedText(
			"monitor evidence approval policy version",
			evidence.ApprovalPolicyVersion,
			128,
		) != nil ||
		strings.TrimSpace(evidence.ReservationToken) !=
			evidence.ReservationToken ||
		evidence.ReservationToken == "" ||
		len(evidence.ReservationToken) > 128 {
		return fmt.Errorf(
			"pullrequest: monitor run evidence target is invalid",
		)
	}
	switch evidence.ActionKind {
	case ActionReviewBatch, ActionConflict, ActionFreshness,
		ActionCompleted, ActionAbandoned:
		return nil
	default:
		return fmt.Errorf(
			"pullrequest: monitor run evidence action kind %q is invalid",
			evidence.ActionKind,
		)
	}
}

func classifySucceededMonitorRun(
	evidence MonitorRunEvidence,
	observation contracts.PullRequestBody,
) (MonitorOutcome, string) {
	observedSourceSHA := evidence.SourceSHA
	if evidence.PublicationProofOverflow {
		return MonitorOutcomeAmbiguous, observedSourceSHA
	}
	switch evidence.ActionKind {
	case ActionCompleted:
		if len(evidence.Publications) == 0 {
			return MonitorOutcomeCompleted, observedSourceSHA
		}
		return MonitorOutcomeAmbiguous, observedSourceSHA
	case ActionAbandoned:
		if len(evidence.Publications) == 0 {
			return MonitorOutcomeAbandoned, observedSourceSHA
		}
		return MonitorOutcomeAmbiguous, observedSourceSHA
	case ActionReviewBatch, ActionConflict, ActionFreshness:
	default:
		return MonitorOutcomeAmbiguous, observedSourceSHA
	}

	publications, exact := exactMonitorPublicationSet(evidence)
	if !exact {
		return MonitorOutcomeAmbiguous, observedSourceSHA
	}
	branch := publications[publisher.OperationPublishPRBranch]
	if branch.Status == publisher.StatusRebaseRequired {
		if len(publications) == 1 &&
			exactRebaseRequiredPublication(evidence, branch) {
			return MonitorOutcomeStale, observedSourceSHA
		}
		return MonitorOutcomeAmbiguous, observedSourceSHA
	}

	required := 2
	if evidence.ActionKind == ActionReviewBatch {
		required = 3
	}
	if len(publications) != required {
		return MonitorOutcomeAmbiguous, observedSourceSHA
	}
	status, statusFound := publications[publisher.OperationPublishPRStatus]
	if !statusFound ||
		!exactSucceededBranchAndStatus(evidence, branch, status) {
		return MonitorOutcomeAmbiguous, observedSourceSHA
	}
	if evidence.ActionKind == ActionReviewBatch {
		response, found := publications[publisher.OperationRespondToReview]
		if !found ||
			!exactSucceededResponse(
				evidence, observation, branch, response,
			) {
			return MonitorOutcomeAmbiguous, observedSourceSHA
		}
	} else if _, found := publications[publisher.OperationRespondToReview]; found {
		return MonitorOutcomeAmbiguous, observedSourceSHA
	}

	publishedSourceSHA := branch.PRAction.Branch.NewSourceSHA
	if branch.PRAction.Branch.ExpectedSource.SHA ==
		publishedSourceSHA {
		return MonitorOutcomeValidatedNoop, publishedSourceSHA
	}
	return MonitorOutcomePublished, publishedSourceSHA
}

func exactMonitorObservation(
	evidence MonitorRunEvidence,
	observation contracts.PullRequestBody,
) bool {
	if observation.Validate(nil) != nil ||
		observation.Provider != string(evidence.Locator.Provider) ||
		observation.Repository != evidence.Locator.Repository ||
		observation.ExternalID != evidence.Locator.ExternalID ||
		observation.URL != evidence.URL ||
		observation.SourceRef != evidence.SourceRef ||
		observation.SourceSHA != evidence.SourceSHA ||
		observation.TargetRef != evidence.TargetRef ||
		observation.TargetSHA != evidence.TargetSHA ||
		observation.Trigger != contracts.PullRequestTrigger(
			evidence.ActionKind,
		) {
		return false
	}
	switch evidence.ActionKind {
	case ActionReviewBatch:
		return observation.State == contracts.PullRequestActive &&
			len(observation.ReviewBatches) == 1
	case ActionConflict:
		return observation.State == contracts.PullRequestActive &&
			observation.Mergeability ==
				contracts.PullRequestConflicted
	case ActionFreshness:
		return observation.State == contracts.PullRequestActive
	case ActionCompleted:
		return observation.State == contracts.PullRequestCompleted
	case ActionAbandoned:
		return observation.State == contracts.PullRequestAbandoned
	default:
		return false
	}
}

func exactMonitorPublicationSet(
	evidence MonitorRunEvidence,
) (map[publisher.OperationKind]publisher.Publication, bool) {
	publications := make(
		map[publisher.OperationKind]publisher.Publication,
		len(evidence.Publications),
	)
	ids := make(map[snapshot.DatabaseID]struct{}, len(evidence.Publications))
	for _, publication := range evidence.Publications {
		if !exactDurableMonitorPublication(evidence, publication) {
			return nil, false
		}
		if _, found := publications[publication.OperationKind]; found {
			return nil, false
		}
		if _, found := ids[publication.ID]; found {
			return nil, false
		}
		ids[publication.ID] = struct{}{}
		publications[publication.OperationKind] = publication.Clone()
	}
	return publications, true
}

func exactDurableMonitorPublication(
	evidence MonitorRunEvidence,
	publication publisher.Publication,
) bool {
	if publication.ID <= 0 || publication.Attempt <= 0 ||
		publication.OperationKey == "" ||
		publication.CreatedAt.IsZero() || publication.UpdatedAt.IsZero() ||
		publication.PRAction == nil ||
		publication.OperationKind != publication.PRAction.Kind ||
		publication.Status != publication.Result.Status ||
		publication.Result.Validate() != nil ||
		publication.PRAction.ValidatePersisted() != nil {
		return false
	}
	key, err := publication.PRAction.OperationKey()
	if err != nil || key != publication.OperationKey {
		return false
	}
	authority, observation, destination, policy, ok :=
		monitorPublicationCommon(*publication.PRAction)
	if !ok ||
		authority.TeamID != evidence.TeamID ||
		authority.WorkflowRunID != evidence.WorkflowRunID ||
		observation != evidence.Observation ||
		destination != evidence.Destination ||
		policy != evidence.ApprovalPolicyVersion {
		return false
	}
	switch publication.OperationKind {
	case publisher.OperationPublishPRBranch,
		publisher.OperationPublishPRStatus,
		publisher.OperationRespondToReview:
		return true
	default:
		return false
	}
}

func monitorPublicationCommon(
	action publisher.PRAction,
) (
	publisher.Authority,
	snapshot.SnapshotRef,
	string,
	string,
	bool,
) {
	switch action.Kind {
	case publisher.OperationPublishPRBranch:
		if action.Branch != nil {
			return action.Branch.Authority, action.Branch.Observation,
				action.Branch.Destination,
				action.Branch.ApprovalPolicyVersion, true
		}
	case publisher.OperationPublishPRStatus:
		if action.Status != nil {
			return action.Status.Authority, action.Status.Observation,
				action.Status.Destination,
				action.Status.ApprovalPolicyVersion, true
		}
	case publisher.OperationRespondToReview:
		if action.Response != nil {
			return action.Response.Authority, action.Response.Observation,
				action.Response.Destination,
				action.Response.ApprovalPolicyVersion, true
		}
	}
	return publisher.Authority{}, snapshot.SnapshotRef{}, "", "", false
}

func exactRebaseRequiredPublication(
	evidence MonitorRunEvidence,
	publication publisher.Publication,
) bool {
	if publication.PRAction == nil || publication.PRAction.Branch == nil ||
		publication.Status != publisher.StatusRebaseRequired {
		return false
	}
	branch := publication.PRAction.Branch
	return exactMonitorBranchRequest(evidence, *branch) &&
		publication.Result.HeadSHA == branch.NewSourceSHA &&
		publication.Result.BaseSHA == branch.ExpectedTargetSHA &&
		publication.Result.ExternalID == "" &&
		publication.Result.URL == ""
}

func exactSucceededBranchAndStatus(
	evidence MonitorRunEvidence,
	branchPublication publisher.Publication,
	statusPublication publisher.Publication,
) bool {
	if branchPublication.PRAction == nil ||
		branchPublication.PRAction.Branch == nil ||
		statusPublication.PRAction == nil ||
		statusPublication.PRAction.Status == nil ||
		branchPublication.Status != publisher.StatusSucceeded ||
		statusPublication.Status != publisher.StatusSucceeded {
		return false
	}
	branch := branchPublication.PRAction.Branch
	status := statusPublication.PRAction.Status
	return exactMonitorBranchRequest(evidence, *branch) &&
		exactMonitorStatusRequest(evidence, *branch, *status) &&
		branch.Authority == status.Authority &&
		reflect.DeepEqual(branch.Evidence, status.Evidence) &&
		branchPublication.Result.ExternalID == branch.SourceRef &&
		branchPublication.Result.URL == "" &&
		branchPublication.Result.HeadSHA == branch.NewSourceSHA &&
		branchPublication.Result.BaseSHA == branch.ExpectedTargetSHA &&
		branchPublication.Result.Detail == "" &&
		statusPublication.Result.ExternalID != "" &&
		statusPublication.Result.URL != "" &&
		validateURL(statusPublication.Result.URL) == nil &&
		statusPublication.Result.HeadSHA == status.SourceSHA &&
		statusPublication.Result.BaseSHA == "" &&
		statusPublication.Result.Detail == ""
}

func exactSucceededResponse(
	evidence MonitorRunEvidence,
	observation contracts.PullRequestBody,
	branchPublication publisher.Publication,
	responsePublication publisher.Publication,
) bool {
	if branchPublication.PRAction == nil ||
		branchPublication.PRAction.Branch == nil ||
		responsePublication.PRAction == nil ||
		responsePublication.PRAction.Response == nil ||
		responsePublication.Status != publisher.StatusSucceeded {
		return false
	}
	branch := branchPublication.PRAction.Branch
	response := responsePublication.PRAction.Response
	return exactMonitorPRLocator(evidence, response.Locator) &&
		response.Observation == evidence.Observation &&
		response.Destination == evidence.Destination &&
		response.ApprovalPolicyVersion == evidence.ApprovalPolicyVersion &&
		response.TargetRef == evidence.TargetRef &&
		exactMonitorObservationBatch(observation, response.Batch) &&
		branch.Authority == response.Authority &&
		reflect.DeepEqual(branch.Evidence, response.Evidence) &&
		responsePublication.Result.ExternalID != "" &&
		responsePublication.Result.URL != "" &&
		validateURL(responsePublication.Result.URL) == nil &&
		responsePublication.Result.HeadSHA == response.Batch.CommitSHA &&
		responsePublication.Result.BaseSHA == "" &&
		responsePublication.Result.Detail == ""
}

func exactMonitorObservationBatch(
	observation contracts.PullRequestBody,
	batch publisher.PRReviewBatch,
) bool {
	return len(observation.ReviewBatches) == 1 &&
		reflect.DeepEqual(observation.ReviewBatches[0], batch)
}

func exactMonitorBranchRequest(
	evidence MonitorRunEvidence,
	branch publisher.BranchPublicationRequest,
) bool {
	return exactMonitorPRLocator(evidence, branch.Locator) &&
		branch.Observation == evidence.Observation &&
		branch.Destination == evidence.Destination &&
		branch.ApprovalPolicyVersion == evidence.ApprovalPolicyVersion &&
		branch.SourceRef == evidence.SourceRef &&
		branch.TargetRef == evidence.TargetRef &&
		branch.ExpectedSource.Exists &&
		branch.ExpectedSource.SHA == evidence.SourceSHA &&
		branch.ExpectedTargetSHA == evidence.TargetSHA
}

func exactMonitorStatusRequest(
	evidence MonitorRunEvidence,
	branch publisher.BranchPublicationRequest,
	status publisher.StatusPublicationRequest,
) bool {
	return exactMonitorPRLocator(evidence, status.Locator) &&
		status.Observation == evidence.Observation &&
		status.Validation == branch.Validation &&
		status.Destination == evidence.Destination &&
		status.ApprovalPolicyVersion == evidence.ApprovalPolicyVersion &&
		status.TargetRef == evidence.TargetRef &&
		status.SourceSHA == branch.NewSourceSHA &&
		status.State == "success"
}

func exactMonitorPRLocator(
	evidence MonitorRunEvidence,
	locator publisher.PRLocator,
) bool {
	return string(locator.Provider) == string(evidence.Locator.Provider) &&
		locator.Repository == evidence.Locator.Repository &&
		locator.ExternalID == evidence.Locator.ExternalID
}

func terminalMonitorRunResult(
	evidence MonitorRunEvidence,
	status MonitorRunStatus,
	outcome MonitorOutcome,
) MonitorRunResult {
	return MonitorRunResult{
		TeamID: evidence.TeamID, BindingID: evidence.BindingID,
		BindingRevision:       evidence.BindingRevision,
		WorkflowRunID:         evidence.WorkflowRunID,
		ActionDigest:          evidence.ActionDigest,
		ReservationToken:      evidence.ReservationToken,
		ObservationSnapshotID: evidence.Observation.ID,
		Cursor:                evidence.Cursor,
		SourceSHA:             evidence.SourceSHA,
		ReconciledSourceSHA:   evidence.SourceSHA,
		TargetSHA:             evidence.TargetSHA,
		RunStatus:             status,
		Outcome:               outcome,
	}
}

func nilMonitorRunEvidenceReader(reader MonitorRunEvidenceReader) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func nilMonitorObservationInspector(
	inspector MonitorObservationInspector,
) bool {
	if inspector == nil {
		return true
	}
	value := reflect.ValueOf(inspector)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ MonitorRunInspector = (*durableMonitorRunInspector)(nil)
