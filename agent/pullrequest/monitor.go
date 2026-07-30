package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

var ErrStaleMonitorSourceVersion = errors.New(
	"pullrequest: stale monitor source version",
)

var ErrAcceptedReviewAuthority = errors.New(
	"pullrequest: accepted review authority is unavailable",
)

const (
	MonitorAcceptedReviewInputName     = "accepted-review"
	MonitorAcceptedCandidateInputName  = "accepted-candidate"
	MonitorAcceptedValidationInputName = "accepted-validation"
)

// MonitorSourceVersion is the exact actionable forge-pr resource version
// selected by one successful standing-pipeline build.
type MonitorSourceVersion struct {
	Provider        string
	ExternalID      string
	SourceSHA       string
	TargetSHA       string
	ActionKind      string
	ActionDigest    string
	Cursor          string
	BindingRevision string
}

// MonitorSourceBuildSpec is constructed only from a durable successful source
// build, its binding-owned pipeline registration, and its captured snapshot.
type MonitorSourceBuildSpec struct {
	TeamID               int
	TeamName             string
	BindingID            int64
	PipelineID           int
	BuildID              int
	AdmissionID          int64
	WorkflowDefinitionID int
	WorkflowName         string
	WorkflowVersion      int
	Observation          snapshot.SnapshotRef
	SelectedVersion      atc.Version
}

// MonitorSourceBuild retains a private copy of the exact selected version.
// Public fields are diagnostic; mutation after construction cannot substitute
// a different revision, action, cursor, head, admission, or snapshot.
type MonitorSourceBuild struct {
	TeamID               int
	TeamName             string
	BindingID            int64
	PipelineID           int
	BuildID              int
	AdmissionID          int64
	WorkflowDefinitionID int
	WorkflowName         string
	WorkflowVersion      int
	Observation          snapshot.SnapshotRef
	Version              MonitorSourceVersion

	authority *monitorSourceBuildAuthority
}

type monitorSourceBuildAuthority struct {
	source MonitorSourceBuild
}

// AcceptedReviewAuthoritySpec is the immutable initial-review evidence named
// by the binding's originating publication occurrence.
type AcceptedReviewAuthoritySpec struct {
	TeamID                  int
	PublicationOccurrenceID int64
	Review                  snapshot.SnapshotRef
	Candidate               snapshot.SnapshotRef
	Validation              snapshot.SnapshotRef
	ReviewWorkflowRunID     snapshot.WorkflowRunID
	OutcomeRevision         int64
}

// AcceptedReviewAuthority is protected at construction so neither scheduler
// data nor authored workflow inputs can substitute the initial review chain.
type AcceptedReviewAuthority struct {
	TeamID                  int
	PublicationOccurrenceID int64
	Review                  snapshot.SnapshotRef
	Candidate               snapshot.SnapshotRef
	Validation              snapshot.SnapshotRef
	ReviewWorkflowRunID     snapshot.WorkflowRunID
	OutcomeRevision         int64

	authority *acceptedReviewAuthority
}

type acceptedReviewAuthority struct {
	value AcceptedReviewAuthority
}

func NewAcceptedReviewAuthority(
	spec AcceptedReviewAuthoritySpec,
) (AcceptedReviewAuthority, error) {
	value := AcceptedReviewAuthority{
		TeamID:                  spec.TeamID,
		PublicationOccurrenceID: spec.PublicationOccurrenceID,
		Review:                  spec.Review, Candidate: spec.Candidate,
		Validation:          spec.Validation,
		ReviewWorkflowRunID: spec.ReviewWorkflowRunID,
		OutcomeRevision:     spec.OutcomeRevision,
	}
	if err := validateAcceptedReviewAuthority(value); err != nil {
		return AcceptedReviewAuthority{}, err
	}
	protected := value
	value.authority = &acceptedReviewAuthority{value: protected}
	return value, nil
}

func (value AcceptedReviewAuthority) Protected() (AcceptedReviewAuthority, error) {
	if value.authority == nil {
		return AcceptedReviewAuthority{}, ErrAcceptedReviewAuthority
	}
	public := value
	public.authority = nil
	if !reflect.DeepEqual(public, value.authority.value) {
		return AcceptedReviewAuthority{}, fmt.Errorf(
			"%w: authority changed after resolution",
			ErrAcceptedReviewAuthority,
		)
	}
	protected := value.authority.value
	protected.authority = value.authority
	return protected, nil
}

