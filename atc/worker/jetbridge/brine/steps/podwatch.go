package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// PodWatchDefinitions migrates watch_test.go.
//
// The consumer of a PodWatcher calls Next() and expects to be told the pod's
// current state — and to keep being told it when the Kubernetes watch drops,
// reconnects, or cannot be re-established at all. A step that stops hearing
// about its pod hangs until it times out, so continuity IS the behavior.
//
// The doubles here are client-go's own: fake.Clientset with a real watch
// reactor, and watch.RaceFreeFake. Both are real implementations of the
// interfaces the runtime consumes, with deterministic delivery.

func PodWatchDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, WatchedPod](
			"a pod {string} that the runtime is watching",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (WatchedPod, error) {
				name, ok := p.GetString(0)
				if !ok {
					return WatchedPod{}, fmt.Errorf("expected a pod name parameter")
				}
				ctx := context.Background()
				clientset := fake.NewSimpleClientset()
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: name, Namespace: "test-namespace", ResourceVersion: "1",
					},
					Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "busybox"}}},
					Status: corev1.PodStatus{Phase: corev1.PodPending},
				}
				if _, err := clientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
					return WatchedPod{}, fmt.Errorf("create pod: %w", err)
				}
				return WatchedPod{
					Name: name, Clientset: clientset, Pod: pod, Ctx: ctx,
					Version: 1,
				}, nil
			},
		),

		// Installing a controllable watch has to happen before the watcher is
		// built, so these steps precede "the runtime starts watching".
		brine.DefineMap[WatchedPod, WatchedPod](
			"the connection to Kubernetes is steady",
			func(in WatchedPod, _ brine.Params, _ *brine.Recorder) (WatchedPod, error) {
				w := watch.NewRaceFreeFake()
				in.Feed = w
				in.Clientset.PrependWatchReactor("pods",
					func(k8stesting.Action) (bool, watch.Interface, error) { return true, w, nil })
				return in.start(), nil
			},
		),

		brine.DefineMap[WatchedPod, WatchedPod](
			"the connection to Kubernetes drops and comes back",
			func(in WatchedPod, _ brine.Params, _ *brine.Recorder) (WatchedPod, error) {
				first, second := watch.NewRaceFreeFake(), watch.NewRaceFreeFake()
				var calls int32
				in.Clientset.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
					if atomic.AddInt32(&calls, 1) == 1 {
						return true, first, nil
					}
					return true, second, nil
				})
				in.Feed, in.SecondFeed = first, second
				return in.start(), nil
			},
		),

		brine.DefineMap[WatchedPod, WatchedPod](
			"the connection to Kubernetes drops and cannot be re-established",
			func(in WatchedPod, _ brine.Params, _ *brine.Recorder) (WatchedPod, error) {
				first := watch.NewRaceFreeFake()
				var calls int32
				in.Clientset.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
					if atomic.AddInt32(&calls, 1) == 1 {
						return true, first, nil
					}
					return true, nil, errors.New("watch unavailable")
				})
				in.Feed = first
				return in.start(), nil
			},
		),

		brine.DefineMap[WatchedPod, WatchObservation](
			"the runtime asks what the pod is doing",
			func(in WatchedPod, _ brine.Params, _ *brine.Recorder) (WatchObservation, error) {
				return in.next(), nil
			},
		),

		brine.DefineMap[WatchObservation, WatchObservation](
			"the pod becomes {string}",
			func(in WatchObservation, p brine.Params, _ *brine.Recorder) (WatchObservation, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return WatchObservation{}, fmt.Errorf("expected a phase parameter")
				}
				w := in.Watched
				w.Version++
				w.Pod.ResourceVersion = fmt.Sprintf("%d", w.Version)
				w.Pod.Status.Phase = corev1.PodPhase(phase)
				if _, err := w.Clientset.CoreV1().Pods("test-namespace").
					UpdateStatus(w.Ctx, w.Pod, metav1.UpdateOptions{}); err != nil {
					return WatchObservation{}, fmt.Errorf("update pod: %w", err)
				}
				if w.Feed != nil {
					w.Feed.Modify(w.Pod.DeepCopy())
				}
				return w.next(), nil
			},
		),

		// The watch channel closing is the disruption a long-running step is
		// most likely to meet: an API server rollout, a network blip.
		brine.DefineMap[WatchObservation, WatchObservation](
			"the watch connection is interrupted and the pod becomes {string}",
			func(in WatchObservation, p brine.Params, _ *brine.Recorder) (WatchObservation, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return WatchObservation{}, fmt.Errorf("expected a phase parameter")
				}
				w := in.Watched
				if w.Feed != nil {
					w.Feed.Stop()
				}
				w.Version++
				w.Pod.ResourceVersion = fmt.Sprintf("%d", w.Version)
				w.Pod.Status.Phase = corev1.PodPhase(phase)
				if _, err := w.Clientset.CoreV1().Pods("test-namespace").
					UpdateStatus(w.Ctx, w.Pod, metav1.UpdateOptions{}); err != nil {
					return WatchObservation{}, fmt.Errorf("update pod: %w", err)
				}
				if w.SecondFeed != nil {
					// Delivered on the reconnected watch.
					go func(p *corev1.Pod) {
						time.Sleep(20 * time.Millisecond)
						w.SecondFeed.Modify(p)
					}(w.Pod.DeepCopy())
				}
				return w.next(), nil
			},
		),

		brine.DefineMap[WatchObservation, WatchObservation](
			"the pod is deleted out from under the step",
			func(in WatchObservation, _ brine.Params, _ *brine.Recorder) (WatchObservation, error) {
				w := in.Watched
				if w.Feed == nil {
					return WatchObservation{}, fmt.Errorf("this scenario needs a controllable watch")
				}
				w.Feed.Delete(w.Pod.DeepCopy())
				return w.next(), nil
			},
		),

		brine.DefineMap[WatchObservation, WatchObservation](
			"the build is cancelled",
			func(in WatchObservation, _ brine.Params, _ *brine.Recorder) (WatchObservation, error) {
				w := in.Watched
				ctx, cancel := context.WithCancel(w.Ctx)
				cancel()
				pod, err := w.Watcher.Next(ctx)
				msg := ""
				if err != nil {
					msg = err.Error()
				}
				return WatchObservation{Watched: w, Pod: pod, Err: err, Message: msg}, nil
			},
		),

		brine.DefineCheck[WatchObservation](
			"the runtime is told the pod is {string}",
			func(in WatchObservation, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a phase parameter")
				}
				if in.Err != nil {
					return fmt.Errorf("expected to be told the pod is %s, got error: %v", want, in.Err)
				}
				if in.Pod == nil {
					return fmt.Errorf("expected a pod, got nil")
				}
				if string(in.Pod.Status.Phase) != want {
					return fmt.Errorf("expected phase %q, got %q", want, in.Pod.Status.Phase)
				}
				return nil
			},
		),

		brine.DefineCheck[WatchObservation](
			"the runtime is told the pod was deleted",
			func(in WatchObservation, _ brine.Params, _ *brine.Recorder) error {
				if in.Err == nil {
					return fmt.Errorf("expected to be told the pod was deleted, got no error")
				}
				if !errors.Is(in.Err, jetbridge.ErrPodDeleted) {
					return fmt.Errorf("expected ErrPodDeleted, got %v", in.Err)
				}
				return nil
			},
		),

		brine.DefineCheck[WatchObservation](
			"the runtime is told to stop waiting",
			func(in WatchObservation, _ brine.Params, _ *brine.Recorder) error {
				if in.Err == nil {
					return fmt.Errorf("expected an error when the build was cancelled, got none")
				}
				if !strings.Contains(in.Message, "context canceled") {
					return fmt.Errorf("expected a cancellation error, got %q", in.Message)
				}
				return nil
			},
		),
	}
}

