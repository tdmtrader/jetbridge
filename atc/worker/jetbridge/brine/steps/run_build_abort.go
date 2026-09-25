package steps

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Aborting a Run build is scoped to that build. When it cannot finish over
// the work it left open, it records a build closure, and the cancellation
// worker closes that work through the real node protocol while the Run keeps
// running. Nothing here requests Run cancellation.
func RunBuildAbortDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[RunOutputRuntime, CancellationLeaseResult]("its build is aborted over {string}", []string{"task-workspace"}, func(in RunOutputRuntime, p brine.Params, rec *brine.Recorder, res brine.Resources) (CancellationLeaseResult, error) {
			work, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseBuildAbort(in, work, rec, res.Get("task-workspace").(TaskWorkspace).Dir)}, nil
		}),
		CheckThat[CancellationLeaseResult]("the build finishes aborted on the node's acknowledgement and its job admits a rerun", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("the Run completes aborted and its payload is reclaimed", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("the producer is interrupted before its hold is released on exact evidence", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("the successful rerun supersedes it and the Run succeeds", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("the other build's live execution is untouched", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("node loss keeps the build open until a restarted worker closes it", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

func exerciseBuildAbort(in RunOutputRuntime, work string, rec *brine.Recorder, workspace string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch work {
	case "an active execution":
		return exerciseAbortedActiveExecution(ctx, in, rec, workspace)
	case "a sibling's live execution":
		return exerciseAbortBesideLiveExecution(ctx, in, rec, workspace)
	case "a lost node":
		return exerciseAbortOverLostNode(ctx, in, rec, workspace)
	case "an unsettled handoff":
		return exerciseAbortedHandoff(ctx, in, rec)
	case "an executing producer":
		return exerciseAbortedExecutingProducer(ctx, in, rec)
	case "a superseded handoff":
		return exerciseSupersededAbortedHandoff(ctx, in, rec)
	}
	return fmt.Errorf("unknown aborted work %q", work)
}

// A1: an open execution, no outputs. The build finishes aborted only once the
// node has acknowledged the interrupted command's exact outcome, and a rerun
// of its job is admitted while the Run is still running.
func exerciseAbortedActiveExecution(ctx context.Context, in RunOutputRuntime, rec *brine.Recorder, workspace string) error {
	h, err := newAbortHarness(in, "abort-active", workspace)
	if err != nil {
		return err
	}
	defer h.stop()
	build := in.Start.Creation.EntryBuilds[0]
	active, err := h.start(ctx, rec, build)
	if err != nil {
		return err
	}
	if err = abortOverOpenWork(in, build); err != nil {
		return err
	}
	source := newRecordingSource(h.in)
	if err = convergeBuildClosure(ctx, h.in, build, func() runs.CancellationWorker { return buildClosureWorker(h.in, source, "closure-worker") }); err != nil {
		return err
	}
	var executionDone, buildDone time.Time
	if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT
 (SELECT completed_at FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind=$2 AND subject=$3),
 (SELECT completed_at FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind=$4 AND subject=$5)`,
		in.Start.Creation.Run.ID(), string(db.CancelExecution), executionSubject(active.admission.Identity), string(db.CancelBuild), strconv.Itoa(build.ID())).Scan(&executionDone, &buildDone); err != nil {
		return fmt.Errorf("the closure did not complete both its execution and its build: %w", err)
	}
	if buildDone.Before(executionDone) {
		return fmt.Errorf("the build finished before the node acknowledged its execution")
	}
	if err = checkExecutionClosedByNode(ctx, in, active.admission); err != nil {
		return err
	}
	if !source.called(active.admission.Identity.ExecutionID, "stop") {
		return fmt.Errorf("the closure never asked the node to stop the open execution")
	}
	if err = active.checkInterrupted(ctx, in); err != nil {
		return err
	}
	if err = checkRunUncancelled(ctx, in); err != nil {
		return err
	}
	job, err := cancellationJob(in.Start)
	if err != nil {
		return err
	}
	rerun, err := job.RerunBuild(build, "brine-rerun")
	if err != nil {
		return fmt.Errorf("the aborted build's job refused a rerun: %w", err)
	}
	if rerun.ID() == build.ID() {
		return fmt.Errorf("the rerun reused the aborted build")
	}
	return checkRunUncancelled(ctx, in)
}

// A5: closing one aborted build makes no node call for another running
// build's execution and changes none of its durable facts, and the Run owes
// no scheduler debt, candidate or terminal operation for it.
func exerciseAbortBesideLiveExecution(ctx context.Context, in RunOutputRuntime, rec *brine.Recorder, workspace string) error {
	h, err := newAbortHarness(in, "abort-beside", workspace)
	if err != nil {
		return err
	}
	defer h.stop()
	aborted, sibling := in.Start.Creation.EntryBuilds[0], in.Start.Creation.EntryBuilds[1]
	if _, err = h.start(ctx, rec, aborted); err != nil {
		return err
	}
	live, err := h.start(ctx, rec, sibling)
	if err != nil {
		return err
	}
	before, err := durableExecutionFacts(ctx, in, sibling, live.admission)
	if err != nil {
		return err
	}
	if err = abortOverOpenWork(in, aborted); err != nil {
		return err
	}
	source := newRecordingSource(h.in)
	if err = convergeBuildClosure(ctx, h.in, aborted, func() runs.CancellationWorker { return buildClosureWorker(h.in, source, "closure-worker") }); err != nil {
		return err
	}
	if calls := source.callsFor(live.admission.Identity.ExecutionID); len(calls) != 0 {
		return fmt.Errorf("closing another build called the node for the live execution: %v", calls)
	}
	after, err := durableExecutionFacts(ctx, in, sibling, live.admission)
	if err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("closing another build changed the live execution's durable facts:\n before %s\n after  %s", before, after)
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	classified, err := client.Classify(ctx, live.admission.Identity)
	if err != nil {
		return err
	}
	if classified.Classification != executioncontrol.ClassificationExecuting {
		return fmt.Errorf("the live execution is %s on its node", classified.Classification)
	}
	if err = syscall.Kill(live.child, 0); err != nil {
		return fmt.Errorf("the live execution's command no longer runs: %w", err)
	}
	var foreign int
	if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT count(*) FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind IN ($2,$3,$4)`,
		in.Start.Creation.Run.ID(), string(db.CancelSchedulerDebt), string(db.CancelCandidate), string(db.CancelTerminalize)).Scan(&foreign); err != nil {
		return err
	}
	if foreign != 0 {
		return fmt.Errorf("a build closure created %d scheduler-debt, candidate or terminal operations", foreign)
	}
	return checkRunUncancelled(ctx, in)
}

// A7: while the node cannot answer, the closure keeps the build open and the
// Run running, and never asks for the Run's cancellation. A worker restarted
// once the node is back resumes the closure from what PostgreSQL retained.
func exerciseAbortOverLostNode(ctx context.Context, in RunOutputRuntime, rec *brine.Recorder, workspace string) error {
	h, err := newAbortHarness(in, "abort-lost-node", workspace)
	if err != nil {
		return err
	}
	defer h.stop()
	build := in.Start.Creation.EntryBuilds[0]
	active, err := h.start(ctx, rec, build)
	if err != nil {
		return err
	}
	if err = abortOverOpenWork(in, build); err != nil {
		return err
	}
	if err = in.Start.Daemon.Output.crash(); err != nil {
		return err
	}
	source := newRecordingSource(h.in)
	for pass := 0; pass < 3; pass++ {
		// A lost node answers nothing; the pass records that as debt.
		first := buildClosureWorker(h.in, source, "closure-worker")
		_ = first.Run(ctx)
		if err = pause(ctx); err != nil {
			return err
		}
	}
	var attempted, closed, open bool
	if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind=$2 AND subject=$3 AND attempt_count>0 AND completed_at IS NULL),
 EXISTS(SELECT 1 FROM pipeline_run_execution_closures WHERE execution_id=$4 AND execution_fence=$5),
 EXISTS(SELECT 1 FROM pipeline_run_build_closures WHERE build_id=$6 AND closed_at IS NULL)`,
		in.Start.Creation.Run.ID(), string(db.CancelExecution), executionSubject(active.admission.Identity),
		string(active.admission.Identity.ExecutionID), int64(active.admission.Identity.Fence), build.ID()).Scan(&attempted, &closed, &open); err != nil {
		return err
	}
	if !attempted || closed || !open {
		return fmt.Errorf("node loss: execution attempted=%t, closed=%t, closure open=%t", attempted, closed, open)
	}
	if completed, _, err := buildOutcome(ctx, in, build); err != nil || completed {
		return fmt.Errorf("node loss finished the build (error %v)", err)
	}
	if err = checkRunUncancelled(ctx, in); err != nil {
		return err
	}

	if err = in.Start.Daemon.Output.restart(ctx, in.Start.Daemon.HTTP); err != nil {
		return err
	}
	// The first worker's process is gone: its lease runs out and a new owner
	// takes over, with nothing but the retained queue to resume from.
	if _, err = in.Start.DB.Conn.ExecContext(ctx, `UPDATE pipeline_run_cancellation_worker SET renewed_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		return err
	}
	if err = convergeBuildClosure(ctx, h.in, build, func() runs.CancellationWorker {
		return buildClosureWorker(h.in, newRecordingSource(h.in), "restarted-closure-worker")
	}); err != nil {
		return err
	}
	if err = checkExecutionClosedByNode(ctx, in, active.admission); err != nil {
		return err
	}
	if err = active.checkInterrupted(ctx, in); err != nil {
		return err
	}
	return checkRunUncancelled(ctx, in)
}

// A2: a producer that never started leaves a held, unsettled handoff. The
// closure releases the hold on the node's never-started evidence and finishes
// the build aborted; the Run then completes aborted by ordinary completion,
// with no manual step, and its payload is reclaimed.
func exerciseAbortedHandoff(ctx context.Context, in RunOutputRuntime, rec *brine.Recorder) error {
	r, _, err := holdProducerSource(ctx, in, rec)
	if err != nil {
		return err
	}
	build := in.Start.Creation.EntryBuilds[0]
	if err = abortOverOpenWork(in, build); err != nil {
		return err
	}
	source := newRecordingSource(in)
	if err = convergeBuildClosure(ctx, in, build, func() runs.CancellationWorker { return buildClosureWorker(in, source, "closure-worker") }); err != nil {
		return err
	}
	var evidence string
	var released bool
	if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT
 coalesce((SELECT classification FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=$1),''),
 EXISTS(SELECT 1 FROM pipeline_run_output_releases WHERE handoff_id=$1)`, string(r.HandoffID)).Scan(&evidence, &released); err != nil {
		return err
	}
	if evidence != string(executioncontrol.ClassificationNeverStarted) || !released {
		return fmt.Errorf("the closure settled the handoff with evidence %q and released=%t", evidence, released)
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, r.ActivationEpoch)
	if _, err = client.RecordStart(ctx, r.Execution, "delayed-pod", "delayed-supervisor"); err == nil {
		return fmt.Errorf("the released source still admitted a delayed start")
	}
	if err = checkRunUncancelled(ctx, in); err != nil {
		return err
	}

	if err = in.Start.Creation.EntryBuilds[1].Finish(db.BuildStatusSucceeded); err != nil {
		return err
	}
	if err = consumeRunScheduling(in.Start); err != nil {
		return err
	}
	if err = finalizeRun(ctx, in); err != nil {
		return err
	}
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	result, found, err := factory.TerminalResult(ctx, in.Start.Creation.Run.ID())
	if err != nil {
		return err
	}
	if !found || result.Status != atc.RunStatusAborted || len(result.Results) != 0 {
		return fmt.Errorf("an effective aborted build did not make the Run aborted: found=%t, %+v", found, result)
	}
	if err = checkNoCancellationRequest(ctx, in); err != nil {
		return err
	}

	// Age the published Run past a one-day TTL. Its terminal publication is
	// immutable by trigger, so the fixture steps around it the way a clock
	// would, and only for this one column.
	if _, err = in.Start.DB.Conn.ExecContext(ctx, `UPDATE pipelines SET run_retention_ttl_days=1 WHERE id=(SELECT template_pipeline_id FROM pipeline_runs WHERE id=$1)`, in.Start.Creation.Run.ID()); err != nil {
		return err
	}
	tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE pipeline_runs SET completed_at=completed_at - interval '2 days' WHERE id=$1`, in.Start.Creation.Run.ID())
	}
	if err == nil {
		err = tx.Commit()
	}
	db.Rollback(tx)
	if err != nil {
		return err
	}
	destroyed, err := db.NewPipelineRunReclaimLifecycle(in.Start.DB.Conn).DestroyReclaimableRun(in.Start.Creation.Run.ID())
	if err != nil {
		return err
	}
	var payloads int
	if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT count(*) FROM pipelines WHERE pipeline_run_id=$1`, in.Start.Creation.Run.ID()).Scan(&payloads); err != nil {
		return err
	}
	if !destroyed || payloads != 0 {
		return fmt.Errorf("the aborted Run's payload was not reclaimed: destroyed=%t, %d payloads", destroyed, payloads)
	}
	return nil
}

// A3: an executing producer with a held source. The closure first asks the
// node for a source-preserving stop, and releases the hold only once the node
// has recorded the producer's exact outcome. It deletes no Pod: the hold's
// Pod still exists when the release is recorded.
func exerciseAbortedExecutingProducer(ctx context.Context, in RunOutputRuntime, rec *brine.Recorder) error {
	r, hold, err := holdProducerSource(ctx, in, rec)
	if err != nil {
		return err
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, r.ActivationEpoch)
	if _, err = client.RecordStart(ctx, r.Execution, hold.PodUID, executioncontrol.ProcessIdentity(freshUUID())); err != nil {
		return err
	}
	build := in.Start.Creation.EntryBuilds[0]
	if err = abortOverOpenWork(in, build); err != nil {
		return err
	}
	source := newRecordingSource(in)
	// Run until the closure has asked for the stop, then one pass more: a
	// release could only follow it.
	for pass, extra := 0, 1; pass < 12 && extra >= 0; pass++ {
		worker := buildClosureWorker(in, source, "closure-worker")
		if err = worker.Run(ctx); err != nil && !onlyExternalCancellationWork(err) {
			return err
		}
		if source.called(r.Execution.ExecutionID, "stop") {
			extra--
		}
		if err = pause(ctx); err != nil {
			return err
		}
	}
	var classification, evidence string
	var released bool
	query := `SELECT
 coalesce((SELECT classification FROM pipeline_run_output_cancellation_classifications WHERE handoff_id=$1),''),
 coalesce((SELECT classification FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=$1),''),
 EXISTS(SELECT 1 FROM pipeline_run_output_releases WHERE handoff_id=$1)`
	if err = in.Start.DB.Conn.QueryRowContext(ctx, query, string(r.HandoffID)).Scan(&classification, &evidence, &released); err != nil {
		return err
	}
	if classification != string(executioncontrol.ClassificationExecuting) || evidence != "" || released {
		return fmt.Errorf("while the producer executed: classification %q, evidence %q, released=%t", classification, evidence, released)
	}
	if !source.called(r.Execution.ExecutionID, "stop") {
		return fmt.Errorf("the closure never asked the node for a source-preserving stop")
	}
	if source.called(r.Execution.ExecutionID, "release") {
		return fmt.Errorf("the node was asked to release a hold its producer still executes over")
	}
	if _, err = client.InspectHold(ctx, r.Execution, r.HandoffID); err != nil {
		return fmt.Errorf("the executing producer's hold is gone: %w", err)
	}
	if err = checkHoldPod(ctx, in, hold.PodUID); err != nil {
		return err
	}
	if completed, _, err := buildOutcome(ctx, in, build); err != nil || completed {
		return fmt.Errorf("the build finished while its producer executed (error %v)", err)
	}

	source.note(r.Execution.ExecutionID, "outcome")
	if _, err = client.RecordOutcome(ctx, r.Execution, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 143}); err != nil {
		return err
	}
	if err = convergeBuildClosure(ctx, in, build, func() runs.CancellationWorker { return buildClosureWorker(in, source, "closure-worker") }); err != nil {
		return err
	}
	if err = in.Start.DB.Conn.QueryRowContext(ctx, query, string(r.HandoffID)).Scan(&classification, &evidence, &released); err != nil {
		return err
	}
	if evidence != string(executioncontrol.ClassificationAuthoritativeFinish) || !released {
		return fmt.Errorf("after the exact outcome: evidence %q, released=%t", evidence, released)
	}
	calls := source.callsFor(r.Execution.ExecutionID)
	stop, outcome, release := slices.Index(calls, "stop"), slices.Index(calls, "outcome"), slices.Index(calls, "release")
	if stop < 0 || release < 0 || !(stop < outcome && outcome < release) {
		return fmt.Errorf("node calls out of order, want stop < outcome < release: %v", calls)
	}
	if err = checkHoldPod(ctx, in, hold.PodUID); err != nil {
		return err
	}
	return checkRunUncancelled(ctx, in)
}

// A4: a rerun of the aborted build's job succeeds, with its own published
// candidate, before the closure settles the aborted build's handoff. The rerun
// supersedes the aborted build, and the Run completes succeeded.
func exerciseSupersededAbortedHandoff(ctx context.Context, in RunOutputRuntime, rec *brine.Recorder) error {
	var err error
	if in.Control, err = in.prepare(); err != nil {
		return err
	}
	build := in.Start.Creation.EntryBuilds[0]
	if err = abortOverOpenWork(in, build); err != nil {
		return err
	}
	job, err := cancellationJob(in.Start)
	if err != nil {
		return err
	}
	rerun, err := job.RerunBuild(build, "brine-rerun")
	if err != nil {
		return fmt.Errorf("the aborted build's job refused a rerun: %w", err)
	}
	rerunIn := in
	rerunIn.Start.Creation.EntryBuilds = append([]db.Build{rerun}, in.Start.Creation.EntryBuilds[1:]...)
	candidate, err := publishRunCandidate(rerunIn, rec)
	if err != nil {
		return fmt.Errorf("the rerun did not publish its candidate: %w", err)
	}
	if candidate.Finish.Release, err = candidate.Finish.daemonRelease(); err != nil {
		return err
	}
	if err = candidate.Finish.recordRelease(candidate.Finish.Release, false); err != nil {
		return err
	}
	if err = rerun.Finish(db.BuildStatusSucceeded); err != nil {
		return err
	}
	var open bool
	if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_build_closures WHERE build_id=$1 AND closed_at IS NULL)`, build.ID()).Scan(&open); err != nil {
		return err
	}
	if !open {
		return fmt.Errorf("the rerun did not succeed before the closure finished")
	}
	source := newRecordingSource(in)
	if err = convergeBuildClosure(ctx, in, build, func() runs.CancellationWorker { return buildClosureWorker(in, source, "closure-worker") }); err != nil {
		return err
	}
	if err = in.Start.Creation.EntryBuilds[1].Finish(db.BuildStatusSucceeded); err != nil {
		return err
	}
	if err = consumeRunScheduling(in.Start); err != nil {
		return err
	}
	if err = finalizeRun(ctx, in); err != nil {
		return err
	}
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	result, found, err := factory.TerminalResult(ctx, in.Start.Creation.Run.ID())
	if err != nil {
		return err
	}
	if !found || result.Status != atc.RunStatusSucceeded {
		return fmt.Errorf("the successful rerun did not supersede the aborted build: found=%t, %+v", found, result)
	}
	var selected int
	if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT s.build_id FROM pipeline_run_output_candidates c JOIN pipeline_run_output_starts s USING(handoff_id)
 JOIN hangar_claims claim USING(claim_id) WHERE s.run_id=$1 AND claim.released_at IS NULL`, in.Start.Creation.Run.ID()).Scan(&selected); err != nil {
		return fmt.Errorf("the succeeded Run selected no single candidate: %w", err)
	}
	if selected != rerun.ID() {
		return fmt.Errorf("the Run selected build %d's candidate, not the rerun's", selected)
	}
	return checkNoCancellationRequest(ctx, in)
}

// abortHarness is the real worker of run_execution_runtime.go, able to start
// active supervised commands for any build of the Run on its one node.
type abortHarness struct {
	in        RunOutputRuntime
	worker    *jetbridge.Worker
	config    jetbridge.Config
	factory   db.PipelineRunFactory
	workspace string
	stops     []func()
}

func newAbortHarness(in RunOutputRuntime, name, workspace string) (*abortHarness, error) {
	row, err := in.Start.DB.PersistNamedWorker(name)
	if err != nil {
		return nil, err
	}
	h := &abortHarness{in: in, config: in.Config, factory: db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory), workspace: workspace}
	h.config.OutputPlaneEnabled = true
	h.config.OutputActivationEpoch = int64(hangarEpoch)
	executor := runWitnessExecutor{localExecutor: localExecutor{client: in.Client, supervisorRoot: workspace}, conn: in.Start.DB.Conn}
	// Recovery reads the interrupted command's exit from its Pod's journal.
	h.in.OutcomeReader = executor.localExecutor
	keys := closureControlKeys(in)
	h.worker = jetbridge.NewWorker(row, in.Client, h.config, jetbridge.WorkerDeps{
		Executor:          executor,
		OutputControls:    jetbridge.NewOutputControls(h.config, jetbridge.NewNodeIPResolver(in.Client), in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch)),
		ExecutionPreparer: &runs.ExecutionStarter{Conn: in.Start.DB.Conn, Factory: h.factory, Source: jetbridge.NewOutputSource(in.Client, h.config, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch)), Epoch: executioncontrol.ActivationEpoch(hangarEpoch), Verifier: keys},
	})
	return h, nil
}

// stop ends every command a scenario left running and joins its waiter.
func (h *abortHarness) stop() {
	for _, stop := range h.stops {
		stop()
	}
}

type activeExecution struct {
	admission db.RunExecutionAdmission
	pod       *corev1.Pod
	marker    string
	child     int
}

// start admits a task execution for the build, schedules its Pod on the node
// and runs a supervised command that stays active in a real child process.
func (h *abortHarness) start(ctx context.Context, rec *brine.Recorder, build db.Build) (activeExecution, error) {
	var out activeExecution
	metadata := db.ContainerMetadata{BuildID: build.ID(), PipelineID: build.PipelineID(), Type: db.ContainerTypeTask}
	spec := runtime.ContainerSpec{TeamID: build.TeamID(), Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}}
	owner := db.NewBuildStepContainerOwner(build.ID(), "abort-step", build.TeamID())
	c, _, err := h.worker.FindOrCreateContainer(ctx, owner, metadata, spec, nil)
	if err != nil {
		return out, err
	}
	tx, err := h.in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	var found bool
	out.admission, found, err = h.factory.RunExecution(ctx, tx, build.ID(), "abort-step")
	db.Rollback(tx)
	if err != nil {
		return out, err
	}
	if !found || out.admission.NodeUID != string(h.in.Node.UID) {
		return out, fmt.Errorf("worker did not admit build %d's execution on its node", build.ID())
	}
	name := jetbridge.GeneratePodName(metadata, c.DBContainer().Handle())
	TrackDisposer(rec, "the aborted build's pod "+name, func() error {
		return releasedIfGone(h.in.Client.CoreV1().Pods(h.config.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{}))
	})
	out.marker = filepath.Join(h.workspace, fmt.Sprintf("abort-command-%d", build.ID()))
	script := `printf x >> "$1"; sleep 60 & C=$!; printf '%s\n' "$C" > "$1.child"; wait "$C"`
	command := runtime.ProcessSpec{ID: fmt.Sprintf("abort-command-%d", build.ID()), Path: "sh", Args: []string{"-c", script, "brine", out.marker}}
	commandCtx, cancel := context.WithCancel(context.Background())
	process, err := c.Run(commandCtx, command, runtime.ProcessIO{Stdout: new(strings.Builder), Stderr: new(strings.Builder)})
	if err != nil {
		cancel()
		return out, err
	}
	done := make(chan struct{})
	go func() { _, _ = process.Wait(commandCtx); close(done) }()
	h.stops = append(h.stops, func() {
		if out.child != 0 {
			_ = syscall.Kill(out.child, syscall.SIGKILL)
		}
		cancel()
		<-done
	})
	if err = bindRunningPod(ctx, h.in, h.config.Namespace, name); err != nil {
		return out, err
	}
	if out.pod, err = h.in.Client.CoreV1().Pods(h.config.Namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
		return out, err
	}
	for out.child == 0 {
		if data, err := os.ReadFile(out.marker + ".child"); err == nil {
			out.child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		select {
		case <-ctx.Done():
			return out, fmt.Errorf("build %d's command never started its child", build.ID())
		case <-time.After(10 * time.Millisecond):
		}
	}
	return out, nil
}

// checkInterrupted proves the node's stop reached the real command: its child
// is gone or reaped, it ran once, and its Pod was not destroyed.
func (a activeExecution) checkInterrupted(ctx context.Context, in RunOutputRuntime) error {
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", a.child)); err == nil {
		_, rest, ok := strings.Cut(string(stat), ") ")
		if !ok || !strings.HasPrefix(rest, "Z ") {
			return fmt.Errorf("the closed execution left a running descendant")
		}
	}
	data, err := os.ReadFile(a.marker)
	if err != nil || string(data) != "x" {
		return fmt.Errorf("the closure repeated or lost the command: %q, %v", data, err)
	}
	current, err := in.Client.CoreV1().Pods(a.pod.Namespace).Get(ctx, a.pod.Name, metav1.GetOptions{})
	if err != nil || current.UID != a.pod.UID || current.DeletionTimestamp != nil {
		return fmt.Errorf("the closure destroyed the execution's Pod: %v", err)
	}
	return nil
}

func bindRunningPod(ctx context.Context, in RunOutputRuntime, namespace, name string) error {
	if err := in.Client.CoreV1().Pods(namespace).Bind(ctx, &corev1.Binding{ObjectMeta: metav1.ObjectMeta{Name: name}, Target: corev1.ObjectReference{Kind: "Node", Name: in.Node.Name}}, metav1.CreateOptions{}); err != nil {
		return err
	}
	pod, err := in.Client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	_, err = in.Client.CoreV1().Pods(namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{})
	return err
}

// holdProducerSource prepares the Run's producer, establishes its signed
// source hold on the node, and retains the hold in the Run.
func holdProducerSource(ctx context.Context, in RunOutputRuntime, rec *brine.Recorder) (output.HandoffRecord, output.CaptureAcknowledgement, error) {
	var hold output.CaptureAcknowledgement
	var err error
	if in.Control, err = in.prepare(); err != nil {
		return output.HandoffRecord{}, hold, err
	}
	if err = checkRuntimeGrant(in, rec); err != nil {
		return output.HandoffRecord{}, hold, err
	}
	r, err := in.readSource()
	if err != nil {
		return r, hold, err
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, r.ActivationEpoch)
	if hold, err = client.InspectHold(ctx, r.Execution, r.HandoffID); err != nil {
		return r, hold, err
	}
	tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return r, hold, err
	}
	err = (RunOutputFinish{Start: in.Start}).repository().AcknowledgeSourceHold(ctx, tx, hold)
	if err == nil {
		err = tx.Commit()
	}
	db.Rollback(tx)
	return r, hold, err
}

// checkHoldPod proves the Pod the hold names still exists, undeleted.
func checkHoldPod(ctx context.Context, in RunOutputRuntime, uid executioncontrol.PodUID) error {
	pods, err := in.Client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if string(pod.UID) == string(uid) {
			if pod.DeletionTimestamp != nil {
				return fmt.Errorf("the closure deleted the producer's Pod")
			}
			return nil
		}
	}
	return fmt.Errorf("the producer's Pod is gone")
}

// abortOverOpenWork aborts the build over the work it left open. Finishing it
// records its build closure, leaves it unfinished and never cancels its Run.
func abortOverOpenWork(in RunOutputRuntime, build db.Build) error {
	if err := build.MarkAsAborted(); err != nil {
		return err
	}
	if err := build.Finish(db.BuildStatusAborted); !errors.Is(err, atc.ErrRunOutputPending) {
		return fmt.Errorf("an aborted build finished over its open work: %v", err)
	}
	var open bool
	if err := in.Start.DB.Conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pipeline_run_build_closures WHERE build_id=$1 AND closed_at IS NULL)`, build.ID()).Scan(&open); err != nil {
		return err
	}
	if !open {
		return fmt.Errorf("aborting build %d over open work recorded no build closure", build.ID())
	}
	return checkRunUncancelled(context.Background(), in)
}

