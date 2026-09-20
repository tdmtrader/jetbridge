package steps

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

// Live execution is opt-in to an explicitly selected cluster. Each scenario
// owns a namespace with admission-enforced security and resource bounds. It
// never adopts an existing namespace or changes nodes/cluster-wide RBAC.
type liveKubernetes struct {
	Clientset kubernetes.Interface
	Config    *rest.Config
	Namespace string
	Marker    string
}

func newLiveKubernetes(ctx context.Context, rec *brine.Recorder, podLimits ...int64) (liveKubernetes, error) {
	podLimit := int64(2)
	if len(podLimits) > 1 {
		return liveKubernetes{}, fmt.Errorf("at most one pod limit is allowed")
	}
	if len(podLimits) == 1 {
		podLimit = podLimits[0]
	}
	if podLimit < 1 {
		return liveKubernetes{}, fmt.Errorf("pod limit must be positive")
	}
	cfg, err := liveKubernetesConfig()
	if err != nil {
		return liveKubernetes{}, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return liveKubernetes{}, err
	}
	ns, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		GenerateName: "brine-runtime-", Labels: map[string]string{
			"app.kubernetes.io/managed-by":               "brine-runtime-tests",
			"pod-security.kubernetes.io/enforce":         "baseline",
			"pod-security.kubernetes.io/enforce-version": "v1.34",
		},
	}}, metav1.CreateOptions{})
	if err != nil {
		return liveKubernetes{}, err
	}
	TrackDisposer(rec, "the owned live namespace "+ns.Name, func() error {
		return liveNamespaces.dispose(client, ns)
	})
	liveNamespaces.sweepStaleOnce(ctx, client)
	fmt.Printf("created owned live namespace %s (%s)\n", ns.Name, ns.UID)
	q := resource.MustParse
	_, err = client.CoreV1().ResourceQuotas(ns.Name).Create(ctx, &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "bounded-test"}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
		corev1.ResourcePods: *resource.NewQuantity(podLimit, resource.DecimalSI), corev1.ResourceRequestsCPU: q("1"), corev1.ResourceLimitsCPU: q("1"),
		corev1.ResourceRequestsMemory: q("512Mi"), corev1.ResourceLimitsMemory: q("512Mi"),
		corev1.ResourceRequestsEphemeralStorage: q("256Mi"), corev1.ResourceLimitsEphemeralStorage: q("256Mi"),
	}}}, metav1.CreateOptions{})
	if err != nil {
		return liveKubernetes{}, err
	}
	_, err = client.CoreV1().LimitRanges(ns.Name).Create(ctx, &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "bounded-containers"}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer,
		Default:        corev1.ResourceList{corev1.ResourceCPU: q("250m"), corev1.ResourceMemory: q("64Mi"), corev1.ResourceEphemeralStorage: q("64Mi")},
		DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: q("50m"), corev1.ResourceMemory: q("32Mi"), corev1.ResourceEphemeralStorage: q("32Mi")},
	}}}}, metav1.CreateOptions{})
	if err != nil {
		return liveKubernetes{}, err
	}
	return liveKubernetes{Clientset: client, Config: cfg, Namespace: ns.Name, Marker: string(ns.UID)}, nil
}

// Shared live runtime setup and observation for tasks and interception.
func newLiveRuntimeWorker(database JetbridgeDB, rec *brine.Recorder, podLimits ...int64) (WorkerReady, error) {
	ctx, cancel := context.WithTimeout(execLogger("live-runtime"), 90*time.Second)
	rec.RegisterDisposer(cancel)
	cluster, err := newLiveKubernetes(ctx, rec, podLimits...)
	if err != nil {
		return WorkerReady{}, err
	}
	worker, err := database.PersistNamedWorker("k8s-worker-1")
	if err != nil {
		return WorkerReady{}, err
	}
	config := jetbridge.NewConfig(cluster.Namespace, "")
	config.PodStartupTimeout, config.PodSchedulingTimeout = 20*time.Second, 20*time.Second
	executor := jetbridge.NewSPDYExecutor(cluster.Clientset, cluster.Config)
	ready := WorkerReady{DB: database, DBWorker: worker, Ctx: ctx,
		Namespace: cluster.Namespace, Clientset: cluster.Clientset, Config: config,
		Executor: executor, ProducerExecutor: executor}
	return ready.rebuild(), nil
}

func requirePodSupervisorState(w WorkerReady, podName string) error {
	// Do not compute the supervisor's directory name from its implementation:
	// inspect actual files and reject an empty match, just as the host guard did.
	command := `found=no
for state in /tmp/concourse-task-*; do
  [ -d "$state" ] || continue
  [ -f "$state/pid" ] && [ -f "$state/log" ] && [ -f "$state/exit" ] || exit 1
  found=yes
done
[ "$found" = yes ]`
	if err := w.Executor.ExecInPod(w.Ctx, w.Namespace, podName, "main", []string{"sh", "-c", command},
		nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "supervisor-state"}); err != nil {
		return fmt.Errorf("supervisor left no complete state in the expected pod: %w", err)
	}
	fmt.Printf("verified remote supervisor state in %s/%s\n", w.Namespace, podName)
	return nil
}

func liveKubernetesConfig() (*rest.Config, error) {
	name := os.Getenv("BRINE_KUBE_CONTEXT")
	if name == "" {
		return nil, fmt.Errorf("real pod execution requires BRINE_KUBE_CONTEXT; no fake fallback is available")
	}
	var cfg *rest.Config
	var err error
	if name == "in-cluster" {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: name}).ClientConfig()
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// apiRouteAddress preserves explicit ports and supplies HTTP(S) defaults for raw TCP.
func apiRouteAddress(endpoint *url.URL) string {
	target := endpoint.Host
	if endpoint.Port() == "" {
		port := "443"
		if endpoint.Scheme == "http" {
			port = "80"
		}
		target = net.JoinHostPort(endpoint.Hostname(), port)
	}
	return target
}

