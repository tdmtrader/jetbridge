package steps

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	reviewclient "github.com/concourse/concourse/agent/review/client"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/pipelinerunserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

// This joins the installed template and image to real HTTP, PostgreSQL, node
// storage and kubelet execution. Only the model executable is substituted.
// Production component methods drive scheduling and settlement explicitly;
// this does not simulate Pod status or claim to test ATC background polling.
func ReviewKubeletSubmitDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[brine.Empty, brine.Empty]("an installed review survives a disconnected {string} client", []string{"jetbridge-db", "auth-server", "review-binaries", "review-workspace"}, func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (brine.Empty, error) {
		surface, _ := p.GetString(0)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		in, executor, err := liveReviewRuntime(ctx, rec, res)
		if err == nil {
			err = liveSubmittedReview(ctx, in, executor, surface, rec, res)
		}
		return brine.Empty{}, err
	})}
}

func liveSubmittedReview(ctx context.Context, in RunOutputRuntime, executor jetbridge.PodExecutor, surface string, rec *brine.Recorder, res brine.Resources) error {
	image := os.Getenv("BRINE_REVIEW_IMAGE")
	if image == "" {
		return fmt.Errorf("installed review acceptance requires the CI-built review image")
	}
	// The template and the credential pin must name the same digest-qualified
	// image: the owner's credentials are delivered into nothing else.
	image, err := digestReviewImage(ctx, image)
	if err != nil {
		return err
	}
	source, signer, config, err := configureRunReadPlaneForClient(RunResultPublication{Start: in.Start}, rec, in.Client, in.Config)
	if err != nil {
		return err
	}
	source.SetExecutor(executor)
	config.HangarEnabled = true
	config.ArtifactDaemonHostPath = in.Start.Daemon.Output.Root
	in.Config = config
	jdb := in.Start.DB
	factory := db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory)
	team, found, err := jdb.TeamFactory.FindTeam("output-start")
	if err != nil || !found {
		return fmt.Errorf("find review team: %v", err)
	}
	if err = team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:owner"}}}); err != nil {
		return err
	}
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
	if err != nil {
		return err
	}
	admitter := runs.NewAdmitter(jdb.Conn, factory, jdb.TeamFactory, display, nil)
	authority, err := runinput.NewAuthority(bytes.Repeat([]byte{0x56}, 32), time.Now)
	if err != nil {
		return err
	}
	admitter.SetSealedInputAuthority(authority)
	verifier, err := inputPublicationVerifier(in.Start.Daemon)
	if err != nil {
		return err
	}
	admitter.SetInputUploadConfig(runs.InputUploadConfig{Source: func(ctx context.Context, epoch int64) (runs.InputUploadNode, error) {
		publisher, uid, err := source.ForInputUpload(ctx, executioncontrol.ActivationEpoch(epoch))
		return runs.InputUploadNode{UID: uid, Publisher: publisher, Verifier: verifier}, err
	}})
	admitter.SetCredentialHandoffConfig(runs.CredentialHandoffConfig{Source: source, Helper: "/usr/local/bin/jb-review-worker", Socket: "/dev/shm/jb-review/auth.sock", Lifetime: 3 * time.Minute, WorkerImages: []string{image}})
	oldEnabled := atc.EnablePipelineRunCreation
	atc.EnablePipelineRunCreation = true
	TrackDisposer(rec, "the pipeline-run creation setting", func() error { atc.EnablePipelineRunCreation = oldEnabled; return nil })
	auth, err := authServer(res)
	if err != nil {
		return err
	}
	auth.mu.Lock()
	auth.RunServices = pipelinerunserver.Services{Admitter: admitter, Epoch: int64(hangarEpoch)}
	auth.ResultReader = &runs.ResultReader{Conn: jdb.Conn, Minter: signer, Source: func(ctx context.Context, epoch executioncontrol.ActivationEpoch) (runs.ResultSource, error) {
		return source.ForResultRead(ctx, epoch)
	}}
	auth.API, err = auth.apiHandler(auth.Verifier)
	auth.mu.Unlock()
	if err != nil {
		return err
	}
	if _, err = auth.fly("login", "-c", auth.URL, "-n", "auth-team", "-u", "owner", "-p", authPassword); err != nil {
		return err
	}
	if _, err = auth.fly("set-pipeline", "--non-interactive", "--team", team.Name(), "-p", "installed-review", "-c", filepath.Join(repoRoot(), "deploy", "review-template.yml"), "-v", "review_worker_image="+image, "-v", "review_model=handoff-finding"); err != nil {
		return err
	}
	if _, err = auth.fly("unpause-pipeline", "--team", team.Name(), "-p", "installed-review"); err != nil {
		return err
	}
	change, err := newReviewChange(res)
	if err != nil {
		return err
	}
	if _, stderr, err := reviewCommand(change.Binaries.CLI, change.captureArgs(change.Input), ""); err != nil {
		return fmt.Errorf("capture live review: %v: %s", err, stderr)
	}
	options := reviewclient.SubmitOptions{Team: team.Name(), Template: "installed-review", Input: change.Input, Receipt: filepath.Join(change.Workspace.Root, "request.json"), AuthFile: filepath.Join(change.Workspace.Root, "auth.json")}
	if err = os.WriteFile(options.AuthFile, []byte(reviewSyntheticAuth), 0600); err != nil {
		return err
	}
	type submission struct {
		result reviewclient.Submission
		err    error
	}
	submitCtx, stopSubmit := context.WithCancel(ctx)
	submitted := make(chan submission, 1)
	go func() {
		result, err := submitReviewFromProcess(submitCtx, auth, change, options, surface)
		submitted <- submission{result, err}
	}()
	joined := false
	defer func() {
		stopSubmit()
		if !joined {
			select {
			case <-submitted:
			case <-time.After(5 * time.Second):
			}
		}
	}()
	var receipt struct {
		RunID int `json:"run_id"`
	}
	for receipt.RunID == 0 {
		if data, err := os.ReadFile(options.Receipt); err == nil {
			_ = json.Unmarshal(data, &receipt)
		}
		if receipt.RunID != 0 {
			break
		}
		select {
		case result := <-submitted:
			joined = true
			return fmt.Errorf("client exited before live task admission: %v", result.err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	run, found, err := factory.GetRunByID(receipt.RunID)
	if err != nil || !found {
		return fmt.Errorf("load live Run: %v", err)
	}
	definition, found, err := factory.Definition(run.ID())
	if err != nil || !found || len(definition.Materialized.Jobs) != 1 {
		return fmt.Errorf("load installed single-job definition: %v", err)
	}
	task, ok := definition.Materialized.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	if !ok || task.Config == nil {
		return fmt.Errorf("installed template lost its inline task")
	}
	var buildID int
	if err = jdb.Conn.QueryRow(`SELECT id FROM builds WHERE pipeline_run_id=$1`, run.ID()).Scan(&buildID); err != nil {
		return err
	}
	build, found, err := jdb.BuildFactory.Build(buildID)
	if err != nil || !found {
		return fmt.Errorf("load live build: %v", err)
	}
	in.Start.Creation = db.RunCreation{Run: run, Config: definition.Materialized, EntryBuilds: []db.Build{build}}
	in.Start.Plan = atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: task.RunInputs, RunResult: task.RunResult, Config: task.Config}
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}}
	starter := &runs.ExecutionStarter{Conn: jdb.Conn, Factory: factory, Source: source, Epoch: executioncontrol.ActivationEpoch(hangarEpoch), Verifier: keys, Output: runs.NewOutputStarter(jdb.Conn, factory, source, int64(hangarEpoch), time.Hour)}
	starter.SetInputReadMinter(signer)
	spec := runtime.ContainerSpec{TeamID: team.ID(), Type: db.ContainerTypeTask, Dir: "/workspace", ImageSpec: runtime.ImageSpec{ImageURL: strings.TrimPrefix(task.Config.RootfsURI, "docker:///")}, Inputs: []runtime.Input{{RunInput: "change", DestinationPath: "/workspace/source"}}, Outputs: map[string]string{"result": "/workspace/result"}}
	spec, err = starter.PrepareTask(ctx, buildID, in.Start.Plan, spec)
	if err != nil {
		return err
	}
	row, err := jdb.PersistNamedWorker("review-installed")
	if err != nil {
		return err
	}
	controls := jetbridge.NewOutputControls(config, jetbridge.NewNodeIPResolver(in.Client), in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	worker := jetbridge.NewWorker(row, in.Client, config, jetbridge.WorkerDeps{
		Executor:          executor,
		OutputControls:    controls,
		ExecutionPreparer: starter,
	})
	metadata := db.ContainerMetadata{BuildID: buildID, PipelineID: build.PipelineID(), Type: db.ContainerTypeTask}
	container, _, err := worker.FindOrCreateContainer(ctx, db.NewBuildStepContainerOwner(buildID, "installed-review", team.ID()), metadata, spec, nil)
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{ID: "installed-review", Path: task.Config.Run.Path, Args: task.Config.Run.Args}, runtime.ProcessIO{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return err
	}
	finished := make(chan error, 1)
	go func() {
		result, err := process.Wait(ctx)
		if err == nil && result.ExitStatus != 0 {
			err = fmt.Errorf("installed worker exited %d: %s%s", result.ExitStatus, stdout.String(), stderr.String())
		}
		finished <- err
	}()
	var ready submission
	select {
	case ready = <-submitted:
		joined = true
	case err := <-finished:
		return fmt.Errorf("worker stopped before submission completed: %v", err)
	case <-ctx.Done():
		return ctx.Err()
	}
	if ready.err != nil || !ready.result.Ready || ready.result.RunID != run.ID() {
		return fmt.Errorf("live submission did not return its ready Run: %v", ready.err)
	}
	// The client process is gone. The model fixture still waits in the real Pod.
	name := jetbridge.GeneratePodName(metadata, container.DBContainer().Handle())
	if err = reviewProbe(ctx, executor, config.Namespace, name, `test ! -e /workspace/result/review.json; test "$(find /dev/shm/jb-review -name auth.json | wc -l)" -eq 1; find /dev/shm/jb-review -name auth.json -exec sh -c 'touch "${1%/*}/continue-review"' sh {} \;`); err != nil {
		return fmt.Errorf("disconnected worker was not waiting for the model: %w", err)
	}
	select {
	case err = <-finished:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if err = reviewProbe(ctx, executor, config.Namespace, name, `test -s /workspace/result/review.json; test -s /workspace/result/review.md; test -z "$(find /dev/shm/jb-review -name 'auth.json*' -print)"`); err != nil {
		return fmt.Errorf("worker failed to publish or clean credentials: %w", err)
	}
	if err = settleLiveReview(ctx, in, controls, keys); err != nil {
		return err
	}
	if err = readCompletedSubmission(ctx, auth, change, options, ready.result, surface); err != nil {
		return err
	}
	var claims, invocations int
	if err = jdb.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_credential_handoffs WHERE run_id=$1 AND ready_at IS NOT NULL`, run.ID()).Scan(&claims); err != nil {
		return err
	}
	if err = jdb.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_invocations WHERE run_id=$1`, run.ID()).Scan(&invocations); err != nil {
		return err
	}
	if claims != 1 || invocations != 1 {
		return fmt.Errorf("live submission duplicated admission or credentials: %d, %d", invocations, claims)
	}
	return nil
}