// convergeBuildClosure runs worker passes until the build's closure closes.
// Each pass may be a fresh worker, which resumes from the retained queue.
func convergeBuildClosure(ctx context.Context, in RunOutputRuntime, build db.Build, worker func() runs.CancellationWorker) error {
	for pass := 0; pass < 30; pass++ {
		w := worker()
		if err := w.Run(ctx); err != nil && !onlyExternalCancellationWork(err) {
			return err
		}
		var closed bool
		if err := in.Start.DB.Conn.QueryRowContext(ctx, `SELECT closed_at IS NOT NULL FROM pipeline_run_build_closures WHERE build_id=$1`, build.ID()).Scan(&closed); err != nil {
			return err
		}
		if closed {
			completed, status, err := buildOutcome(ctx, in, build)
			if err != nil {
				return err
			}
			if !completed || status != string(db.BuildStatusAborted) {
				return fmt.Errorf("the closure closed over a build that is %s (completed=%t)", status, completed)
			}
			return nil
		}
		if err := pause(ctx); err != nil {
			return err
		}
	}
	return fmt.Errorf("the build closure never closed")
}

func pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(1100 * time.Millisecond):
		return nil
	}
}

func buildOutcome(ctx context.Context, in RunOutputRuntime, build db.Build) (bool, string, error) {
	var completed bool
	var status string
	err := in.Start.DB.Conn.QueryRowContext(ctx, `SELECT completed,status FROM builds WHERE id=$1`, build.ID()).Scan(&completed, &status)
	return completed, status, err
}

