package integration_test

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// k3sImage is the K3s image used for the test cluster.
// K3s v1.31 provides CNCF-certified Kubernetes without the kubeadm
// complexity that plagues KinD in DinD environments.
var k3sImage = "rancher/k3s:v1.31.6-k3s1"

// k3sContainer holds the testcontainers K3s instance for the test suite.
// It's set in createK3sCluster and cleaned up in deleteK3sCluster.
var k3sContainer *k3s.K3sContainer

// splitImageRef splits "repo:tag" into its parts. If no tag is present,
// "latest" is returned as the default tag.
func splitImageRef(image string) (string, string) {
	parts := strings.SplitN(image, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], "latest"
}

// findFreePort asks the OS for an available port and returns it.
func findFreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("failed to find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// verifyPrerequisites checks that required CLIs are on PATH.
func verifyPrerequisites() error {
	var missing []string
	for _, bin := range []string{"docker", "helm", "kubectl"} {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required CLIs on PATH: %s", strings.Join(missing, ", "))
	}
	return nil
}

// createK3sCluster creates an ephemeral K3s cluster via testcontainers
// and returns the path to a kubeconfig file written to disk.
// K3s replaces KinD — no kubeadm, no nested containerd, no timeout patches.
// A single container provides a full CNCF-certified Kubernetes cluster.
func createK3sCluster() string {
	ctx := context.Background()
	kubeconfigPath := filepath.Join(os.TempDir(), "k3s-kubeconfig-integration")

	log.Printf("Creating K3s cluster via testcontainers (%s)...", k3sImage)
	var err error
	k3sContainer, err = k3s.Run(ctx, k3sImage)
	if err != nil {
		log.Fatalf("failed to create K3s cluster: %v", err)
	}

	// Export kubeconfig to file for helm/kubectl CLI commands.
	kubeconfig, err := k3sContainer.GetKubeConfig(ctx)
	if err != nil {
		log.Fatalf("failed to get kubeconfig from K3s: %v", err)
	}
	if err := os.WriteFile(kubeconfigPath, kubeconfig, 0600); err != nil {
		log.Fatalf("failed to write kubeconfig: %v", err)
	}

	log.Printf("K3s cluster ready (kubeconfig: %s)", kubeconfigPath)
	return kubeconfigPath
}

// ensureConcourseImage checks if the Concourse Docker image exists locally
// and builds it from source if not found.
func ensureConcourseImage(image string) {
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		log.Printf("Concourse image %q not found locally, building from source...", image)
		root := mustRepoRoot()
		cmd := exec.Command("docker", "build", "-f", "Dockerfile.build", "-t", image, root)
		cmd.Dir = root
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("failed to build Concourse image: %v", err)
		}
	}
}

// testDependencyImages are public images used by integration tests that
// should be pre-loaded into the K3s cluster. They are pulled by the host
// Docker daemon and loaded via testcontainers' LoadImages API.
var testDependencyImages = []string{
	"docker.io/library/postgres:16",
	"docker.io/library/busybox:latest",
	"docker.io/library/alpine:latest",
	"docker.io/concourse/mock-resource:latest",
}

// loadImagesIntoCluster loads the locally-built Concourse image and test
// dependency images into the K3s cluster via testcontainers' LoadImages API.
// After loading, it restarts CoreDNS — K3s starts system pods immediately
// but they fail without the pause image. Loading pause + restarting fixes it.
func loadImagesIntoCluster(concourseImage string) {
	ctx := context.Background()

	// Load the locally-built Concourse image.
	log.Printf("Loading %s into K3s cluster...", concourseImage)
	if err := k3sContainer.LoadImages(ctx, concourseImage); err != nil {
		log.Fatalf("failed to load image %s into K3s: %v", concourseImage, err)
	}
	log.Println("Concourse image loaded.")

	// Pull and load test dependency images (includes pause image for K3s pods).
	for _, img := range testDependencyImages {
		log.Printf("Pre-pulling %s on host...", img)
		pullCmd := exec.Command("docker", "pull", "--quiet", img)
		pullCmd.Stdout = os.Stderr
		pullCmd.Stderr = os.Stderr
		if err := pullCmd.Run(); err != nil {
			log.Printf("warning: failed to pull %s on host: %v", img, err)
			continue
		}

		log.Printf("Loading %s into K3s cluster...", img)
		if err := k3sContainer.LoadImages(ctx, img); err != nil {
			log.Printf("warning: failed to load %s into K3s: %v", img, err)
		}
	}
	log.Println("Image loading complete.")
}

