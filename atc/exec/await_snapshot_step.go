package exec

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
)

type WorkflowWaitStore interface {
	CreateOrGet(context.Context, workflowwait.CreateRequest) (workflowwait.Wait, bool, error)
	Expire(context.Context, workflowwait.ExecutionKey, time.Time) (workflowwait.Wait, bool, error)
}

type AwaitSnapshotTimedOutError struct{}

func (AwaitSnapshotTimedOutError) Error() string { return "await_snapshot: durable wait timed out" }

type AwaitSnapshotCancelledError struct{}

func (AwaitSnapshotCancelledError) Error() string {
	return "await_snapshot: durable wait was cancelled"
}

type AwaitSnapshotStep struct {
	planID          atc.PlanID
	attempt         string
	plan            atc.AwaitSnapshotPlan
	metadata        StepMetadata
	delegateFactory BuildStepDelegateFactory
	waits           WorkflowWaitStore
	outputSealer    snapshot.OutputSealer
	metadataStore   snapshot.MetadataStore
	contentStore    snapshot.ContentStore
	pollInterval    time.Duration
}

type AwaitSnapshotStepOption func(*AwaitSnapshotStep)

func NewAwaitSnapshotStep(
	planID atc.PlanID,
	attempts []int,
	plan atc.AwaitSnapshotPlan,
	metadata StepMetadata,
	delegateFactory BuildStepDelegateFactory,
	waits WorkflowWaitStore,
	outputSealer snapshot.OutputSealer,
	metadataStore snapshot.MetadataStore,
	contentStore snapshot.ContentStore,
	pollInterval time.Duration,
	opts ...AwaitSnapshotStepOption,
) Step {
	attempt := "0"
	if len(attempts) > 0 {
		parts := make([]string, len(attempts))
		for index, value := range attempts {
			parts[index] = strconv.Itoa(value)
		}
		attempt = strings.Join(parts, ".")
	}
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}
	step := &AwaitSnapshotStep{
		planID: planID, attempt: attempt, plan: plan, metadata: metadata, delegateFactory: delegateFactory,
		waits: waits, outputSealer: outputSealer, metadataStore: metadataStore, contentStore: contentStore, pollInterval: pollInterval,
	}
	for _, option := range opts {
		option(step)
	}
	return step
}

func (step *AwaitSnapshotStep) Run(ctx context.Context, state RunState) (bool, error) {
	delegate := step.delegateFactory.BuildStepDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "await_snapshot", tracing.Attrs{
		"name": step.plan.Name, "question": step.plan.Question, "type": step.plan.Type.String(),
	})
	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	return ok, err
}