// checkExecutionClosedByNode proves the execution was closed on the node's
// signed outcome for the exact command, never invented.
func checkExecutionClosedByNode(ctx context.Context, in RunOutputRuntime, a db.RunExecutionAdmission) error {
	var classification string
	if err := in.Start.DB.Conn.QueryRowContext(ctx, `SELECT classification FROM pipeline_run_execution_closures WHERE execution_id=$1 AND execution_fence=$2`,
		string(a.Identity.ExecutionID), int64(a.Identity.Fence)).Scan(&classification); err != nil {
		return fmt.Errorf("the execution was not closed: %w", err)
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	classified, err := client.Classify(ctx, a.Identity)
	if err != nil {
		return err
	}
	if classification != string(executioncontrol.ClassificationAuthoritativeFinish) || classified.Classification != executioncontrol.ClassificationAuthoritativeFinish || classified.Acknowledgement == nil {
		return fmt.Errorf("the execution was closed as %q while its node says %s", classification, classified.Classification)
	}
	return nil
}

// checkRunUncancelled proves the Run is still running and nothing, system or
// owner, asked for its cancellation.
func checkRunUncancelled(ctx context.Context, in RunOutputRuntime) error {
	var status string
	if err := in.Start.DB.Conn.QueryRowContext(ctx, `SELECT status FROM pipeline_runs WHERE id=$1`, in.Start.Creation.Run.ID()).Scan(&status); err != nil {
		return err
	}
	if status != string(atc.RunStatusRunning) {
		return fmt.Errorf("aborting one build left its Run %s", status)
	}
	return checkNoCancellationRequest(ctx, in)
}

func checkNoCancellationRequest(ctx context.Context, in RunOutputRuntime) error {
	var requested bool
	var by string
	if err := in.Start.DB.Conn.QueryRowContext(ctx, `SELECT cancel_requested_at IS NOT NULL,coalesce(cancel_requested_by,'') FROM pipeline_runs WHERE id=$1`, in.Start.Creation.Run.ID()).Scan(&requested, &by); err != nil {
		return err
	}
	if requested || by != "" {
		return fmt.Errorf("aborting one build cancelled its whole Run (requested by %q)", by)
	}
	return nil
}

// finalizeRun publishes the Run through ordinary completion.
func finalizeRun(ctx context.Context, in RunOutputRuntime) error {
	tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	completed, err := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory).FinalizeOutputRun(ctx, tx, in.Start.Creation.Run.ID())
	if err != nil {
		return err
	}
	if !completed {
		return fmt.Errorf("ordinary completion did not publish the Run")
	}
	return tx.Commit()
}

