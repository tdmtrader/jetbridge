package behavioral_test

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

// kindClusterName is the KinD cluster used by this Ginkgo process.
// In parallel mode (--procs=N), each process sets a unique name in
// SynchronizedBeforeSuite (e.g., "concourse-behavioral-2").
var kindClusterName = "concourse-behavioral"

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

	// Use K8s 1.31 node image: KinD generates kubeadm v1beta3 config, and
	// timeoutForControlPlane was removed from v1beta3 in K8s 1.32+. Using
	// 1.31 lets us set the timeout to survive slow DinD environments.
	kindConfig := []byte(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.31.6@sha256:28b7cbb993dfe093c76641a0c95807637213c9109b761f1d422c32ee50f7b8ad
kubeadmConfigPatches:
- |
  kind: ClusterConfiguration
  timeoutForControlPlane: 10m0s
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

// loadImagesIntoKind loads the locally-built Concourse image into the
// KinD cluster via the Go library. The image is exported from the local
// Docker daemon as a tar archive and imported into each KinD node's
// containerd. Only the locally-built image needs pre-loading because
// it is not available on any registry.
func loadImagesIntoKind(concourseImage string) {
	log.Printf("Loading local image %s into KinD cluster...", concourseImage)

	nodeList, err := kindProvider.ListInternalNodes(kindClusterName)
	if err != nil {
		log.Fatalf("failed to list KinD nodes: %v", err)
	}

	// Save docker image to a temp tar archive.
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

	// Load the tar archive onto each KinD node.
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
}

// helmDeployConcourse deploys Concourse via the local Helm chart.
// Does not use `helm --wait` because PVCs with WaitForFirstConsumer
// binding mode won't bind until a build pod mounts them. Instead,
// waits explicitly for the web pod to become ready.
func helmDeployConcourse(kubeconfig, namespace, chartPath, image string) {
	repo, tag := splitImageRef(image)

	// Create namespace (ignore if exists).
	exec.Command("kubectl", "--kubeconfig", kubeconfig,
		"create", "namespace", namespace).Run()

	log.Printf("Deploying Concourse chart from %s into namespace %s...", chartPath, namespace)
	// Base extra args for Concourse web.
	extraArgs := ""

	// When COLLECT_OTEL is set, inject OTel exporter addresses so Concourse
	// sends metrics and traces to the in-cluster OTel collector.
	if os.Getenv("COLLECT_OTEL") == "1" {
		otelAddr := fmt.Sprintf("otel-collector.%s.svc.cluster.local:4317", namespace)
		extraArgs = "--tracing-otlp-address=" + otelAddr + ",--otel-metrics-otlp-address=" + otelAddr
		log.Printf("OTel collection enabled, exporting to %s", otelAddr)
	}

	helmArgs := []string{
		"upgrade", "--install", "concourse", chartPath,
		"--namespace", namespace,
		"--kubeconfig", kubeconfig,
		"--set", fmt.Sprintf("image.repository=%s", repo),
		"--set", fmt.Sprintf("image.tag=%s", tag),
		"--set", "image.pullPolicy=IfNotPresent", // only the Concourse image is pre-loaded
		// Use emptyDir for PostgreSQL — ephemeral KinD clusters don't need
		// persistent storage, and KinD's local-path-provisioner struggles
		// under resource contention when multiple clusters run in parallel.
		"--set", "postgresql.persistence.enabled=false",
		"--timeout", "5m",
	}

	if extraArgs != "" {
		helmArgs = append(helmArgs, "--set", fmt.Sprintf("web.extraArgs={%s}", extraArgs))
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

// preloadImages pulls commonly-used images on the host Docker daemon
// and loads them into the KinD node via tar archive. This avoids
// requiring the KinD node's containerd to have outbound internet
// access (e.g. when running inside Colima with restricted networking)
// and also avoids Docker Hub rate limits.
func preloadImages() {
	images := []string{
		"docker.io/concourse/mock-resource:latest",
		"docker.io/library/busybox:latest",
		"docker.io/library/alpine:3.19",
		"docker.io/library/alpine:latest",
		"docker.io/library/nginx:alpine",
	}

	nodeList, err := kindProvider.ListInternalNodes(kindClusterName)
	if err != nil {
		log.Printf("warning: failed to list KinD nodes for image preload: %v", err)
		return
	}

	for _, image := range images {
		log.Printf("Pre-pulling %s on host...", image)
		pullCmd := exec.Command("docker", "pull", "--quiet", image)
		pullCmd.Stdout = os.Stderr
		pullCmd.Stderr = os.Stderr
		if err := pullCmd.Run(); err != nil {
			log.Printf("warning: failed to pull %s on host: %v", image, err)
			continue
		}

		imgTar, err := os.CreateTemp("", "kind-dep-*.tar")
		if err != nil {
			log.Printf("warning: failed to create temp file for %s: %v", image, err)
			continue
		}
		imgTar.Close()

		saveCmd := exec.Command("docker", "save", "-o", imgTar.Name(), image)
		saveCmd.Stdout = os.Stderr
		saveCmd.Stderr = os.Stderr
		if err := saveCmd.Run(); err != nil {
			log.Printf("warning: docker save failed for %s: %v", image, err)
			os.Remove(imgTar.Name())
			continue
		}

		log.Printf("Loading %s into KinD nodes...", image)
		for _, node := range nodeList {
			f, err := os.Open(imgTar.Name())
			if err != nil {
				log.Printf("warning: failed to open archive for %s: %v", image, err)
				continue
			}
			if err := nodeutils.LoadImageArchive(node, f); err != nil {
				log.Printf("warning: failed to load %s onto node %s: %v", image, node.String(), err)
			}
			f.Close()
		}
		os.Remove(imgTar.Name())
	}
	log.Printf("Image preloading complete.")
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