// liveExecutionRoute forwards unmodified TLS; closing it cuts only owned connections.
func liveExecutionRoute(ctx context.Context, rec *brine.Recorder, config *rest.Config) (*jetbridge.SPDYExecutor, net.Listener, error) {
	endpoint, err := url.Parse(config.Host)
	if err != nil {
		return nil, nil, err
	}
	target := apiRouteAddress(endpoint)
	route, err := routeWithDrops("127.0.0.1:0", target, 0, false)
	if err != nil {
		return nil, nil, err
	}
	TrackDisposer(rec, "the live execution route", route.Close)
	routed := rest.CopyConfig(config)
	if routed.ServerName == "" {
		routed.ServerName = endpoint.Hostname()
	}
	endpoint.Host = route.Addr().String()
	routed.Host = endpoint.String()
	execClient, err := kubernetes.NewForConfig(routed)
	if err != nil {
		return nil, nil, err
	}
	if err := execClient.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Error(); err != nil {
		return nil, nil, fmt.Errorf("verify transparent API route: %w", err)
	}
	return jetbridge.NewSPDYExecutor(execClient, routed), route, nil
}

// Hold an owned pod for lifecycle observation without delaying its processes.
// Register UID-checked release before the write; callers still own pod deletion.
const podObservationFinalizer = "brine.concourse-ci.org/observe-termination"

func holdLivePodDeletion(w WorkerReady, pod *corev1.Pod, rec *brine.Recorder) (func() error, error) {
	release := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if current.UID != pod.UID {
				return fmt.Errorf("refuse to change finalizers on replacement pod")
			}
			kept := make([]string, 0, len(current.Finalizers))
			found := false
			for _, f := range current.Finalizers {
				if f == podObservationFinalizer {
					found = true
				} else {
					kept = append(kept, f)
				}
			}
			if !found {
				return nil
			}
			current.Finalizers = kept
			_, err = w.Clientset.CoreV1().Pods(w.Namespace).Update(ctx, current, metav1.UpdateOptions{})
			if err == nil {
				fmt.Printf("removed owned pod-observation finalizer %s/%s UID %s\n", w.Namespace, pod.Name, pod.UID)
			}
			return err
		})
	}
	// Register before attempting the write: even an ambiguous transport failure
	// must not strand a finalizer on this owned pod.
	TrackDisposer(rec, "the pod-observation finalizer on "+pod.Name, release)
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(w.Ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != pod.UID {
			return fmt.Errorf("observed pod replaced before finalizer")
		}
		for _, f := range current.Finalizers {
			if f == podObservationFinalizer {
				return nil
			}
		}
		current.Finalizers = append(current.Finalizers, podObservationFinalizer)
		_, err = w.Clientset.CoreV1().Pods(w.Namespace).Update(w.Ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err == nil {
		fmt.Printf("added owned pod-observation finalizer %s/%s UID %s\n", w.Namespace, pod.Name, pod.UID)
	}
	return release, err
}

// awaitLivePodExit observes a single-container, non-restarting fixture pod.
// Its caller owns the pod and context deadline. Phase follows the expected exit;
// kubelet status and exact logs must independently confirm the actual completion.
func awaitLivePodExit(ctx context.Context, client kubernetes.Interface, original *corev1.Pod, code int, expectedLog string) (*corev1.Pod, string, error) {
	if original == nil || original.UID == "" || original.ResourceVersion == "" || original.Name == "" || original.Namespace == "" {
		return nil, "", fmt.Errorf("completed-pod observation requires an original API identity")
	}
	phase := corev1.PodFailed
	if code == 0 {
		phase = corev1.PodSucceeded
	}
	pods := client.CoreV1().Pods(original.Namespace)
	for {
		pod, err := pods.Get(ctx, original.Name, metav1.GetOptions{})
		if err != nil {
			return nil, "", err
		}
		if pod.UID != original.UID {
			return nil, "", fmt.Errorf("completed pod %q changed identity", original.Name)
		}
		if pod.Status.Phase == phase {
			if pod.Spec.NodeName == "" || pod.ResourceVersion == original.ResourceVersion || len(pod.Status.ContainerStatuses) != 1 {
				return nil, "", fmt.Errorf("pod %q has no actual terminal lifecycle", original.Name)
			}
			c := pod.Status.ContainerStatuses[0]
			if c.Name != "main" || c.ContainerID == "" || c.RestartCount != 0 || c.State.Terminated == nil ||
				int(c.State.Terminated.ExitCode) != code || c.State.Terminated.StartedAt.IsZero() || c.State.Terminated.FinishedAt.IsZero() {
				return nil, "", fmt.Errorf("pod %q did not really exit %d without restarting: %+v", original.Name, code, c)
			}
			log, err := pods.GetLogs(original.Name, &corev1.PodLogOptions{Container: "main"}).DoRaw(ctx)
			if err != nil {
				return nil, "", err
			}
			if string(log) != expectedLog {
				return nil, "", fmt.Errorf("completed pod %q actual log mismatch: got %q, want %q", original.Name, log, expectedLog)
			}
			return pod, string(log), nil
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			return nil, "", fmt.Errorf("pod %q ended in phase %s, want %s for exit %d", original.Name, pod.Status.Phase, phase, code)
		}
		select {
		case <-ctx.Done():
			return nil, "", fmt.Errorf("wait for actual pod %q exit: %w", original.Name, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