func (step *AwaitSnapshotStep) run(ctx context.Context, state RunState, delegate BuildStepDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx).Session("await-snapshot-step", lager.Data{
		"step-name": step.plan.Name, "job-id": step.metadata.JobID,
	})
	delegate.Initializing(logger)
	if step.waits == nil || step.metadataStore == nil || step.contentStore == nil {
		return false, fmt.Errorf("await_snapshot: durable waits and snapshots must be enabled on the web node")
	}
	if step.metadata.TeamID <= 0 || step.metadata.BuildID <= 0 {
		return false, fmt.Errorf("await_snapshot: server build identity is unavailable")
	}
	if warning, err := atc.ValidateIdentifier(step.plan.Name); err != nil || warning != nil {
		return false, fmt.Errorf("await_snapshot: invalid output artifact name")
	}
	if step.plan.MergeApproval == nil {
		if warning, err := atc.ValidateIdentifier(step.plan.Question); err != nil || warning != nil {
			return false, fmt.Errorf("await_snapshot: invalid question artifact name")
		}
	} else if step.outputSealer == nil {
		return false, fmt.Errorf("await_snapshot: server-bound approval question sealing is unavailable")
	}
	if step.plan.Type != snapshot.TypeRef("human-answer/v1") {
		return false, fmt.Errorf("await_snapshot: expected type must be human-answer/v1")
	}
	policy := workflowwait.TimeoutPolicy(step.plan.OnTimeout)
	if err := policy.Validate(); err != nil {
		return false, fmt.Errorf("await_snapshot: invalid timeout policy")
	}
	if strings.Contains(step.plan.WorkflowRunID, "((") {
		return false, fmt.Errorf("await_snapshot: unresolved workflow run identifier")
	}
	runID, err := snapshot.ParseWorkflowRunID(step.plan.WorkflowRunID)
	if err != nil {
		return false, fmt.Errorf("await_snapshot: invalid workflow run identifier")
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return false, fmt.Errorf("await_snapshot: an ordinary timeout wrapper is required")
	}
	if step.plan.MergeApproval != nil {
		if step.plan.MergeApprovalValidation == nil {
			return false, fmt.Errorf("await_snapshot: authoritative validation plan is unavailable")
		}
		if err := requireValidationRequirement(ctx, "await_snapshot", state.ArtifactRepository(), step.metadata, step.metadataStore, step.contentStore, mergeRequirement(*step.plan.MergeApprovalValidation), step.plan.MergeApproval.Input); err != nil {
			return false, err
		}
	}
	questionName, question, err := step.questionRef(ctx, state.ArtifactRepository(), runID)
	if err != nil {
		return false, err
	}
	var defaultRef *snapshot.SnapshotRef
	if policy == workflowwait.TimeoutDefault {
		id, err := snapshot.ParseSnapshotID(step.plan.DefaultSnapshotID)
		if err != nil {
			return false, fmt.Errorf("await_snapshot: invalid default snapshot identifier")
		}
		manifest, found, err := step.metadataStore.GetAuthorized(ctx, step.metadata.TeamID, id)
		if err != nil {
			return false, fmt.Errorf("await_snapshot: default snapshot authorization failed")
		}
		if !found || !usableAwaitManifest(manifest, id, step.plan.Type) {
			return false, SnapshotUnavailableError{}
		}
		ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
		defaultRef = &ref
	} else if step.plan.DefaultSnapshotID != "" {
		return false, fmt.Errorf("await_snapshot: default snapshot requires default timeout policy")
	}

	key := workflowwait.ExecutionKey{
		TeamID: step.metadata.TeamID, WorkflowRunID: runID, BuildID: int64(step.metadata.BuildID),
		PlanID: step.planID.String(), Attempt: step.attempt, OutputName: step.plan.Name,
	}
	waitWorkflowDefinitionID := 0
	if step.plan.WorkflowPort != "" {
		waitWorkflowDefinitionID = step.plan.WorkflowDefinitionID
	}
	request := workflowwait.CreateRequest{
		Key: key, QuestionName: questionName, Question: question, ExpectedType: step.plan.Type,
		Deadline: deadline.UTC(), TimeoutPolicy: policy, Default: defaultRef,
		WorkflowPort: step.plan.WorkflowPort, WorkflowDefinitionID: waitWorkflowDefinitionID,
	}
	delegate.Starting(logger)
	wait, _, err := step.waits.CreateOrGet(ctx, request)
	if err != nil {
		return false, fmt.Errorf("await_snapshot: durable wait creation failed")
	}
	if wait.Key != key || wait.Question != question || wait.ExpectedType != step.plan.Type {
		return false, fmt.Errorf("await_snapshot: durable wait identity mismatch")
	}

	for {
		finishCtx, releaseFinish := step.storeContext(ctx, wait.Deadline)
		finished, ok, finishErr := step.finish(finishCtx, state, delegate, logger, wait)
		releaseFinish()
		if finished {
			return ok, finishErr
		}
		delay := step.pollInterval
		untilDeadline := time.Until(wait.Deadline)
		if untilDeadline <= 0 {
			delay = 0
		} else if untilDeadline < delay {
			delay = untilDeadline
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
			// The final poll is armed to fire exactly at the deadline, so this
			// branch routinely runs with the step context already expired.
			refreshCtx, releaseRefresh := step.storeContext(ctx, wait.Deadline)
			wait, err = step.refresh(refreshCtx, key, time.Now().UTC())
			releaseRefresh()
			if err != nil {
				return false, err
			}
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return false, ctx.Err()
			}
			detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), awaitSnapshotFinalizeGrace)
			wait, err = step.refresh(detached, key, time.Now().UTC())
			cancel()
			if err != nil {
				return false, err
			}
			if wait.Status == workflowwait.StatusWaiting {
				return false, context.DeadlineExceeded
			}
		}
	}
}

// awaitSnapshotFinalizeGrace bounds the durable-store work that necessarily
// happens after the step's own deadline has passed: expiring the wait row and
// authorizing whatever answer it ended up holding. It is deliberately short --
// this is finalization, not more waiting.
const awaitSnapshotFinalizeGrace = 5 * time.Second

// storeContext picks the context for a durable-store call.
//
// The durable wait's deadline is the step context's own deadline, so the last
// poll and the context expiry come due at the same instant and the loop keeps
// running for one more pass afterwards. Handing the expired context to the
// store throws away exactly the outcome that pass exists to publish: a human's
// answer that landed inside the final poll window, or the on_timeout: default
// substitute the store just materialized. Once the deadline has elapsed the
// remaining work runs on a short detached budget instead.
//
// Cancellation that is not a deadline expiry is a genuine abort and is passed
// through untouched, so aborting a build still interrupts the wait at once.
func (step *AwaitSnapshotStep) storeContext(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	noop := func() {}
	err := ctx.Err()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return ctx, noop
	}
	if err == nil && time.Now().Before(deadline) {
		return ctx, noop
	}
	return context.WithTimeout(context.WithoutCancel(ctx), awaitSnapshotFinalizeGrace)
}

