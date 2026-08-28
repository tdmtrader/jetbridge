package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PW-03 against a REAL kube-apiserver.
//
// The fake-clientset version of this needed WatchBus — a hand-written
// reimplementation of API-server field-selector filtering, with buffering for
// events that arrive before the runtime establishes its lazy watch. About a
// hundred lines whose correctness rests on my model of the API.
//
// Here there is no double at all. The runtime asks for
// metadata.name=<pod> and the real API server enforces it, so a neighbour's
// event is filtered by the thing that filters it in production.

// RealWatch is a real cluster with two pods in it.
type RealWatch struct {
	Clientset kubernetes.Interface
	Namespace string
	Name      string
	Ctx       context.Context
	Watcher   *jetbridge.PodWatcher
	Observed  *corev1.Pod
	Err       error
}

func PodWatchRealDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, RealWatch](
			"a real cluster running pods {string} and {string}",
			[]string{"real-cluster"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (RealWatch, error) {
				mine, _ := p.GetString(0)
				theirs, ok := p.GetString(1)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected two pod names")
				}
				rc, ok := res.Get("real-cluster").(*realCluster)
				if !ok {
					return RealWatch{}, fmt.Errorf("real-cluster resource is %T", res.Get("real-cluster"))
				}

				ctx := context.Background()
				ns := fmt.Sprintf("pw03-%d", time.Now().UnixNano())
				if _, err := rc.Clientset.CoreV1().Namespaces().Create(ctx,
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
					metav1.CreateOptions{}); err != nil {
					return RealWatch{}, fmt.Errorf("create namespace: %w", err)
				}

				for _, name := range []string{mine, theirs} {
					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
						Spec: corev1.PodSpec{Containers: []corev1.Container{
							{Name: "main", Image: "busybox"},
						}},
					}
					if _, err := rc.Clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
						return RealWatch{}, fmt.Errorf("create pod %q: %w", name, err)
					}
				}

				return RealWatch{
					Clientset: rc.Clientset, Namespace: ns, Name: mine, Ctx: ctx,
					Watcher: jetbridge.NewPodWatcher(rc.Clientset, ns, mine),
				}, nil
			},
		),

		brine.DefineMap[RealWatch, RealWatch](
			"the runtime reads its pod, then {string} fails and {string} starts running",
			func(in RealWatch, p brine.Params, _ *brine.Recorder) (RealWatch, error) {
				theirs, _ := p.GetString(0)
				mine, ok := p.GetString(1)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected two pod names")
				}

				// First read: comes from Get, establishing lastResourceVersion.
				first, cancel := context.WithTimeout(in.Ctx, 20*time.Second)
				defer cancel()
				if _, err := in.Watcher.Next(first); err != nil {
					return RealWatch{}, fmt.Errorf("initial read: %w", err)
				}

				pods := in.Clientset.CoreV1().Pods(in.Namespace)
				setPhase := func(name string, phase corev1.PodPhase) error {
					pod, err := pods.Get(in.Ctx, name, metav1.GetOptions{})
					if err != nil {
						return fmt.Errorf("get %q: %w", name, err)
					}
					pod.Status.Phase = phase
					_, err = pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{})
					return err
				}
				// The neighbour changes FIRST. An unscoped watch delivers this
				// one before ours, and the step is told the wrong thing.
				if err := setPhase(theirs, corev1.PodFailed); err != nil {
					return RealWatch{}, err
				}
				if err := setPhase(mine, corev1.PodRunning); err != nil {
					return RealWatch{}, err
				}

				next, cancel2 := context.WithTimeout(in.Ctx, 20*time.Second)
				defer cancel2()
				pod, err := in.Watcher.Next(next)
				in.Observed, in.Err = pod, err
				return in, nil
			},
		),

		// Keeps its own body: it pins the pod's IDENTITY as well as its phase,
		// and the identity mismatch is the whole point of the scenario, so its
		// message says what an unscoped watch would do to the step.
		brine.DefineCheck[RealWatch](
			"the real API server told the runtime only about its own pod, now {string}",
			func(in RealWatch, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a phase parameter")
				}
				if in.Err != nil {
					return fmt.Errorf("expected to be told the pod is %s, got error: %v", want, in.Err)
				}
				if in.Observed == nil {
					return fmt.Errorf("expected a pod, got nil")
				}
				if in.Observed.Name != in.Name {
					return fmt.Errorf(
						"the runtime was told about pod %q, but it is watching %q — the watch is not "+
							"scoped, so a step acts on a phase belonging to somebody else's pod",
						in.Observed.Name, in.Name)
				}
				if string(in.Observed.Status.Phase) != want {
					return fmt.Errorf("expected phase %q, got %q", want, in.Observed.Status.Phase)
				}
				return nil
			},
		),
	}
}

// The rest of PW-01..PW-07 that a real API server can carry. What stays on the
// fake is exactly the set that needs a FORCED failure the real server will not
// produce on demand: a watch that drops and comes back, one that cannot be
// re-established, and a watch error arriving as a Status object.

func PodWatchRealExtraDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, RealWatch](
			"a real cluster running pod {string}",
			[]string{"real-cluster"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (RealWatch, error) {
				name, ok := p.GetString(0)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected a pod name parameter")
				}
				rc, ok := res.Get("real-cluster").(*realCluster)
				if !ok {
					return RealWatch{}, fmt.Errorf("real-cluster resource is %T", res.Get("real-cluster"))
				}
				ctx := context.Background()
				ns := fmt.Sprintf("pw-%d", time.Now().UnixNano())
				if _, err := rc.Clientset.CoreV1().Namespaces().Create(ctx,
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
					metav1.CreateOptions{}); err != nil {
					return RealWatch{}, fmt.Errorf("create namespace: %w", err)
				}
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
					Spec: corev1.PodSpec{Containers: []corev1.Container{
						{Name: "main", Image: "busybox"},
					}},
				}
				if _, err := rc.Clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
					return RealWatch{}, fmt.Errorf("create pod %q: %w", name, err)
				}
				return RealWatch{
					Clientset: rc.Clientset, Namespace: ns, Name: name, Ctx: ctx,
					Watcher: jetbridge.NewPodWatcher(rc.Clientset, ns, name),
				}, nil
			},
		),

		Refine[RealWatch]("the runtime asks the real cluster what its pod is doing",
			func(in RealWatch, _ Args) RealWatch {
				return in.next()
			}),

		brine.DefineMap[RealWatch, RealWatch](
			"the pod really becomes {string}",
			func(in RealWatch, p brine.Params, _ *brine.Recorder) (RealWatch, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected a phase parameter")
				}
				if err := in.setPhase(corev1.PodPhase(phase)); err != nil {
					return RealWatch{}, err
				}
				return in.next(), nil
			},
		),

		// A burst: both transitions land before the runtime reads, so a
		// runtime that settled on the first would be waiting on a state the
		// cluster has already left.
		brine.DefineMap[RealWatch, RealWatch](
			"the pod really goes {string} then {string} before the runtime looks",
			func(in RealWatch, p brine.Params, _ *brine.Recorder) (RealWatch, error) {
				first, _ := p.GetString(0)
				second, ok := p.GetString(1)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected two phases")
				}
				if err := in.setPhase(corev1.PodPhase(first)); err != nil {
					return RealWatch{}, err
				}
				if err := in.setPhase(corev1.PodPhase(second)); err != nil {
					return RealWatch{}, err
				}
				// Drain to the latest the cluster holds.
				out := in.next()
				for out.Err == nil && out.Observed != nil &&
					string(out.Observed.Status.Phase) != second {
					out = out.next()
				}
				return out, nil
			},
		),

		brine.DefineMap[RealWatch, RealWatch](
			"the pod is really deleted out from under the step",
			func(in RealWatch, _ brine.Params, _ *brine.Recorder) (RealWatch, error) {
				zero := int64(0)
				if err := in.Clientset.CoreV1().Pods(in.Namespace).Delete(in.Ctx, in.Name,
					metav1.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
					return RealWatch{}, fmt.Errorf("delete pod: %w", err)
				}
				// A REAL deletion is two events, not one: MODIFIED as the
				// deletionTimestamp is set, then DELETED. The fake clientset
				// emitted only the second, so the old scenario passed on a
				// single read. A real consumer keeps calling Next, and so does
				// this — the property under test is that the runtime EVENTUALLY
				// says the pod is gone rather than waiting on it forever, not
				// that it says so on the first read.
				out := in
				for i := 0; i < 10; i++ {
					out = out.next()
					if out.Err != nil {
						break
					}
				}
				return out, nil
			},
		),

		Refine[RealWatch]("the build is cancelled while the runtime waits on the real cluster",
			func(in RealWatch, _ Args) RealWatch {
				ctx, cancel := context.WithCancel(in.Ctx)
				type answer struct {
					pod *corev1.Pod
					err error
				}
				done := make(chan answer, 1)
				go func() {
					defer func() {
						if r := recover(); r != nil {
							done <- answer{nil, fmt.Errorf("the runtime panicked: %v", r)}
						}
					}()
					pod, err := in.Watcher.Next(ctx)
					done <- answer{pod, err}
				}()
				// Let it reach the blocking read, then pull the context.
				time.Sleep(200 * time.Millisecond)
				cancel()
				select {
				case a := <-done:
					in.Observed, in.Err = a.pod, a.err
				case <-time.After(10 * time.Second):
					in.Observed, in.Err = nil, fmt.Errorf(
						"the runtime was still waiting 10s after the build was cancelled")
				}
				return in
			}),

		CheckString[RealWatch]("the runtime is really told the pod is {string}",
			"the pod's phase",
			func(in RealWatch) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the runtime was told nothing about the pod: %v", in.Err)
				}
				if in.Observed == nil {
					return "", fmt.Errorf("expected a pod, got nil")
				}
				return string(in.Observed.Status.Phase), nil
			}),

		CheckThat[RealWatch]("the runtime is really told the pod was deleted",
			func(in RealWatch) error {
				if in.Err == nil {
					return fmt.Errorf(
						"expected to be told the pod was deleted; the step would wait on a pod that " +
							"no longer exists until its build times out")
				}
				if !errors.Is(in.Err, jetbridge.ErrPodDeleted) {
					return fmt.Errorf("expected ErrPodDeleted, got %v", in.Err)
				}
				return nil
			}),

		CheckThat[RealWatch]("the runtime really stops waiting",
			func(in RealWatch) error {
				if in.Err == nil {
					return fmt.Errorf("expected an error when the build was cancelled, got none")
				}
				if !strings.Contains(in.Err.Error(), "context canceled") {
					return fmt.Errorf("expected a cancellation error, got %q", in.Err.Error())
				}
				return nil
			}),
	}
}

func (w RealWatch) next() RealWatch {
	ctx, cancel := context.WithTimeout(w.Ctx, 20*time.Second)
	defer cancel()
	pod, err := w.Watcher.Next(ctx)
	w.Observed, w.Err = pod, err
	return w
}

func (w RealWatch) setPhase(phase corev1.PodPhase) error {
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	pod, err := pods.Get(w.Ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get pod: %w", err)
	}
	pod.Status.Phase = phase
	if _, err := pods.UpdateStatus(w.Ctx, pod, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update pod status: %w", err)
	}
	return nil
}
