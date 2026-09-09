package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
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
// It is a separate state from StepOutcome — which carries no exit status and
// no cluster — because half of what this family asserts is "exit 0 and the pod
// is gone", and a state that cannot express that would push the assertion back
// onto the double.
type ProcessOutcome struct {
	Namespace         string
	Clientset         *fake.Clientset
	Ctx               context.Context
	Handle            string
	ExitStatus        int
	Err               error
	Message           string
	Stderr            string
	ImagePullFailures float64
}

// severingExecutor is a PodExecutor whose behavioral difference from the real
// SPDY one is named and narrow: the exec connection dies, and it kills the pod
// on its way out. That is a real adapter in PHILOSOPHY.md's sense — the thing
// it does differently (sever, rather than stream) is the thing the scenarios
// need, and it is not simulating the behavior under test. What is under test is
// what the RUNTIME does once an exec has been severed: fetch the pod, work out
// why, and put that in the build log.
//
// It records nothing, so there is nothing to assert on but the log.
type severingExecutor struct {
	clientset *fake.Clientset
	sever     func(ctx context.Context, clientset *fake.Clientset, namespace, podName string) error
}

func (e severingExecutor) ExecInPod(
	ctx context.Context,
	namespace, podName, _ string,
	_ []string,
	stdin io.Reader,
	_, _ io.Writer,
	_ bool,
	_ jetbridge.ExecAttrs,
) error {
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	return e.sever(ctx, e.clientset, namespace, podName)
}

func ProcessDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// --- Workers whose configuration the scenarios need ---

		// Both callers of the shared diagnostics remain covered. Production
		// uses execProcess; direct compatibility retains the legacy Wait route.
		TransformUsing[brine.Empty, ClusterReady](
			"a jetbridge worker using {string} execution",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, a Args, res brine.Resources) (ClusterReady, error) {
				var opts []ClusterOption
				switch a.String(0) {
				case "production":
					opts = append(opts, WithExecutor(localExecutor{}))
				case "direct compatibility":
				default:
					return ClusterReady{}, fmt.Errorf("unknown execution mode %q", a.String(0))
				}
				cluster, err := NewCluster(res, opts...)
				return cluster.Ready(), err
			},
		),

		// The default startup timeout is five minutes. A scenario that waits
		// on it does not fail, it hangs, so the timeout scenarios get an
		// impatient worker — the same move process_test.go makes. Deliberately
		// distinct from "a jetbridge worker that waits only seconds for a pod
		// to be scheduled": that one is patient enough (3s) for scheduling
		// scenarios, this one is as impatient as the runtime allows.
		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker that gives a pod only a moment to start",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				ready, err := newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.PodStartupTimeout = 200 * time.Millisecond
					cfg.PodSchedulingTimeout = 200 * time.Millisecond
				})
				if err != nil {
					return ClusterReady{}, err
				}
				// The startup deadline is only enforced on the exec path.
				ready.Worker.SetExecutor(localExecutor{})
				return ready, nil
			},
		),
		// RF-15. The pod dies underneath the exec, and the exec reports the
		// severed connection rather than the death. Only the runtime can join
		// the two up for the user.
		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker whose exec is severed by an out-of-memory kill",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				return newSeveringWorker(res, func(ctx context.Context, clientset *fake.Clientset, namespace, podName string) error {
					if err := updateTaskPodStatus(ctx, clientset, namespace, podName, func(pod *corev1.Pod) {
						pod.Status.Phase = corev1.PodFailed
						pod.Status.ContainerStatuses = []corev1.ContainerStatus{terminatedStatus("main", corev1.ContainerStateTerminated{
							ExitCode: 137, Reason: "OOMKilled",
						})}
					}); err != nil {
						return err
					}
					return errors.New("exec stream: unable to upgrade connection: container not found")
				})
			},
		),

		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker whose exec is severed after the pod disappears",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				return newSeveringWorker(res, func(ctx context.Context, clientset *fake.Clientset, namespace, podName string) error {
					if err := clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{}); err != nil {
						return fmt.Errorf("delete pod %q: %w", podName, err)
					}
					return errors.New("exec stream: connection refused")
				})
			},
		),

		// --- Nodes the diagnostics read back (RF-11) ---

		Transform[ClusterReady, ClusterReady](
			"the cluster has a spot node {string} that is short of disk",
			func(in ClusterReady, a Args) (ClusterReady, error) {
				name := a.String(0)

				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   name,
						Labels: map[string]string{"cloud.google.com/gke-spot": "true"},
					},
					Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Message: "disk usage exceeds threshold"},
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					}},
				}
				if _, err := in.Clientset.CoreV1().Nodes().Create(in.Ctx, node, metav1.CreateOptions{}); err != nil {
					return ClusterReady{}, fmt.Errorf("create node %q: %w", name, err)
				}
				return in, nil
			},
		),

		Transform[ClusterReady, ClusterReady](
			"the cluster has a cordoned node {string}",
			func(in ClusterReady, a Args) (ClusterReady, error) {
				name := a.String(0)

				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Spec:       corev1.NodeSpec{Unschedulable: true},
					Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					}},
				}
				if _, err := in.Clientset.CoreV1().Nodes().Create(in.Ctx, node, metav1.CreateOptions{}); err != nil {
					return ClusterReady{}, fmt.Errorf("create node %q: %w", name, err)
				}
				return in, nil
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
				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       in.Dir,
						ImageSpec: runtime.ImageSpec{ImageURL: in.ImageURL, Privileged: in.Privileged},
						Sidecars:  in.Sidecars,
					},
					&noopDelegate{},
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

		// PE-09. The exit code is the step's result; the phase follows from it
		// the way the kubelet sets it.
		Transform[StepRunning, ProcessOutcome](
			"the pod ends with the main container exiting {int}",
			func(in StepRunning, a Args) (ProcessOutcome, error) {
				code := a.Int(0)

				phase := corev1.PodSucceeded
				if code != 0 {
					phase = corev1.PodFailed
				}
				return in.settleProcess(func(pod *corev1.Pod) {
					pod.Status.Phase = phase
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{terminatedStatus("main", corev1.ContainerStateTerminated{
						ExitCode: int32(code),
					})}
				})
			},
		),

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

		// SC-10. The pod outlives the main container while sidecars run, so
		// the runtime has to take it away or the sidecars run forever.
		Transform[StepRunning, ProcessOutcome](
			"the main container exits {int} while the sidecar {string} keeps running",
			func(in StepRunning, a Args) (ProcessOutcome, error) {
				return in.settleProcess(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodRunning
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{
						terminatedStatus("main", corev1.ContainerStateTerminated{
							ExitCode: int32(a.Int(0)),
						}),
						runningStatus(a.String(1)),
					}
				})
			},
		),

		// SC-08. A sidecar that cannot start blocks the step forever unless
		// the runtime fails on its behalf.
		Transform[StepRunning, ProcessOutcome](
			"the sidecar {string} cannot pull the image {string} while the main container is still being created",
			func(in StepRunning, a Args) (ProcessOutcome, error) {
				return in.settleProcess(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodPending
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{
						waitingStatus("main", corev1.ContainerStateWaiting{
							Reason: "ContainerCreating", Message: "waiting for container",
						}),
						waitingStatus(a.String(0), corev1.ContainerStateWaiting{
							Reason: "ImagePullBackOff", Message: fmt.Sprintf("Back-off pulling image %q", a.String(1)),
						}),
					}
				})
			},
		),

		// SC-09. The mirror image of SC-08: once the step's own command has
		// finished, a broken sidecar is not the user's problem and must not
		// turn a green build red.
		Transform[StepRunning, ProcessOutcome](
			"the sidecar {string} cannot pull the image {string} after the main container exited {int}",
			func(in StepRunning, a Args) (ProcessOutcome, error) {
				return in.settleProcess(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodRunning
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{
						terminatedStatus("main", corev1.ContainerStateTerminated{
							ExitCode: int32(a.Int(2)),
						}),
						waitingStatus(a.String(0), corev1.ContainerStateWaiting{
							Reason: "ImagePullBackOff", Message: fmt.Sprintf("Back-off pulling image %q", a.String(1)),
						}),
					}
				})
			},
		),

		// RF-10. The image name is the one thing a user needs off this failure,
		// and the scheduling condition is what tells them the cluster was fine.
		Transform[StepRunning, ProcessOutcome](
			"the main container cannot pull the image {string} after being scheduled onto {string}",
			func(in StepRunning, a Args) (ProcessOutcome, error) {
				return in.settleProcess(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodPending
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{waitingStatus("main", corev1.ContainerStateWaiting{
						Reason: "ImagePullBackOff", Message: fmt.Sprintf("Back-off pulling image %q", a.String(0)),
					})}
					pod.Status.Conditions = []corev1.PodCondition{{
						Type:    corev1.PodScheduled,
						Status:  corev1.ConditionTrue,
						Reason:  "Scheduled",
						Message: fmt.Sprintf("Successfully assigned %s/%s to %s", pod.Namespace, pod.Name, a.String(1)),
					}}
				})
			},
		),

		// RF-10/RF-11. Eviction is the cluster's decision, not the pipeline's,
		// and the node is the only place the user can go to check.
		//
		// On the production path the node has to keep deciding it: a pause
		// pod that dies before the command runs is replaced exactly once, so
		// a single eviction never reaches the build. The replacement meets the
		// same node, and the SECOND eviction is the one the diagnostics
		// describe. Direct compatibility never creates a replacement.
		Transform[StepRunning, ProcessOutcome](
			"the node {string} evicts the pod for running out of {string}",
			func(in StepRunning, a Args) (ProcessOutcome, error) {
				return in.settleProcessOnEveryPod(func(pod *corev1.Pod) {
					pod.Spec.NodeName = a.String(0)
					pod.Status.Phase = corev1.PodFailed
					pod.Status.Reason = "Evicted"
					pod.Status.Message = "The node was low on resource: " + a.String(1) + "."
				})
			},
		),

		// RF-10. A container that has been OOM-killed twice is a memory-limit
		// problem, and the restart history is how the user tells that from a
		// one-off. A Failed pause pod is replaced once before the step gives
		// up, so the limit has to be too low for the replacement as well.
		Transform[StepRunning, ProcessOutcome](
			"the main container on node {string} is killed twice for exceeding {string}",
			func(in StepRunning, a Args) (ProcessOutcome, error) {
				return in.settleProcessOnEveryPod(func(pod *corev1.Pod) {
					pod.Spec.NodeName = a.String(0)
					pod.Status.Phase = corev1.PodFailed
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
						Name:         "main",
						RestartCount: 2,
						State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 137, Reason: "OOMKilled",
							Message: "container exceeded " + a.String(1) + " memory limit",
						}},
						LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 137, Reason: "OOMKilled",
						}},
					}}
				})
			},
		),

		// RF-08. The pod is scheduled and simply never comes up. Without a
		// deadline the step waits forever; without diagnostics the user has no
		// idea what it was waiting for.
		brine.DefineMap[StepRunning, ProcessOutcome](
			"the pod is scheduled but never reaches Running",
			func(in StepRunning, _ brine.Params, _ *brine.Recorder) (ProcessOutcome, error) {
				return in.settleProcess(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodPending
					pod.Status.Conditions = []corev1.PodCondition{{
						Type: corev1.PodScheduled, Status: corev1.ConditionTrue, Reason: "Scheduled",
					}}
				})
			},
		),

		// RF-15. The pod is up, the exec goes in, and the severing executor
		// decides how it comes back out.
		Transform[StepRunning, ProcessOutcome](
			"the pod reaches Running on node {string} and the step execs into it",
			func(in StepRunning, a Args) (ProcessOutcome, error) {
				return in.settleProcess(func(pod *corev1.Pod) {
					pod.Spec.NodeName = a.String(0)
					pod.Status.Phase = corev1.PodRunning
				})
			},
		),

		// RF-12. A single dropped API call is weather, not a build failure.
		Transform[StepRunning, ProcessOutcome](
			"the pod succeeds but the next {int} status reads fail",
			func(in StepRunning, a Args) (ProcessOutcome, error) {
				if err := in.mutatePod(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodSucceeded
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{terminatedStatus("main", corev1.ContainerStateTerminated{ExitCode: 0})}
				}); err != nil {
					return ProcessOutcome{}, err
				}

				// Installed only after the pod is already complete, so the
				// scenario is about surviving the errors rather than about
				// what the pod did while they happened.
				var seen int32
				in.Clientset.PrependReactor("get", "pods",
					func(k8stesting.Action) (bool, apiruntime.Object, error) {
						if atomic.AddInt32(&seen, 1) <= int32(a.Int(0)) {
							return true, nil, errors.New("transient API error")
						}
						return false, nil, nil
					})

				result, waitErr := in.Process.Wait(in.Ctx)
				return in.report(result, waitErr), nil
			},
		),

		// RF-13. A cluster that has genuinely gone away must not leave the
		// build hanging on it.
		brine.DefineMap[StepRunning, ProcessOutcome](
			"every read of the pod status fails",
			func(in StepRunning, _ brine.Params, _ *brine.Recorder) (ProcessOutcome, error) {
				in.Clientset.PrependReactor("get", "pods",
					func(k8stesting.Action) (bool, apiruntime.Object, error) {
						return true, nil, errors.New("persistent API error")
					})
				result, waitErr := in.Process.Wait(in.Ctx)
				return in.report(result, waitErr), nil
			},
		),

		// --- What the operator's dashboard sees (StepRunning -> ProcessOutcome) ---

		brine.DefineMap[StepRunning, ProcessOutcome](
			"the image cannot be pulled, with the failure counters read either side",
			func(in StepRunning, _ brine.Params, _ *brine.Recorder) (ProcessOutcome, error) {
				metric.Metrics.K8sImagePullFailures.Delta()

				if err := in.mutatePod(func(pod *corev1.Pod) {
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{waitingStatus("main", corev1.ContainerStateWaiting{
						Reason: "ImagePullBackOff", Message: "Back-off pulling image",
					})}
				}); err != nil {
					return ProcessOutcome{}, err
				}

				result, waitErr := in.Process.Wait(in.Ctx)
				out := in.report(result, waitErr)
				out.ImagePullFailures = metric.Metrics.K8sImagePullFailures.Delta()
				return out, nil
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

		CheckContains[ProcessOutcome]("the step fails saying {string}",
			"the failure",
			func(in ProcessOutcome) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("expected the step to fail, it exited %d", in.ExitStatus)
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
				pods, err := in.Clientset.CoreV1().Pods(in.Namespace).List(in.Ctx, metav1.ListOptions{})
				if err != nil {
					return fmt.Errorf("list pods: %w", err)
				}
				if len(pods.Items) != 0 {
					names := make([]string, 0, len(pods.Items))
					for _, pod := range pods.Items {
						names = append(names, pod.Name)
					}
					return fmt.Errorf("expected no pods left in the cluster, found %s", strings.Join(names, ", "))
				}
				return nil
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

// newSeveringWorker builds a worker whose exec transport dies in the given way.
func newSeveringWorker(
	res brine.Resources,
	sever func(context.Context, *fake.Clientset, string, string) error,
) (ClusterReady, error) {
	// Short deadlines: a severed dial now costs one pause-pod replacement,
	// and the replacement never reaches Running on a fake cluster, so the
	// scenario would otherwise sit out the production startup timeout before
	// reporting the death the diagnostics are about.
	ready, err := newConfiguredWorker(res, func(cfg *jetbridge.Config) {
		cfg.PodStartupTimeout = 200 * time.Millisecond
		cfg.PodSchedulingTimeout = 200 * time.Millisecond
	})
	if err != nil {
		return ClusterReady{}, err
	}
	ready.Worker.SetExecutor(severingExecutor{clientset: ready.Clientset, sever: sever})
	return ready, nil
}

// mutatePod applies a status mutation and pushes it back to the cluster.
func (in StepRunning) mutatePod(mutate func(*corev1.Pod)) error {
	return updateTaskPodStatus(in.Ctx, in.Clientset, in.Namespace, in.Handle, mutate)
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

// settleProcess is settlePod's counterpart for this family: same shape, but it
// keeps the exit status and the cluster rather than only the error.
func (in StepRunning) settleProcess(mutate func(*corev1.Pod)) (ProcessOutcome, error) {
	if err := in.mutatePod(mutate); err != nil {
		return ProcessOutcome{}, err
	}
	result, waitErr := in.Process.Wait(in.Ctx)
	return in.report(result, waitErr), nil
}

// settleProcessOnEveryPod is settleProcess for a fate the cluster keeps
// handing out. The runtime replaces a pause pod that dies before the step's
// command runs — once, whatever the phase (recreatePausePod) — and on a fake
// cluster the replacement would otherwise sit unstarted until the startup
// timeout. So the mutation is applied to the pod that exists now AND to every
// pod created afterwards; the replacement dies the same way, the one
// replacement is spent, and the classification the scenario asserts stands.
// The legacy Wait route never creates a pod, so there the reactor is inert.
func (in StepRunning) settleProcessOnEveryPod(mutate func(*corev1.Pod)) (ProcessOutcome, error) {
	in.Clientset.PrependReactor("create", "pods",
		func(action k8stesting.Action) (bool, apiruntime.Object, error) {
			if pod, ok := action.(k8stesting.CreateActionImpl).GetObject().(*corev1.Pod); ok {
				mutate(pod)
			}
			return false, nil, nil
		})
	return in.settleProcess(mutate)
}
