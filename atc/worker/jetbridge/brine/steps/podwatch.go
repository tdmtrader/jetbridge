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
		Refine[WatchedPod]("the connection to Kubernetes is steady",
			func(in WatchedPod, _ Args) WatchedPod {
				w := watch.NewRaceFreeFake()
				in.Feed = w
				in.Clientset.PrependWatchReactor("pods",
					func(k8stesting.Action) (bool, watch.Interface, error) { return true, w, nil })
				return in.start()
			}),

		Refine[WatchedPod]("the connection to Kubernetes drops and comes back",
			func(in WatchedPod, _ Args) WatchedPod {
				first, second := watch.NewRaceFreeFake(), watch.NewRaceFreeFake()
				var calls int32
				in.Clientset.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
					if atomic.AddInt32(&calls, 1) == 1 {
						return true, first, nil
					}
					return true, second, nil
				})
				in.Feed, in.SecondFeed = first, second
				return in.start()
			}),

		Refine[WatchedPod]("the connection to Kubernetes drops and cannot be re-established",
			func(in WatchedPod, _ Args) WatchedPod {
				first := watch.NewRaceFreeFake()
				var calls int32
				in.Clientset.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
					if atomic.AddInt32(&calls, 1) == 1 {
						return true, first, nil
					}
					return true, nil, errors.New("watch unavailable")
				})
				in.Feed = first
				return in.start()
			}),

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

		// Being told an error, or nothing at all, is not being told a phase:
		// both are reported from the getter, which is where the sentence's
		// presumption fails.
		CheckString[WatchObservation]("the runtime is told the pod is {string}",
			"the pod's phase",
			func(in WatchObservation) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("expected to be told the pod's phase, got error: %v", in.Err)
				}
				if in.Pod == nil {
					return "", fmt.Errorf("expected a pod, got nil")
				}
				return string(in.Pod.Status.Phase), nil
			}),

		CheckThat[WatchObservation]("the runtime is told to stop waiting",
			func(in WatchObservation) error {
				if in.Err == nil {
					return fmt.Errorf("expected an error when the build was cancelled, got none")
				}
				if !strings.Contains(in.Message, "context canceled") {
					return fmt.Errorf("expected a cancellation error, got %q", in.Message)
				}
				return nil
			}),
	}
}

func (w WatchedPod) start() WatchedPod {
	w.Watcher = jetbridge.NewPodWatcher(w.Clientset, "test-namespace", w.Name)
	return w
}

func (w WatchedPod) next() WatchObservation {
	ctx, cancel := context.WithTimeout(w.Ctx, 5*time.Second)
	defer cancel()
	return w.nextWith(ctx)
}

// nextWith reports a panic inside the runtime as an ordinary failure.
//
// A panic here is a real defect — a watch error arrives as a Status object,
// and a runtime that mistakes it for a pod dereferences nil and takes the ATC
// down with it. But letting the panic escape kills the adapter process, and
// brine is then left waiting on a runner that will never answer: the run hangs
// for its full timeout and reports nothing about why. Catching it turns the
// same defect into a named failure in under a second.
func (w WatchedPod) nextWith(ctx context.Context) (obs WatchObservation) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("the runtime panicked while reading the watch: %v", r)
			obs = WatchObservation{Watched: w, Err: err, Message: err.Error()}
		}
	}()
	pod, err := w.Watcher.Next(ctx)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return WatchObservation{Watched: w, Pod: pod, Err: err, Message: msg}
}