// labelNodesForArtifactCache labels all K3s nodes with the label that
// the JetBridge artifact daemon node affinity requires. Without this,
// build pods are Unschedulable when the artifact daemon is enabled.
func labelNodesForArtifactCache(kubeconfig string) {
	log.Println("Labeling K3s nodes for artifact cache scheduling...")
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig,
		"label", "nodes", "--all", "concourse.dev/artifact-cache=ready", "--overwrite")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("warning: failed to label nodes: %v", err)
	}
}

// waitForCoreDNS waits until K3s's CoreDNS is running and ready.
// Without this, pods that resolve cluster-internal hostnames (like the
// migrate-db init container looking up the DB service) fail immediately
// with DNS resolution errors.
func waitForCoreDNS(kubeconfig string) {
	log.Println("Waiting for CoreDNS to be ready...")
	waitCmd := exec.Command("kubectl",
		"--kubeconfig", kubeconfig,
		"-n", "kube-system",
		"wait", "--for=condition=ready", "pod",
		"-l", "k8s-app=kube-dns",
		"--timeout=120s",
	)
	waitCmd.Stdout = os.Stderr
	waitCmd.Stderr = os.Stderr
	if err := waitCmd.Run(); err != nil {
		log.Printf("warning: CoreDNS wait failed: %v (proceeding anyway)", err)
	} else {
		log.Println("CoreDNS is ready.")
	}
}

// helmDeployConcourse deploys Concourse via the local Helm chart.
// artifactDaemonTLSEnabled reports whether the suite should deploy the artifact
// daemon with mTLS hardening enabled. Opt-in via ARTIFACT_DAEMON_TLS so the
// default suite run stays on plain HTTP; set it to verify the TLS data path.
func artifactDaemonTLSEnabled() bool {
	v := os.Getenv("ARTIFACT_DAEMON_TLS")
	return v == "1" || strings.EqualFold(v, "true")
}

