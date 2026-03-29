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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// k3sImage is the K3s image used for the test cluster.
var k3sImage = "rancher/k3s:v1.31.6-k3s1"

// k3sContainer holds the testcontainers K3s instance for this Ginkgo process.
// In parallel mode (--procs=N), each process gets its own K3s container.
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

// createK3sCluster creates an ephemeral K3s cluster via testcontainers.
// K3s replaces KinD — no kubeadm, no nested containerd, no timeout patches.
func createK3sCluster() string {
	ctx := context.Background()
	kubeconfigPath := filepath.Join(os.TempDir(), "k3s-kubeconfig-behavioral")

	log.Printf("Creating K3s cluster via testcontainers (%s)...", k3sImage)
	var err error
	k3sContainer, err = k3s.Run(ctx, k3sImage)
	if err != nil {
		log.Fatalf("failed to create K3s cluster: %v", err)
	}

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

// loadImagesIntoCluster loads the locally-built Concourse image and test
// dependency images into the K3s cluster via testcontainers' LoadImages API.
func loadImagesIntoCluster(concourseImage string) {
	ctx := context.Background()

	log.Printf("Loading %s into K3s cluster...", concourseImage)
	if err := k3sContainer.LoadImages(ctx, concourseImage); err != nil {
		log.Fatalf("failed to load image %s into K3s: %v", concourseImage, err)
	}
	log.Println("Concourse image loaded.")

	// Pull and load test dependency images.
	images := []string{
		"docker.io/rancher/mirrored-pause:3.6", // K3s sandbox image — must be pre-loaded since K3s can't resolve DNS in DinD
		"docker.io/library/postgres:16",
		"docker.io/concourse/mock-resource:latest",
		"docker.io/library/busybox:latest",
		"docker.io/library/alpine:3.19",
		"docker.io/library/alpine:latest",
		"docker.io/library/nginx:alpine",
	}

	for _, img := range images {
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

// helmDeployConcourse deploys Concourse via the local Helm chart.
func helmDeployConcourse(kubeconfig, namespace, chartPath, image string) {
	repo, tag := splitImageRef(image)

	exec.Command("kubectl", "--kubeconfig", kubeconfig,
		"create", "namespace", namespace).Run()

	log.Printf("Deploying Concourse chart from %s into namespace %s...", chartPath, namespace)
	extraArgs := ""

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
		"--set", "image.pullPolicy=IfNotPresent",
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
		descCmd := exec.Command("kubectl", "--kubeconfig", kubeconfig,
			"-n", namespace, "describe", "pods")
		descCmd.Stdout = os.Stderr
		descCmd.Stderr = os.Stderr
		descCmd.Run()
		log.Fatalf("timed out waiting for concourse-web pod: %v", err)
	}
}

// portForwardManager manages an in-process port-forward tunnel.
type portForwardManager struct {
	restConfig *rest.Config
	client     kubernetes.Interface
	namespace  string
	port       int
	done       chan struct{}
}

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
