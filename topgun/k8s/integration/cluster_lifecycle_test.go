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

// ensureKindCluster creates a KinD cluster if it doesn't already exist.
// Returns the kubeconfig path and whether a new cluster was created.
func ensureKindCluster(name string) (string, bool) {
	out, err := exec.Command("kind", "get", "clusters").Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == name {
				log.Printf("KinD cluster %q already exists, reusing", name)
				return writeKindKubeconfig(name), false
			}
		}
	}

	log.Printf("Creating KinD cluster %q...", name)
	cmd := exec.Command("kind", "create", "cluster", "--name", name, "--wait", "120s")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("failed to create KinD cluster: %v", err)
	}

	return writeKindKubeconfig(name), true
}

// writeKindKubeconfig writes the kubeconfig for the given cluster to a temp file.
func writeKindKubeconfig(name string) string {
	out, err := exec.Command("kind", "get", "kubeconfig", "--name", name).Output()
	if err != nil {
		log.Fatalf("failed to get kubeconfig for KinD cluster: %v", err)
	}

	path := filepath.Join(os.TempDir(), fmt.Sprintf("kind-kubeconfig-%s", name))
	if err := os.WriteFile(path, out, 0600); err != nil {
		log.Fatalf("failed to write kubeconfig: %v", err)
	}

	return path
}

// loadImagesIntoKind ensures all required Docker images are available
// inside the KinD cluster.
func loadImagesIntoKind(concourseImage, clusterName string) {
	nodeName := clusterName + "-control-plane"

	// Load the locally-built Concourse image into KinD.
	if err := exec.Command("docker", "image", "inspect", concourseImage).Run(); err != nil {
		log.Printf("Concourse image %q not found locally, building from source...", concourseImage)
		root := mustRepoRoot()
		cmd := exec.Command("docker", "build", "-f", "Dockerfile.build", "-t", concourseImage, root)
		cmd.Dir = root
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("failed to build Concourse image: %v", err)
		}
	}
	kindLoadImage(concourseImage, clusterName)

	// Pre-pull registry images directly inside the KinD node.
	registryImages := []string{
		"docker.io/library/postgres:16",
		"docker.io/concourse/mock-resource:latest",
		"docker.io/library/busybox:latest",
		"docker.io/concourse/registry-image-resource:latest",
		"docker.io/library/nginx:alpine",
		"docker.io/library/alpine:3.19",
	}
	for _, img := range registryImages {
		if exec.Command("docker", "exec", nodeName, "crictl", "inspecti", "-q", img).Run() == nil {
			log.Printf("Image %q already present in KinD node %q, skipping pull", img, nodeName)
			continue
		}
		log.Printf("Pre-pulling %q inside KinD node %q...", img, nodeName)
		cmd := exec.Command("docker", "exec", nodeName, "crictl", "pull", img)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("failed to pre-pull %s in KinD node: %v", img, err)
		}
	}
}

// kindLoadImage loads a locally-built Docker image into a KinD cluster.
func kindLoadImage(img, clusterName string) {
	log.Printf("Loading %q into KinD cluster %q...", img, clusterName)
	cmd := exec.Command("kind", "load", "docker-image", img, "--name", clusterName)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err == nil {
		return
	}

	// Fallback: docker save → temp file → kind load image-archive.
	log.Printf("Direct load failed, falling back to image-archive for %q...", img)
	archive, err := os.CreateTemp("", "kind-image-*.tar")
	if err != nil {
		log.Fatalf("failed to create temp archive: %v", err)
	}
	defer os.Remove(archive.Name())
	archive.Close()

	saveCmd := exec.Command("docker", "save", "-o", archive.Name(), img)
	saveCmd.Stdout = os.Stderr
	saveCmd.Stderr = os.Stderr
	if err := saveCmd.Run(); err != nil {
		log.Fatalf("docker save %s failed: %v", img, err)
	}

	loadCmd := exec.Command("kind", "load", "image-archive", archive.Name(), "--name", clusterName)
	loadCmd.Stdout = os.Stderr
	loadCmd.Stderr = os.Stderr
	if err := loadCmd.Run(); err != nil {
		log.Fatalf("kind load image-archive %s failed: %v", img, err)
	}
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
		"--timeout=180s",
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