func helmDeployConcourse(kubeconfig, namespace, chartPath, image string) {
	repo, tag := splitImageRef(image)

	// Wait for CoreDNS before deploying — the migrate-db init container
	// needs DNS to resolve the PostgreSQL service hostname.
	waitForCoreDNS(kubeconfig)

	// Label nodes so build pods with artifact daemon affinity can schedule.
	labelNodesForArtifactCache(kubeconfig)

	// Create namespace (ignore if exists).
	exec.Command("kubectl", "--kubeconfig", kubeconfig,
		"create", "namespace", namespace).Run()

	log.Printf("Deploying Concourse chart from %s into namespace %s...", chartPath, namespace)

	// Build the list of extra args for the web node.
	extraArgs := []string{}

	// When OTEL_EXPORTER_OTLP_ENDPOINT is set, enable server-side tracing.
	if otlpEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); otlpEndpoint != "" {
		otlpEndpoint = resolveEndpoint(otlpEndpoint)
		log.Printf("Enabling OTel tracing: --tracing-otlp-address=%s", otlpEndpoint)
		extraArgs = append(extraArgs,
			"--tracing-otlp-address="+otlpEndpoint,
			"--tracing-service-name=concourse-integration-test",
		)
	}
	if otlpMetrics := os.Getenv("OTEL_METRICS_OTLP_ENDPOINT"); otlpMetrics != "" {
		log.Printf("Enabling OTel metrics: --otel-metrics-otlp-address=%s", otlpMetrics)
		extraArgs = append(extraArgs, "--otel-metrics-otlp-address="+otlpMetrics)
	}

	helmArgs := []string{
		"upgrade", "--install", "concourse", chartPath,
		"--namespace", namespace,
		"--kubeconfig", kubeconfig,
		"--set", fmt.Sprintf("image.repository=%s", repo),
		"--set", fmt.Sprintf("image.tag=%s", tag),
		"--set", "image.pullPolicy=IfNotPresent",
		// Use emptyDir for PostgreSQL — ephemeral test clusters don't need
		// persistent storage, and PVC provisioning can stall in DinD.
		"--set", "postgresql.persistence.enabled=false",
		// Disable cache PVC — the flag --kubernetes-cache-pvc doesn't exist
		// in the built binary yet. The artifact daemon approach is used instead.
		"--set", "cachePvc.enabled=false",
		"--set", "artifactStorePvc.enabled=false",
		// Enable the DaemonSet artifact daemon — needed for artifact passing
		// between steps. Default is false in values.yaml.
		"--set", "artifactDaemon.enabled=true",
		"--timeout", "5m",
	}
	// Optionally harden the daemon with mTLS (opt-in via ARTIFACT_DAEMON_TLS).
	// The chart auto-generates the CA + server/client certs; the web pod and
	// init containers are wired for HTTPS automatically. Kept opt-in so the
	// default suite run stays on plain HTTP and is unaffected.
	if artifactDaemonTLSEnabled() {
		helmArgs = append(helmArgs, "--set", "artifactDaemon.tls.enabled=true")
	}
	for i, arg := range extraArgs {
		helmArgs = append(helmArgs, "--set", fmt.Sprintf("web.extraArgs[%d]=%s", i, arg))
	}
	cmd := exec.Command("helm", helmArgs...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("helm upgrade --install failed: %v", err)
	}

	// Wait for artifact daemon DaemonSet to be ready.
	log.Println("Waiting for artifact daemon to be ready...")
	daemonWait := exec.Command("kubectl",
		"--kubeconfig", kubeconfig,
		"-n", namespace,
		"wait", "--for=condition=ready", "pod",
		"-l", "app.kubernetes.io/component=artifact-daemon",
		"--timeout=120s",
	)
	daemonWait.Stdout = os.Stderr
	daemonWait.Stderr = os.Stderr
	if err := daemonWait.Run(); err != nil {
		log.Printf("warning: artifact daemon wait failed: %v", err)
		// Dump daemon pod status for diagnostics.
		descCmd := exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"-n", namespace, "describe", "pods",
			"-l", "app.kubernetes.io/component=artifact-daemon")
		descCmd.Stdout = os.Stderr
		descCmd.Stderr = os.Stderr
		descCmd.Run()
		logsCmd := exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"-n", namespace, "logs",
			"-l", "app.kubernetes.io/component=artifact-daemon",
			"--all-containers", "--tail=50")
		logsCmd.Stdout = os.Stderr
		logsCmd.Stderr = os.Stderr
		logsCmd.Run()
		log.Fatalf("artifact daemon is required for tests — aborting")
	} else {
		log.Println("Artifact daemon is ready.")
	}

	log.Println("Waiting for concourse-web pod to be ready...")
	waitCmd := exec.Command("kubectl",
		"--kubeconfig", kubeconfig,
		"-n", namespace,
		"wait", "--for=condition=ready", "pod",
		"-l", "app.kubernetes.io/component=web",
		"--timeout=300s",
	)
	waitCmd.Stdout = os.Stderr
	waitCmd.Stderr = os.Stderr
	if err := waitCmd.Run(); err != nil {
		log.Printf("=== Web pod not ready — dumping diagnostics ===")

		// Describe all pods.
		descCmd := exec.Command("kubectl",
			"--kubeconfig", kubeconfig, "-n", namespace, "describe", "pods")
		descCmd.Stdout = os.Stderr
		descCmd.Stderr = os.Stderr
		descCmd.Run()

		// Get logs from the web container (--previous to get logs from the
		// last crashed instance, since it's in CrashLoopBackOff).
		for _, prev := range []string{"", "--previous"} {
			args := []string{"--kubeconfig", kubeconfig, "-n", namespace,
				"logs", "-l", "app.kubernetes.io/component=web", "-c", "concourse-web"}
			if prev != "" {
				args = append(args, prev)
			}
			label := "current"
			if prev != "" {
				label = "previous"
			}
			log.Printf("--- web container logs (%s) ---", label)
			logsCmd := exec.Command("kubectl", args...)
			logsCmd.Stdout = os.Stderr
			logsCmd.Stderr = os.Stderr
			logsCmd.Run()
		}

		// Also get migrate-db init container logs.
		log.Printf("--- migrate-db init container logs ---")
		migrateCmd := exec.Command("kubectl",
			"--kubeconfig", kubeconfig, "-n", namespace,
			"logs", "-l", "app.kubernetes.io/component=web", "-c", "migrate-db")
		migrateCmd.Stdout = os.Stderr
		migrateCmd.Stderr = os.Stderr
		migrateCmd.Run()

		log.Fatalf("timed out waiting for concourse-web pod: %v", err)
	}
}

// portForwardManager manages an in-process port-forward tunnel to a
// Kubernetes pod using the client-go SPDY transport.
type portForwardManager struct {
	restConfig *rest.Config
	client     kubernetes.Interface
	namespace  string
	port       int
	done       chan struct{}
}

