package steps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func RunInputDeliveryDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[RunInputAdmission, RunInputAdmission]("its real worker delivers inputs with {string}", []string{"real-cluster"}, func(in RunInputAdmission, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunInputAdmission, error) {
		mode, _ := p.GetString(0)
		return in, exerciseRunInputDelivery(in, mode, rec, res)
	})}
}

func exerciseRunInputDelivery(in RunInputAdmission, mode string, rec *brine.Recorder, res brine.Resources) error {
	if in.Err != nil || in.Source.Candidate == nil {
		return fmt.Errorf("input admission failed: %v", in.Err)
	}
	conn := in.Source.Start.DB.Conn
	factory := db.NewPipelineRunFactory(conn, in.Source.Start.DB.LockFactory)
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Source.Start.Daemon.ControlPublic)}}}
	starter := &runs.ExecutionStarter{Conn: conn, Factory: factory, Epoch: executioncontrol.ActivationEpoch(hangarEpoch), Verifier: keys}
	configured, ok := any(starter).(interface {
		SetInputReadMinter(hangaroutput.WarrantMinter)
	})
	if !ok {
		return fmt.Errorf("the Run starter has no per-container input read admission")
	}
	_, signer, config, err := configureRunReadPlane(in.Source, rec, res)
	if err != nil {
		return err
	}
	configured.SetInputReadMinter(signer)
	config.OutputPlaneEnabled = true
	config.HangarEnabled = true
	config.OutputActivationEpoch = int64(hangarEpoch)
	config.ArtifactDaemonHostPath = in.Source.Start.Daemon.Output.Root
	client := in.Source.Candidate.Runtime.Client
	source := jetbridge.NewOutputSource(client, config, in.Source.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	starter.Source = source
	definition, found, err := factory.Definition(in.Run.ID)
	if err != nil || !found {
		return fmt.Errorf("missing consuming definition: %v", err)
	}
	task := definition.Materialized.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	plan := atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: task.RunInputs, RunResult: task.RunResult, Config: task.Config}
	var buildID, pipelineID int
	if err := conn.QueryRow(`SELECT id,pipeline_id FROM builds WHERE pipeline_run_id=$1 AND run_job_name=$2`, in.Run.ID, definition.Materialized.Jobs[0].Name).Scan(&buildID, &pipelineID); err != nil {
		return err
	}
	spec := runtime.ContainerSpec{TeamID: in.Template.TeamID(), Dir: "/workspace", Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}}
	for _, route := range task.RunInputs {
		spec.Inputs = append(spec.Inputs, runtime.Input{RunInput: route.Name, DestinationPath: filepath.Join(spec.Dir, route.Input)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	spec, err = starter.PrepareTask(ctx, buildID, plan, spec)
	if err != nil {
		return err
	}
	switch mode {
	case "changed ref":
		spec.Inputs[0].HangarTree.Generation++
	case "changed name":
		spec.Inputs[0].RunInput = "other"
	case "missing slot":
		spec.Inputs = nil
	case "missing task identity":
		spec.RunTaskID = ""
	case "aborted build":
		_, err = conn.Exec(`UPDATE builds SET aborted=true WHERE id=$1`, buildID)
	}
	if err != nil {
		return err
	}
	row, err := in.Source.Start.DB.PersistNamedWorker("run-input-delivery")
	if err != nil {
		return err
	}
	worker := jetbridge.NewWorker(row, client, config)
	cluster, err := getRealCluster(res)
	if err != nil {
		return err
	}
	worker.SetExecutor(jetbridge.NewSPDYExecutor(client, cluster.env.Config))
	worker.SetOutputControls(jetbridge.NewOutputControls(config, jetbridge.NewNodeIPResolver(client), in.Source.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch)))
	worker.SetExecutionPreparer(starter)
	owner := db.NewBuildStepContainerOwner(buildID, "consume-input", spec.TeamID)
	metadata := db.ContainerMetadata{BuildID: buildID, PipelineID: pipelineID, Type: db.ContainerTypeTask}
	container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, nil)
	if mode != "live" {
		if err == nil {
			return fmt.Errorf("the worker admitted %s", mode)
		}
		var leases int
		if err := conn.QueryRow(`SELECT count(*) FROM hangar_read_leases`).Scan(&leases); err != nil {
			return err
		}
		if leases != 0 {
			return fmt.Errorf("refused input left %d read leases", leases)
		}
		return nil
	}
	if err != nil {
		return err
	}
	name := jetbridge.GeneratePodName(metadata, container.DBContainer().Handle())
	TrackDisposer(rec, "the run input pod "+name, func() error {
		return releasedIfGone(client.CoreV1().Pods(config.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{}))
	})
	// Envtest accepts the real generated Pod. No fake executor is installed;
	// process execution/read-only mount enforcement remain the live tier's job.
	if _, err = container.Run(ctx, runtime.ProcessSpec{Path: "true"}, runtime.ProcessIO{}); err != nil {
		return err
	}
	pod, err := client.CoreV1().Pods(config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return verifyRunInputPod(ctx, in, container.DBContainer().Handle(), pod, task)
}

func verifyRunInputPod(ctx context.Context, in RunInputAdmission, handle string, pod *corev1.Pod, task *atc.TaskStep) error {
	volumes := map[string]corev1.Volume{}
	for _, volume := range pod.Spec.Volumes {
		volumes[volume.Name] = volume
	}
	for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		for _, mount := range c.VolumeMounts {
			if _, found := volumes[mount.Name]; !found {
				return fmt.Errorf("Pod mount %s has no volume", mount.Name)
			}
		}
	}
	verifier, err := output.NewReadWarrantVerifier(brineReadWarrantKey, output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		return err
	}
	count := 0
	for _, init := range pod.Spec.InitContainers {
		if !strings.HasPrefix(init.Name, "materialize-run-input-") {
			continue
		}
		count++
		if len(init.Command) != 3 || len(init.VolumeMounts) != 1 || !init.VolumeMounts[0].ReadOnly {
			return fmt.Errorf("input initializer has no exact read-only verification mount")
		}
		mount := init.VolumeMounts[0]
		volume := volumes[mount.Name]
		if volume.HostPath == nil {
			return fmt.Errorf("input volume is not node-local")
		}
		mainMounts := 0
		for _, main := range pod.Spec.Containers {
			if main.Name != "main" {
				continue
			}
			for _, m := range main.VolumeMounts {
				if m.Name == mount.Name {
					mainMounts++
					if !m.ReadOnly {
						return fmt.Errorf("task input is writable")
					}
				}
			}
		}
		if mainMounts != 1 {
			return fmt.Errorf("input is not mounted exactly once in the task")
		}
		encoded, found := scriptAssignment(init.Command[2], "REQUEST_B64")
		if !found {
			return fmt.Errorf("initializer has no read request")
		}
		body, err := base64.StdEncoding.DecodeString(strings.Trim(encoded, "'"))
		if err != nil {
			return err
		}
		var request output.ManagedReadRequest
		if err = json.Unmarshal(body, &request); err != nil {
			return err
		}
		if request.Ref != in.Source.Candidate.Record.Ref || request.Destination.Handle != handle || request.Destination.Volume != mount.Name {
			return fmt.Errorf("read warrant is not bound to the actual consumer volume")
		}
		claims, err := verifier.Verify(request.Warrant, request.Ref, request.Destination)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(volume.HostPath.Path, 0755); err != nil {
			return err
		}
		assignment := "\nROOT=" + mount.MountPath + "\n"
		if strings.Count(init.Command[2], assignment) != 1 {
			return fmt.Errorf("initializer does not identify its verification mount")
		}
		command := strings.Replace(init.Command[2], assignment, "\nROOT='"+strings.ReplaceAll(volume.HostPath.Path, "'", "'\\''")+"'\n", 1)
		_, host, _, _ := splitDaemonAddress(in.Source.Start.Daemon.Output.URL)
		cmd := exec.CommandContext(ctx, init.Command[0], "-c", command)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOST_IP=" + host}
		out, err := cmd.CombinedOutput()
		if strings.Contains(string(out), request.Warrant) {
			return fmt.Errorf("initializer disclosed its read warrant")
		}
		if err != nil {
			return fmt.Errorf("initializing actual input: %w (%s)", err, abbrev(string(out)))
		}
		data, err := os.ReadFile(filepath.Join(volume.HostPath.Path, ".hangar-materialized"))
		var ref hangar.TreeRef
		if err != nil || json.Unmarshal(data, &ref) != nil || ref != request.Ref {
			return fmt.Errorf("actual task input has no exact sealed receipt: %v", err)
		}
		tx, err := in.Source.Start.DB.Conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		_, loadErr := db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()).LoadReadLease(ctx, tx, claims.ReadLeaseID)
		db.Rollback(tx)
		if !errors.Is(loadErr, output.ErrConflict) {
			return fmt.Errorf("materialized input retained a live read lease: %v", loadErr)
		}
	}
	if count == 0 || count != len(task.RunInputs) {
		return fmt.Errorf("not every task input was initialized")
	}
	return nil
}
