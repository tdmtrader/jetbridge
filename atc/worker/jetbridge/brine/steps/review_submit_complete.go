package steps

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/agent/detached"
	"github.com/concourse/concourse/agent/review"
	reviewclient "github.com/concourse/concourse/agent/review/client"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// submittedWorkload is what differs between detached workloads when a
// submitted Run is driven through its real worker to a published result.
type submittedWorkload struct {
	// Input is the run_inputs name the submission uploaded.
	Input string
	// Receive verifies the input the Run materialized against the local
	// original, and returns the directory the worker reads and its digest.
	Receive func(materialized string) (string, string, error)
	// Mode is the worker's mode argument, if any, and Model the provider
	// fixture's mode. The model must wait for release after the handoff.
	Mode  []string
	Model string
	// Results are the files the worker publishes; the template moves them
	// from its report directory into the result root.
	Results []string
	// Submit resumes the saved submission from a fresh local process.
	Submit func(context.Context) (detached.Submission, error)
	// Read retrieves and checks the completed result from a fresh local process.
	Read func(context.Context) error
	// Published, when set, sees the result directory once the worker's files
	// are in it, before the capture seals it.
	Published func(directory string) error
	// Then, when set, drives the template's later result producers of the
	// same build after the credential-receiving producer is released.
	Then func(context.Context, submittedRun) error
}

// Join the actual upload, admitted Run, local client process, private worker
// session and capture/read plane. Envtest supplies Pod identity; real kubelet
// transport and mount enforcement are independently required by the live tier.
func finishSubmittedReview(in RunInputAdmission, auth *AuthFixture, change ReviewChange, options reviewclient.SubmitOptions, pending reviewclient.Submission, surface string, rec *brine.Recorder, res brine.Resources) error {
	return finishSubmittedRun(in, auth, change, pending, submittedWorkload{
		Input: "change",
		Receive: func(materialized string) (string, string, error) {
			original, err := review.LoadBundle(change.Input)
			if err != nil {
				return "", "", err
			}
			path := filepath.Join(materialized, review.RunInputBundleDir)
			received, err := review.LoadBundle(path)
			if err != nil || received.Digest != original.Digest {
				return "", "", fmt.Errorf("uploaded review changed in transit: %v", err)
			}
			return path, received.Digest, nil
		},
		Model:   "handoff-finding",
		Results: []string{"review.json", "review.md"},
		Submit: func(ctx context.Context) (detached.Submission, error) {
			return submitReviewFromProcess(ctx, auth, change, options, surface)
		},
		Read: func(ctx context.Context) error {
			return readCompletedSubmission(ctx, auth, change, options, pending, surface)
		},
	}, rec, res)
}

