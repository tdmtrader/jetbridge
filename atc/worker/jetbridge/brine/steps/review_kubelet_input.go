package steps

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

// Consume an actually uploaded, Run-owned input through the production worker
// and generated initializer. Root inside the task must still be unable to
// change the mounted tree; a Pod spec's readOnly bit alone is not this proof.
func liveReviewReadOnlyInput(ctx context.Context, in RunOutputRuntime, executor jetbridge.PodExecutor, rec *brine.Recorder) error {
	previousGate := atc.EnablePipelineRunCreation
	atc.EnablePipelineRunCreation = true
	defer func() { atc.EnablePipelineRunCreation = previousGate }()
	jdb := in.Start.DB
	factory := db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory)
	source, signer, config, err := configureRunReadPlaneForClient(RunResultPublication{Start: in.Start}, rec, in.Client, in.Config)
	if err != nil {
		return err
	}
	source.SetExecutor(executor)
	config.HangarEnabled = true
	config.ArtifactDaemonHostPath = in.Start.Daemon.Output.Root
	team, found, err := jdb.TeamFactory.FindTeam("output-start")
	if err != nil || !found {
		return fmt.Errorf("find input owner: %v", err)
	}
	if err = team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:owner"}}}); err != nil {
		return err
	}
	task := &atc.TaskStep{Name: "read", TaskID: freshUUID(), RunInputs: []atc.RunInput{{Name: "change", Input: "source"}}, Config: &atc.TaskConfig{Platform: "linux", Inputs: []atc.TaskInputConfig{{Name: "source"}}, Run: atc.TaskRunConfig{Path: "sh"}}}
	template, _, err := team.SavePipeline(atc.PipelineRef{Name: "live-input"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "read", PlanSequence: []atc.Step{{Config: task}}}}}, 0, false)
	if err != nil {
		return err
	}
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
	if err != nil {
		return err
	}
	admitter := runs.NewAdmitter(jdb.Conn, factory, jdb.TeamFactory, display, nil)
	authority, err := runinput.NewAuthority(bytes.Repeat([]byte{0x61}, 32), time.Now)
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
	archive, err := durableTarOfOneFile("manifest.json", "sealed review input")
	if err != nil {
		return err
	}
	ref := runs.TemplateRef{Team: team.Name(), Pipeline: template.PipelineRef()}
	principal := invocationPrincipal("owner")
	input, err := admitter.UploadInput(ctx, ref, principal, "change", int64(hangarEpoch), bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tx, err := admitter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, _, err := admitter.AdmitVersionedRun(ctx, tx, runs.Admission{Template: ref, Principal: principal, ContractKey: "live-read-only", Inputs: map[string]atc.RunInputSource{"change": input}}, int64(hangarEpoch))
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	var buildID, pipelineID int
	if err = jdb.Conn.QueryRow(`SELECT id,pipeline_id FROM builds WHERE pipeline_run_id=$1 AND run_job_name='read'`, run.ID).Scan(&buildID, &pipelineID); err != nil {
		return err
	}
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}}
	starter := &runs.ExecutionStarter{Conn: jdb.Conn, Factory: factory, Source: source, Epoch: executioncontrol.ActivationEpoch(hangarEpoch), Verifier: keys}
	starter.SetInputReadMinter(signer)
	plan := atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: task.RunInputs, Config: task.Config}
	spec := runtime.ContainerSpec{TeamID: team.ID(), Type: db.ContainerTypeTask, Dir: "/workspace", ImageSpec: runtime.ImageSpec{ImageURL: "busybox:1.37"}, Inputs: []runtime.Input{{RunInput: "change", DestinationPath: "/workspace/source"}}}
	spec, err = starter.PrepareTask(ctx, buildID, plan, spec)
	if err != nil {
		return err
	}
	row, err := jdb.PersistNamedWorker("review-live-input")
	if err != nil {
		return err
	}
	worker := jetbridge.NewWorker(row, in.Client, config, jetbridge.WorkerDeps{
		Executor:          executor,
		OutputControls:    jetbridge.NewOutputControls(config, jetbridge.NewNodeIPResolver(in.Client), in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch)),
		ExecutionPreparer: starter,
	})
	metadata := db.ContainerMetadata{BuildID: buildID, PipelineID: pipelineID, Type: db.ContainerTypeTask}
	container, _, err := worker.FindOrCreateContainer(ctx, db.NewBuildStepContainerOwner(buildID, "read-input", team.ID()), metadata, spec, nil)
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{ID: "read-sealed-input", Path: "sh", Args: []string{"-ec", `test "$(cat source/manifest.json)" = 'sealed review input'; if touch source/changed 2>/tmp/write-error; then exit 1; fi; grep -q 'Read-only file system' /tmp/write-error; printf verified`}}, runtime.ProcessIO{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return err
	}
	result, err := process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 || stdout.String() != "verified" {
		return fmt.Errorf("real task could not verify its immutable input (exit %d): %v: %s", result.ExitStatus, err, stderr.String())
	}
	return nil
}
