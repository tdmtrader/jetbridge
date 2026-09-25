//go:build live
// +build live

package jetbridge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/worker/jetbridge"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// liveDeployment is the JetBridge release the live tier runs against: its web
// pod and its artifact daemon, read once in TestMain before any test runs.
//
// It used to be read per test and adopted leniently: no daemon, no permission
// to list one, or TLS off each returned quietly with the config untouched, so
// the tier's coverage became a function of whatever the cluster happened to be
// running and shrank without turning anything red. The chart no longer has an
// off switch for any of it -- the daemon is always deployed, mTLS is always
// required and resolution is always signed -- so an absence is a broken or
// unreachable deployment, and the run stops on it instead of degrading.
type liveDeployment struct {
	namespace string

	daemon          appsv1.DaemonSet
	daemonContainer corev1.Container

	webPod       corev1.Pod
	webContainer corev1.Container

	hostPath   string
	port       int
	service    string
	tlsDir     string
	resolveKey []byte
}

// deployed is set by TestMain; every live test may rely on it.
var deployed *liveDeployment

// releaseNamespace is where the deployment under test runs: its web and its
// artifact daemon, which the chart puts in one release namespace. That is not
// the namespace these tests schedule their own pods into (K8S_TEST_NAMESPACE).
func releaseNamespace() string {
	if ns := os.Getenv("K8S_ARTIFACT_DAEMON_NAMESPACE"); ns != "" {
		return ns
	}
	return "cicd"
}

// liveKubeconfig resolves the kubeconfig the way kubeClient always has:
// KUBECONFIG, then ~/.kube/config, then in-cluster.
func liveKubeconfig() string {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		// Check if the default kubeconfig file exists; if not, leave it
		// empty so NewConfig/NewClientset will fall back to in-cluster config.
		home, _ := os.UserHomeDir()
		candidate := home + "/.kube/config"
		if _, err := os.Stat(candidate); err == nil {
			kubeconfig = candidate
		}
	}

	// When running inside a K8s pod (SA token exists) but the standard
	// KUBERNETES_SERVICE_HOST env var isn't set (some container runtimes
	// don't inject it), set it to the well-known in-cluster DNS name so
	// that rest.InClusterConfig() succeeds.
	if kubeconfig == "" {
		if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); err == nil {
			if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
				os.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
				os.Setenv("KUBERNETES_SERVICE_PORT", "443")
			}
		}
	}
	return kubeconfig
}

// discoverDeployment reads the deployed web and daemon and materializes the
// daemon's client credentials into tlsRoot.
//
// Everything is read from the workloads' own command lines and volumes, never
// assumed: renaming the service, moving the storage path or rotating a Secret
// needs no change here or in whatever invokes these tests.
func discoverDeployment(ctx context.Context, clientset kubernetes.Interface, namespace, tlsRoot string) (*liveDeployment, error) {
	d := &liveDeployment{namespace: namespace, tlsDir: tlsRoot}

	daemons, err := clientset.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=artifact-daemon",
	})
	if err != nil {
		return nil, fmt.Errorf("listing artifact daemons in %s: %w", namespace, err)
	}
	if len(daemons.Items) != 1 {
		return nil, fmt.Errorf("want exactly one artifact daemon DaemonSet in %s, found %d (set K8S_ARTIFACT_DAEMON_NAMESPACE)", namespace, len(daemons.Items))
	}
	d.daemon = daemons.Items[0]
	d.daemonContainer = d.daemon.Spec.Template.Spec.Containers[0]

	// The storage path decides whether the worker gets a DaemonSet backend at
	// all -- empty means artifact passing is silently off, not broken -- and the
	// service name is the SAN the daemon's server certificate is verified
	// against, so a stale one fails every ATC-side mTLS call while the init
	// containers, which skip hostname verification, keep working.
	d.service = d.daemon.Name
	if v, ok := d.daemonFlag("service-name"); ok {
		d.service = v
	}
	d.hostPath, _ = d.daemonFlag("storage-path")
	if d.hostPath == "" {
		return nil, fmt.Errorf("artifact daemon %s/%s has no --storage-path", namespace, d.daemon.Name)
	}
	d.port = 7780
	if v, ok := d.daemonFlag("port"); ok {
		if d.port, err = strconv.Atoi(v); err != nil {
			return nil, fmt.Errorf("artifact daemon --port=%q: %w", v, err)
		}
	}

	// mTLS: a plain-HTTP caller gets 400 on every request.
	tlsSecret := d.daemonSecretVolume("daemon-tls")
	if tlsSecret == "" {
		return nil, fmt.Errorf("artifact daemon %s/%s mounts no daemon-tls Secret, but the chart always requires mTLS", namespace, d.daemon.Name)
	}
	secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, tlsSecret, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading daemon TLS secret %s/%s: %w", namespace, tlsSecret, err)
	}
	for _, key := range []string{"ca.crt", "client.crt", "client.key"} {
		data, found := secret.Data[key]
		if !found {
			return nil, fmt.Errorf("daemon TLS secret %s/%s has no %s", namespace, tlsSecret, key)
		}
		if err := os.WriteFile(filepath.Join(tlsRoot, key), data, 0o600); err != nil {
			return nil, err
		}
	}

	// Resolve capability: a config without the signing key mints tokens the
	// daemon refuses. The failure is quiet in the worst way -- the init
	// container exits 0 and the step then fails on a missing file -- so the key
	// is read from the Secret the daemon itself mounts, under the name its own
	// --resolve-capability-key flag gives it.
	keySecret := d.daemonSecretVolume("resolve-capability")
	if keySecret == "" {
		return nil, fmt.Errorf("artifact daemon %s/%s mounts no resolve-capability Secret, but the chart always enforces signed resolution", namespace, d.daemon.Name)
	}
	keyName := "resolve.key"
	if v, ok := d.daemonFlag("resolve-capability-key"); ok && filepath.Base(v) != "." {
		keyName = filepath.Base(v)
	}
	secret, err = clientset.CoreV1().Secrets(namespace).Get(ctx, keySecret, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading resolve capability secret %s/%s: %w", namespace, keySecret, err)
	}
	if d.resolveKey = secret.Data[keyName]; len(d.resolveKey) == 0 {
		return nil, fmt.Errorf("resolve capability secret %s/%s has no %s", namespace, keySecret, keyName)
	}

	if err := d.findWeb(ctx, clientset); err != nil {
		return nil, err
	}
	return d, nil
}