func validateAcceptedReviewAuthority(value AcceptedReviewAuthority) error {
	if value.TeamID <= 0 || value.PublicationOccurrenceID <= 0 ||
		value.Review.Validate() != nil ||
		value.Review.Type != snapshot.TypeRef("review/v1") ||
		value.Candidate.Validate() != nil ||
		value.Candidate.Type != snapshot.TypeRef("repository/v1") ||
		value.Validation.Validate() != nil ||
		value.Validation.Type != snapshot.TypeRef("validation/v1") ||
		value.ReviewWorkflowRunID.Validate() != nil ||
		value.OutcomeRevision <= 0 {
		return ErrAcceptedReviewAuthority
	}
	return nil
}

type AcceptedReviewAuthorityResolver interface {
	ResolveAcceptedReviewAuthority(
		context.Context,
		int,
		int64,
	) (AcceptedReviewAuthority, bool, error)
}

func NewMonitorSourceBuild(spec MonitorSourceBuildSpec) (MonitorSourceBuild, error) {
	version, err := parseMonitorSourceVersion(spec.SelectedVersion)
	if err != nil {
		return MonitorSourceBuild{}, err
	}
	source := MonitorSourceBuild{
		TeamID: spec.TeamID, TeamName: spec.TeamName,
		BindingID: spec.BindingID, PipelineID: spec.PipelineID,
		BuildID: spec.BuildID, AdmissionID: spec.AdmissionID,
		WorkflowDefinitionID: spec.WorkflowDefinitionID,
		WorkflowName:         spec.WorkflowName,
		WorkflowVersion:      spec.WorkflowVersion,
		Observation:          spec.Observation,
		Version:              version,
	}
	if err := validateMonitorSourceBuild(source); err != nil {
		return MonitorSourceBuild{}, err
	}
	protected := source
	source.authority = &monitorSourceBuildAuthority{source: protected}
	return source, nil
}

// Protected returns the immutable source authority retained at construction.
func (source MonitorSourceBuild) Protected() (MonitorSourceBuild, error) {
	if source.authority == nil {
		return MonitorSourceBuild{}, fmt.Errorf(
			"%w: monitor source has no protected authority",
			ErrStaleMonitorSourceVersion,
		)
	}
	public := source
	public.authority = nil
	if !reflect.DeepEqual(public, source.authority.source) {
		return MonitorSourceBuild{}, fmt.Errorf(
			"%w: monitor source changed after capture",
			ErrStaleMonitorSourceVersion,
		)
	}
	protected := source.authority.source
	protected.authority = source.authority
	return protected, nil
}

// MonitorLaunch is the only binder-facing launch request. Reservation was
// committed before this value can be constructed.
type MonitorLaunch struct {
	Source         MonitorSourceBuild
	AcceptedReview AcceptedReviewAuthority
	Reservation    LaunchReservation
}

func (request MonitorLaunch) Validate() error {
	source, err := request.Source.Protected()
	if err != nil {
		return err
	}
	accepted, err := request.AcceptedReview.Protected()
	if err != nil {
		return err
	}
	reservation := request.Reservation
	if reservation.BindingID != source.BindingID ||
		accepted.TeamID != source.TeamID ||
		reservation.BindingRevision <= 0 ||
		reservation.BaseRevision <= 0 ||
		reservation.ActionDigest != source.Version.ActionDigest ||
		reservation.ObservationSnapshotID != source.Observation.ID ||
		reservation.Cursor != Cursor(source.Version.Cursor) ||
		reservation.SourceSHA != source.Version.SourceSHA ||
		reservation.TargetSHA != source.Version.TargetSHA ||
		strings.TrimSpace(reservation.Token) != reservation.Token ||
		reservation.Token == "" || len(reservation.Token) > 128 {
		return ErrReservationMismatch
	}
	return nil
}

type MonitorRunLauncher interface {
	LaunchMonitor(context.Context, MonitorLaunch) (snapshot.WorkflowRunID, error)
}

type MonitorRunStatus string

const (
	MonitorRunSucceeded MonitorRunStatus = "succeeded"
	MonitorRunFailed    MonitorRunStatus = "failed"
	MonitorRunErrored   MonitorRunStatus = "errored"
	MonitorRunAborted   MonitorRunStatus = "aborted"
)

