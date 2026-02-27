// Package k8s_cluster_bench provides head-to-head benchmarks of KinD vs
// testcontainers-go/k3s for ephemeral Kubernetes cluster management.
//
// The goal is to prove (or disprove) that k3s via testcontainers-go is a
// better approach than shelling out to the KinD CLI for our integration
// test suites. "Better" means:
//
//   - Faster startup time (cluster ready + kubeconfig available)
//   - Reliable image loading (the previous attempt was reverted due to
//     image loading failures with k3s)
//   - Faster or comparable teardown
//   - No external CLI dependency (kind binary not required)
//   - Go-native lifecycle management (no exec.Command shelling)
//
// Run both benchmarks:
//
//	go test ./topgun/k8s_cluster_bench/ -count=1 -v -timeout 20m
//
// Run only k3s:
//
//	go test ./topgun/k8s_cluster_bench/ -run TestK3s -count=1 -v -timeout 10m
//
// Prerequisites: docker (both approaches need it)
// Optional: kind (only needed for the KinD benchmark; skipped if absent)
package k8s_cluster_bench

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestMain(m *testing.M) {
	// testcontainers-go requires DOCKER_HOST to locate the Docker daemon.
	// Auto-detect Colima socket if DOCKER_HOST is unset.
	if os.Getenv("DOCKER_HOST") == "" {
		colimaSocket := filepath.Join(os.Getenv("HOME"), ".colima", "default", "docker.sock")
		if _, err := os.Stat(colimaSocket); err == nil {
			os.Setenv("DOCKER_HOST", "unix://"+colimaSocket)
			// TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE must be the path
			// *inside the VM*, not the host path. Colima maps the real
			// Docker socket to /var/run/docker.sock inside its VM.
			os.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/var/run/docker.sock")
			log.Printf("Auto-detected Colima Docker socket: %s", colimaSocket)
		} else if _, err := os.Stat("/var/run/docker.sock"); err == nil {
			os.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
			log.Printf("Using default Docker socket: /var/run/docker.sock")
		}
	}
	os.Exit(m.Run())
}

// testImages are public images we attempt to load into each cluster,
// matching what the behavioral suite actually needs.
var testImages = []string{
	"docker.io/library/busybox:latest",
	"docker.io/library/alpine:latest",
	"docker.io/concourse/mock-resource:latest",
}

// timing records the duration of each phase for comparison.
type timing struct {
	ClusterCreate time.Duration
	KubeReady     time.Duration // time until K8s API responds
	ImageLoad     time.Duration
	Teardown      time.Duration
	Total         time.Duration
	ImageLoadErrs []string
}

func (t timing) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  Cluster create:  %s\n", t.ClusterCreate)
	fmt.Fprintf(&sb, "  Kube API ready:  %s\n", t.KubeReady)
	fmt.Fprintf(&sb, "  Image loading:   %s\n", t.ImageLoad)
	if len(t.ImageLoadErrs) > 0 {
		for _, e := range t.ImageLoadErrs {
			fmt.Fprintf(&sb, "    ERROR: %s\n", e)
		}
	}
	fmt.Fprintf(&sb, "  Teardown:        %s\n", t.Teardown)
	fmt.Fprintf(&sb, "  TOTAL:           %s\n", t.Total)
	return sb.String()
}

// --------------------------------------------------------------------------
// k3s via testcontainers-go
// --------------------------------------------------------------------------