// durableExecutionFacts renders everything the Run holds about one build's
// execution, so a comparison sees any change a closure made to it.
func durableExecutionFacts(ctx context.Context, in RunOutputRuntime, build db.Build, a db.RunExecutionAdmission) (string, error) {
	var facts string
	err := in.Start.DB.Conn.QueryRowContext(ctx, `SELECT jsonb_build_object(
 'build',(SELECT jsonb_build_object('status',status,'completed',completed,'aborted',aborted) FROM builds WHERE id=$1),
 'executions',(SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY e.execution_id),'[]'::jsonb) FROM pipeline_run_executions e WHERE e.build_id=$1),
 'starts',(SELECT count(*) FROM pipeline_run_execution_starts WHERE execution_id=$2 AND execution_fence=$3),
 'closures',(SELECT count(*) FROM pipeline_run_execution_closures WHERE execution_id=$2 AND execution_fence=$3),
 'closure',(SELECT count(*) FROM pipeline_run_build_closures WHERE build_id=$1),
 'operations',(SELECT count(*) FROM pipeline_run_cancellation_operations WHERE run_id=$4 AND (subject=$5 OR (kind=$6 AND subject=$1::text))))::text`,
		build.ID(), string(a.Identity.ExecutionID), int64(a.Identity.Fence), in.Start.Creation.Run.ID(), executionSubject(a.Identity), string(db.CancelBuild)).Scan(&facts)
	return facts, err
}

