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

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
)

const k3sImage = "rancher/k3s:v1.27.1-k3s1"

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
// Docker is required by testcontainers. Helm and kubectl are used for
// deploying Concourse and port-forwarding.
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

// createK3sCluster creates an ephemeral k3s cluster via testcontainers
// and returns the container and a path to the kubeconfig file.
func createK3sCluster(ctx context.Context) (*k3s.K3sContainer, string) {
	log.Printf("Creating k3s cluster via testcontainers (image: %s)...", k3sImage)
	k3sContainer, err := k3s.Run(ctx, k3sImage)
	if err != nil {
		log.Fatalf("failed to start k3s container: %v", err)
	}

	kubeConfigYaml, err := k3sContainer.GetKubeConfig(ctx)
	if err != nil {
		log.Fatalf("failed to get kubeconfig from k3s container: %v", err)
	}

	kubeconfigPath := filepath.Join(os.TempDir(), "k3s-kubeconfig-behavioral")
	if err := os.WriteFile(kubeconfigPath, kubeConfigYaml, 0600); err != nil {
		log.Fatalf("failed to write kubeconfig: %v", err)
	}

	log.Printf("k3s cluster ready (kubeconfig: %s)", kubeconfigPath)
	return k3sContainer, kubeconfigPath
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

// loadImagesIntoK3s loads all required Docker images into the k3s container.
func loadImagesIntoK3s(ctx context.Context, container *k3s.K3sContainer, concourseImage string) {
	allImages := []string{
		concourseImage,
		"docker.io/library/postgres:16",
		"docker.io/concourse/mock-resource:latest",
		"docker.io/library/busybox:latest",
		"docker.io/concourse/registry-image-resource:latest",
		"docker.io/library/nginx:alpine",
		"docker.io/library/alpine:3.19",
	}

	log.Printf("Loading %d images into k3s cluster...", len(allImages))
	if err := container.LoadImages(ctx, allImages...); err != nil {
		log.Fatalf("failed to load images into k3s: %v", err)
	}
	log.Println("All images loaded into k3s cluster.")
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

// terminateK3sCluster terminates the k3s container unless SKIP_TEARDOWN is set.
func terminateK3sCluster(container *k3s.K3sContainer) {
	if os.Getenv("SKIP_TEARDOWN") == "1" {
		log.Println("SKIP_TEARDOWN=1: keeping k3s container running")
		return
	}
	log.Println("Terminating k3s container...")
	if err := testcontainers.TerminateContainer(container); err != nil {
		log.Printf("warning: failed to terminate k3s container: %v", err)
	}
}