func (step *AwaitSnapshotStep) refresh(ctx context.Context, key workflowwait.ExecutionKey, now time.Time) (workflowwait.Wait, error) {
	wait, found, err := step.waits.Expire(ctx, key, now)
	if err != nil {
		// Wrapped, not swallowed: TimeoutStep only converts a context deadline
		// into a clean failure when it can still see one through the chain.
		return workflowwait.Wait{}, fmt.Errorf("await_snapshot: durable wait refresh failed: %w", err)
	}
	if !found {
		return workflowwait.Wait{}, fmt.Errorf("await_snapshot: durable wait disappeared")
	}
	if wait.Key != key {
		return workflowwait.Wait{}, fmt.Errorf("await_snapshot: durable wait identity mismatch")
	}
	return wait, nil
}

func (step *AwaitSnapshotStep) finish(
	ctx context.Context,
	state RunState,
	delegate BuildStepDelegate,
	logger lager.Logger,
	wait workflowwait.Wait,
) (bool, bool, error) {
	switch wait.Status {
	case workflowwait.StatusWaiting:
		return false, false, nil
	case workflowwait.StatusResolved:
		if wait.Answer == nil {
			return true, false, fmt.Errorf("await_snapshot: resolved wait has no answer")
		}
	case workflowwait.StatusTimedOut:
		if wait.Answer == nil {
			return true, false, AwaitSnapshotTimedOutError{}
		}
	case workflowwait.StatusCancelled:
		return true, false, AwaitSnapshotCancelledError{}
	default:
		return true, false, fmt.Errorf("await_snapshot: durable wait returned an invalid state")
	}
	manifest, found, err := step.metadataStore.GetAuthorized(ctx, step.metadata.TeamID, wait.Answer.ID)
	if err != nil {
		return true, false, fmt.Errorf("await_snapshot: answer snapshot authorization failed: %w", err)
	}
	if !found || !usableAwaitManifest(manifest, wait.Answer.ID, step.plan.Type) ||
		manifest.Digest != wait.Answer.Digest || manifest.Type != wait.Answer.Type {
		return true, false, SnapshotUnavailableError{}
	}
	artifact, err := runtime.NewSnapshotArtifact(manifest, step.contentStore)
	if err != nil {
		return true, false, fmt.Errorf("await_snapshot: answer snapshot cannot be materialized")
	}
	ref := *wait.Answer
	repository := state.ArtifactRepository()
	name := build.ArtifactName(step.plan.Name)
	if existing, present := repository.ArtifactEntryFor(name); present {
		if identicalSnapshotArtifact(existing, ref, artifact) {
			delegate.Finished(logger, true)
			return true, true, nil
		}
		return true, false, fmt.Errorf("await_snapshot: artifact name is already produced")
	}
	if err := repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		name: {Artifact: artifact, Snapshot: &ref},
	}); err != nil {
		return true, false, fmt.Errorf("await_snapshot: artifact publication failed")
	}
	delegate.Finished(logger, true)
	return true, true, nil
}