func executionSubject(id executioncontrol.Identity) string {
	return fmt.Sprintf("%s/%d", id.ExecutionID, id.Fence)
}

func closureControlKeys(in RunOutputRuntime) hangaroutput.ControlKeyRing {
	return hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}}
}

// buildClosureWorker is the production cancellation worker: factory,
// sources, executions and finality, in atccmd's order, over the given node
// source.
func buildClosureWorker(in RunOutputRuntime, source runs.CancellationSourcePlane, owner string) runs.CancellationWorker {
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	keys := closureControlKeys(in)
	sources := &runs.CancellationSources{Conn: in.Start.DB.Conn, Factory: factory, Repository: (RunOutputFinish{Start: in.Start}).repository(), Source: source, Coordinator: &hangaroutput.Coordinator{OwnerID: owner, HoldVerifier: keys}, Verifier: keys}
	executions := &runs.CancellationExecutions{Conn: in.Start.DB.Conn, Factory: factory, Source: source, Verifier: keys}
	return runs.CancellationWorker{Conn: in.Start.DB.Conn, Factory: factory, OwnerID: owner,
		Actions: runs.CancellationActionSet{factory, sources, executions, runs.CancellationActionFunc(factory.ExecuteCancellationFinality)}}
}

// recordingSource is the real node source, with every node call it makes
// recorded in order against the execution it names.
type recordingSource struct {
	*jetbridge.OutputSource
	mu    *sync.Mutex
	calls *[]recordedNodeCall
}

