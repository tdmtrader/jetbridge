package steps

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// ---------------------------------------------------------------------------
// container_extra.go — the remainder of container_test.go.
//
// Everything here is named with a Run/RunExtra prefix because this package is
// worked on by several agents at once and a bare `DirectRun` would collide.
//
// Three of the migrated blocks were spy tests in the sense of
// coverage_matrix.md Addendum 2 — they read `execCalls[0].command`,
// `execCalls[0].podName`, `execCalls[0].tty`, or `len(execCalls)`. Those are
// converted by giving the double real work to do (localShellAdapter, which
// runs the command; localExecAdapter's sibling behavior for tar) and asserting
// the round trip, or dispositioned in container-run.feature where no
// seam-level equivalent exists.
// ---------------------------------------------------------------------------

// RunExtraDirectRun is a step whose pod IS the step: no exec transport is
// configured, so the kubelet runs the command itself. The recorded Command is
// carried so the check can compare the pod against what the scenario asked
// for rather than against a literal.
type RunExtraDirectRun struct {
	Namespace string
	Clientset *fake.Clientset
	Ctx       context.Context
	Handle    string
	Pod       *corev1.Pod
	Command   []string
	Process   runtime.Process
}

// RunExtraMounts is what FindOrCreateContainer handed back BEFORE anything was
// scheduled: the container and one volume mount per declared path. The pod
// does not exist yet, which is the whole point of the block it replaces.
type RunExtraMounts struct {
	Namespace string
	Clientset *fake.Clientset
	Ctx       context.Context
	Handle    string
	Container runtime.Container
	Mounts    []runtime.VolumeMount
	Ran       bool
}

// RunExtraOutput is a finished exec-mode step whose output directory is a real
// directory on this host. The volume the caller was handed is the only way
// back to those bytes once the pod is gone.
type RunExtraOutput struct {
	Ctx        context.Context
	Handle     string
	OutputPath string
	Volume     *jetbridge.Volume
	Log        string
	ExitStatus int
}

// RunExtraOutputRead is what came back out of that volume.
type RunExtraOutputRead struct {
	Files map[string]string
	Err   error
}

// RunExtraMetrics is the operator-visible counter movement across one Run.
// Both deltas are read inside the same step that performed the Run, so the
// pair is a snapshot of that Run and nothing else.
type RunExtraMetrics struct {
	Created float64
	Failed  float64
	RunErr  error
}

// RunExtraDBOutcome is what a caller of FindOrCreateContainer got when the
// database, rather than Kubernetes, was the thing that went wrong.
type RunExtraDBOutcome struct {
	DB      JetbridgeDB
	Handle  string
	Err     error
	Message string
}

// runExtraStaleCreatedFails is a real db.Worker with one transition broken:
// a container found in `creating` cannot be completed. It mirrors the ginkgo
// suite's failStaleCreatedTransition, which lives in a _test.go file and so
// cannot be imported. Everything before the fault is a real row, so what the
// worker leaves behind is asserted against the database.
type runExtraStaleCreatedFails struct{ db.Worker }

func (w runExtraStaleCreatedFails) FindContainer(owner db.ContainerOwner) (db.CreatingContainer, db.CreatedContainer, error) {
	creating, created, err := w.Worker.FindContainer(owner)
	if err != nil || creating == nil {
		return creating, created, err
	}
	return runExtraCreatedFails{creating}, created, nil
}

type runExtraCreatedFails struct{ db.CreatingContainer }

func (runExtraCreatedFails) Created() (db.CreatedContainer, error) {
	return nil, fmt.Errorf("db connection lost")
}

// ContainerExtraDefinitions is the single entry point for this file.
func ContainerExtraDefinitions() []brine.StepDefinition {
	defs := runExtraDirectDefinitions()
	defs = append(defs, runExtraPauseDefinitions()...)
	defs = append(defs, runExtraMountDefinitions()...)
	defs = append(defs, runExtraOutputDefinitions()...)
	defs = append(defs, runExtraMetricDefinitions()...)
	defs = append(defs, runExtraDBDefinitions()...)
	return defs
}

// ---------------------------------------------------------------------------
// Direct mode: the pod IS the step
// ---------------------------------------------------------------------------

func runExtraDirectDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[ContainerDraft, RunExtraDirectRun](
			"the step runs {string} with no exec transport configured",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (RunExtraDirectRun, error) {
				script, ok := p.GetString(0)
				if !ok {
					return RunExtraDirectRun{}, fmt.Errorf("expected a command parameter")
				}

				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runExtraSpecFromDraft(in),
					&noopDelegate{},
				)
				if err != nil {
					return RunExtraDirectRun{}, fmt.Errorf("find or create container %q: %w", in.Handle, err)
				}

				command := []string{"/bin/sh", "-c", script}
				process, err := container.Run(in.Ctx, runtime.ProcessSpec{
					Path: command[0],
					Args: command[1:],
					Dir:  in.Dir,
				}, runtime.ProcessIO{})
				if err != nil {
					return RunExtraDirectRun{}, fmt.Errorf("run container %q: %w", in.Handle, err)
				}

				pod, err := runExtraTheOnlyPod(in.Ctx, in.Clientset, in.Namespace)
				if err != nil {
					return RunExtraDirectRun{}, err
				}

				return RunExtraDirectRun{
					Namespace: in.Namespace, Clientset: in.Clientset, Ctx: in.Ctx,
					Handle: in.Handle, Pod: pod, Command: command, Process: process,
				}, nil
			},
		),

		// PE-02. With no exec transport there is nowhere to exec FROM, so the
		// pod has to carry the command. A pause pod here would leave the step
		// running `sleep` forever and nothing would ever execute the command.
		brine.DefineCheck[RunExtraDirectRun](
			"the pod itself carries the step's command",
			func(in RunExtraDirectRun, _ brine.Params, _ *brine.Recorder) error {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				got := append(append([]string{}, main.Command...), main.Args...)
				if strings.Join(got, "\x00") != strings.Join(in.Command, "\x00") {
					return fmt.Errorf(
						"expected the pod to run %v itself, it runs %v — the step's command is not what the kubelet will execute",
						in.Command, got)
				}
				return nil
			},
		),

		brine.DefineCheck[RunExtraDirectRun](
			"that pod works in {string}",
			func(in RunExtraDirectRun, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a directory parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				if main.WorkingDir != want {
					return fmt.Errorf("expected the step to work in %q, it works in %q", want, main.WorkingDir)
				}
				return nil
			},
		),

		// The pod-level half of PE-04, which the matrix records as having no
		// named test. A step that runs without a seccomp profile can issue
		// syscalls the runtime default blocks — on a shared build cluster that
		// is the difference between a sandbox and a foothold.
		brine.DefineCheck[RunExtraDirectRun](
			"the step is confined by the runtime's default seccomp profile",
			func(in RunExtraDirectRun, _ brine.Params, _ *brine.Recorder) error {
				sc := in.Pod.Spec.SecurityContext
				if sc == nil {
					return fmt.Errorf("expected the pod to carry a security context, it has none")
				}
				if sc.SeccompProfile == nil {
					return fmt.Errorf("expected the pod to name a seccomp profile, it names none")
				}
				if sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
					return fmt.Errorf("expected the seccomp profile %q, got %q",
						corev1.SeccompProfileTypeRuntimeDefault, sc.SeccompProfile.Type)
				}
				return nil
			},
		),

		// The process ID is what a restarted web passes back to Attach. An
		// empty one cannot be re-attached to, so the step would be re-run.
		brine.DefineCheck[RunExtraDirectRun](
			"the step has an identity a restarted web could attach to",
			func(in RunExtraDirectRun, _ brine.Params, _ *brine.Recorder) error {
				if in.Process == nil {
					return fmt.Errorf("expected a process, got none")
				}
				if in.Process.ID() == "" {
					return fmt.Errorf("expected the process to have an id, it has an empty one")
				}
				return nil
			},
		),

		// A step with no working directory declares no workspace, so the pod
		// must not invent one. An unasked-for emptyDir would silently shadow
		// whatever the image ships at that path.
		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it declares no working directory",
			func(in ContainerDraft, _ brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				in.Dir = ""
				return in, nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the step has nothing mounted at all",
			func(in PodCreated, _ brine.Params, _ *brine.Recorder) error {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				if len(main.VolumeMounts) != 0 {
					paths := make([]string, 0, len(main.VolumeMounts))
					for _, vm := range main.VolumeMounts {
						paths = append(paths, vm.MountPath)
					}
					return fmt.Errorf("expected the step to see no mounts, it sees [%s]",
						strings.Join(paths, ", "))
				}
				return nil
			},
		),
	}
}