func (w WatchedPod) start() WatchedPod {
	w.Watcher = jetbridge.NewPodWatcher(w.Clientset, "test-namespace", w.Name)
	return w
}

func (w WatchedPod) next() WatchObservation {
	ctx, cancel := context.WithTimeout(w.Ctx, 5*time.Second)
	defer cancel()
	pod, err := w.Watcher.Next(ctx)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return WatchObservation{Watched: w, Pod: pod, Err: err, Message: msg}
}

// NoisyNeighbourDefinitions adds an unrelated pod to the namespace, in a
// different phase, so that a watch reporting the wrong pod is detectable.
func NoisyNeighbourDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[WatchedPod, WatchedPod](
			"another pod {string} is churning in the same namespace",
			func(in WatchedPod, p brine.Params, _ *brine.Recorder) (WatchedPod, error) {
				name, ok := p.GetString(0)
				if !ok {
					return WatchedPod{}, fmt.Errorf("expected a pod name parameter")
				}
				neighbour := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: name, Namespace: "test-namespace", ResourceVersion: "1",
					},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "busybox"}}},
					// A DIFFERENT phase from the watched pod, so a watch that
					// reported this one instead would be caught.
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				}
				if _, err := in.Clientset.CoreV1().Pods("test-namespace").
					Create(in.Ctx, neighbour, metav1.CreateOptions{}); err != nil {
					return WatchedPod{}, fmt.Errorf("create neighbour pod: %w", err)
				}
				return in, nil
			},
		),
	}
}
