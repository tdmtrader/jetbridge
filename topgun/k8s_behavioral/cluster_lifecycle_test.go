package behavioral_test

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
)

// kindClusterName is the KinD cluster used by this suite. A unique name
// avoids collisions with the user's other clusters.
const kindClusterName = "concourse-behavioral"

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
	kubeconfigPath := filepath.Join(os.TempDir(), "kind-kubeconfig-behavioral")

	// Delete any leftover cluster from a previous interrupted run.
	kindProvider.Delete(kindClusterName, "")

	log.Printf("Creating KinD cluster %q...", kindClusterName)
	err := kindProvider.Create(kindClusterName,
		cluster.CreateWithWaitForReady(120*time.Second),
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
	helmArgs := []string{
		"upgrade", "--install", "concourse", chartPath,
		"--namespace", namespace,
		"--kubeconfig", kubeconfig,
		"--set", fmt.Sprintf("image.repository=%s", repo),
		"--set", fmt.Sprintf("image.tag=%s", tag),
		"--set", "image.pullPolicy=IfNotPresent", // only the Concourse image is pre-loaded
		// Reduce ATC polling intervals from 10s defaults to speed up
		// build scheduling in integration tests.
		"--set", "web.extraArgs={--component-runner-interval=2s,--gc-interval=2s}",
		"--timeout", "5m",
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

// portForwardManager manages a kubectl port-forward process that
// automatically restarts when the connection drops. This is necessary
// because kubectl port-forward is unreliable over long-running test suites.
type portForwardManager struct {
	kubeconfig string
	namespace  string
	svcName    string
	port       int
	done       chan struct{}
	cmd        *exec.Cmd
}

// startPortForward starts a kubectl port-forward to the Concourse
// web service on a random available port. The returned manager auto-restarts
// the port-forward if it dies.
func startPortForward(kubeconfig, namespace string) (string, *portForwardManager) {
	port := findFreePort()
	svcName := discoverWebService(kubeconfig, namespace)

	mgr := &portForwardManager{
		kubeconfig: kubeconfig,
		namespace:  namespace,
		svcName:    svcName,
		port:       port,
		done:       make(chan struct{}),
	}

	mgr.start()

	// Start watchdog that restarts port-forward on crash.
	go mgr.watchdog()

	return fmt.Sprintf("http://localhost:%d", port), mgr
}

func (m *portForwardManager) start() {
	log.Printf("Starting port-forward on localhost:%d -> svc/%s:8080", m.port, m.svcName)
	m.cmd = exec.Command("kubectl",
		"--kubeconfig", m.kubeconfig,
		"-n", m.namespace,
		"port-forward", "svc/"+m.svcName,
		fmt.Sprintf("%d:8080", m.port),
	)
	m.cmd.Stdout = os.Stderr
	m.cmd.Stderr = os.Stderr
	if err := m.cmd.Start(); err != nil {
		log.Fatalf("failed to start port-forward: %v", err)
	}
	// Wait until the port-forward is actually accepting connections
	// rather than using a fixed sleep. This prevents tests from hitting
	// "connection refused" during port-forward restarts.
	m.waitForReady()
}

// waitForReady polls the local port until it accepts TCP connections,
// confirming the port-forward is actually ready to proxy traffic.
func (m *portForwardManager) waitForReady() {
	addr := fmt.Sprintf("127.0.0.1:%d", m.port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			log.Printf("Port-forward on localhost:%d is ready", m.port)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	log.Printf("warning: port-forward readiness check timed out after 30s")
}

func (m *portForwardManager) watchdog() {
	for {
		if m.cmd != nil && m.cmd.Process != nil {
			m.cmd.Wait()
		}
		select {
		case <-m.done:
			return
		default:
			log.Printf("Port-forward died, restarting...")
			time.Sleep(1 * time.Second)
			m.start()
		}
	}
}

func (m *portForwardManager) Stop() {
	close(m.done)
	if m.cmd != nil && m.cmd.Process != nil {
		m.cmd.Process.Kill()
		m.cmd.Wait()
	}
}

// discoverWebService finds the Concourse web service name in the namespace.
func discoverWebService(kubeconfig, namespace string) string {
	out, err := exec.Command("kubectl",
		"--kubeconfig", kubeconfig,
		"-n", namespace,
		"get", "svc",
		"-l", "app.kubernetes.io/component=web",
		"-o", "jsonpath={.items[0].metadata.name}",
	).Output()
	if err == nil && len(out) > 0 {
		return strings.TrimSpace(string(out))
	}
	for _, name := range []string{"concourse-web", "concourse-concourse-jetbridge-web"} {
		if exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", "svc", name).Run() == nil {
			return name
		}
	}
	log.Fatalf("could not find Concourse web service in namespace %s", namespace)
	return ""
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

// preloadImages pulls commonly-used images directly into the KinD node
// via `crictl pull`. This avoids Docker Hub rate limits and TLS timeout
// failures that occur when kubelet tries to pull images at test time.
//
// We use `crictl pull` inside the KinD node rather than `kind load
// docker-image` because the latter fails for multi-platform registry
// images when Docker Desktop uses a containerd image store (the
// same issue documented in loadImagesIntoKind).
func preloadImages() {
	images := []string{
		"docker.io/concourse/mock-resource:latest",
		"docker.io/library/busybox:latest",
		"docker.io/library/alpine:3.19",
		"docker.io/library/alpine:latest",
		"docker.io/library/nginx:alpine",
	}
	node := kindClusterName + "-control-plane"

	for _, image := range images {
		log.Printf("Pulling %s directly into KinD node via crictl...", image)
		cmd := exec.Command("docker", "exec", node, "crictl", "pull", image)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// Non-fatal: some images may not be needed by all tests.
			log.Printf("warning: failed to pull %s into KinD node: %v", image, err)
		}
	}
	log.Printf("Image preloading complete.")
}

// tuneReaperInterval reduces the K8s Worker Reaper component interval in the
// database. The reaper runs on a 30s interval by default and is the component
// that detects "destroying" containers and actually deletes K8s pods. After a
// build completes, exec-mode pods are deliberately left running (for fly hijack)
// and cleaned up by the reaper. This 30s interval dominates pod-cleanup time
// in tests that assert pods are gone after pipeline teardown.
func tuneReaperInterval(kubeconfig, namespace, interval string) {
	log.Printf("Tuning k8s_worker_reaper interval to %s...", interval)
	cmd := exec.Command("kubectl",
		"--kubeconfig", kubeconfig,
		"-n", namespace,
		"exec", "deploy/concourse-concourse-jetbridge-db", "--",
		"psql", "-U", "concourse", "-c",
		fmt.Sprintf("UPDATE components SET interval = '%s' WHERE name = 'k8s_worker_reaper';", interval),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("warning: failed to tune reaper interval: %v", err)
	}
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
