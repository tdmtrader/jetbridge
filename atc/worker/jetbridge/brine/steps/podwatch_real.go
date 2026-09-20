package steps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
)

// PW-03 uses real kubelet transitions in an owned live namespace.
// Other watch fixtures below still use an owned API-only control plane.

// RealWatch owns a watch route to the real API and its scenario's pod(s).
type RealWatch struct {
	Clientset    kubernetes.Interface
	Namespace    string
	Name         string
	Ctx          context.Context
	Watcher      *jetbridge.PodWatcher
	Observed     *corev1.Pod
	Err          error
	route        net.Listener
	target       string
	config       *rest.Config
	directConfig *rest.Config
	access       *podAccess
}

func PodWatchRealDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, RealWatch](
			"a Kubernetes cluster with gated pods {string} and {string}",
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder) (RealWatch, error) {
				mine, mineOK := p.GetString(0)
				theirs, theirsOK := p.GetString(1)
				if !mineOK || !theirsOK {
					return RealWatch{}, fmt.Errorf("expected two pod names")
				}
				return newLiveScopedWatch(rec, mine, theirs)
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

				return in.observeLiveScopedTransitions(mine, theirs)
			},
		),
	}
}

// Real API state changes, stream interruption/replay and cancellation.
// Expiry observes history aging out of the live control plane.

func PodWatchRealExtraDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, RealWatch](
			"a live cluster with gated pod {string}",
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder) (RealWatch, error) {
				name, ok := p.GetString(0)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected a pod name")
				}
				return newLiveGatedWatch(rec, name)
			},
		),

		brine.DefineMapUsing[brine.Empty, RealWatch](
			"a Kubernetes API with a Pending pod {string}",
			[]string{"real-cluster"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (RealWatch, error) {
				name, ok := p.GetString(0)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected a pod name")
				}
				return newRealWatch(res, rec, name)
			},
		),

		brine.DefineMap[RealWatch, RealWatch](
			"the runtime has revocable permission to watch its pod",
			func(in RealWatch, _ brine.Params, rec *brine.Recorder) (RealWatch, error) {
				return in.withRevocableWatch(rec)
			},
		),

		brine.DefineMap[RealWatch, RealWatch](
			"the watch connection drops after its watch permission is revoked",
			func(in RealWatch, _ brine.Params, rec *brine.Recorder) (RealWatch, error) {
				return in.dropRevokedWatch(rec)
			},
		),

		Refine[RealWatch]("the runtime asks what its pod is doing",
			func(in RealWatch, _ Args) RealWatch {
				return in.next()
			}),

		brine.DefineMap[RealWatch, RealWatch](
			"the watched pod becomes {string}",
			func(in RealWatch, p brine.Params, _ *brine.Recorder) (RealWatch, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected a phase parameter")
				}
				if phase != string(corev1.PodRunning) {
					return RealWatch{}, fmt.Errorf("live watched pod startup requires Running, got %q", phase)
				}
				running, err := in.startLiveWatchedPod()
				if err != nil {
					return RealWatch{}, err
				}
				fmt.Printf("actual fallback Running %s/%s UID %s RV %s node %s container %s after watch revocation\n", in.Namespace, in.Name, running.UID, running.ResourceVersion, running.Spec.NodeName, running.Status.ContainerStatuses[0].ContainerID)
				return in.next(), nil
			},
		),

		brine.DefineMap[RealWatch, RealWatch](
			"the watch is interrupted and the pod becomes {string} before being deleted",
			func(in RealWatch, p brine.Params, rec *brine.Recorder) (RealWatch, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected a pod phase")
				}
				return in.replayAfterDisconnect(corev1.PodPhase(phase), rec)
			},
		),

		// A burst: both transitions land before the runtime reads, so a
		// runtime that settled on the first would be waiting on a state the
		// cluster has already left.
		brine.DefineMap[RealWatch, RealWatch](
			"the pod goes {string} then {string} before the runtime looks",
			func(in RealWatch, p brine.Params, _ *brine.Recorder) (RealWatch, error) {
				first, _ := p.GetString(0)
				second, ok := p.GetString(1)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected two phases")
				}
				return in.observeLiveBurst(first, second)
			},
		),

		brine.DefineMap[RealWatch, RealWatch](
			"the pod is deleted out from under the step",
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

		Refine[RealWatch]("the build is cancelled while the runtime waits for its pod",
			func(in RealWatch, _ Args) RealWatch {
				return in.cancelEstablishedRead()
			}),

		CheckString[RealWatch]("the runtime is told its pod is {string}",
			"the pod's phase",
			func(in RealWatch) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the runtime was told nothing about the pod: %v", in.Err)
				}
				if in.Observed == nil {
					return "", fmt.Errorf("expected a pod, got nil")
				}
				if in.Observed.Name != in.Name {
					return "", fmt.Errorf("the runtime was told about pod %q, but it is watching %q", in.Observed.Name, in.Name)
				}
				return string(in.Observed.Status.Phase), nil
			}),

		CheckThat[RealWatch]("the runtime is told its pod was deleted",
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

		CheckThat[RealWatch]("the runtime stops waiting",
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

// cancelEstablishedRead gives the HTTP watch and the next read independent
// lifetimes. Cancelling the read must interrupt Next even while the real
// API stream stays open; closing both would also wake the outer retry loop.
func (w RealWatch) cancelEstablishedRead() RealWatch {
	if w.Err != nil || w.Observed == nil {
		w.Err = fmt.Errorf("establish cancellation premise: initial pod read failed: %v", w.Err)
		return w
	}
	watchCtx, stopWatch := context.WithTimeout(w.Ctx, 10*time.Second)
	defer stopWatch()
	defer w.Watcher.Stop()

	// A persisted metadata change establishes the real stream without
	// inventing a kubelet phase. The shared checkpoint checks the exact
	// API-assigned resourceVersion observed by the production watcher.
	if _, err := w.watchCheckpoint(watchCtx); err != nil {
		w.Err = fmt.Errorf("establish real watch before cancellation: %w", err)
		return w
	}

	readCtx, cancelRead := context.WithCancel(w.Ctx)
	defer cancelRead()
	done := make(chan RealWatch, 1)
	started := make(chan struct{})
	go func() {
		out := w
		defer func() {
			if r := recover(); r != nil {
				out.Err = fmt.Errorf("the runtime panicked while waiting: %v", r)
			}
			done <- out
		}()
		close(started)
		out.Observed, out.Err = w.Watcher.Next(readCtx)
	}()
	<-started
	// There must be no queued update completing this read. The API has no
	// kubelet or scheduler, and this scenario publishes no further update.
	select {
	case out := <-done:
		w.Err = fmt.Errorf("expected an idle watch read before cancellation, got pod %v, error %v", out.Observed, out.Err)
		return w
	case <-time.After(100 * time.Millisecond):
	}
	cancelRead()
	select {
	case out := <-done:
		return out
	case <-time.After(3 * time.Second):
		// A missing cancellation branch must fail without leaking its
		// goroutine. Close the real stream, then join the failed read.
		stopWatch()
		w.Watcher.Stop()
		select {
		case <-done:
			w.Err = fmt.Errorf("the runtime was still waiting 3s after the read was cancelled")
		case <-time.After(3 * time.Second):
			w.Err = fmt.Errorf("the runtime did not stop even after its real watch was closed")
		}
		return w
	}
}

func ownWatchRoute(rec *brine.Recorder, address, target string) (net.Listener, error) {
	route, err := routeWithDrops(address, target, 0, false)
	if err != nil {
		return nil, err
	}
	TrackDisposer(rec, "the watch route on "+address, route.Close)
	return route, nil
}

// One fixture owns the real API objects, watch, and transparent TCP route.
// The route forwards TLS bytes unchanged; it implements no Kubernetes behavior.
func newRealWatch(res brine.Resources, rec *brine.Recorder, names ...string) (RealWatch, error) {
	if len(names) == 0 {
		return RealWatch{}, fmt.Errorf("a real watch needs a pod")
	}
	api, err := getRealCluster(res)
	if err != nil {
		return RealWatch{}, err
	}
	return newRealWatchOn(api, rec, names...)
}

func newRealWatchOn(api *realCluster, rec *brine.Recorder, names ...string) (RealWatch, error) {
	if len(names) == 0 {
		return RealWatch{}, fmt.Errorf("a real watch needs a pod")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ns, err := api.Clientset.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "pod-watch-"}}, metav1.CreateOptions{})
	if err != nil {
		return RealWatch{}, err
	}
	registerNamespacePodCleanup(rec, api.Clientset, ns.Name)
	for _, name := range names {
		_, err := api.Clientset.CoreV1().Pods(ns.Name).Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "busybox"}}},
		}, metav1.CreateOptions{})
		if err != nil {
			return RealWatch{}, fmt.Errorf("create watched pod %q: %w", name, err)
		}
	}
	return (RealWatch{
		Clientset: api.Clientset, Namespace: ns.Name, Name: names[0],
		Ctx: context.Background(), config: api.RESTConfig,
	}).withWatchRoute(rec)
}