// startPortForward creates an in-process port-forward tunnel to the
// Concourse web pod on a random available port.
func startPortForward(kubeconfig, namespace string) (string, *portForwardManager) {
	port := findFreePort()

	rc, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("failed to build rest config for port-forward: %v", err)
	}

	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		log.Fatalf("failed to create K8s client for port-forward: %v", err)
	}

	mgr := &portForwardManager{
		restConfig: rc,
		client:     client,
		namespace:  namespace,
		port:       port,
		done:       make(chan struct{}),
	}

	initialReady := make(chan struct{})
	go mgr.run(initialReady)

	select {
	case <-initialReady:
	case <-time.After(30 * time.Second):
		log.Printf("warning: port-forward readiness timed out after 30s")
	}

	return fmt.Sprintf("http://localhost:%d", port), mgr
}

func (m *portForwardManager) run(initialReady chan<- struct{}) {
	first := true
	for {
		select {
		case <-m.done:
			return
		default:
		}

		var readySig chan<- struct{}
		if first {
			readySig = initialReady
		}

		err := m.forward(readySig)
		first = false

		select {
		case <-m.done:
			return
		default:
			log.Printf("Port-forward died (%v), restarting...", err)
			time.Sleep(time.Second)
		}
	}
}

func (m *portForwardManager) forward(readySig chan<- struct{}) error {
	podName, err := m.findWebPod()
	if err != nil {
		return fmt.Errorf("find web pod: %w", err)
	}

	reqURL := m.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(m.namespace).
		Name(podName).
		SubResource("portforward").
		URL()

	transport, upgrader, err := spdy.RoundTripperFor(m.restConfig)
	if err != nil {
		return fmt.Errorf("create SPDY transport: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", reqURL)

	stopChan := make(chan struct{})
	readyChan := make(chan struct{})

	forwarding := make(chan struct{})
	go func() {
		select {
		case <-m.done:
			close(stopChan)
		case <-forwarding:
		}
	}()

	fw, err := portforward.New(
		dialer,
		[]string{fmt.Sprintf("%d:8080", m.port)},
		stopChan,
		readyChan,
		os.Stderr,
		os.Stderr,
	)
	if err != nil {
		close(forwarding)
		return fmt.Errorf("create port forwarder: %w", err)
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- fw.ForwardPorts()
		close(forwarding)
	}()

	select {
	case <-readyChan:
		log.Printf("Port-forward ready on localhost:%d -> %s:8080", m.port, podName)
		if readySig != nil {
			close(readySig)
		}
	case err := <-errChan:
		return fmt.Errorf("port-forward failed before ready: %w", err)
	}

	return <-errChan
}

func (m *portForwardManager) findWebPod() (string, error) {
	pods, err := m.client.CoreV1().Pods(m.namespace).List(
		context.Background(),
		metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=web"},
	)
	if err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return pod.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no ready pod with label app.kubernetes.io/component=web in namespace %q", m.namespace)
}

func (m *portForwardManager) Stop() {
	close(m.done)
}

// waitForAPI polls the Concourse /api/v1/info endpoint until it responds.
func waitForAPI(url string, timeout time.Duration) {
	client := &http.Client{Timeout: 5 * time.Second}
	log.Printf("Waiting for Concourse API at %s...", url)
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			log.Fatalf("timed out waiting for Concourse API at %s after %s", url, timeout)
		}
		resp, err := client.Get(url + "/api/v1/info")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				log.Println("Concourse API is ready.")
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func mustRepoRoot() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		if _, statErr := os.Stat("/src/go.mod"); statErr == nil {
			return "/src"
		}
		log.Fatalf("failed to find repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// resolveEndpoint resolves hostnames in a host:port endpoint to IP addresses.
func resolveEndpoint(endpoint string) string {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	if ip := net.ParseIP(host); ip != nil {
		return endpoint
	}
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		log.Printf("warning: cannot resolve %q, using as-is: %v", host, err)
		return endpoint
	}
	resolved := addrs[0] + ":" + port
	log.Printf("Resolved OTLP endpoint %s -> %s", endpoint, resolved)
	return resolved
}

// deleteK3sCluster terminates the K3s testcontainer unless SKIP_TEARDOWN is set.
func deleteK3sCluster() {
	if os.Getenv("SKIP_TEARDOWN") == "1" {
		log.Printf("SKIP_TEARDOWN=1: keeping K3s cluster running")
		return
	}
	if k3sContainer != nil {
		log.Println("Terminating K3s cluster...")
		if err := testcontainers.TerminateContainer(k3sContainer); err != nil {
			log.Printf("warning: failed to terminate K3s container: %v", err)
		}
	}
}
