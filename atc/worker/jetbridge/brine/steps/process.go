package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ProcessDefinitions carries the pod-lifecycle family, migrated from
// process_test.go's Wait, diagnostics, startup-timeout, transient-API,
// metrics and sidecar-lifecycle blocks.
//
// The failure-detection half of that file already lives in
// ../features/failure-priority.feature. What is left is the other half of what
// a step's consumer experiences: the exit status it gets back, whether its pod
// is cleaned up afterwards, and — when something goes wrong — whether the build
// log says enough for the user to tell a cluster problem from a pipeline one.
//
// Every check below reads either the value Wait returned, the build log the
// step's stderr received, the pods actually left in the cluster, or a
// process-global metric an operator's dashboard scrapes. None of them reads a
// double's recorded calls (coverage_matrix Addendum 2).

// ProcessOutcome is the terminal state for this family: what the step's
// consumer saw, plus enough of the cluster to ask whether the pod outlived it.
//
// It retains cluster and execution observations beyond the basic StepOutcome.
type ProcessOutcome struct {
	compatibilityContainer runtime.Container
	podUID                 string
	runtimeTrace           *execObservation
	runtimeCreates         int
	NodeName               string
	Namespace              string
	Clientset              kubernetes.Interface
	Ctx                    context.Context
	Handle                 string
	ExitStatus             int
	Err                    error
	Message                string
	Stderr                 string
	ImagePullFailures      float64
	FailedContainer        string
}

func ProcessDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// --- Workers whose configuration the scenarios need ---

		// Both real-API paths stay explicit: production execProcess and the
		// direct compatibility watcher. Reported status is not pod execution.
		brine.DefineMap[WorkerReady, WorkerReady](
			"the worker uses {string} execution",
			func(in WorkerReady, p brine.Params, rec *brine.Recorder) (WorkerReady, error) {
				mode, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected an execution mode")
				}
				switch mode {
				case "production":
					if in.ProducerExecutor == nil {
						return WorkerReady{}, fmt.Errorf("worker has no production execution transport")
					}
					in.Executor = in.ProducerExecutor
				case "direct compatibility":
					in.Executor = nil
				default:
					return WorkerReady{}, fmt.Errorf("unknown execution mode %q", mode)
				}
				ctx, cancel := context.WithTimeout(in.Ctx, 20*time.Second)
				rec.RegisterDisposer(cancel)
				in.Ctx = ctx
				return in.rebuild(), nil
			},
		),
		brine.DefineMapUsing[WorkerReady, ProcessOutcome](
			"the runtime diagnoses pod deletion on a {string} node",
			[]string{"real-cluster"},
			func(in WorkerReady, p brine.Params, rec *brine.Recorder, resources brine.Resources) (ProcessOutcome, error) {
				kind, ok := p.GetString(0)
				if !ok {
					return ProcessOutcome{}, fmt.Errorf("expected a node condition")
				}
				cluster, ok := resources.Get("real-cluster").(*realCluster)
				if !ok {
					return ProcessOutcome{}, fmt.Errorf("missing owned API cluster")
				}
				name := "nonexistent-node"
				switch kind {
				case "cordoned":
					name = "draining-node-1"
					node, err := in.Clientset.CoreV1().Nodes().Create(in.Ctx, &corev1.Node{
						ObjectMeta: metav1.ObjectMeta{Name: name},
						Spec:       corev1.NodeSpec{Unschedulable: true},
					}, metav1.CreateOptions{})
					if err != nil {
						return ProcessOutcome{}, err
					}
					registerAPICleanup(rec, "diagnostic node "+name, func(ctx context.Context) error {
						return in.Clientset.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &node.UID}})
					})
				case "missing":
					if _, err := in.Clientset.CoreV1().Nodes().Get(in.Ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
						return ProcessOutcome{}, fmt.Errorf("diagnostic node must be absent: %v", err)
					}
				default:
					return ProcessOutcome{}, fmt.Errorf("unknown diagnostic node condition %q", kind)
				}
				return observeDiagnosticDeletion(in, "diagnostic-deletion", name, cluster)
			},
		),

		// --- A described container (sidecars and all) that keeps its process ---

		// "the container runs" throws the process away and keeps the pod spec,
		// which is right for the container-spec family and useless here: the
		// sidecar-lifecycle scenarios need to wait on the step. This is the
		// same transition keeping the other half.
		brine.DefineMap[ContainerDraft, StepRunning](
			"the described container starts",
			func(in ContainerDraft, _ brine.Params, _ *brine.Recorder) (StepRunning, error) {
				spec, err := containerSpecFromDraft(in)
				if err != nil {
					return StepRunning{}, err
				}
				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					spec,
					nil,
				)
				if err != nil {
					return StepRunning{}, fmt.Errorf("find or create container %q: %w", in.Handle, err)
				}

				stderr := new(bytes.Buffer)
				process, err := container.Run(in.Ctx,
					runtime.ProcessSpec{Path: "/bin/sh"},
					runtime.ProcessIO{Stderr: stderr},
				)
				if err != nil {
					return StepRunning{}, fmt.Errorf("run container %q: %w", in.Handle, err)
				}

				return StepRunning{
					Namespace: in.Namespace,
					Clientset: in.Clientset,
					Ctx:       in.Ctx,
					Handle:    in.Handle,
					Process:   process,
					Stderr:    stderr,
				}, nil
			},
		),

		// --- What happens to the step (StepRunning -> ProcessOutcome) ---

		// PE-10. Aborting a build must not leave the pod behind burning a node.
		brine.DefineMap[StepRunning, ProcessOutcome](
			"the build is cancelled while the step is waiting",
			func(in StepRunning, _ brine.Params, _ *brine.Recorder) (ProcessOutcome, error) {
				cancelCtx, cancel := context.WithCancel(in.Ctx)
				cancel()
				result, waitErr := in.Process.Wait(cancelCtx)
				return in.report(result, waitErr), nil
			},
		),

		// Real authorization failures exercise the initial-read retry boundary.
		brine.DefineMapUsing[WorkerReady, ProcessOutcome](
			"every read of pod {string} fails",
			[]string{"real-cluster"},
			func(in WorkerReady, p brine.Params, rec *brine.Recorder, res brine.Resources) (ProcessOutcome, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ProcessOutcome{}, fmt.Errorf("read fault requires a pod name")
				}
				cluster, ok := res.Get("real-cluster").(*realCluster)
				if !ok {
					return ProcessOutcome{}, fmt.Errorf("real-cluster resource is %T", res.Get("real-cluster"))
				}
				return observeInitialReadFailures(in, rec, cluster.RESTConfig, handle, 0)
			},
		),

		// --- Checks over ProcessOutcome ---

		// A step that failed has no exit status to compare, so "it failed
		// instead" is reported from the getter.
		CheckInt[ProcessOutcome]("the step comes back with exit status {int}",
			"the step's exit status",
			func(in ProcessOutcome) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("expected an exit status, the step failed with %q", in.Message)
				}
				return in.ExitStatus, nil
			}),

		// Anchor the count and classification: a substring check for "3
		// consecutive" would also accept a runtime that stopped after 13.
		Assert[ProcessOutcome]("the initial pod read exhausts {int} attempts",
			func(in ProcessOutcome, args Args) error {
				want := args.Int(0)
				if want <= 0 {
					return fmt.Errorf("expected a positive initial-read attempt count, got %d", want)
				}
				if in.Err == nil {
					return fmt.Errorf("expected the step to fail, it exited %d", in.ExitStatus)
				}
				prefix := fmt.Sprintf("%d consecutive API errors during initial sync:", want)
				if !strings.HasPrefix(in.Message, prefix) {
					return fmt.Errorf("expected the failure to start with %q, got %q", prefix, in.Message)
				}
				return nil
			}),

		CheckContains[ProcessOutcome]("the step fails saying {string}",
			"the failure",
			func(in ProcessOutcome) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("expected the step to fail, it exited %d", in.ExitStatus)
				}
				if in.runtimeTrace != nil {
					if err := in.runtimeTrace.requireRuntimeAttempts(in.Namespace, in.Handle, in.runtimeCreates, 1); err != nil {
						return "", err
					}
				}
				return in.Message, nil
			}),

		// Keeps its own body: the message caps the log it prints at 600
		// characters, and the generic one would dump the whole build log. A
		// trailing detail func does not help — detail is appended to the
		// failure, it does not shorten the value the comparison already
		// prints.
		CheckContains[ProcessOutcome]("the build log shows {string}",
			"the build log",
			func(in ProcessOutcome) (string, error) { return in.Stderr, nil }),

		// Names every pod it found, which is what tells a leak apart from a
		// pod the step never took away.
		CheckThat[ProcessOutcome]("the pod has been removed from the cluster",
			func(in ProcessOutcome) error {
				// The Delete request must already have happened. Kubernetes
				// removes a running sidecar asynchronously; a requested
				// deletion is not success until the API object is actually gone.
				ctx, cancel := context.WithTimeout(in.Ctx, 15*time.Second)
				defer cancel()
				var names []string
				remaining := func() error {
					return fmt.Errorf("expected no pods left in the cluster, found %s", strings.Join(names, ", "))
				}
				observedDeletion := false
				for {
					pods, err := in.Clientset.CoreV1().Pods(in.Namespace).List(ctx, metav1.ListOptions{})
					if err != nil {
						if ctx.Err() != nil && len(names) != 0 {
							return remaining()
						}
						return fmt.Errorf("list pods: %w", err)
					}
					if len(pods.Items) == 0 {
						return nil
					}
					names = names[:0]
					allDeleting := true
					for _, pod := range pods.Items {
						names = append(names, pod.Name)
						allDeleting = allDeleting && pod.DeletionTimestamp != nil
					}
					if !allDeleting {
						return remaining()
					}
					if !observedDeletion {
						for _, pod := range pods.Items {
							fmt.Printf("observed runtime-requested deletion: pod %s/%s UID %s at %s; awaiting actual removal\n",
								pod.Namespace, pod.Name, pod.UID, pod.DeletionTimestamp)
						}
						observedDeletion = true
					}
					select {
					case <-ctx.Done():
						return remaining()
					case <-time.After(100 * time.Millisecond):
					}
				}
			}),

		// --- Checks over ProcessOutcome ---

		// Keeps its own body: the counter is a float64, and comparing it as an
		// int would accept a fractional delta this equality rejects.
		Assert[ProcessOutcome](
			"the image pull failure count has gone up by {int}",
			func(in ProcessOutcome, args Args) error {
				want := args.Int(0)

				if in.ImagePullFailures != float64(want) {
					return fmt.Errorf("expected the image pull failure count to go up by %d, it went up by %v",
						want, in.ImagePullFailures)
				}
				return nil
			},
		),

		CheckString[ProcessOutcome]("the failure came from {string} execution",
			"the failure's execution path",
			func(in ProcessOutcome) (string, error) {
				if in.Err == nil {
					return "", errors.New("expected the step to fail, it succeeded")
				}
				if strings.Contains(in.Message, "waiting for pod running:") {
					return "production", nil
				}
				return "direct compatibility", nil
			}),
	}
}

// report packages what Wait returned together with the cluster it ran against,
// so a check can ask about the pod as well as the result.
func (in StepRunning) report(result runtime.ProcessResult, waitErr error) ProcessOutcome {
	message := errorMessage(waitErr)
	return ProcessOutcome{
		Namespace:  in.Namespace,
		Clientset:  in.Clientset,
		Ctx:        in.Ctx,
		Handle:     in.Handle,
		ExitStatus: result.ExitStatus,
		Err:        waitErr,
		Message:    message,
		Stderr:     in.Stderr.String(),
	}
}