type MonitorOutcome string

const (
	MonitorOutcomePublished     MonitorOutcome = "published"
	MonitorOutcomeValidatedNoop MonitorOutcome = "validated_noop"
	MonitorOutcomeStale         MonitorOutcome = "stale"
	MonitorOutcomeAmbiguous     MonitorOutcome = "ambiguous"
	MonitorOutcomeCompleted     MonitorOutcome = "completed"
	MonitorOutcomeAbandoned     MonitorOutcome = "abandoned"
)

// MonitorRunResult is a trusted terminal projection. Task-specific output
// verification lives behind MonitorRunInspector; the coordinator still
// verifies the result identifies the exact attached reservation.
type MonitorRunResult struct {
	TeamID                int
	BindingID             int64
	BindingRevision       int64
	WorkflowRunID         snapshot.WorkflowRunID
	ActionDigest          string
	ReservationToken      string
	ObservationSnapshotID snapshot.SnapshotID
	Cursor                Cursor
	SourceSHA             string
	TargetSHA             string
	RunStatus             MonitorRunStatus
	Outcome               MonitorOutcome
	AttentionReason       string
}

type MonitorRunInspector interface {
	InspectMonitorRun(
		context.Context,
		int,
		snapshot.WorkflowRunID,
	) (MonitorRunResult, bool, error)
}

type MonitorCoordinator interface {
	ReserveAndLaunch(
		context.Context,
		MonitorSourceBuild,
	) (snapshot.WorkflowRunID, bool, error)
	Acknowledge(context.Context, MonitorRunResult) (Binding, error)
	ReconcileTerminal(context.Context, int, int64) (Binding, error)
}

type monitorCoordinator struct {
	bindings       BindingStore
	launcher       MonitorRunLauncher
	runs           MonitorRunInspector
	acceptedReview AcceptedReviewAuthorityResolver
	reservationTTL time.Duration
}

func NewMonitorCoordinator(
	bindings BindingStore,
	launcher MonitorRunLauncher,
	runs MonitorRunInspector,
	acceptedReview AcceptedReviewAuthorityResolver,
	reservationTTL time.Duration,
) (MonitorCoordinator, error) {
	if bindings == nil || launcher == nil || runs == nil ||
		acceptedReview == nil {
		return nil, fmt.Errorf(
			"pullrequest: monitor coordinator dependencies are required",
		)
	}
	if reservationTTL <= 0 || reservationTTL > MaxLaunchReservationTTL {
		return nil, fmt.Errorf(
			"pullrequest: monitor reservation TTL must be positive and at most %s",
			MaxLaunchReservationTTL,
		)
	}
	return &monitorCoordinator{
		bindings: bindings, launcher: launcher, runs: runs,
		acceptedReview: acceptedReview,
		reservationTTL: reservationTTL,
	}, nil
}

