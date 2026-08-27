package steps

import (
	"context"
	"fmt"
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