type recordedNodeCall struct {
	execution executioncontrol.ExecutionID
	call      string
}

func newRecordingSource(in RunOutputRuntime) recordingSource {
	return recordingSource{OutputSource: in.source(), mu: &sync.Mutex{}, calls: &[]recordedNodeCall{}}
}

func (s recordingSource) note(execution executioncontrol.ExecutionID, call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.calls = append(*s.calls, recordedNodeCall{execution, call})
}

func (s recordingSource) callsFor(execution executioncontrol.ExecutionID) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var calls []string
	for _, c := range *s.calls {
		if c.execution == execution {
			calls = append(calls, c.call)
		}
	}
	return calls
}

func (s recordingSource) called(execution executioncontrol.ExecutionID, call string) bool {
	return slices.Contains(s.callsFor(execution), call)
}

func (s recordingSource) ClassifyExecution(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch, id executioncontrol.Identity) (executioncontrol.ClassifyResult, error) {
	s.note(id.ExecutionID, "classify")
	return s.OutputSource.ClassifyExecution(ctx, name, uid, epoch, id)
}

func (s recordingSource) ExecutionStart(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch, id executioncontrol.Identity) (executioncontrol.Acknowledgement, error) {
	s.note(id.ExecutionID, "start")
	return s.OutputSource.ExecutionStart(ctx, name, uid, epoch, id)
}