// ---------------------------------------------------------------------------
// Exec mode: the pod is a placeholder the step is exec'd into
// ---------------------------------------------------------------------------

func runExtraPauseDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The observable difference between the two modes. The ginkgo test
		// pinned the exact pause string; what matters to a consumer is that
		// the pod is NOT the step, so the pod outlives the command.
		brine.DefineCheck[TaskOutcome](
			"the pod is a placeholder, not the step's command",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) error {
				pod, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
					Get(in.Cluster.Ctx, in.Handle, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("get pod %q: %w", in.Handle, err)
				}
				main, err := mainContainer(pod)
				if err != nil {
					return err
				}
				all := strings.Join(append(append([]string{}, main.Command...), main.Args...), " ")
				if all == "" {
					return fmt.Errorf("expected the pod to run a placeholder command, it runs nothing")
				}
				if strings.Contains(all, in.Script) {
					return fmt.Errorf(
						"expected the pod NOT to carry the step's command, its entrypoint is %q — "+
							"the step was baked into the pod instead of exec'd into it", all)
				}
				return nil
			},
		),

		// The pod must survive the command in both directions: a consumer
		// still has to be able to stream the step's outputs out of it, and an
		// operator still has to be able to intercept a failed step. Deleting
		// it here is what the GC is for.
		brine.DefineCheck[TaskOutcome](
			"the pod is still on the cluster afterwards",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) error {
				pods, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
					List(in.Cluster.Ctx, metav1.ListOptions{})
				if err != nil {
					return fmt.Errorf("list pods: %w", err)
				}
				for _, pod := range pods.Items {
					if pod.Name == in.Handle {
						return nil
					}
				}
				return fmt.Errorf(
					"expected the pod %q to still be on the cluster after the step finished, it is gone — "+
						"its outputs can no longer be streamed out and it cannot be intercepted",
					in.Handle)
			},
		),

		// fly hijack: the intercepted command's exit code is what the operator
		// sees in their shell. Swallowing it makes a failed hijack look clean.
		brine.DefineCheck[InterceptOutcome](
			"the intercepted command exits {int}",
			func(in InterceptOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected an exit status parameter")
				}
				if in.Err != nil {
					return fmt.Errorf("expected the interception to report exit %d, it failed: %v", want, in.Err)
				}
				if in.ExitStatus != want {
					return fmt.Errorf("expected the intercepted command to exit %d, it exited %d (log: %q)",
						want, in.ExitStatus, in.Log)
				}
				return nil
			},
		),
	}
}

// ---------------------------------------------------------------------------
// What the caller is handed before anything is scheduled
// ---------------------------------------------------------------------------

func runExtraMountDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[ContainerDraft, RunExtraMounts](
			"the container is created but not yet run",
			func(in ContainerDraft, _ brine.Params, _ *brine.Recorder) (RunExtraMounts, error) {
				// An exec transport is what makes the handed-back volumes
				// capable of I/O at all; without one the worker hands back
				// stubs (covered separately in volume-streaming.feature).
				in.Worker.SetExecutor(execStub{})

				container, mounts, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runExtraSpecFromDraft(in),
					&noopDelegate{},
				)
				if err != nil {
					return RunExtraMounts{}, fmt.Errorf("find or create container %q: %w", in.Handle, err)
				}
				return RunExtraMounts{
					Namespace: in.Namespace, Clientset: in.Clientset, Ctx: in.Ctx,
					Handle: in.Handle, Container: container, Mounts: mounts,
				}, nil
			},
		),

		brine.DefineMap[RunExtraMounts, RunExtraMounts](
			"the step then runs",
			func(in RunExtraMounts, _ brine.Params, _ *brine.Recorder) (RunExtraMounts, error) {
				if _, err := in.Container.Run(in.Ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "true"},
				}, runtime.ProcessIO{}); err != nil {
					return RunExtraMounts{}, fmt.Errorf("run container %q: %w", in.Handle, err)
				}
				in.Ran = true
				return in, nil
			},
		),

		// CO-04/CO-05: the caller is a build step, and these mounts are how it
		// finds its own inputs, outputs and caches. A missing one means the
		// step's artifact never gets registered.
		brine.DefineCheck[RunExtraMounts](
			"the caller is handed a volume mounted at {string}",
			func(in RunExtraMounts, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a path parameter")
				}
				var seen []string
				for _, m := range in.Mounts {
					if m.MountPath == want {
						if m.Volume == nil {
							return fmt.Errorf("the mount at %q carries no volume", want)
						}
						return nil
					}
					seen = append(seen, m.MountPath)
				}
				return fmt.Errorf("expected a volume mounted at %q, the caller was handed [%s]",
					want, strings.Join(seen, ", "))
			},
		),

		brine.DefineCheck[RunExtraMounts](
			"the caller is handed {int} volumes in all",
			func(in RunExtraMounts, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected a count parameter")
				}
				if len(in.Mounts) != want {
					var seen []string
					for _, m := range in.Mounts {
						seen = append(seen, m.MountPath)
					}
					return fmt.Errorf("expected %d volumes, the caller was handed %d ([%s])",
						want, len(in.Mounts), strings.Join(seen, ", "))
				}
				return nil
			},
		),

		// Two mounts sharing a handle would make the artifact registry point
		// two paths at one blob — the CO-05 failure, one step's outputs
		// overwriting another's.
		brine.DefineCheck[RunExtraMounts](
			"every volume the caller was handed has its own handle",
			func(in RunExtraMounts, _ brine.Params, _ *brine.Recorder) error {
				seen := map[string]string{}
				for _, m := range in.Mounts {
					if m.Volume == nil {
						return fmt.Errorf("the mount at %q carries no volume", m.MountPath)
					}
					handle := m.Volume.Handle()
					if handle == "" {
						return fmt.Errorf("the volume mounted at %q has an empty handle, "+
							"so nothing downstream can refer to it", m.MountPath)
					}
					if other, dup := seen[handle]; dup {
						return fmt.Errorf("the volumes at %q and %q share the handle %q",
							other, m.MountPath, handle)
					}
					seen[handle] = m.MountPath
				}
				return nil
			},
		),

		// The deferral itself: the pod name is not known until Run, because
		// the command is not known until Run.
		brine.DefineCheck[RunExtraMounts](
			"no volume the caller was handed knows a pod yet",
			func(in RunExtraMounts, _ brine.Params, _ *brine.Recorder) error {
				for _, m := range in.Mounts {
					vol, ok := m.Volume.(*jetbridge.Volume)
					if !ok {
						return fmt.Errorf("the volume at %q is %T, not a jetbridge volume", m.MountPath, m.Volume)
					}
					if vol.PodName() != "" {
						return fmt.Errorf("the volume at %q already names the pod %q before the step ran",
							m.MountPath, vol.PodName())
					}
				}
				return nil
			},
		),

		// And afterwards it does — which is the only reason StreamOut can
		// reach the step's outputs at all.
		brine.DefineCheck[RunExtraMounts](
			"every volume the caller was handed now names the pod {string}",
			func(in RunExtraMounts, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pod name parameter")
				}
				if !in.Ran {
					return fmt.Errorf("the step has not run yet")
				}
				for _, m := range in.Mounts {
					vol, ok := m.Volume.(*jetbridge.Volume)
					if !ok {
						return fmt.Errorf("the volume at %q is %T, not a jetbridge volume", m.MountPath, m.Volume)
					}
					if vol.PodName() != want {
						return fmt.Errorf("expected the volume at %q to name the pod %q, it names %q",
							m.MountPath, want, vol.PodName())
					}
				}
				return nil
			},
		),
	}
}

// ---------------------------------------------------------------------------
// A step's output, read back out of the volume the caller was handed
// ---------------------------------------------------------------------------

func runExtraOutputDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// This is coverage_matrix.md Addendum 2's round trip applied to the
		// "Output volume extraction after exec" block, which asserted
		// `lastCall.command == ["tar","cf","-","-C",path,"."]` and
		// `lastCall.podName`. Neither proves a single byte moved. Here the
		// step really writes a file and the bytes really come back.
		brine.DefineMapUsing[brine.Empty, RunExtraOutput](
			"a step {string} that writes {string} into its output directory",
			[]string{"jetbridge-db", "task-workspace"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (RunExtraOutput, error) {
				handle, _ := p.GetString(0)
				content, ok := p.GetString(1)
				if !ok {
					return RunExtraOutput{}, fmt.Errorf("expected a handle and a content parameter")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return RunExtraOutput{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				workspace, ok := res.Get("task-workspace").(TaskWorkspace)
				if !ok {
					return RunExtraOutput{}, fmt.Errorf("task-workspace resource is %T", res.Get("task-workspace"))
				}
				dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return RunExtraOutput{}, err
				}
				// The local shell adapter runs in THIS host's /tmp, which
				// outlives the run; a second invocation would find the first
				// one's supervisor state and replay its log without ever
				// writing the file. Same fix as the intercept steps.
				if err := clearSupervisorState(handle); err != nil {
					return RunExtraOutput{}, err
				}

				ctx := context.Background()
				namespace := "test-namespace"
				clientset := fake.NewSimpleClientset()
				worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig(namespace, ""))
				worker.SetExecutor(localShellAdapter{})

				outputPath := filepath.Join(workspace.Dir, "result")
				container, mounts, err := worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       workspace.Dir,
						Type:      db.ContainerTypeTask,
						ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
						Outputs:   runtime.OutputPaths{"result": outputPath},
					},
					&noopDelegate{},
				)
				if err != nil {
					return RunExtraOutput{}, fmt.Errorf("find or create container %q: %w", handle, err)
				}

				log := new(bytes.Buffer)
				script := fmt.Sprintf("mkdir -p %s && printf %s > %s/output.txt",
					outputPath, content, outputPath)
				process, err := container.Run(ctx, runtime.ProcessSpec{
					ID:   handle,
					Path: "/bin/sh",
					Args: []string{"-c", script},
				}, runtime.ProcessIO{Stdout: log, Stderr: log})
				if err != nil {
					return RunExtraOutput{}, fmt.Errorf("run step %q: %w", handle, err)
				}

				pod, err := runExtraTheOnlyPod(ctx, clientset, namespace)
				if err != nil {
					return RunExtraOutput{}, err
				}
				if err := runExtraMarkRunning(ctx, clientset, namespace, pod.Name); err != nil {
					return RunExtraOutput{}, err
				}

				result, waitErr := process.Wait(ctx)
				if waitErr != nil {
					return RunExtraOutput{}, fmt.Errorf("wait for step %q: %w (log: %q)", handle, waitErr, log.String())
				}
				if result.ExitStatus != 0 {
					return RunExtraOutput{}, fmt.Errorf(
						"the step exited %d before it could write its output (log: %q)",
						result.ExitStatus, log.String())
				}

				var vol *jetbridge.Volume
				for _, m := range mounts {
					if m.MountPath != outputPath {
						continue
					}
					v, ok := m.Volume.(*jetbridge.Volume)
					if !ok {
						return RunExtraOutput{}, fmt.Errorf("the output mount carries %T, not a jetbridge volume", m.Volume)
					}
					vol = v
				}
				if vol == nil {
					return RunExtraOutput{}, fmt.Errorf("the caller was handed no volume for the output at %q", outputPath)
				}

				return RunExtraOutput{
					Ctx: ctx, Handle: handle, OutputPath: outputPath,
					Volume: vol, Log: log.String(), ExitStatus: result.ExitStatus,
				}, nil
			},
		),

		brine.DefineMap[RunExtraOutput, RunExtraOutputRead](
			"its output is streamed out of the volume the caller was handed",
			func(in RunExtraOutput, _ brine.Params, _ *brine.Recorder) (RunExtraOutputRead, error) {
				stream, err := in.Volume.StreamOut(in.Ctx, ".", compression.NewGzipCompression())
				if err != nil {
					return RunExtraOutputRead{Err: err}, nil
				}
				defer stream.Close()
				files, err := filesInGzippedTar(stream)
				return RunExtraOutputRead{Files: files, Err: err}, nil
			},
		),

		brine.DefineCheck[RunExtraOutputRead](
			"the streamed output holds {string} containing {string}",
			func(in RunExtraOutputRead, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a file name and its content")
				}
				if in.Err != nil {
					return fmt.Errorf("reading the step's output failed: %v", in.Err)
				}
				got, found := in.Files[name]
				if !found {
					names := make([]string, 0, len(in.Files))
					for n := range in.Files {
						names = append(names, n)
					}
					return fmt.Errorf("expected %q in the step's output, it holds [%s]",
						name, strings.Join(names, ", "))
				}
				if got != want {
					return fmt.Errorf("expected %q to contain %q, got %q", name, want, got)
				}
				return nil
			},
		),
	}
}

