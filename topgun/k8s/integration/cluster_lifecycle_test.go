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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
)

// kindClusterName is the KinD cluster used by this suite. A unique name
// avoids collisions with the user's other clusters.
var kindClusterName = "concourse-integration"

// kindProvider is the Go-native KinD provider. Using the library API
// instead of shelling out to the `kind` CLI makes the tests fully
// self-contained — only docker, helm, and kubectl are needed on PATH.
var kindProvider *cluster.Provider

func init() {
	kindProvider = cluster.NewProvider(
		cluster.ProviderWithDocker(),
	)
}

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
// Note: kind is NOT required — cluster lifecycle is managed via the Go library.
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

// createKindCluster creates an ephemeral KinD cluster via the Go library
// and returns the path to a kubeconfig file written to disk.
// Any pre-existing cluster with the same name is deleted first.
func createKindCluster() string {
	kubeconfigPath := filepath.Join(os.TempDir(), "kind-kubeconfig-"+kindClusterName)

	// Delete any leftover cluster from a previous interrupted run.
	kindProvider.Delete(kindClusterName, "")

	// KinD config with extended timeouts for DinD environments where
	// kubeadm init is slower due to nested filesystems (fuse-overlayfs/vfs).
	// The default controlPlaneComponentHealthCheck is 4m which is too short.
	// KinD v0.27.0 uses K8s 1.32 which requires kubeadm v1beta4.
	kindConfig := []byte(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
kubeadmConfigPatches:
- |
  apiVersion: kubeadm.k8s.io/v1beta4
  kind: ClusterConfiguration
  timeouts:
    controlPlaneComponentHealthCheck: 10m0s
`)

	log.Printf("Creating KinD cluster %q...", kindClusterName)
	err := kindProvider.Create(kindClusterName,
		cluster.CreateWithRawConfig(kindConfig),
		cluster.CreateWithWaitForReady(10*time.Minute),
		cluster.CreateWithDisplayUsage(false),
		cluster.CreateWithDisplaySalutation(false),
	)
	if err != nil {
		log.Fatalf("failed to create KinD cluster: %v", err)
	}

	// Export kubeconfig to a file for helm/kubectl CLI commands.
	kubeconfig, err := kindProvider.KubeConfig(kindClusterName, false)
	if err != nil {
		log.Fatalf("failed to get kubeconfig from KinD: %v", err)
	}
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600); err != nil {
		log.Fatalf("failed to write kubeconfig: %v", err)
	}

	log.Printf("KinD cluster ready (kubeconfig: %s)", kubeconfigPath)
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
// should be pre-loaded into the KinD node. These are pulled by the host
// Docker daemon and loaded via tar archive into KinD nodes, which avoids
// requiring the KinD node to have outbound internet access (e.g. when
// running inside Colima with restricted networking).
var testDependencyImages = []string{
	"docker.io/library/busybox:latest",
	"docker.io/library/alpine:latest",
	"docker.io/concourse/mock-resource:latest",
}

// loadImagesIntoKind loads the locally-built Concourse image and test
// dependency images into the KinD cluster. Images are exported from the
// host Docker daemon as tar archives and imported into each KinD node's
// containerd via the Go library.
func loadImagesIntoKind(concourseImage string) {
	log.Printf("Loading local image %s into KinD cluster...", concourseImage)

	nodeList, err := kindProvider.ListInternalNodes(kindClusterName)
	if err != nil {
		log.Fatalf("failed to list KinD nodes: %v", err)
	}

	// Save the locally-built concourse image to a temp tar archive and
	// import into each KinD node. We use the Go library for this because
	// the local image is single-arch and doesn't have the multi-arch
	// issues that affect registry images.
	tmpFile, err := os.CreateTemp("", "kind-image-*.tar")
	if err != nil {
		log.Fatalf("failed to create temp file for image archive: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	cmd := exec.Command("docker", "save", "-o", tmpFile.Name(), concourseImage)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("docker save failed for %s: %v", concourseImage, err)
	}

	for _, node := range nodeList {
		f, err := os.Open(tmpFile.Name())
		if err != nil {
			log.Fatalf("failed to open image archive: %v", err)
		}
		if err := nodeutils.LoadImageArchive(node, f); err != nil {
			f.Close()
			log.Fatalf("failed to load image onto node %s: %v", node.String(), err)
		}
		f.Close()
	}
	log.Println("Local image loaded into KinD cluster.")

	// Pre-load test dependency images into the KinD cluster.
	// Pull on host first, then docker save (produces single-arch tar) and
	// load via nodeutils.LoadImageArchive. This avoids the multi-arch
	// "content digest not found" bug with `kind load docker-image` on
	// containerd image stores (Docker Desktop / Colima).
	for _, img := range testDependencyImages {
		log.Printf("Pre-pulling %s on host...", img)
		pullCmd := exec.Command("docker", "pull", "--quiet", img)
		pullCmd.Stdout = os.Stderr
		pullCmd.Stderr = os.Stderr
		if err := pullCmd.Run(); err != nil {
			log.Printf("warning: failed to pull %s on host: %v", img, err)
			continue
		}

		imgTar, err := os.CreateTemp("", "kind-dep-*.tar")
		if err != nil {
			log.Printf("warning: failed to create temp file for %s: %v", img, err)
			continue
		}
		imgTar.Close()

		saveCmd := exec.Command("docker", "save", "-o", imgTar.Name(), img)
		saveCmd.Stdout = os.Stderr
		saveCmd.Stderr = os.Stderr
		if err := saveCmd.Run(); err != nil {
			log.Printf("warning: docker save failed for %s: %v", img, err)
			os.Remove(imgTar.Name())
			continue
		}

		log.Printf("Loading %s into KinD nodes...", img)
		for _, node := range nodeList {
			f, err := os.Open(imgTar.Name())
			if err != nil {
				log.Printf("warning: failed to open archive for %s: %v", img, err)
				continue
			}
			if err := nodeutils.LoadImageArchive(node, f); err != nil {
				log.Printf("warning: failed to load %s onto node %s: %v", img, node.String(), err)
			}
			f.Close()
		}
		os.Remove(imgTar.Name())
	}
}


// helmDeployConcourse deploys Concourse via the local Helm chart.
func helmDeployConcourse(kubeconfig, namespace, chartPath, image string) {
	repo, tag := splitImageRef(image)

	// Create namespace (ignore if exists).
	exec.Command("kubectl", "--kubeconfig", kubeconfig,
		"create", "namespace", namespace).Run()

	log.Printf("Deploying Concourse chart from %s into namespace %s...", chartPath, namespace)

	// Build the list of extra args for the web node.
	extraArgs := []string{}

	// When OTEL_EXPORTER_OTLP_ENDPOINT is set, enable server-side tracing.
	// The endpoint must be reachable from inside the KinD cluster. We resolve
	// hostnames to IPs because KinD nodes may not have host DNS entries
	// (e.g., host.docker.internal). For macOS, port-forward Tempo gRPC to
	// localhost and set OTEL_EXPORTER_OTLP_ENDPOINT=host.docker.internal:4317.
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

	// Helm --set for arrays: web.extraArgs[0]=...,web.extraArgs[1]=...
	helmArgs := []string{
		"upgrade", "--install", "concourse", chartPath,
		"--namespace", namespace,
		"--kubeconfig", kubeconfig,
		"--set", fmt.Sprintf("image.repository=%s", repo),
		"--set", fmt.Sprintf("image.tag=%s", tag),
		"--set", "image.pullPolicy=IfNotPresent",
		"--timeout", "5m",
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
		log.Fatalf("timed out waiting for concourse-web pod: %v", err)
	}
}

// portForwardManager manages an in-process port-forward tunnel to a
// Kubernetes pod using the client-go SPDY transport. Unlike shelling out
// to kubectl, this runs entirely in-process — no subprocess management,
// no watchdog. If the connection drops, it automatically reconnects.
type portForwardManager struct {
	restConfig *rest.Config
	client     kubernetes.Interface
	namespace  string
	port       int
	done       chan struct{}
}

// startPortForward creates an in-process port-forward tunnel to the
// Concourse web pod on a random available port. The tunnel auto-reconnects
// if the underlying SPDY connection drops.
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

	// Start tunnel in background with auto-reconnect; block until ready.
	initialReady := make(chan struct{})
	go mgr.run(initialReady)

	select {
	case <-initialReady:
		// Tunnel is accepting connections.
	case <-time.After(30 * time.Second):
		log.Printf("warning: port-forward readiness timed out after 30s")
	}

	return fmt.Sprintf("http://localhost:%d", port), mgr
}

// run maintains the port-forward tunnel, reconnecting if it drops.
// Closes initialReady on the first successful connection.
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

// forward establishes a single port-forward tunnel and blocks until it
// exits. If readySig is non-nil, it is closed when the tunnel is ready.
func (m *portForwardManager) forward(readySig chan<- struct{}) error {
	podName, err := m.findWebPod()
	if err != nil {
		return fmt.Errorf("find web pod: %w", err)
	}

	// Build SPDY dialer targeting the pod's portforward subresource.
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

	// Bridge m.done → stopChan so Stop() terminates this tunnel.
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
		os.Stderr, // informational output (e.g. "Forwarding from ...")
		os.Stderr, // error output
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

	// Wait for the tunnel to be ready or fail.
	select {
	case <-readyChan:
		log.Printf("Port-forward ready on localhost:%d -> %s:8080", m.port, podName)
		if readySig != nil {
			close(readySig)
		}
	case err := <-errChan:
		return fmt.Errorf("port-forward failed before ready: %w", err)
	}

	// Block until the tunnel exits.
	return <-errChan
}

// findWebPod returns the name of a Ready pod with the Concourse web label.
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
		log.Fatalf("failed to find repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// resolveEndpoint resolves hostnames in a host:port endpoint to IP addresses.
// This is needed because KinD nodes don't have the host machine's DNS
// entries (e.g., host.docker.internal won't resolve inside a KinD pod).
func resolveEndpoint(endpoint string) string {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint // not host:port, return as-is
	}
	// If it's already an IP, nothing to do.
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

// deleteKindCluster deletes the KinD cluster via the Go library
// unless SKIP_TEARDOWN is set.
func deleteKindCluster() {
	if os.Getenv("SKIP_TEARDOWN") == "1" {
		log.Printf("SKIP_TEARDOWN=1: keeping KinD cluster %q running", kindClusterName)
		return
	}
	log.Printf("Deleting KinD cluster %q...", kindClusterName)
	if err := kindProvider.Delete(kindClusterName, ""); err != nil {
		log.Printf("warning: failed to delete KinD cluster: %v", err)
	}
}
