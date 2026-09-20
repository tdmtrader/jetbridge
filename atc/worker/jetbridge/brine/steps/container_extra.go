package steps

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ---------------------------------------------------------------------------
// container_extra.go — the remainder of container_test.go.
//
// Earlier spy assertions inspected executor calls. Command and stream
// behavior now runs through real Kubernetes exec; API-only cases below inspect
// pod assembly. See container-run.feature for the disposition of seam checks.
// ---------------------------------------------------------------------------

// RunExtraMounts is what FindOrCreateContainer handed back BEFORE anything was
// scheduled: the container and one volume mount per declared path. The pod
// does not exist yet, which is the whole point of the block it replaces.
type RunExtraMounts struct {
	Namespace string
	Clientset kubernetes.Interface
	Ctx       context.Context
	Handle    string
	Container runtime.Container
	Mounts    []runtime.VolumeMount
	Ran       bool
}

// RunExtraMetrics is the operator-visible counter movement across one Run.
// Both deltas are read inside the same step that performed the Run, so the
// pair is a snapshot of that Run and nothing else.
type RunExtraMetrics struct {
	Created float64
	Failed  float64
	RunErr  error
	Pods    []corev1.Pod
}

// ContainerExtraDefinitions is the single entry point for this file.
func ContainerExtraDefinitions() []brine.StepDefinition {
	defs := runExtraDirectDefinitions()
	defs = append(defs, runExtraPauseDefinitions()...)
	defs = append(defs, runExtraMountDefinitions()...)
	defs = append(defs, runExtraMetricDefinitions()...)
	defs = append(defs, containerConcurrencyDefinitions()...)

	return defs
}

// ---------------------------------------------------------------------------
// Direct mode: the pod IS the step
// ---------------------------------------------------------------------------

func runExtraDirectDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		Transform[ContainerDraft, PodCreated](
			"the step runs command {string} with arguments {string} directly",
			func(in ContainerDraft, a Args) (PodCreated, error) {
				spec, err := containerSpecFromDraft(in)
				if err != nil {
					return PodCreated{}, err
				}

				spec.Type = db.ContainerTypeTask

				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					spec,
					nil,
				)
				if err != nil {
					return PodCreated{}, fmt.Errorf("find or create container %q: %w", in.Handle, err)
				}

				// Snapshot the creation counter around this same direct Run.
				metric.Metrics.ContainersCreated.Delta()
				process, err := container.Run(in.Ctx, runtime.ProcessSpec{
					Path: a.String(0),
					Args: splitList(a.String(1)),
					Dir:  in.Dir,
				}, runtime.ProcessIO{})
				if err != nil {
					return PodCreated{}, fmt.Errorf("run container %q: %w", in.Handle, err)
				}

				if _, direct := process.(*jetbridge.Process); !direct {
					return PodCreated{}, fmt.Errorf("expected direct process, got %T", process)
				}
				if created := metric.Metrics.ContainersCreated.Delta(); created < 1 {
					return PodCreated{}, fmt.Errorf("direct Run did not count its created container: %v", created)
				}
				pod, err := runExtraTheOnlyPod(in.Ctx, in.Clientset, in.Namespace)
				if err != nil {
					return PodCreated{}, err
				}

				return PodCreated{
					Namespace: in.Namespace, Ctx: in.Ctx,
					Handle: in.Handle, Pod: pod, Process: process,
				}, nil
			},
		),

		// The process ID is what a restarted web passes back to Attach. An
		// empty one cannot be re-attached to, so the step would be re-run.
		CheckThat[PodCreated]("the step has an identity a restarted web could attach to",
			func(in PodCreated) error {
				if in.Process == nil {
					return fmt.Errorf("expected a process, got none")
				}
				if in.Process.ID() == "" {
					return fmt.Errorf("expected the process to have an id, it has an empty one")
				}
				return nil
			}),

		// A step with no working directory declares no workspace, so the pod
		// must not invent one. An unasked-for emptyDir would silently shadow
		// whatever the image ships at that path.
		Refine[ContainerDraft]("it declares no working directory",
			func(in ContainerDraft, _ Args) ContainerDraft {
				in.Dir = ""
				return in
			}),

		CheckThat[PodCreated]("the step has nothing mounted at all",
			func(in PodCreated) error {
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
			}),
	}
}

// ---------------------------------------------------------------------------
// Exec mode: the pod is a placeholder the step is exec'd into
// ---------------------------------------------------------------------------

func runExtraPauseDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// Preserve the literal pause argv as well as separation from the task.
		// Equivalent-looking entrypoints must not silently weaken this contract.
		CheckThat[TaskOutcome]("the pod is a placeholder, not the step's command",
			func(in TaskOutcome) error {
				pod, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
					Get(in.Cluster.Ctx, in.podName(), metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("get pod %q: %w", in.podName(), err)
				}
				main, err := mainContainer(pod)
				if err != nil {
					return err
				}
				want := []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"}
				if !reflect.DeepEqual(main.Command, want) || len(main.Args) != 0 {
					return fmt.Errorf("pause argv: command=%q args=%q, want command=%q and no args", main.Command, main.Args, want)
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
			}),

		// The pod must survive the command in both directions: a consumer
		// still has to be able to stream the step's outputs out of it, and an
		// operator still has to be able to intercept a failed step. Deleting
		// it here is what the GC is for.
		CheckThat[TaskOutcome]("the pod is still on the cluster afterwards",
			func(in TaskOutcome) error {
				pods, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
					List(in.Cluster.Ctx, metav1.ListOptions{})
				if err != nil {
					return fmt.Errorf("list pods: %w", err)
				}
				for _, pod := range pods.Items {
					if pod.Name == in.podName() {
						return nil
					}
				}
				return fmt.Errorf(
					"expected the pod %q to still be on the cluster after the step finished, it is gone — "+
						"its outputs can no longer be streamed out and it cannot be intercepted",
					in.podName())
			}),

		// fly hijack: the intercepted command's exit code is what the operator
		// sees in their shell. Swallowing it makes a failed hijack look clean.
		// A wrong exit code is diagnosed from the command's log, so the check
		// carries it into the failure; an interception that failed outright has
		// no exit code to compare, which is the getter's error.
		CheckInt[InterceptOutcome]("the intercepted command exits {int}",
			"the intercepted command's exit status",
			func(in InterceptOutcome) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("the interception failed: %v", in.Err)
				}
				return in.ExitStatus, nil
			},
			func(in InterceptOutcome) string { return fmt.Sprintf("log: %q", in.Log) }),
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
				executor := in.MountExecutor
				if executor == nil {
					return RunExtraMounts{}, fmt.Errorf("draft has no production execution transport")
				}
				in.Worker.SetExecutor(executor)

				spec, err := containerSpecFromDraft(in)
				if err != nil {
					return RunExtraMounts{}, err
				}

				container, mounts, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					spec,
					nil,
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

		// One table states the complete pre-Run contract, not just membership.
		brine.DefineCheck[RunExtraMounts]("the caller is handed these deferred volumes",
			func(in RunExtraMounts, p brine.Params, _ *brine.Recorder) error {
				rows := p.RequireDataTable()
				if len(rows) < 2 || len(rows[0]) != 1 || rows[0][0] != "mount path" {
					return fmt.Errorf("expected a nonempty table headed 'mount path'")
				}
				expected := map[string]bool{}
				for _, row := range rows[1:] {
					if len(row) != 1 || !filepath.IsAbs(row[0]) || expected[row[0]] {
						return fmt.Errorf("expected distinct absolute mount paths, got %v", row)
					}
					expected[row[0]] = true
				}
				if in.Ran {
					return fmt.Errorf("expected deferred volumes before Run")
				}
				if len(in.Mounts) != len(expected) {
					return fmt.Errorf("expected %d returned mounts, got %d", len(expected), len(in.Mounts))
				}
				handles := map[string]string{}
				for _, mount := range in.Mounts {
					if !expected[mount.MountPath] {
						return fmt.Errorf("unexpected or repeated returned mount %q", mount.MountPath)
					}
					vol, ok := mount.Volume.(*jetbridge.Volume)
					if !ok || vol == nil {
						return fmt.Errorf("mount %q must carry a non-nil *jetbridge.Volume, got %T", mount.MountPath, mount.Volume)
					}
					if !vol.HasExecutor() {
						return fmt.Errorf("volume at %q has no executor before Run", mount.MountPath)
					}
					if vol.PodName() != "" {
						return fmt.Errorf("volume at %q already names pod %q before Run", mount.MountPath, vol.PodName())
					}
					handle := vol.Handle()
					if handle == "" {
						return fmt.Errorf("volume at %q has an empty handle", mount.MountPath)
					}
					if other, exists := handles[handle]; exists {
						return fmt.Errorf("volumes at %q and %q share handle %q", other, mount.MountPath, handle)
					}
					handles[handle] = mount.MountPath
					delete(expected, mount.MountPath)
				}
				if len(expected) != 0 {
					return fmt.Errorf("missing returned mounts: %v", expected)
				}
				return nil
			}),

		// And afterwards it does — which is the only reason StreamOut can
		// reach the step's outputs at all. Keeps its own body: this is a for-all
		// over every handed-back volume rather than one derived value or a
		// membership, and it names the volume that disagrees.
		Assert[RunExtraMounts](
			"every volume the caller was handed now names the pod {string}",
			func(in RunExtraMounts, args Args) error {
				want := args.String(0)

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
// What the operator's counters say a Run did
// ---------------------------------------------------------------------------

func runExtraMetricDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[WorkerReady, RunExtraMetrics](
			"a step's pod creation is {string} using {string} mode",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (RunExtraMetrics, error) {
				outcome, okOutcome := p.GetString(0)
				mode, okMode := p.GetString(1)
				if !okOutcome || !okMode || (outcome != "accepted" && outcome != "refused") {
					return RunExtraMetrics{}, fmt.Errorf("expected an accepted/refused outcome and direct/exec mode")
				}
				switch mode {
				case "direct":
					in.Worker.SetExecutor(nil)
				case "exec":
					if in.ProducerExecutor == nil {
						return RunExtraMetrics{}, fmt.Errorf("worker has no production execution transport")
					}
					in.Worker.SetExecutor(in.ProducerExecutor)
				default:
					return RunExtraMetrics{}, fmt.Errorf("unknown pod creation mode %q", mode)
				}
				ctx, cancel := context.WithTimeout(in.Ctx, 10*time.Second)
				defer cancel()
				in.Ctx = ctx
				if outcome == "refused" {
					// NamespaceLifecycle admission, not a reactor, refuses new
					// pods after this scenario's namespace starts terminating.
					namespaces := in.Clientset.CoreV1().Namespaces()
					ns, err := namespaces.Get(ctx, in.Namespace, metav1.GetOptions{})
					if err != nil {
						return RunExtraMetrics{}, err
					}
					if err := namespaces.Delete(ctx, ns.Name, metav1.DeleteOptions{
						Preconditions: &metav1.Preconditions{UID: &ns.UID},
					}); err != nil {
						return RunExtraMetrics{}, err
					}
					ns, err = namespaces.Get(ctx, ns.Name, metav1.GetOptions{})
					if err != nil || ns.DeletionTimestamp == nil {
						return RunExtraMetrics{}, fmt.Errorf("namespace must be terminating before refusal: %v", err)
					}
				}
				return runExtraCountedRun(in, "metric-"+mode+"-handle")
			},
		),

		// The two counters are what an operator's dashboard is built on. A
		// failure counted as a success hides a cluster that has stopped
		// admitting pods behind a healthy-looking creation rate. Keeps its own
		// body: both counters are expectations, so the pair has to move
		// together, and the run's error is part of reading a wrong pair.
		Assert[RunExtraMetrics](
			"the operator sees {int} container created and {int} failed",
			func(in RunExtraMetrics, args Args) error {
				created := args.Int(0)
				failed := args.Int(1)

				if in.Created != float64(created) || in.Failed != float64(failed) {
					return fmt.Errorf(
						"expected the operator to see %d created and %d failed, they see %.0f created and %.0f failed (run error: %v)",
						created, failed, in.Created, in.Failed, in.RunErr)
				}
				if (failed > 0) != (in.RunErr != nil) || len(in.Pods) != created {
					return fmt.Errorf("counts do not describe the API outcome: %d pods, run error %v", len(in.Pods), in.RunErr)
				}
				for _, pod := range in.Pods {
					if pod.UID == "" {
						return fmt.Errorf("created pod %q has no API-assigned identity", pod.Name)
					}
				}
				return nil
			},
		),
	}
}

// runExtraCountedRun drains both counters, performs one Run, and reads them
// back — all inside one step, so the pair describes that Run and nothing else.
func runExtraCountedRun(in WorkerReady, handle string) (RunExtraMetrics, error) {
	container, _, err := in.Worker.FindOrCreateContainer(
		in.Ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    in.TeamID,
			Dir:       "/workdir",
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
		},
		nil,
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

	pods, err := in.Clientset.CoreV1().Pods(in.Namespace).List(in.Ctx, metav1.ListOptions{})
	if err != nil {
		return RunExtraMetrics{}, fmt.Errorf("observe actual pod creation: %w", err)
	}
	return RunExtraMetrics{
		Pods:    pods.Items,
		Created: metric.Metrics.ContainersCreated.Delta(),
		Failed:  metric.Metrics.FailedContainers.Delta(),
		RunErr:  runErr,
	}, nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func runExtraTheOnlyPod(ctx context.Context, clientset kubernetes.Interface, namespace string) (*corev1.Pod, error) {
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