func (coordinator *monitorCoordinator) ReserveAndLaunch(
	ctx context.Context,
	source MonitorSourceBuild,
) (snapshot.WorkflowRunID, bool, error) {
	if ctx == nil {
		return 0, false, fmt.Errorf(
			"pullrequest: monitor launch requires context",
		)
	}
	protected, err := source.Protected()
	if err != nil {
		return 0, false, err
	}
	binding, found, err := coordinator.bindings.Get(
		ctx, protected.TeamID, protected.BindingID,
	)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, ErrBindingNotFound
	}
	revision, err := validateMonitorSourceAgainstBinding(protected, binding)
	if err != nil {
		return 0, false, err
	}
	if binding.OriginatingPublicationOccurrence == nil ||
		*binding.OriginatingPublicationOccurrence <= 0 {
		return 0, false, ErrAcceptedReviewAuthority
	}
	accepted, found, err := coordinator.acceptedReview.
		ResolveAcceptedReviewAuthority(
			ctx, binding.TeamID,
			*binding.OriginatingPublicationOccurrence,
		)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, ErrAcceptedReviewAuthority
	}
	protectedAccepted, err := accepted.Protected()
	if err != nil {
		return 0, false, err
	}
	if protectedAccepted.TeamID != binding.TeamID ||
		protectedAccepted.PublicationOccurrenceID !=
			*binding.OriginatingPublicationOccurrence {
		return 0, false, ErrAcceptedReviewAuthority
	}
	reservation, reserved, err := coordinator.bindings.ReserveLaunch(
		ctx,
		ReserveLaunch{
			TeamID: protected.TeamID, BindingID: protected.BindingID,
			ExpectedRevision:      revision,
			ActionDigest:          protected.Version.ActionDigest,
			ObservationSnapshotID: protected.Observation.ID,
			Cursor:                Cursor(protected.Version.Cursor),
			SourceSHA:             protected.Version.SourceSHA,
			TargetSHA:             protected.Version.TargetSHA,
			ExpiresIn:             coordinator.reservationTTL,
		},
	)
	if err != nil {
		if errors.Is(err, ErrStaleBindingRevision) {
			return 0, false, fmt.Errorf(
				"%w: %v", ErrStaleMonitorSourceVersion, err,
			)
		}
		return 0, false, err
	}
	if !reserved {
		return 0, false, nil
	}
	launch := MonitorLaunch{
		Source: protected, AcceptedReview: protectedAccepted,
		Reservation: reservation,
	}
	if err := launch.Validate(); err != nil {
		return 0, false, err
	}
	runID, launchErr := coordinator.launcher.LaunchMonitor(ctx, launch)
	if launchErr == nil {
		if runID.Validate() != nil {
			return 0, false, fmt.Errorf(
				"pullrequest: monitor launcher returned an invalid run",
			)
		}
		return runID, true, nil
	}

	current, found, readErr := coordinator.bindings.Get(
		ctx, protected.TeamID, protected.BindingID,
	)
	if readErr != nil {
		return runID, false, errors.Join(launchErr, readErr)
	}
	if !found {
		return runID, false, errors.Join(launchErr, ErrBindingNotFound)
	}
	if sameUnattachedMonitorReservation(current.Active, reservation) {
		_, releaseErr := coordinator.bindings.ReleaseLaunch(
			ctx,
			ReleaseLaunch{
				TeamID: protected.TeamID, BindingID: protected.BindingID,
				ExpectedRevision: current.Revision,
				ActionDigest:     reservation.ActionDigest,
				ReservationToken: reservation.Token,
			},
		)
		if releaseErr != nil {
			return runID, false, errors.Join(launchErr, releaseErr)
		}
	}
	return runID, false, launchErr
}

func (coordinator *monitorCoordinator) Acknowledge(
	ctx context.Context,
	result MonitorRunResult,
) (Binding, error) {
	if ctx == nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: monitor acknowledgement requires context",
		)
	}
	if err := validateMonitorRunResult(result); err != nil {
		return Binding{}, err
	}
	binding, found, err := coordinator.bindings.Get(
		ctx, result.TeamID, result.BindingID,
	)
	if err != nil {
		return Binding{}, err
	}
	if !found {
		return Binding{}, ErrBindingNotFound
	}
	if sameAcknowledgedMonitorResult(binding, result) {
		return binding, nil
	}
	if err := validateMonitorResultAgainstBinding(binding, result); err != nil {
		return Binding{}, err
	}

	switch result.RunStatus {
	case MonitorRunFailed, MonitorRunErrored, MonitorRunAborted:
		return coordinator.releaseTerminalRun(ctx, binding, result)
	case MonitorRunSucceeded:
	default:
		return Binding{}, fmt.Errorf(
			"pullrequest: monitor run status %q is not terminal",
			result.RunStatus,
		)
	}

	switch result.Outcome {
	case MonitorOutcomePublished, MonitorOutcomeValidatedNoop:
		return coordinator.bindings.AcknowledgeAction(
			ctx, acknowledgeRequest(binding.Revision, result),
		)
	case MonitorOutcomeCompleted, MonitorOutcomeAbandoned:
		state := BindingCompleted
		if result.Outcome == MonitorOutcomeAbandoned {
			state = BindingAbandoned
		}
		return coordinator.bindings.MarkTerminal(
			ctx,
			TerminalBinding{
				TeamID: result.TeamID, BindingID: result.BindingID,
				ExpectedRevision: binding.Revision, State: state,
				ActionDigest:          result.ActionDigest,
				ReservationToken:      result.ReservationToken,
				WorkflowRunID:         result.WorkflowRunID,
				ObservationSnapshotID: result.ObservationSnapshotID,
				Cursor:                result.Cursor, SourceSHA: result.SourceSHA,
				TargetSHA: result.TargetSHA,
			},
		)
	case MonitorOutcomeStale:
		return coordinator.releaseTerminalRun(ctx, binding, result)
	case MonitorOutcomeAmbiguous:
		if err := validateAttentionReason(result.AttentionReason); err != nil {
			return Binding{}, err
		}
		paused, err := coordinator.bindings.MarkAttention(
			ctx, result.TeamID, result.BindingID, result.AttentionReason,
		)
		if err != nil {
			return Binding{}, err
		}
		return coordinator.releaseTerminalRun(ctx, paused, result)
	default:
		return Binding{}, fmt.Errorf(
			"pullrequest: succeeded monitor run has unsafe outcome %q",
			result.Outcome,
		)
	}
}