// ---------------------------------------------------------------------------
// What the operator's counters say a Run did
// ---------------------------------------------------------------------------

func runExtraMetricDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[ClusterReady, RunExtraMetrics](
			"a step is run on it",
			func(in ClusterReady, _ brine.Params, _ *brine.Recorder) (RunExtraMetrics, error) {
				return runExtraCountedRun(in, "metric-direct-handle")
			},
		),

		brine.DefineMap[ClusterReady, RunExtraMetrics](
			"a step is run on it through an exec transport",
			func(in ClusterReady, _ brine.Params, _ *brine.Recorder) (RunExtraMetrics, error) {
				in.Worker.SetExecutor(execStub{})
				return runExtraCountedRun(in, "metric-exec-handle")
			},
		),

		brine.DefineMap[ClusterReady, RunExtraMetrics](
			"a step is run on it but the cluster refuses to create the pod",
			func(in ClusterReady, _ brine.Params, _ *brine.Recorder) (RunExtraMetrics, error) {
				in.Clientset.PrependReactor("create", "pods",
					func(k8stesting.Action) (bool, apiruntime.Object, error) {
						return true, nil, fmt.Errorf("simulated pod creation failure")
					})
				return runExtraCountedRun(in, "metric-fail-handle")
			},
		),

		// The two counters are what an operator's dashboard is built on. A
		// failure counted as a success hides a cluster that has stopped
		// admitting pods behind a healthy-looking creation rate.
		brine.DefineCheck[RunExtraMetrics](
			"the operator sees {int} container created and {int} failed",
			func(in RunExtraMetrics, p brine.Params, _ *brine.Recorder) error {
				created, _ := p.GetInt(0)
				failed, ok := p.GetInt(1)
				if !ok {
					return fmt.Errorf("expected two counts")
				}
				if in.Created != float64(created) || in.Failed != float64(failed) {
					return fmt.Errorf(
						"expected the operator to see %d created and %d failed, they see %.0f created and %.0f failed (run error: %v)",
						created, failed, in.Created, in.Failed, in.RunErr)
				}
				return nil
			},
		),
	}
}

// runExtraCountedRun drains both counters, performs one Run, and reads them
// back — all inside one step, so the pair describes that Run and nothing else.
func runExtraCountedRun(in ClusterReady, handle string) (RunExtraMetrics, error) {
	container, _, err := in.Worker.FindOrCreateContainer(
		in.Ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			Dir:       "/workdir",
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
		},
		&noopDelegate{},
	)
	if err != nil {
		return RunExtraMetrics{}, fmt.Errorf("find or create container %q: %w", handle, err)
	}

	metric.Metrics.ContainersCreated.Delta()
	metric.Metrics.FailedContainers.Delta()

	_, runErr := container.Run(in.Ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hello"},
	}, runtime.ProcessIO{})

	return RunExtraMetrics{
		Created: metric.Metrics.ContainersCreated.Delta(),
		Failed:  metric.Metrics.FailedContainers.Delta(),
		RunErr:  runErr,
	}, nil
}

// ---------------------------------------------------------------------------
// When the database, not Kubernetes, is what went wrong
// ---------------------------------------------------------------------------

func runExtraDBDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The lookup is the FIRST thing FindOrCreateContainer does, so a lost
		// connection has to surface as a lookup failure and not as a second
		// row for a container that already exists.
		brine.DefineMapUsing[brine.Empty, RunExtraDBOutcome](
			"a worker that lost its database connection before the container was requested",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (RunExtraDBOutcome, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return RunExtraDBOutcome{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				if _, err := database.PersistNamedWorker("k8s-worker-1"); err != nil {
					return RunExtraDBOutcome{}, err
				}

				// Load the worker over its own connection and then close it,
				// so every statement it issues fails the way a lost connection
				// does. Mirrors the ginkgo suite's closedConnWorker.
				conn := database.runner.OpenConn()
				logger := lagertest.NewTestLogger("brine-closed-conn")
				factory := db.NewWorkerFactory(conn, db.NewStaticWorkerCache(logger, conn, 0))
				lost, found, err := factory.GetWorker("k8s-worker-1")
				if err != nil {
					return RunExtraDBOutcome{}, fmt.Errorf("get worker over the second connection: %w", err)
				}
				if !found {
					return RunExtraDBOutcome{}, fmt.Errorf("worker k8s-worker-1 not found over the second connection")
				}
				if err := conn.Close(); err != nil {
					return RunExtraDBOutcome{}, fmt.Errorf("close the second connection: %w", err)
				}

				return runExtraRequest(database, lost, "db-fail-handle"), nil
			},
		),

		// Handles are globally unique. One already taken on another worker
		// misses this worker's lookup and then collides on insert; reporting
		// that as anything other than a create failure would have the step
		// scheduled against a container row it does not own.
		brine.DefineMapUsing[brine.Empty, RunExtraDBOutcome](
			"a container {string} whose handle another worker already holds",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (RunExtraDBOutcome, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return RunExtraDBOutcome{}, fmt.Errorf("expected a handle parameter")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return RunExtraDBOutcome{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				mine, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return RunExtraDBOutcome{}, err
				}
				theirs, err := database.PersistNamedWorker("k8s-worker-2")
				if err != nil {
					return RunExtraDBOutcome{}, err
				}
				if _, err := theirs.CreateContainer(
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
				); err != nil {
					return RunExtraDBOutcome{}, fmt.Errorf("create the other worker's container: %w", err)
				}
				return runExtraRequest(database, mine, handle), nil
			},
		),

		// A row left in `creating` by a crashed web is invisible to the
		// collector. The next request must adopt it — a second row would
		// orphan the first one's pod.
		brine.DefineMapUsing[brine.Empty, RunExtraDBOutcome](
			"a container {string} left half-created by a crash, requested again",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (RunExtraDBOutcome, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return RunExtraDBOutcome{}, fmt.Errorf("expected a handle parameter")
				}
				database, dbWorker, err := runExtraStaleContainer(res, handle)
				if err != nil {
					return RunExtraDBOutcome{}, err
				}
				return runExtraRequest(database, dbWorker, handle), nil
			},
		),

		brine.DefineMapUsing[brine.Empty, RunExtraDBOutcome](
			"a container {string} left half-created by a crash on a database that still cannot complete it",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (RunExtraDBOutcome, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return RunExtraDBOutcome{}, fmt.Errorf("expected a handle parameter")
				}
				database, dbWorker, err := runExtraStaleContainer(res, handle)
				if err != nil {
					return RunExtraDBOutcome{}, err
				}
				return runExtraRequest(database, runExtraStaleCreatedFails{dbWorker}, handle), nil
			},
		),

		brine.DefineCheck[RunExtraDBOutcome](
			"requesting the container fails saying {string}",
			func(in RunExtraDBOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
				}
				if in.Err == nil {
					return fmt.Errorf("expected the request to fail saying %q, it succeeded", want)
				}
				if !strings.Contains(in.Message, want) {
					return fmt.Errorf("expected the failure to say %q, it said %q", want, in.Message)
				}
				return nil
			},
		),

		brine.DefineCheck[RunExtraDBOutcome](
			"requesting the container succeeds",
			func(in RunExtraDBOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Err != nil {
					return fmt.Errorf("expected the request to succeed, it failed: %v", in.Err)
				}
				return nil
			},
		),

		brine.DefineCheck[RunExtraDBOutcome](
			"the container row {string} is left in state {string}",
			func(in RunExtraDBOutcome, p brine.Params, _ *brine.Recorder) error {
				handle, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a handle and a state")
				}
				var state string
				if err := in.DB.Conn.QueryRow(
					`SELECT state FROM containers WHERE handle = $1`, handle,
				).Scan(&state); err != nil {
					return fmt.Errorf("read the state of container %q: %w", handle, err)
				}
				if state != want {
					return fmt.Errorf("expected the container %q to be left in state %q, it is %q",
						handle, want, state)
				}
				return nil
			},
		),

		brine.DefineCheck[RunExtraDBOutcome](
			"the database holds exactly {int} row for the handle {string}",
			func(in RunExtraDBOutcome, p brine.Params, _ *brine.Recorder) error {
				want, _ := p.GetInt(0)
				handle, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a count and a handle")
				}
				var count int
				if err := in.DB.Conn.QueryRow(
					`SELECT count(*) FROM containers WHERE handle = $1`, handle,
				).Scan(&count); err != nil {
					return fmt.Errorf("count rows for handle %q: %w", handle, err)
				}
				if count != want {
					return fmt.Errorf("expected %d row(s) for the handle %q, the database holds %d",
						want, handle, count)
				}
				return nil
			},
		),
	}
}