// findWeb picks a running web pod. The web Deployment rolls out with Recreate,
// so right after an upgrade there can briefly be none; wait for one rather
// than fail on the gap.
func (d *liveDeployment) findWeb(ctx context.Context, clientset kubernetes.Interface) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		pods, err := clientset.CoreV1().Pods(d.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/component=web",
		})
		if err != nil {
			return fmt.Errorf("listing web pods in %s: %w", d.namespace, err)
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
				continue
			}
			for _, c := range pod.Spec.Containers {
				if len(c.Args) > 0 && c.Args[0] == "web" {
					d.webPod, d.webContainer = pod, c
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no running web pod in %s: %w", d.namespace, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// configure points a live config at the deployed daemon and gives it the
// deployed web's task cache store, so a live worker builds the pods the web
// would.
func (d *liveDeployment) configure(cfg *jetbridge.Config) {
	cfg.CacheStore, _ = d.webFlag("kubernetes-cache-store")
	cfg.CacheHostPath, _ = d.webFlag("kubernetes-cache-host-path")
	cfg.ArtifactDaemonNamespace = d.namespace
	cfg.ArtifactDaemonService = d.service
	cfg.ArtifactDaemonHostPath = d.hostPath
	cfg.ArtifactDaemonPort = d.port
	cfg.ArtifactDaemonTLSEnabled = true
	cfg.ArtifactDaemonTLSCACert = filepath.Join(d.tlsDir, "ca.crt")
	cfg.ArtifactDaemonTLSCert = filepath.Join(d.tlsDir, "client.crt")
	cfg.ArtifactDaemonTLSKey = filepath.Join(d.tlsDir, "client.key")
	cfg.ArtifactDaemonResolveCapabilityKey = d.resolveKey
}

func (d *liveDeployment) String() string {
	return fmt.Sprintf("web pod %s/%s; daemon %s/%s (storage path %q, service %s, port %d, mTLS, signed resolve)",
		d.namespace, d.webPod.Name, d.namespace, d.daemon.Name, d.hostPath, d.service, d.port)
}

func (d *liveDeployment) daemonSecretVolume(name string) string {
	for _, volume := range d.daemon.Spec.Template.Spec.Volumes {
		if volume.Name == name && volume.Secret != nil {
			return volume.Secret.SecretName
		}
	}
	return ""
}

// daemonFlag returns the value of the daemon's --name flag ("true" for a bare
// boolean flag) and whether it is set.
func (d *liveDeployment) daemonFlag(name string) (string, bool) {
	return flagIn(append(append([]string{}, d.daemonContainer.Command...), d.daemonContainer.Args...), name)
}

// webFlag returns the value of the web's --name flag, or of the CONCOURSE_NAME
// environment variable go-flags reads for it; a variable filled from a
// Secret or ConfigMap counts as set with an unknown value.
func (d *liveDeployment) webFlag(name string) (string, bool) {
	if v, ok := flagIn(d.webContainer.Args, name); ok {
		return v, true
	}
	envName := "CONCOURSE_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	for _, env := range d.webContainer.Env {
		if env.Name == envName {
			return env.Value, true
		}
	}
	return "", false
}

// webPort returns the web container's port with the given name.
func (d *liveDeployment) webPort(name string) (string, error) {
	for _, port := range d.webContainer.Ports {
		if port.Name == name {
			return strconv.Itoa(int(port.ContainerPort)), nil
		}
	}
	return "", fmt.Errorf("web container %s/%s declares no %q port", d.namespace, d.webPod.Name, name)
}

func flagIn(args []string, name string) (string, bool) {
	value, found := "", false
	for _, arg := range args {
		switch {
		case arg == "--"+name:
			value, found = "true", true
		case strings.HasPrefix(arg, "--"+name+"="):
			value, found = strings.TrimPrefix(arg, "--"+name+"="), true
		}
	}
	return value, found
}