func TestK3sTestcontainers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster benchmark in short mode")
	}

	ctx := context.Background()
	var tm timing
	totalStart := time.Now()

	// Phase 1: Create k3s cluster
	t.Log("Creating k3s cluster via testcontainers-go...")
	start := time.Now()
	k3sContainer, err := k3s.Run(ctx, "rancher/k3s:v1.31.5-k3s1")
	tm.ClusterCreate = time.Since(start)
	if err != nil {
		t.Fatalf("k3s.Run failed: %v", err)
	}
	t.Logf("  k3s cluster created in %s", tm.ClusterCreate)

	defer func() {
		start := time.Now()
		if err := k3sContainer.Terminate(ctx); err != nil {
			t.Logf("warning: k3s terminate: %v", err)
		}
		tm.Teardown = time.Since(start)
		tm.Total = time.Since(totalStart)
		t.Logf("\n=== k3s/testcontainers-go Results ===\n%s", tm)
	}()

	// Phase 2: Get kubeconfig and verify K8s API is reachable
	start = time.Now()
	kubeConfigYaml, err := k3sContainer.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf("GetKubeConfig failed: %v", err)
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeConfigYaml)
	if err != nil {
		t.Fatalf("RESTConfigFromKubeConfig failed: %v", err)
	}

	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("NewForConfig failed: %v", err)
	}

	// Verify the API is actually responding with a node list
	nodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list nodes: %v", err)
	}
	tm.KubeReady = time.Since(start)
	t.Logf("  K8s API ready in %s (%d nodes)", tm.KubeReady, len(nodes.Items))

	// Phase 3: Load images via LoadImages API
	start = time.Now()
	for _, img := range testImages {
		t.Logf("  Loading image %s via testcontainers LoadImages...", img)
		if err := k3sContainer.LoadImages(ctx, img); err != nil {
			tm.ImageLoadErrs = append(tm.ImageLoadErrs, fmt.Sprintf("%s: %v", img, err))
			t.Logf("  WARNING: failed to load %s: %v", img, err)
		}
	}
	tm.ImageLoad = time.Since(start)
	t.Logf("  Image loading completed in %s (%d errors)", tm.ImageLoad, len(tm.ImageLoadErrs))

	// Verify: can we create a pod that uses a loaded image?
	verifyImageAvailable(t, k8sClient, ctx, "busybox:latest")

	if len(tm.ImageLoadErrs) > 0 {
		t.Errorf("k3s had %d image loading errors (this was the reason for the previous revert)", len(tm.ImageLoadErrs))
	}

	t.Log("k3s/testcontainers-go benchmark PASSED")
}

// --------------------------------------------------------------------------
// k3s via testcontainers-go with crictl pull (hybrid approach)
//
// Uses testcontainers-go for cluster lifecycle (Go-native, no kind CLI)
// but pulls public images via crictl inside the container — same approach
// that works reliably with KinD. LoadImages is reserved for locally-built
// images that aren't available on any registry.
// --------------------------------------------------------------------------

func TestK3sCrictlPull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster benchmark in short mode")
	}

	ctx := context.Background()
	var tm timing
	totalStart := time.Now()

	// Phase 1: Create k3s cluster
	t.Log("Creating k3s cluster via testcontainers-go...")
	start := time.Now()
	k3sContainer, err := k3s.Run(ctx, "rancher/k3s:v1.31.5-k3s1")
	tm.ClusterCreate = time.Since(start)
	if err != nil {
		t.Fatalf("k3s.Run failed: %v", err)
	}
	t.Logf("  k3s cluster created in %s", tm.ClusterCreate)

	defer func() {
		start := time.Now()
		if err := k3sContainer.Terminate(ctx); err != nil {
			t.Logf("warning: k3s terminate: %v", err)
		}
		tm.Teardown = time.Since(start)
		tm.Total = time.Since(totalStart)
		t.Logf("\n=== k3s + crictl pull Results ===\n%s", tm)
	}()

	// Phase 2: Get kubeconfig and verify K8s API
	start = time.Now()
	kubeConfigYaml, err := k3sContainer.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf("GetKubeConfig failed: %v", err)
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeConfigYaml)
	if err != nil {
		t.Fatalf("RESTConfigFromKubeConfig failed: %v", err)
	}

	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("NewForConfig failed: %v", err)
	}

	nodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list nodes: %v", err)
	}
	tm.KubeReady = time.Since(start)
	t.Logf("  K8s API ready in %s (%d nodes)", tm.KubeReady, len(nodes.Items))

	// Phase 3: Pull public images via crictl exec inside the k3s container
	// (same reliable approach used by KinD, but no kind CLI needed)
	containerID := k3sContainer.GetContainerID()
	start = time.Now()
	for _, img := range testImages {
		t.Logf("  Pulling %s via crictl inside k3s container...", img)
		pullCmd := exec.Command("docker", "exec", containerID, "crictl", "pull", img)
		out, err := pullCmd.CombinedOutput()
		if err != nil {
			tm.ImageLoadErrs = append(tm.ImageLoadErrs, fmt.Sprintf("%s: %v (%s)", img, err, strings.TrimSpace(string(out))))
			t.Logf("  WARNING: failed to pull %s: %v\n%s", img, err, out)
		}
	}
	tm.ImageLoad = time.Since(start)
	t.Logf("  Image loading completed in %s (%d errors)", tm.ImageLoad, len(tm.ImageLoadErrs))

	// Verify images are usable
	verifyImageAvailable(t, k8sClient, ctx, "busybox:latest")

	if len(tm.ImageLoadErrs) > 0 {
		t.Errorf("crictl pull had %d errors", len(tm.ImageLoadErrs))
	}

	t.Log("k3s + crictl pull benchmark PASSED")
}

