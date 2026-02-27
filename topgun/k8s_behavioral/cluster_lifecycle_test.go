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
)

// kindClusterName is the KinD cluster used by this suite. A unique name
// avoids collisions with the user's other clusters.
const kindClusterName = "concourse-behavioral"

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
	for _, bin := range []string{"docker", "kind", "helm", "kubectl"} {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required CLIs on PATH: %s", strings.Join(missing, ", "))
	}
	return nil
}

// createKindCluster creates an ephemeral KinD cluster and returns the
// path to the generated kubeconfig file. Any pre-existing cluster with
// the same name is deleted first to avoid conflicts.
func createKindCluster() string {
	kubeconfigPath := filepath.Join(os.TempDir(), "kind-kubeconfig-behavioral")

	// Delete any leftover cluster from a previous interrupted run.
	exec.Command("kind", "delete", "cluster", "--name", kindClusterName).Run()

	log.Printf("Creating KinD cluster %q...", kindClusterName)
	cmd := exec.Command("kind", "create", "cluster",
		"--name", kindClusterName,
		"--kubeconfig", kubeconfigPath,
		"--wait", "120s",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("failed to create KinD cluster: %v", err)
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
// KinD cluster. Public images (postgres, busybox, etc.) are NOT
// pre-loaded — they are pulled by the kubelet at runtime.  Only the
// locally-built image needs pre-loading because it is not available on
// any registry.
//
// Note: Docker with containerd image store can cause `ctr images import`
// failures for multi-platform registry images. Limiting pre-loading to
// the local image avoids this issue entirely.
func loadImagesIntoKind(concourseImage string) {
	log.Printf("Loading local image %s into KinD cluster...", concourseImage)
	cmd := exec.Command("kind", "load", "docker-image", concourseImage, "--name", kindClusterName)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("failed to load image %s into KinD: %v", concourseImage, err)
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
		"--set", "web.extraArgs={--component-runner-interval=2s,--build-tracker-interval=2s,--gc-interval=2s}",
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

// tuneSchedulerInterval reduces the ATC scheduler component interval in the
// database. The scheduler's interval is hard-coded at 10s in the ATC source
// and not exposed as a CLI flag. For integration tests, this delay dominates
// per-test runtime since every build must wait for the scheduler to poll.
func tuneSchedulerInterval(kubeconfig, namespace, interval string) {
	log.Printf("Tuning scheduler interval to %s...", interval)
	cmd := exec.Command("kubectl",
		"--kubeconfig", kubeconfig,
		"-n", namespace,
		"exec", "deploy/concourse-concourse-jetbridge-db", "--",
		"psql", "-U", "concourse", "-c",
		fmt.Sprintf("UPDATE components SET interval = '%s' WHERE name = 'scheduler';", interval),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("warning: failed to tune scheduler interval: %v", err)
	}
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

// deleteKindCluster deletes the KinD cluster unless SKIP_TEARDOWN is set.
func deleteKindCluster() {
	if os.Getenv("SKIP_TEARDOWN") == "1" {
		log.Printf("SKIP_TEARDOWN=1: keeping KinD cluster %q running", kindClusterName)
		return
	}
	log.Printf("Deleting KinD cluster %q...", kindClusterName)
	cmd := exec.Command("kind", "delete", "cluster", "--name", kindClusterName)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("warning: failed to delete KinD cluster: %v", err)
	}
}