func settleLiveReview(ctx context.Context, in RunOutputRuntime, controls jetbridge.OutputControlResolver, keys hangaroutput.ControlKeyRing) error {
	r, err := in.readSource()
	if err != nil {
		return err
	}
	finish := RunOutputFinish{Start: in.Start}
	finish.Start.Record = r
	repository := finish.repository()
	receipts := hangaroutput.ReceiptKeyRing{ActiveKeyID: hangarReceiptKeyID, ActivationEpoch: r.ActivationEpoch, Keys: []hangaroutput.ReceiptKeyEntry{{ID: hangarReceiptKeyID, Epoch: r.ActivationEpoch, PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ReceiptPublic)}}}
	verifier, err := receipts.SignatureVerifier(output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		return err
	}
	drain := &jetbridge.OutputDrain{Client: in.Client, Controls: controls, Namespace: in.Config.Namespace}
	coordinator := &hangaroutput.Coordinator{Transactor: brineTransactor{conn: in.Start.DB.Conn}, Repository: repository, Dialer: hangaroutput.SourceDialerFunc(func(node string) (hangaroutput.SourceControl, error) {
		return controls.ForNode(ctx, node)
	}), Drain: drain, Verifier: verifier, HoldVerifier: keys, Announcer: hangaroutput.AnnouncerFunc(repository.RecordAnnouncement), OwnerID: freshUUID(), ReceiptKeyID: hangarReceiptKeyID}
	for {
		// The production recoverer asks again while kubelet closes the Pod.
		// A single advance must not fabricate synchronous termination evidence.
		if _, err = coordinator.Advance(ctx, r.HandoffID); err != nil && !errors.Is(err, output.ErrSealUnconfirmed) {
			return err
		}
		r, err = in.readSource()
		if err != nil {
			return err
		}
		if r.State == output.CaptureStateRegistered && r.Receipt != nil {
			break
		}
		if r.State == output.CaptureStateFailed {
			return fmt.Errorf("live result capture failed: %s", r.TerminalFailure)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("live result capture remained %s: %w", r.State, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	finish.Release, err = finish.daemonRelease()
	if err != nil {
		return err
	}
	if err = drain.ReleaseDrain(ctx, in.Node.Name, r.Execution, r.HandoffID); err != nil {
		return err
	}
	if err = finish.recordRelease(finish.Release, false); err != nil {
		return err
	}
	if err = in.Start.Creation.EntryBuilds[0].Finish(db.BuildStatusSucceeded); err != nil {
		return err
	}
	if err = consumeRunScheduling(in.Start); err != nil {
		return err
	}
	result := finalizeRunResult(RunResultPublication{Start: in.Start}, false)
	if result.Err != nil || !result.Completed {
		return fmt.Errorf("live result did not finalize: %v", result.Err)
	}
	return nil
}
