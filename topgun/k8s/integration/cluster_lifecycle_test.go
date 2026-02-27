package integration_test

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
const kindClusterName = "concourse-integration"

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
	kubeconfigPath := filepath.Join(os.TempDir(), "kind-kubeconfig-integration")

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
// pre-loaded — they are pulled by the kubelet at runtime.
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
		"--set", "image.pullPolicy=IfNotPresent",
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

// startPortForward starts a kubectl port-forward to the Concourse
// web service on a random available port.
func startPortForward(kubeconfig, namespace string) (string, *exec.Cmd) {
	port := findFreePort()

	svcName := discoverWebService(kubeconfig, namespace)

	log.Printf("Starting port-forward on localhost:%d -> svc/%s:8080", port, svcName)
	cmd := exec.Command("kubectl",
		"--kubeconfig", kubeconfig,
		"-n", namespace,
		"port-forward", "svc/"+svcName,
		fmt.Sprintf("%d:8080", port),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Fatalf("failed to start port-forward: %v", err)
	}

	time.Sleep(2 * time.Second)

	return fmt.Sprintf("http://localhost:%d", port), cmd
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