func (step *AwaitSnapshotStep) questionRef(
	ctx context.Context,
	repository *build.Repository,
	runID snapshot.WorkflowRunID,
) (string, snapshot.SnapshotRef, error) {
	if step.plan.MergeApproval == nil {
		ref, _, err := step.authorizedAwaitInput(ctx, repository, step.plan.Question, snapshot.TypeRef("question/v1"))
		return step.plan.Question, ref, err
	}
	intent := step.plan.MergeApproval
	if step.plan.WorkflowDefinitionID <= 0 || strings.TrimSpace(step.metadata.TeamName) == "" ||
		strings.TrimSpace(step.metadata.SnapshotCreatedBy) == "" {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: server workflow identity is unavailable")
	}
	if warning, err := atc.ValidateIdentifier(intent.Input); err != nil || warning != nil {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: invalid merge approval input")
	}
	if strings.Contains(intent.Destination, "((") || strings.Contains(intent.Prompt, "((") ||
		publishParametersContainInterpolation(intent.Parameters) {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: unresolved merge approval intent")
	}
	subject, subjectManifest, err := step.authorizedAwaitInput(ctx, repository, intent.Input, snapshot.TypeRef("repository-change/v1"))
	if err != nil {
		return "", snapshot.SnapshotRef{}, err
	}
	// The base assertion a human approves is derived from the exact snapshot
	// being approved, never authored. publish_snapshot derives it from the same
	// snapshot, so the two descriptions of one merge agree by construction.
	baseSHA, err := publisher.MergeBaseFromChange(subjectManifest)
	if err != nil {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: merge base is unavailable for the approved change")
	}
	parameters, err := publisher.StampMergeBase(intent.Parameters, baseSHA)
	if err != nil {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: invalid merge approval intent")
	}
	approvalContext, err := publisher.BuildMergeApprovalContext(publisher.MergeApprovalRequest{
		TeamID: step.metadata.TeamID, WorkflowRunID: runID, BuildID: int64(step.metadata.BuildID),
		Input: subject, Publisher: intent.Publisher, Mode: publisher.ModeMerge,
		Destination: intent.Destination, Parameters: parameters,
		ExpectedBaseSHA:       baseSHA,
		ApprovalPolicyVersion: intent.ApprovalPolicyVersion,
	})
	if err != nil {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: invalid merge approval intent")
	}
	contextJSON, err := json.Marshal(approvalContext)
	if err != nil {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: encode merge approval context")
	}
	document := contracts.QuestionDocument{
		SchemaVersion: "1.0.0", Prompt: intent.Prompt, Context: string(contextJSON),
		Options: []string{"approve", "reject"}, Default: "reject",
	}
	if err := document.Validate(); err != nil {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: invalid merge approval question")
	}
	archive, err := mergeApprovalQuestionArchive(document)
	if err != nil {
		return "", snapshot.SnapshotRef{}, err
	}
	definitionID := step.plan.WorkflowDefinitionID
	runIDCopy := runID
	port := snapshot.Port{Name: "question", Type: snapshot.TypeRef("question/v1")}
	sealed, err := step.outputSealer.Seal(ctx, snapshot.SealRequest{
		BuildID: step.metadata.BuildID, TeamID: step.metadata.TeamID, TeamName: step.metadata.TeamName,
		CreatedBy: step.metadata.SnapshotCreatedBy, PlanID: step.planID.String(), Attempt: step.attempt,
		StepKind: "await_snapshot", StepName: step.plan.Name,
		WorkflowDefinitionID: &definitionID, WorkflowRunID: &runIDCopy,
		InputOrder: []string{intent.Input}, Inputs: map[string]snapshot.SnapshotRef{intent.Input: subject},
		OutputDeclarations: []snapshot.Port{port}, Outputs: []snapshot.OutputSource{{
			ClientKey: "question", Port: port,
			OpenTar: func(context.Context) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(archive)), nil
			},
		}},
	})
	if err != nil {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: seal merge approval question: %w", err)
	}
	output, found := sealed["question"]
	if len(sealed) != 1 || !found || output.Validate() != nil || output.Port != port {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("await_snapshot: merge approval question sealer returned an invalid output")
	}
	return step.plan.Name + "-merge-question", output.Snapshot, nil
}

func (step *AwaitSnapshotStep) authorizedAwaitInput(
	ctx context.Context,
	repository *build.Repository,
	name string,
	expectedType snapshot.TypeRef,
) (snapshot.SnapshotRef, snapshot.Snapshot, error) {
	entry, found := repository.ArtifactEntryFor(build.ArtifactName(name))
	if !found || entry.Artifact == nil || entry.Snapshot == nil || entry.Snapshot.Type != expectedType {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, fmt.Errorf("await_snapshot: sealed input snapshot is unavailable")
	}
	ref := *entry.Snapshot
	if err := ref.Validate(); err != nil {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, fmt.Errorf("await_snapshot: sealed input snapshot is unavailable")
	}
	manifest, found, err := step.metadataStore.GetAuthorized(ctx, step.metadata.TeamID, ref.ID)
	if err != nil {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, fmt.Errorf("await_snapshot: snapshot authorization failed")
	}
	if !found || manifest.Validate() != nil || manifest.ID != ref.ID || manifest.Type != ref.Type ||
		manifest.Digest != ref.Digest || manifest.Type != expectedType || manifest.ContentState != snapshot.ContentStateAvailable {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, fmt.Errorf("await_snapshot: sealed input snapshot is unavailable")
	}
	return ref, manifest, nil
}

func mergeApprovalQuestionArchive(document contracts.QuestionDocument) ([]byte, error) {
	payload, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("await_snapshot: encode merge approval question")
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{
		Name: "question.json", Mode: 0600, Size: int64(len(payload)), Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, fmt.Errorf("await_snapshot: create merge approval question archive: %w", err)
	}
	if _, err := writer.Write(payload); err != nil {
		return nil, fmt.Errorf("await_snapshot: write merge approval question archive: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("await_snapshot: close merge approval question archive: %w", err)
	}
	return archive.Bytes(), nil
}

func usableAwaitManifest(manifest snapshot.Snapshot, id snapshot.SnapshotID, typ snapshot.TypeRef) bool {
	return manifest.Validate() == nil && manifest.ID == id && manifest.Type == typ &&
		manifest.ContentState == snapshot.ContentStateAvailable
}