// runExtraStaleContainer leaves a real row in `creating`, the way a web that
// died between the insert and the transition would.
func runExtraStaleContainer(res brine.Resources, handle string) (JetbridgeDB, db.Worker, error) {
	database, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return JetbridgeDB{}, nil, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
	}
	dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
	if err != nil {
		return JetbridgeDB{}, nil, err
	}
	if _, err := dbWorker.CreateContainer(
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
	); err != nil {
		return JetbridgeDB{}, nil, fmt.Errorf("leave a creating container behind: %w", err)
	}
	return database, dbWorker, nil
}

func runExtraRequest(database JetbridgeDB, dbWorker db.Worker, handle string) RunExtraDBOutcome {
	worker := jetbridge.NewWorker(dbWorker, fake.NewSimpleClientset(), jetbridge.NewConfig("test-namespace", ""))
	_, _, err := worker.FindOrCreateContainer(
		context.Background(),
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
		},
		&noopDelegate{},
	)
	out := RunExtraDBOutcome{DB: database, Handle: handle, Err: err}
	if err != nil {
		out.Message = err.Error()
	}
	return out
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// runExtraSpecFromDraft mirrors the spec container_spec.go's "the container
// runs" builds, so a draft refined by the shared Given steps means the same
// thing on this path.
func runExtraSpecFromDraft(in ContainerDraft) runtime.ContainerSpec {
	var inputs []runtime.Input
	for _, path := range in.Inputs {
		inputs = append(inputs, runtime.Input{DestinationPath: path})
	}
	outputs := runtime.OutputPaths{}
	for i, path := range in.Outputs {
		outputs[fmt.Sprintf("output-%d", i)] = path
	}

	spec := runtime.ContainerSpec{
		TeamID:       1,
		Dir:          in.Dir,
		ImageSpec:    runtime.ImageSpec{ImageURL: in.ImageURL, Privileged: in.Privileged},
		Env:          in.ContainerEnv,
		Inputs:       inputs,
		Caches:       in.Caches,
		ScratchPaths: in.Scratch,
		Sidecars:     in.Sidecars,
		Limits: runtime.ContainerLimits{
			CPU:           in.LimitCPU,
			Memory:        in.LimitMemory,
			CPURequest:    in.RequestCPU,
			MemoryRequest: in.RequestMemory,
		},
	}
	if len(outputs) > 0 {
		spec.Outputs = outputs
	}
	return spec
}

func runExtraTheOnlyPod(ctx context.Context, clientset *fake.Clientset, namespace string) (*corev1.Pod, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	if len(pods.Items) != 1 {
		return nil, fmt.Errorf("expected exactly 1 pod, found %d", len(pods.Items))
	}
	pod := pods.Items[0]
	return &pod, nil
}

func runExtraMarkRunning(ctx context.Context, clientset *fake.Clientset, namespace, name string) error {
	pods := clientset.CoreV1().Pods(namespace)
	pod, err := pods.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get pod %q: %w", name, err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionTrue},
	}
	if _, err := pods.UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update pod status: %w", err)
	}
	return nil
}