// withWatchRoute owns a transparent TCP route without changing API responses.
func (w RealWatch) withWatchRoute(rec *brine.Recorder) (RealWatch, error) {
	endpoint, err := url.Parse(w.config.Host)
	if err != nil {
		return RealWatch{}, err
	}
	target := apiRouteAddress(endpoint)
	route, err := ownWatchRoute(rec, "127.0.0.1:0", target)
	if err != nil {
		return RealWatch{}, err
	}
	config := rest.CopyConfig(w.config)
	if config.ServerName == "" {
		config.ServerName = endpoint.Hostname()
	}
	endpoint.Host = route.Addr().String()
	config.Host = endpoint.String()
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return RealWatch{}, err
	}
	if w.Watcher != nil {
		w.Watcher.Stop()
	}
	watcher := jetbridge.NewPodWatcher(client, w.Namespace, w.Name)
	w.Watcher = watcher
	TrackDisposer(rec, "the routed pod watcher for "+w.Name,
		func() error { watcher.Stop(); return nil })
	w.directConfig = w.config
	w.route, w.target, w.config = route, target, config
	return w, nil
}

// The checkpoint is observed through a live watch before its TCP path closes.
// Both a phase change and deletion then happen while no connection can deliver
// them. Reconnecting must replay history: neither an initial snapshot nor Get
// can supply the phase of a pod which is already gone.
func (w RealWatch) replayAfterDisconnect(phase corev1.PodPhase, rec *brine.Recorder) (RealWatch, error) {
	if w.Err != nil || w.Observed == nil || w.route == nil {
		return RealWatch{}, fmt.Errorf("replay requires a successful initial real-pod read")
	}
	if phase != corev1.PodRunning && phase != corev1.PodSucceeded {
		return RealWatch{}, fmt.Errorf("real replay requires Running or Succeeded, got %q", phase)
	}
	initialVersion := w.Observed.ResourceVersion
	watchCtx, cancelWatch := context.WithTimeout(w.Ctx, 60*time.Second)
	defer cancelWatch()
	defer w.Watcher.Stop()
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	checkpoint, err := w.watchCheckpoint(watchCtx)
	if err != nil {
		return RealWatch{}, err
	}
	address := w.route.Addr().String()
	if err := w.route.Close(); err != nil {
		return RealWatch{}, fmt.Errorf("interrupt real watch connection: %w", err)
	}
	// The direct admin client and exec connection do not traverse the closed
	// runtime route. Both real transitions happen while its stream is disconnected.
	target, err := w.startLiveWatchedPod()
	if err != nil {
		return RealWatch{}, err
	}
	if phase == corev1.PodSucceeded {
		target, err = w.completeLiveWatchedPod(target)
		if err != nil {
			return RealWatch{}, err
		}
	}
	fmt.Printf("actual disconnected replay target %s/%s UID %s RV %s phase %s node %s container %s checkpoint %s\n",
		w.Namespace, w.Name, target.UID, target.ResourceVersion, target.Status.Phase, target.Spec.NodeName,
		target.Status.ContainerStatuses[0].ContainerID, checkpoint.ResourceVersion)
	grace := int64(1)
	if err := pods.Delete(watchCtx, w.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &grace, Preconditions: &metav1.Preconditions{UID: &checkpoint.UID},
	}); err != nil {
		return RealWatch{}, err
	}
	for {
		remaining, err := pods.Get(watchCtx, w.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		if err != nil {
			return RealWatch{}, err
		}
		if remaining.UID != checkpoint.UID {
			return RealWatch{}, fmt.Errorf("replay deletion encountered a replacement pod")
		}
		select {
		case <-watchCtx.Done():
			return RealWatch{}, fmt.Errorf("pod must be gone before reconnecting: %w", watchCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	fmt.Printf("actual replay pod %s/%s UID %s is absent before reconnecting at checkpoint %s\n",
		w.Namespace, w.Name, checkpoint.UID, checkpoint.ResourceVersion)
	resumed, err := ownWatchRoute(rec, address, w.target)
	if err != nil {
		return RealWatch{}, fmt.Errorf("restore real watch connection: %w", err)
	}
	w.route = resumed
	readCtx, cancelRead := context.WithTimeout(w.Ctx, 5*time.Second)
	defer cancelRead()
	for {
		w.Observed, w.Err = w.Watcher.Next(readCtx)
		if w.Err != nil || w.Observed == nil {
			return w, nil
		}
		if w.Observed.UID != checkpoint.UID || w.Observed.Name != w.Name {
			w.Err = fmt.Errorf("replay returned a different pod identity")
			return w, nil
		}
		// Do not hide a stale resume version by draining its already-consumed
		// checkpoint along with legitimate kubelet startup updates.
		if w.Observed.ResourceVersion == checkpoint.ResourceVersion || w.Observed.ResourceVersion == initialVersion {
			w.Err = fmt.Errorf("replay returned an already-consumed checkpoint")
			return w, nil
		}
		if w.Observed.Status.Phase == corev1.PodPending ||
			(phase == corev1.PodSucceeded && w.Observed.Status.Phase == corev1.PodRunning) {
			continue
		}
		return w, nil
	}
}

// watchCheckpoint proves that the production watcher has a live stream and
// has consumed the API-assigned version before a connection is disrupted.
func (w RealWatch) watchCheckpoint(ctx context.Context) (*corev1.Pod, error) {
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	pod, err := pods.Get(ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations["brine.jetbridge/watch-checkpoint"] = pod.ResourceVersion
	checkpoint, err := pods.Update(ctx, pod, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	seen, err := w.Watcher.Next(ctx)
	if err != nil || seen == nil || seen.Name != w.Name || seen.ResourceVersion != checkpoint.ResourceVersion {
		return nil, fmt.Errorf("real watch did not observe its checkpoint: pod %v, error %v", seen, err)
	}
	return checkpoint, nil
}

// newLiveScopedWatch uses the same bounded namespace as the other live cases.
// Both pods are gated so the first runtime read precedes their real transitions.
func newLiveScopedWatch(rec *brine.Recorder, mine, theirs string) (RealWatch, error) {
	if mine == theirs {
		return RealWatch{}, fmt.Errorf("selector premise requires two distinct pods")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	rec.RegisterDisposer(cancel)
	cluster, err := newLiveKubernetes(ctx, rec)
	if err != nil {
		return RealWatch{}, err
	}
	grace := int64(1)
	for i, name := range []string{mine, theirs} {
		command := "exec sleep 900"
		if i == 1 {
			command = "printf 'selector-neighbour-failed\\n'; exit 1"
		}
		pod, err := cluster.Clientset.CoreV1().Pods(cluster.Namespace).Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace,
				SchedulingGates: []corev1.PodSchedulingGate{{Name: startupSchedulingGate}},
				Containers:      []corev1.Container{{Name: "main", Image: "busybox:1.37.0", Command: []string{"sh", "-ec", command}}},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return RealWatch{}, err
		}
		if pod.UID == "" || pod.ResourceVersion == "" || pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName != "" {
			return RealWatch{}, fmt.Errorf("selector pod %q lacks an actual unscheduled Pending identity", name)
		}
		fmt.Printf("actual gated selector pod %s/%s UID %s RV %s\n", cluster.Namespace, name, pod.UID, pod.ResourceVersion)
	}
	watcher := jetbridge.NewPodWatcher(cluster.Clientset, cluster.Namespace, mine)
	TrackDisposer(rec, "the scoped pod watcher for "+mine,
		func() error { watcher.Stop(); return nil })
	return RealWatch{Clientset: cluster.Clientset, Namespace: cluster.Namespace, Name: mine,
		Ctx: ctx, Watcher: watcher, config: cluster.Config}, nil
}

// releaseSchedulingGate changes only the original owned pod spec, never status.
func (w RealWatch) releaseSchedulingGate(original *corev1.Pod) error {
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		pod, err := pods.Get(w.Ctx, original.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.UID == "" || pod.UID != original.UID || pod.Spec.NodeName != "" ||
			len(pod.Spec.SchedulingGates) != 1 || pod.Spec.SchedulingGates[0].Name != startupSchedulingGate {
			return fmt.Errorf("selector gate no longer belongs to the original unscheduled pod %q", original.Name)
		}
		pod.Spec.SchedulingGates = nil
		_, err = pods.Update(w.Ctx, pod, metav1.UpdateOptions{})
		return err
	})
}

func (w RealWatch) observeLiveScopedTransitions(mine, theirs string) (RealWatch, error) {
	if mine != w.Name || theirs == mine {
		return w, fmt.Errorf("selector transition names do not match the two-pod premise")
	}
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	initial, err := w.Watcher.Next(w.Ctx)
	if err != nil {
		return w, fmt.Errorf("initial selector read: %w", err)
	}
	if initial == nil || initial.Name != mine || initial.UID == "" || initial.Status.Phase != corev1.PodPending || initial.Spec.NodeName != "" {
		return w, fmt.Errorf("initial selector read did not observe the gated Pending pod")
	}
	neighbour, err := pods.Get(w.Ctx, theirs, metav1.GetOptions{})
	if err != nil {
		return w, err
	}

	if err := w.releaseSchedulingGate(neighbour); err != nil {
		return w, err
	}
	failed, _, err := awaitLivePodExit(w.Ctx, w.Clientset, neighbour, 1, "selector-neighbour-failed\n")
	if err != nil {
		return w, err
	}
	fmt.Printf("actual selector neighbour %s/%s UID %s RV %s node %s container %s exited 1 before watched pod release\n",
		w.Namespace, theirs, failed.UID, failed.ResourceVersion, failed.Spec.NodeName, failed.Status.ContainerStatuses[0].ContainerID)
	if err := w.releaseSchedulingGate(initial); err != nil {
		return w, err
	}
	running, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, mine)
	if err != nil {
		return w, err
	}
	if running.UID != initial.UID || running.ResourceVersion == initial.ResourceVersion {
		return w, fmt.Errorf("watched pod did not advance the original API identity")
	}
	fmt.Printf("actual selector watched pod %s/%s UID %s RV %s node %s is Running after neighbour failure\n",
		w.Namespace, mine, running.UID, running.ResourceVersion, running.Spec.NodeName)

	// A kubelet produces intermediate Pending updates. Consume only those
	// belonging to this pod; never discard a neighbour event to make scoping pass.
	// Return every unexpected identity/phase to the original assertion.
	next, cancel := context.WithTimeout(w.Ctx, 20*time.Second)
	defer cancel()
	for {
		w.Observed, w.Err = w.Watcher.Next(next)
		if w.Err != nil || w.Observed == nil || w.Observed.Name != mine || w.Observed.Status.Phase != corev1.PodPending {
			return w, nil
		}
	}
}