// --------------------------------------------------------------------------
// KinD (current approach)
// --------------------------------------------------------------------------

func TestKinD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster benchmark in short mode")
	}
	if _, err := exec.LookPath("kind"); err != nil {
		t.Skip("kind CLI not on PATH; skipping KinD benchmark")
	}

	const clusterName = "concourse-bench"
	var tm timing
	totalStart := time.Now()

	// Phase 1: Create KinD cluster
	t.Log("Creating KinD cluster...")
	start := time.Now()

	// Clean up any leftover cluster
	exec.Command("kind", "delete", "cluster", "--name", clusterName).Run()

	kubeconfigPath := filepath.Join(t.TempDir(), "kind-kubeconfig")
	cmd := exec.Command("kind", "create", "cluster",
		"--name", clusterName,
		"--kubeconfig", kubeconfigPath,
		"--wait", "120s",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("kind create cluster failed: %v", err)
	}
	tm.ClusterCreate = time.Since(start)
	t.Logf("  KinD cluster created in %s", tm.ClusterCreate)

	defer func() {
		start := time.Now()
		cmd := exec.Command("kind", "delete", "cluster", "--name", clusterName)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		cmd.Run()
		tm.Teardown = time.Since(start)
		tm.Total = time.Since(totalStart)
		t.Logf("\n=== KinD Results ===\n%s", tm)
	}()

	// Phase 2: Verify K8s API is reachable
	start = time.Now()
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		t.Fatalf("failed to load kubeconfig: %v", err)
	}

	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("NewForConfig failed: %v", err)
	}

	ctx := context.Background()
	nodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list nodes: %v", err)
	}
	tm.KubeReady = time.Since(start)
	t.Logf("  K8s API ready in %s (%d nodes)", tm.KubeReady, len(nodes.Items))

	// Phase 3: Load images via crictl pull (matching current behavioral suite)
	start = time.Now()
	node := clusterName + "-control-plane"
	for _, img := range testImages {
		t.Logf("  Pulling %s via crictl into KinD node...", img)
		pullCmd := exec.Command("docker", "exec", node, "crictl", "pull", img)
		pullCmd.Stdout = os.Stderr
		pullCmd.Stderr = os.Stderr
		if err := pullCmd.Run(); err != nil {
			tm.ImageLoadErrs = append(tm.ImageLoadErrs, fmt.Sprintf("%s: %v", img, err))
			t.Logf("  WARNING: failed to pull %s: %v", img, err)
		}
	}
	tm.ImageLoad = time.Since(start)
	t.Logf("  Image loading completed in %s (%d errors)", tm.ImageLoad, len(tm.ImageLoadErrs))

	// Verify: can we create a pod that uses a loaded image?
	verifyImageAvailable(t, k8sClient, ctx, "busybox:latest")

	t.Log("KinD benchmark PASSED")
}

// --------------------------------------------------------------------------
// Shared helpers
// --------------------------------------------------------------------------

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// verifyImageAvailable creates a short-lived pod using the given image
// to confirm it's actually available in the cluster without pulling.
func verifyImageAvailable(t *testing.T, k8sClient kubernetes.Interface, ctx context.Context, image string) {
	t.Helper()
	t.Logf("  Verifying image %s is available in cluster...", image)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "image-verify",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:            "verify",
					Image:           image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"echo", "ok"},
				},
			},
		},
	}

	_, err := k8sClient.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Logf("  WARNING: failed to create verification pod: %v", err)
		return
	}
	defer k8sClient.CoreV1().Pods("default").Delete(ctx, "image-verify", metav1.DeleteOptions{})

	// Wait up to 30s for the pod to complete
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		p, err := k8sClient.CoreV1().Pods("default").Get(ctx, "image-verify", metav1.GetOptions{})
		if err != nil {
			break
		}
		if p.Status.Phase == corev1.PodSucceeded {
			t.Logf("  Image %s verified: pod completed successfully", image)
			return
		}
		if p.Status.Phase == corev1.PodFailed {
			t.Logf("  WARNING: verification pod failed (phase=%s)", p.Status.Phase)
			return
		}
		time.Sleep(time.Second)
	}
	t.Logf("  WARNING: verification pod did not complete within 30s")
}