func finishSubmittedRun(in RunInputAdmission, auth *AuthFixture, change ReviewChange, pending detached.Submission, workload submittedWorkload, rec *brine.Recorder, res brine.Resources) error {
	// A template may have later result producers to drive and read back.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runtime := in.Source.Candidate.Runtime
	jdb := runtime.Start.DB
	var initialInvocations int
	if err := jdb.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_invocations WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&initialInvocations); err != nil {
		return err
	}
	factory := db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory)
	run, found, err := factory.GetRunByID(pending.RunID)
	if err != nil || !found {
		return fmt.Errorf("load submitted Run: %v", err)
	}
	definition, found, err := factory.Definition(run.ID())
	if err != nil || !found {
		return fmt.Errorf("load submitted definition: %v", err)
	}
	task := definition.Materialized.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	var buildID int
	if err = jdb.Conn.QueryRow(`SELECT id FROM builds WHERE pipeline_run_id=$1 AND run_job_name=$2`, run.ID(), definition.Materialized.Jobs[0].Name).Scan(&buildID); err != nil {
		return err
	}
	build, found, err := jdb.BuildFactory.Build(buildID)
	if err != nil || !found {
		return fmt.Errorf("load submitted build: %v", err)
	}
	runtime.Start.Creation = db.RunCreation{Run: run, Config: definition.Materialized, EntryBuilds: []db.Build{build}}
	runtime.Start.Plan = atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: task.RunInputs, RunResult: task.RunResult, Config: task.Config}
	// The producer's container declares the output its template selects.
	runtime.Spec.Outputs = map[string]string{task.RunResult.Output: "/workspace/" + task.RunResult.Output}
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(runtime.Start.Daemon.ControlPublic)}}}
	source, signer, _, err := configureRunReadPlane(in.Source, rec, res)
	if err != nil {
		return err
	}
	input, err := submittedRunInput(ctx, runtime.Start, source, signer, workload.Input)
	if err != nil {
		return err
	}
	change.Input, change.Digest, err = workload.Receive(input)
	if err != nil {
		return err
	}
	change.RunID = run.ID()
	if err = change.memoryRuntime(); err != nil {
		return err
	}
	socket := filepath.Join(change.Workspace.Runtime, "auth.sock")
	source.SetExecutor(localExecutor{client: runtime.Client})
	in.Port.SetCredentialHandoffConfig(runs.CredentialHandoffConfig{Source: source, Helper: change.Binaries.Worker, Socket: socket, Lifetime: time.Minute, WorkerImages: []string{brineCredentialWorkerImage}})

	candidate, err := driveSubmittedProducer(ctx, runtime, factory, buildID, "submitted-review", keys, rec, func(directory string) error {
		change.Output = filepath.Join(directory, "report")
		args := append(append([]string{}, workload.Mode...), "--input", change.Input, "--output", change.Output, "--runtime-dir", change.Workspace.Runtime, "--codex", change.Binaries.Provider, "--model", workload.Model, "--timeout", "45s", "--auth-socket", socket, "--handoff-timeout", "30s", "--run-id", strconv.Itoa(run.ID()))
		worker := exec.CommandContext(ctx, change.Binaries.Worker, args...)
		var stderr bytes.Buffer
		worker.Stderr = &stderr
		if err := worker.Start(); err != nil {
			return err
		}
		joined := false
		defer func() {
			if !joined {
				_ = worker.Process.Signal(os.Interrupt)
				_ = worker.Wait()
			}
		}()
		// The helper also waits for the actual socket, covering startup ordering.
		ready, err := workload.Submit(ctx)
		if err != nil {
			return err
		}
		if !ready.Ready || ready.RunID != pending.RunID || ready.Handle != pending.Handle {
			return fmt.Errorf("resumed client lost its original ready Run")
		}
		if err = reviewAbsent(change.Output); err != nil {
			return fmt.Errorf("model completed before the submission client disconnected")
		}
		files, err := filepath.Glob(filepath.Join(change.Workspace.Runtime, "*", "codex", "auth.json"))
		if err != nil || len(files) != 1 {
			return fmt.Errorf("ready Run has no single private credential session: %v", err)
		}
		// Only the deterministic model is released. Both submission transports
		// have already exited/closed; no client connection keeps the worker alive.
		if err = os.WriteFile(filepath.Join(filepath.Dir(files[0]), "continue-review"), nil, 0600); err != nil {
			return err
		}
		err = worker.Wait()
		joined = true
		if err != nil {
			return fmt.Errorf("detached worker: %w: %s", err, stderr.Bytes())
		}
		change.Stderr = stderr.Bytes()
		if err = reviewNoCredentials(change); err != nil {
			return err
		}
		for _, name := range workload.Results {
			if err = os.Rename(filepath.Join(change.Output, name), filepath.Join(directory, name)); err != nil {
				return err
			}
		}
		if err = os.Remove(change.Output); err != nil {
			return err
		}
		if workload.Published != nil {
			return workload.Published(directory)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if workload.Then != nil {
		if err = workload.Then(ctx, submittedRun{Runtime: runtime, Factory: factory, BuildID: buildID, RunID: run.ID(), Definition: definition.Materialized, Keys: keys, Input: input}); err != nil {
			return err
		}
	}
	if err = build.Finish(db.BuildStatusSucceeded); err != nil {
		return err
	}
	if err = consumeRunScheduling(runtime.Start); err != nil {
		return err
	}
	result := finalizeRunResult(RunResultPublication{Start: runtime.Start, Candidate: &candidate}, false)
	if result.Err != nil || !result.Completed {
		return fmt.Errorf("submitted Run did not complete: %v", result.Err)
	}
	if err = configureRunDownload(result, auth, rec, res); err != nil {
		return err
	}
	if err = workload.Read(ctx); err != nil {
		return err
	}
	var claims, invocations int
	if err = jdb.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_credential_handoffs WHERE run_id=$1 AND ready_at IS NOT NULL`, run.ID()).Scan(&claims); err != nil {
		return err
	}
	if err = jdb.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_invocations WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&invocations); err != nil {
		return err
	}
	if claims != 1 || invocations != initialInvocations {
		return fmt.Errorf("resumption duplicated delivery or admission: %d, %d", claims, invocations)
	}
	return nil
}

// submittedRun is what a later result producer of a submitted Run's build
// needs to be driven the way the first one was.
type submittedRun struct {
	// Runtime is the first producer's runtime; a later producer replaces its
	// plan and container outputs.
	Runtime    RunOutputRuntime
	Factory    db.PipelineRunFactory
	BuildID    int
	RunID      int
	Definition atc.Config
	Keys       hangaroutput.ControlKeyRing
	// Input is the Run input as the node materialized it.
	Input string
}

// driveSubmittedProducer takes one result producer of a submitted Run's build
// through the real capture plane: its exact execution admitted and witnessed
// at start, its original Pod running, work filling the reserved output, then
// the actual finish witnessed and the capture's release recorded. Envtest
// supplies Pod identity; the live tier supplies kubelet enforcement.
func driveSubmittedProducer(ctx context.Context, runtime RunOutputRuntime, factory db.PipelineRunFactory, buildID int, planID atc.PlanID, keys hangaroutput.ControlKeyRing, rec *brine.Recorder, work func(directory string) error) (RunOutputCandidate, error) {
	jdb := runtime.Start.DB
	candidate, err := publishRunCandidateStarted(runtime, rec, func(directory string, start executioncontrol.Acknowledgement) error {
		record, err := runtime.readSource()
		if err != nil {
			return err
		}
		tx, err := jdb.Conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer db.Rollback(tx)
		a, _, err := factory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{BuildID: buildID, PlanID: planID, Kind: db.ContainerTypeTask, Epoch: int64(hangarEpoch), NodeName: runtime.Node.Name, NodeUID: string(runtime.Node.UID), HandoffID: record.HandoffID})
		if err != nil {
			return err
		}
		if a.Identity != start.Identity {
			return fmt.Errorf("submission started a different execution")
		}
		if err = factory.RecordRunExecutionWitness(ctx, tx, buildID, a.PlanID, start, keys); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		pods, err := runtime.Client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		matched := false
		for _, pod := range pods.Items {
			if string(pod.UID) != string(start.PodUID) {
				continue
			}
			matched = true
			now := metav1.Now()
			pod.Status.Phase, pod.Status.StartTime = corev1.PodRunning, &now
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", ContainerID: "brine://" + freshUUID(), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}}}
			if _, err = runtime.Client.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, &pod, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
		if !matched {
			return fmt.Errorf("submitted worker has no original Pod")
		}
		return work(directory)
	})
	if err != nil {
		return candidate, err
	}
	control := jetbridge.NewOutputControlClient(runtime.Start.Daemon.Output.URL, runtime.Start.Daemon.HTTP, runtime.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	finished, err := control.Classify(ctx, candidate.Record.Execution)
	if err != nil || finished.Acknowledgement == nil {
		return candidate, fmt.Errorf("worker left no actual finish: %v", err)
	}
	tx, err := jdb.Conn.BeginTx(ctx, nil)
	if err != nil {
		return candidate, err
	}
	defer db.Rollback(tx)
	if err = factory.RecordRunExecutionWitness(ctx, tx, buildID, planID, *finished.Acknowledgement, keys); err != nil {
		return candidate, err
	}
	if err = tx.Commit(); err != nil {
		return candidate, err
	}
	candidate.Finish.Release, err = candidate.Finish.daemonRelease()
	if err != nil {
		return candidate, err
	}
	return candidate, candidate.Finish.recordRelease(candidate.Finish.Release, false)
}

func submittedRunInput(ctx context.Context, start RunOutputStart, source *jetbridge.OutputSource, signer *output.ReadWarrantSigner, name string) (string, error) {
	tx, err := start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	selected, err := db.LoadRunTask(ctx, tx, start.Creation.EntryBuilds[0].ID(), start.Plan.TaskID, int64(hangarEpoch))
	db.Rollback(tx)
	if err != nil {
		return "", err
	}
	binding, ok := selected.Inputs[name]
	if !ok {
		return "", fmt.Errorf("submitted Run has no %s input", name)
	}
	node, err := source.ForResultRead(ctx, executioncontrol.ActivationEpoch(hangarEpoch))
	if err != nil {
		return "", err
	}
	nonce, err := output.NewReadWarrantNonce(rand.Reader)
	if err != nil {
		return "", err
	}
	prefix, err := db.HangarConsumerPrefixHeld("brine-submitted-review")
	if err != nil {
		return "", err
	}
	destination := output.ReadDestination{Handle: freshUUID(), Volume: "source"}
	admission := hangaroutput.ReadAdmission{Transactor: brineTransactor{conn: start.DB.Conn}, Leases: db.NewHangarOutputRepository(prefix), Stat: node, Minter: signer, Clock: output.ClockFunc(func() time.Time { return time.Now().UTC() })}
	warrant, err := admission.Admit(ctx, hangaroutput.ReadRequest{ReadLeaseID: output.ReadLeaseID(freshUUID()), WarrantNonce: nonce, ClaimID: binding.ClaimID, Ref: binding.Ref, Destination: destination, ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), MaterializationTimeout: time.Minute})
	if err != nil {
		return "", err
	}
	if err := node.MaterializeManagedOutput(ctx, output.ManagedReadRequest{Ref: binding.Ref, Destination: destination, Warrant: warrant.Token}); err != nil {
		return "", err
	}
	return filepath.Join(start.Daemon.Output.Root, "steps", destination.Handle, destination.Volume), nil
}

func submitReviewFromProcess(ctx context.Context, auth *AuthFixture, change ReviewChange, options reviewclient.SubmitOptions, surface string) (reviewclient.Submission, error) {
	var result reviewclient.Submission
	if surface == "CLI" {
		cmd := exec.CommandContext(ctx, change.Binaries.CLI, "review", "submit", "--target", "auth", "--team", options.Team, "--template", options.Template, "--input", options.Input, "--receipt", options.Receipt, "--auth-file", options.AuthFile)
		cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return result, fmt.Errorf("submit CLI: %w: %s", err, stderr.Bytes())
		}
		err := json.Unmarshal(stdout.Bytes(), &result)
		return result, err
	}
	cmd := exec.CommandContext(ctx, change.Binaries.CLI, "review", "mcp", "--target", "auth", "--team", options.Team, "--template", options.Template, "--auth-file", options.AuthFile)
	cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
	session, err := sdk.NewClient(&sdk.Implementation{Name: "brine-detached", Version: "1"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return result, err
	}
	defer session.Close()
	response, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "review_submit", Arguments: map[string]any{"input": options.Input, "receipt": options.Receipt}})
	if err != nil {
		return result, err
	}
	if response.IsError || response.StructuredContent == nil {
		return result, fmt.Errorf("MCP submission did not return a typed handle: %+v", response)
	}
	data, err := json.Marshal(response.StructuredContent)
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, session.Close()
}

func readCompletedSubmission(ctx context.Context, auth *AuthFixture, change ReviewChange, options reviewclient.SubmitOptions, pending reviewclient.Submission, surface string) error {
	var data []byte
	if surface == "CLI" {
		cmd := exec.CommandContext(ctx, change.Binaries.CLI, "review", "result", "--target", "auth", "--team", options.Team, "--template", options.Template, "--run", strconv.Itoa(pending.Handle.Number))
		cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("fresh result CLI: %w: %s", err, stderr.Bytes())
		}
		data = stdout.Bytes()
	} else {
		cmd := exec.CommandContext(ctx, change.Binaries.CLI, "review", "mcp", "--target", "auth", "--team", options.Team, "--template", options.Template)
		cmd.Env = append(os.Environ(), "FLY_HOME="+auth.Home)
		session, err := sdk.NewClient(&sdk.Implementation{Name: "brine-fresh", Version: "1"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
		if err != nil {
			return err
		}
		defer session.Close()
		response, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "review_result", Arguments: map[string]any{"run": pending.Handle.Number}})
		if err != nil {
			return err
		}
		if response.IsError || response.StructuredContent == nil {
			return fmt.Errorf("fresh MCP has no typed report: %+v", response)
		}
		data, err = json.Marshal(response.StructuredContent)
		if err != nil {
			return err
		}
	}
	var report review.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return err
	}
	if report.RunID == nil || *report.RunID != pending.RunID || report.Verdict != "findings" || len(report.Assessment.Findings) != 1 || report.Assessment.Findings[0].ID != "f-001" {
		return fmt.Errorf("fresh client lost the submitted Run's typed finding")
	}
	return nil
}