func (coordinator *monitorCoordinator) ReconcileTerminal(
	ctx context.Context,
	teamID int,
	bindingID int64,
) (Binding, error) {
	if ctx == nil || teamID <= 0 || bindingID <= 0 {
		return Binding{}, fmt.Errorf(
			"pullrequest: monitor terminal reconciliation requires context and identities",
		)
	}
	binding, found, err := coordinator.bindings.Get(ctx, teamID, bindingID)
	if err != nil {
		return Binding{}, err
	}
	if !found {
		return Binding{}, ErrBindingNotFound
	}
	if binding.Active == nil || binding.Active.WorkflowRunID == nil {
		return binding, nil
	}
	result, terminal, err := coordinator.runs.InspectMonitorRun(
		ctx, teamID, *binding.Active.WorkflowRunID,
	)
	if err != nil {
		return Binding{}, err
	}
	if !terminal {
		return binding, nil
	}
	return coordinator.Acknowledge(ctx, result)
}

func (coordinator *monitorCoordinator) releaseTerminalRun(
	ctx context.Context,
	binding Binding,
	result MonitorRunResult,
) (Binding, error) {
	runID := result.WorkflowRunID
	return coordinator.bindings.ReleaseLaunch(
		ctx,
		ReleaseLaunch{
			TeamID: result.TeamID, BindingID: result.BindingID,
			ExpectedRevision: binding.Revision,
			ActionDigest:     result.ActionDigest,
			ReservationToken: result.ReservationToken,
			WorkflowRunID:    &runID,
		},
	)
}

func acknowledgeRequest(
	revision int64,
	result MonitorRunResult,
) AcknowledgeAction {
	return AcknowledgeAction{
		TeamID: result.TeamID, BindingID: result.BindingID,
		ExpectedRevision: revision, ActionDigest: result.ActionDigest,
		ReservationToken:      result.ReservationToken,
		WorkflowRunID:         result.WorkflowRunID,
		ObservationSnapshotID: result.ObservationSnapshotID,
		Cursor:                result.Cursor, SourceSHA: result.SourceSHA,
		TargetSHA: result.TargetSHA,
	}
}

func parseMonitorSourceVersion(version atc.Version) (MonitorSourceVersion, error) {
	const fieldCount = 8
	if len(version) != fieldCount {
		return MonitorSourceVersion{}, fmt.Errorf(
			"%w: version has unexpected fields",
			ErrStaleMonitorSourceVersion,
		)
	}
	parsed := MonitorSourceVersion{
		Provider: version["provider"], ExternalID: version["external_id"],
		SourceSHA: version["source_sha"], TargetSHA: version["target_sha"],
		ActionKind: version["action_kind"], ActionDigest: version["action_digest"],
		Cursor: version["cursor"], BindingRevision: version["binding_revision"],
	}
	if err := validateMonitorSourceVersion(parsed); err != nil {
		return MonitorSourceVersion{}, err
	}
	return parsed, nil
}