func (s recordingSource) StopExecution(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch, id executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error) {
	s.note(id.ExecutionID, "stop")
	return s.OutputSource.StopExecution(ctx, name, uid, epoch, id)
}

func (s recordingSource) InterruptExecution(ctx context.Context, node string, start executioncontrol.Acknowledgement) error {
	s.note(start.Identity.ExecutionID, "interrupt")
	return s.OutputSource.InterruptExecution(ctx, node, start)
}

func (s recordingSource) RecoverExecutionOutcome(ctx context.Context, node string, start executioncontrol.Acknowledgement) (executioncontrol.ClassifyResult, error) {
	s.note(start.Identity.ExecutionID, "recover")
	return s.OutputSource.RecoverExecutionOutcome(ctx, node, start)
}

func (s recordingSource) BaseRuntimeControl(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch, id executioncontrol.Identity) (*runtime.ExecutionControl, error) {
	s.note(id.ExecutionID, "reconcile")
	return s.OutputSource.BaseRuntimeControl(ctx, name, uid, epoch, id)
}

func (s recordingSource) CaptureControl(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch) (hangaroutput.SourceControl, error) {
	control, err := s.OutputSource.CaptureControl(ctx, name, uid, epoch)
	if err != nil {
		return nil, err
	}
	return recordingCapture{SourceControl: control, source: s}, nil
}

type recordingCapture struct {
	hangaroutput.SourceControl
	source recordingSource
}

func (c recordingCapture) InspectHold(ctx context.Context, id executioncontrol.Identity, handoff output.HandoffID) (output.CaptureAcknowledgement, error) {
	c.source.note(id.ExecutionID, "inspect-hold")
	return c.SourceControl.InspectHold(ctx, id, handoff)
}

func (c recordingCapture) Observe(ctx context.Context, id executioncontrol.Identity, wait time.Duration) (executioncontrol.ObserveFinishOrStopResult, error) {
	c.source.note(id.ExecutionID, "observe")
	return c.SourceControl.Observe(ctx, id, wait)
}

func (c recordingCapture) AcknowledgeRelease(ctx context.Context, intent output.ReleaseIntent) (output.ReleaseAcknowledgement, error) {
	c.source.note(intent.Execution.ExecutionID, "release")
	return c.SourceControl.AcknowledgeRelease(ctx, intent)
}
