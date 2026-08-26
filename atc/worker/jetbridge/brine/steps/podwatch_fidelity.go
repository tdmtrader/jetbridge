package steps

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	k8stesting "k8s.io/client-go/testing"
)

// PodWatchFidelityDefinitions closes PW-03, which pod-watch.feature carried as
// an explicit hole: nothing observed that the watch is scoped to one pod, or
// that a reconnect resumes from the resource version it last saw.
//
// watch_test.go covered both by reading the ListOptions the runtime passed —
// a spy assertion. The conversion is Addendum 2's: replace the recording
// double with a WORKING one and assert the round trip. These connections do
// what the API server does with those options — filter a namespace-wide
// stream by field selector, and replay only what happened after a given
// resource version — so the step's own observations go wrong when the runtime
// stops supplying them.
//
// Nothing here asserts on the options. It consumes them.

// WatchBus is a namespace-wide event stream that filters per watcher, the way
// an API server does. Filtering happens at DELIVERY, not at push: the runtime
// establishes its watch lazily, so at the moment a neighbouring pod changes
// there may be no watcher and no selector yet. Events that predate the watch
// are held and flushed through the filter when it connects.
type WatchBus struct {
	mu       sync.Mutex
	selector fields.Selector
	live     *watch.RaceFreeFakeWatcher
	pending  []*corev1.Pod
}

func (b *WatchBus) connect(r k8stesting.WatchRestrictions) *watch.RaceFreeFakeWatcher {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selector = r.Fields
	b.live = watch.NewRaceFreeFake()
	for _, pod := range b.pending {
		if b.matches(pod) {
			b.live.Modify(pod)
		}
	}
	b.pending = nil
	return b.live
}

func (b *WatchBus) deliver(pod *corev1.Pod) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.live == nil {
		b.pending = append(b.pending, pod)
		return
	}
	if b.matches(pod) {
		b.live.Modify(pod)
	}
}

// matches is the API server's question: is this watcher entitled to see this
// pod? An absent or empty selector means the watcher asked for everything.
func (b *WatchBus) matches(pod *corev1.Pod) bool {
	if b.selector == nil || b.selector.Empty() {
		return true
	}
	return b.selector.Matches(fields.Set{"metadata.name": pod.Name})
}

// WatchReplay holds what a disconnected watcher missed, and hands it back only
// to a reconnect that names the version it last saw.
type WatchReplay struct {
	mu     sync.Mutex
	missed []*corev1.Pod
}

func (r *WatchReplay) missWhileDown(pod *corev1.Pod) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missed = append(r.missed, pod)
}

func (r *WatchReplay) sinceVersion(rv string) []*corev1.Pod {
	r.mu.Lock()
	defer r.mu.Unlock()
	// An empty resource version means "start from now" — whatever landed
	// during the gap is gone, exactly as against a real API server.
	if rv == "" {
		return nil
	}
	return r.missed
}

func PodWatchFidelityDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// PW-03. A namespace with more than one pod in it, and a connection
		// that honours whatever scoping the runtime asks for.
		brine.DefineMap[WatchedPod, WatchedPod](
			"the connection to Kubernetes carries every pod in the namespace",
			func(in WatchedPod, _ brine.Params, _ *brine.Recorder) (WatchedPod, error) {
				bus := &WatchBus{}
				in.Bus = bus
				in.Clientset.PrependWatchReactor("pods",
					func(a k8stesting.Action) (bool, watch.Interface, error) {
						var restrictions k8stesting.WatchRestrictions
						if wa, ok := a.(k8stesting.WatchAction); ok {
							restrictions = wa.GetWatchRestrictions()
						}
						return true, bus.connect(restrictions), nil
					})
				return in.start(), nil
			},
		),

		brine.DefineMap[WatchObservation, WatchObservation](
			"another pod {string} in the namespace reports {string}",
			func(in WatchObservation, p brine.Params, _ *brine.Recorder) (WatchObservation, error) {
				name, _ := p.GetString(0)
				phase, ok := p.GetString(1)
				if !ok {
					return WatchObservation{}, fmt.Errorf("expected a pod name and a phase")
				}
				w := in.Watched
				if w.Bus == nil {
					return WatchObservation{}, fmt.Errorf("this scenario needs a namespace-wide connection")
				}
				neighbour := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: name, Namespace: "test-namespace", ResourceVersion: "99",
					},
					Status: corev1.PodStatus{Phase: corev1.PodPhase(phase)},
				}
				if _, err := w.Clientset.CoreV1().Pods("test-namespace").
					Create(w.Ctx, neighbour, metav1.CreateOptions{}); err != nil {
					return WatchObservation{}, fmt.Errorf("create neighbour pod: %w", err)
				}
				w.Bus.deliver(neighbour)
				// Deliberately does not advance the observation: the next step
				// pushes this pod's own event and reads once, so whichever
				// event survived the filter first is what the step is told.
				return in, nil
			},
		),

		// The resource-version half. The reconnect gets a watcher whose
		// contents depend on the version the runtime supplies.
		brine.DefineMap[WatchedPod, WatchedPod](
			"the connection replays only what happened after the version it is given",
			func(in WatchedPod, _ brine.Params, _ *brine.Recorder) (WatchedPod, error) {
				first := watch.NewRaceFreeFake()
				replay := &WatchReplay{}
				in.Replay = replay
				in.Feed = first
				var calls int32
				in.Clientset.PrependWatchReactor("pods",
					func(a k8stesting.Action) (bool, watch.Interface, error) {
						if atomic.AddInt32(&calls, 1) == 1 {
							return true, first, nil
						}
						rv := ""
						if wa, ok := a.(k8stesting.WatchAction); ok {
							rv = wa.GetWatchRestrictions().ResourceVersion
						}
						resumed := watch.NewRaceFreeFake()
						for _, pod := range replay.sinceVersion(rv) {
							resumed.Modify(pod)
						}
						return true, resumed, nil
					})
				return in.start(), nil
			},
		),

		// The existing cancellation scenario cancels BEFORE asking, which the
		// non-blocking check at the top of Next's loop catches. This one
		// cancels while the runtime is already blocked on the watch channel —
		// the only path the inner select guards. A deletion probe removed that
		// inner case and both suites stayed green, because neither ever had a
		// watcher waiting when the cancellation arrived. The consequence is a
		// build that ignores cancellation and hangs until its timeout.
		brine.DefineMap[WatchObservation, WatchObservation](
			"the build is cancelled while the runtime is still waiting",
			func(in WatchObservation, _ brine.Params, _ *brine.Recorder) (WatchObservation, error) {
				w := in.Watched
				ctx, cancel := context.WithCancel(w.Ctx)
				defer cancel()

				type answer struct {
					pod *corev1.Pod
					err error
				}
				done := make(chan answer, 1)
				go func() {
					defer func() {
						if r := recover(); r != nil {
							done <- answer{nil, fmt.Errorf(
								"the runtime panicked while waiting: %v", r)}
						}
					}()
					pod, err := w.Watcher.Next(ctx)
					done <- answer{pod, err}
				}()

				// Give the watcher time to establish its watch and block on an
				// empty channel, which is the state under test.
				time.Sleep(100 * time.Millisecond)
				cancel()

				select {
				case a := <-done:
					msg := ""
					if a.err != nil {
						msg = a.err.Error()
					}
					return WatchObservation{Watched: w, Pod: a.pod, Err: a.err, Message: msg}, nil
				case <-time.After(3 * time.Second):
					err := fmt.Errorf(
						"the runtime was still waiting 3s after the build was cancelled — " +
							"a step in this state hangs until its build timeout")
					return WatchObservation{Watched: w, Err: err, Message: err.Error()}, nil
				}
			},
		),

		// A watch error arrives as a metav1.Status, not a pod — "too old
		// resource version" is the routine one after an API server rollout.
		// The runtime has to step over it. Treating it as a pod yields a nil
		// pointer that the very next line dereferences, which takes down the
		// ATC rather than the build.
		brine.DefineMap[WatchObservation, WatchObservation](
			"the watch reports an error instead of a pod",
			func(in WatchObservation, _ brine.Params, _ *brine.Recorder) (WatchObservation, error) {
				w := in.Watched
				if w.Feed == nil {
					return WatchObservation{}, fmt.Errorf("this scenario needs a controllable watch")
				}
				w.Feed.Error(&metav1.Status{
					Status:  metav1.StatusFailure,
					Reason:  metav1.StatusReasonGone,
					Message: "too old resource version",
				})
				// Does not advance: the next step pushes a real pod event and
				// reads once, so a runtime that mistook this for a pod is
				// caught on the read.
				return in, nil
			},
		),

		brine.DefineMap[WatchObservation, WatchObservation](
			"the pod finishes while the connection is down",
			func(in WatchObservation, _ brine.Params, _ *brine.Recorder) (WatchObservation, error) {
				w := in.Watched
				if w.Replay == nil || w.Feed == nil {
					return WatchObservation{}, fmt.Errorf("this scenario needs a replaying connection")
				}
				w.Version++
				w.Pod.ResourceVersion = fmt.Sprintf("%d", w.Version)
				w.Pod.Status.Phase = corev1.PodSucceeded
				if _, err := w.Clientset.CoreV1().Pods("test-namespace").
					UpdateStatus(w.Ctx, w.Pod, metav1.UpdateOptions{}); err != nil {
					return WatchObservation{}, fmt.Errorf("update pod: %w", err)
				}
				// Recorded before the drop, so the reconnect can find it.
				w.Replay.missWhileDown(w.Pod.DeepCopy())
				w.Feed.Stop()
				return w.next(), nil
			},
		),
	}
}