func validateMonitorSourceVersion(version MonitorSourceVersion) error {
	switch Provider(version.Provider) {
	case ProviderGitHub, ProviderAzureDevOps:
	default:
		return fmt.Errorf(
			"%w: provider is invalid", ErrStaleMonitorSourceVersion,
		)
	}
	if err := validateIdentifier(
		"external id", version.ExternalID, maxExternalIDBytes,
	); err != nil {
		return fmt.Errorf("%w: %v", ErrStaleMonitorSourceVersion, err)
	}
	if err := validateObjectID("source sha", version.SourceSHA); err != nil {
		return fmt.Errorf("%w: %v", ErrStaleMonitorSourceVersion, err)
	}
	if err := validateObjectID("target sha", version.TargetSHA); err != nil {
		return fmt.Errorf("%w: %v", ErrStaleMonitorSourceVersion, err)
	}
	switch ActionKind(version.ActionKind) {
	case ActionReviewBatch, ActionConflict, ActionFreshness,
		ActionCompleted, ActionAbandoned:
	default:
		return fmt.Errorf(
			"%w: action kind is invalid", ErrStaleMonitorSourceVersion,
		)
	}
	if !isDigest(version.ActionDigest) {
		return fmt.Errorf(
			"%w: action digest is invalid", ErrStaleMonitorSourceVersion,
		)
	}
	cursor := Cursor(version.Cursor)
	if err := cursor.Validate(); err != nil || cursor == "" {
		return fmt.Errorf(
			"%w: cursor is invalid", ErrStaleMonitorSourceVersion,
		)
	}
	if _, err := parsePositiveMonitorRevision(version.BindingRevision); err != nil {
		return err
	}
	return nil
}

func validateMonitorSourceBuild(source MonitorSourceBuild) error {
	if source.TeamID <= 0 || source.BindingID <= 0 ||
		source.PipelineID <= 0 || source.BuildID <= 0 ||
		source.AdmissionID <= 0 || source.WorkflowDefinitionID <= 0 ||
		source.WorkflowVersion <= 0 {
		return fmt.Errorf(
			"%w: monitor source identities are invalid",
			ErrStaleMonitorSourceVersion,
		)
	}
	if strings.TrimSpace(source.TeamName) != source.TeamName ||
		source.TeamName == "" || len(source.TeamName) > 255 ||
		!utf8.ValidString(source.TeamName) ||
		strings.TrimSpace(source.WorkflowName) != source.WorkflowName ||
		source.WorkflowName == "" || len(source.WorkflowName) > 255 ||
		!utf8.ValidString(source.WorkflowName) {
		return fmt.Errorf(
			"%w: monitor source names are invalid",
			ErrStaleMonitorSourceVersion,
		)
	}
	if err := source.Observation.Validate(); err != nil ||
		source.Observation.Type != snapshot.TypeRef("pull-request/v1") {
		return fmt.Errorf(
			"%w: captured observation is invalid",
			ErrStaleMonitorSourceVersion,
		)
	}
	return validateMonitorSourceVersion(source.Version)
}

func validateMonitorSourceAgainstBinding(
	source MonitorSourceBuild,
	binding Binding,
) (int64, error) {
	revision, err := parsePositiveMonitorRevision(
		source.Version.BindingRevision,
	)
	if err != nil {
		return 0, err
	}
	if binding.ID != source.BindingID || binding.TeamID != source.TeamID ||
		binding.PipelineID == nil || *binding.PipelineID != source.PipelineID ||
		binding.MonitorWorkflowDefinitionID != source.WorkflowDefinitionID ||
		binding.MonitorWorkflowVersion != source.WorkflowVersion ||
		string(binding.Locator.Provider) != source.Version.Provider ||
		binding.Locator.ExternalID != source.Version.ExternalID ||
		binding.State != BindingActive || binding.Paused ||
		binding.OperatorTerminated {
		return 0, fmt.Errorf(
			"%w: binding projection changed",
			ErrStaleMonitorSourceVersion,
		)
	}
	if binding.Revision != revision &&
		!sameMonitorSourceReservation(binding.Active, revision, source) {
		return 0, fmt.Errorf(
			"%w: binding revision changed",
			ErrStaleMonitorSourceVersion,
		)
	}
	return revision, nil
}

func sameMonitorSourceReservation(
	active *LaunchReservation,
	baseRevision int64,
	source MonitorSourceBuild,
) bool {
	return active != nil &&
		active.BaseRevision == baseRevision &&
		active.ActionDigest == source.Version.ActionDigest &&
		active.ObservationSnapshotID == source.Observation.ID &&
		active.Cursor == Cursor(source.Version.Cursor) &&
		active.SourceSHA == source.Version.SourceSHA &&
		active.TargetSHA == source.Version.TargetSHA
}

func parsePositiveMonitorRevision(raw string) (int64, error) {
	if raw == "" || raw[0] == '0' || raw[0] == '+' || raw[0] == '-' {
		return 0, fmt.Errorf(
			"%w: binding revision is not canonical",
			ErrStaleMonitorSourceVersion,
		)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != raw {
		return 0, fmt.Errorf(
			"%w: binding revision is not canonical",
			ErrStaleMonitorSourceVersion,
		)
	}
	return value, nil
}

func validateMonitorRunResult(result MonitorRunResult) error {
	if result.TeamID <= 0 || result.BindingID <= 0 ||
		result.BindingRevision <= 0 || result.WorkflowRunID.Validate() != nil ||
		result.ObservationSnapshotID.Validate() != nil ||
		!isDigest(result.ActionDigest) ||
		strings.TrimSpace(result.ReservationToken) != result.ReservationToken ||
		result.ReservationToken == "" || len(result.ReservationToken) > 128 ||
		result.Cursor == "" || result.Cursor.Validate() != nil ||
		validateObjectID("monitor result source sha", result.SourceSHA) != nil ||
		validateObjectID("monitor result target sha", result.TargetSHA) != nil {
		return fmt.Errorf("pullrequest: monitor run result is invalid")
	}
	switch result.RunStatus {
	case MonitorRunSucceeded:
		switch result.Outcome {
		case MonitorOutcomePublished, MonitorOutcomeValidatedNoop,
			MonitorOutcomeStale, MonitorOutcomeAmbiguous,
			MonitorOutcomeCompleted, MonitorOutcomeAbandoned:
		default:
			return fmt.Errorf(
				"pullrequest: succeeded monitor run requires a classified outcome",
			)
		}
	case MonitorRunFailed, MonitorRunErrored, MonitorRunAborted:
		if result.Outcome != "" {
			return fmt.Errorf(
				"pullrequest: unsuccessful monitor run cannot claim a safe outcome",
			)
		}
	default:
		return fmt.Errorf("pullrequest: monitor run status is invalid")
	}
	return nil
}

func validateMonitorResultAgainstBinding(
	binding Binding,
	result MonitorRunResult,
) error {
	if binding.ID != result.BindingID || binding.TeamID != result.TeamID ||
		result.BindingRevision > binding.Revision ||
		binding.Active == nil || binding.Active.WorkflowRunID == nil ||
		*binding.Active.WorkflowRunID != result.WorkflowRunID ||
		binding.Active.ActionDigest != result.ActionDigest ||
		binding.Active.Token != result.ReservationToken ||
		binding.Active.ObservationSnapshotID != result.ObservationSnapshotID ||
		binding.Active.Cursor != result.Cursor ||
		binding.Active.SourceSHA != result.SourceSHA ||
		binding.Active.TargetSHA != result.TargetSHA {
		return ErrReservationMismatch
	}
	return nil
}

func sameAcknowledgedMonitorResult(
	binding Binding,
	result MonitorRunResult,
) bool {
	if result.RunStatus != MonitorRunSucceeded ||
		result.Outcome != MonitorOutcomePublished &&
			result.Outcome != MonitorOutcomeValidatedNoop &&
			result.Outcome != MonitorOutcomeCompleted &&
			result.Outcome != MonitorOutcomeAbandoned {
		return false
	}
	return binding.LastAcknowledgedWorkflowRunID != nil &&
		*binding.LastAcknowledgedWorkflowRunID == result.WorkflowRunID &&
		binding.LastAcknowledgedActionDigest == result.ActionDigest &&
		binding.LastObservationSnapshotID != nil &&
		*binding.LastObservationSnapshotID == result.ObservationSnapshotID &&
		binding.AcknowledgedCursor == result.Cursor &&
		binding.LastReconciledSourceSHA == result.SourceSHA &&
		binding.LastReconciledTargetSHA == result.TargetSHA
}

func sameUnattachedMonitorReservation(
	active *LaunchReservation,
	reservation LaunchReservation,
) bool {
	return active != nil && active.WorkflowRunID == nil &&
		active.ActionDigest == reservation.ActionDigest &&
		active.Token == reservation.Token &&
		active.ObservationSnapshotID == reservation.ObservationSnapshotID &&
		active.Cursor == reservation.Cursor &&
		active.SourceSHA == reservation.SourceSHA &&
		active.TargetSHA == reservation.TargetSHA
}

func validateAttentionReason(reason string) error {
	if strings.TrimSpace(reason) != reason || reason == "" ||
		len(reason) > maxAttentionReasonBytes || !utf8.ValidString(reason) {
		return fmt.Errorf("pullrequest: monitor attention reason is invalid")
	}
	return nil
}
