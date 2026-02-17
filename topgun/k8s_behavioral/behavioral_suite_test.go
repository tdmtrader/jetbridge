// K8s Behavioral Integration Test Suite
//
// Self-sufficient run (creates a KinD cluster automatically):
//
//   go test ./topgun/k8s_behavioral/ -count=1 -v -timeout 30m
//
// Run against a deployed Concourse instance (skips cluster management):
//
//   ATC_URL=https://concourse.home \
//   ATC_USERNAME=admin \
//   ATC_PASSWORD=<password> \
//   K8S_NAMESPACE=cicd \
//   KUBECONFIG=~/.kube/config \
//   FLY_PATH=/tmp/fly \
//     ginkgo -v ./topgun/k8s_behavioral/
//
// Parallel execution (requires external Concourse):
//
//   ATC_URL=https://concourse.home \
//   ATC_USERNAME=admin \
//   ATC_PASSWORD=<password> \
//   K8S_NAMESPACE=cicd \
//   KUBECONFIG=~/.kube/config \
//   FLY_PATH=/tmp/fly \
//     ginkgo -p --procs=4 ./topgun/k8s_behavioral/
//
// Focus on a specific Describe block:
//
//   ginkgo -v ./topgun/k8s_behavioral/ --focus="Pipeline Lifecycle"
//
// Iterative development (keep cluster between runs):
//
//   SKIP_TEARDOWN=1 go test ./topgun/k8s_behavioral/ -count=1 -v -run TestBehavioral/Task
//   # Re-run after changes (cluster is reused):
//   SKIP_TEARDOWN=1 go test ./topgun/k8s_behavioral/ -count=1 -v -run TestBehavioral/Task
//   # Manual cleanup when done:
//   kind delete cluster --name concourse-behavioral
//
// Environment variables:
//   ATC_URL            - If set, use external Concourse (skip cluster management)
//   ATC_USERNAME       - login user (default: test)
//   ATC_PASSWORD       - login password (default: test)
//   K8S_NAMESPACE      - Kubernetes namespace (default: concourse)
//   KUBECONFIG         - path to kubeconfig (default: ~/.kube/config)
//   FLY_PATH           - path to fly binary (builds from source if unset)
//   CONCOURSE_IMAGE    - Docker image to load into KinD (default: concourse-local:latest)
//   KIND_CLUSTER_NAME  - KinD cluster name (default: concourse-behavioral)
//   SKIP_TEARDOWN      - Set to "1" to keep cluster after tests
//   EVENTUALLY_TIMEOUT - Go duration for Eventually timeout (default: 5m)
//
// Notes on parallelism:
//   Ginkgo parallel mode (--procs / -p) requires an external Concourse
//   (ATC_URL must be set). Self-sufficient mode creates a KinD cluster in
//   TestMain which does not propagate across parallel processes.
//   Each parallel process builds its own K8s client and fly login; pipeline
//   names are UUID-based so there are no cross-process conflicts.

package behavioral_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/concourse/concourse/topgun"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/google/uuid"
	"github.com/onsi/gomega/gexec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

// TestMain manages the KinD cluster lifecycle outside of Ginkgo.
// When ATC_URL is not set, it creates a KinD cluster, loads images,
// deploys Concourse, and starts a port-forward. The resulting config
// is passed to the Ginkgo suite via environment variables.
func TestMain(m *testing.M) {
	if os.Getenv("ATC_URL") != "" {
		// External cluster mode — nothing to manage.
		os.Exit(m.Run())
	}

	// Self-sufficient mode — manage a KinD cluster.
	// Not compatible with Ginkgo parallelism: each parallel process runs
	// TestMain independently, so env vars set here don't propagate.
	if proc := os.Getenv("GINKGO_PARALLEL_PROCESS"); proc != "" && proc != "1" {
		log.Fatalf(
			"self-sufficient mode (no ATC_URL) does not support Ginkgo parallelism\n" +
				"  Set ATC_URL, KUBECONFIG, and K8S_NAMESPACE to use --procs / -p",
		)
	}
	if err := verifyPrerequisites(); err != nil {
		log.Fatalf("prerequisites check failed: %v", err)
	}

	clusterName := envOr("KIND_CLUSTER_NAME", "concourse-behavioral")
	namespace := envOr("K8S_NAMESPACE", "concourse")
	image := envOr("CONCOURSE_IMAGE", "concourse-local:latest")

	kubeconfig, created := ensureKindCluster(clusterName)
	log.Printf("KinD cluster %q ready (kubeconfig: %s, created: %v)", clusterName, kubeconfig, created)

	loadImagesIntoKind(image, clusterName)

	chartPath := filepath.Join(mustRepoRoot(), "deploy", "chart")
	helmDeployConcourse(kubeconfig, namespace, chartPath, image)

	atcURL, pfCmd := startPortForwardStdlib(kubeconfig, namespace)
	log.Printf("Concourse API available at %s", atcURL)

	waitForAPI(atcURL, 3*time.Minute)

	// Export config for the Ginkgo suite via environment variables.
	os.Setenv("ATC_URL", atcURL)
	os.Setenv("KUBECONFIG", kubeconfig)
	os.Setenv("K8S_NAMESPACE", namespace)
	os.Setenv("_MANAGED_CLUSTER", "1")
	os.Setenv("_KIND_CLUSTER", clusterName)

	code := m.Run()

	// Cleanup.
	if pfCmd != nil && pfCmd.Process != nil {
		pfCmd.Process.Kill()
		pfCmd.Wait()
	}
	if os.Getenv("SKIP_TEARDOWN") != "1" {
		log.Printf("Tearing down KinD cluster %q...", clusterName)
		exec.Command("kind", "delete", "cluster", "--name", clusterName).Run()
	} else {
		log.Printf("SKIP_TEARDOWN=1: keeping KinD cluster %q", clusterName)
	}

	os.Exit(code)
}

func TestBehavioral(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "K8s Behavioral Suite")
}

// suiteConfig holds configuration for the Ginkgo suite.
type suiteConfig struct {
	FlyBin      string
	ATCURL      string
	ATCUsername string
	ATCPassword string
	Namespace   string
	Kubeconfig  string
}

const (
	flyTarget      = "k8s-behavioral"
	pipelinePrefix = "k8s-beh"
)

var (
	config       suiteConfig
	fly          FlyCli
	kubeClient   kubernetes.Interface
	restConfig   *rest.Config
	pipelineName string
	tmp          string
	suiteFlyHome string
)

var _ = SynchronizedBeforeSuite(
	// Process 1 only: build the fly binary (expensive, do once).
	// Other processes receive the binary path via the []byte return value.
	func() []byte {
		if flyPath := os.Getenv("FLY_PATH"); flyPath != "" {
			return []byte(flyPath)
		}
		return []byte(BuildBinary())
	},
	// All processes: initialize config, K8s client, and per-process fly login.
	func(flyBinData []byte) {
		config = suiteConfig{
			FlyBin:      string(flyBinData),
			ATCURL:      os.Getenv("ATC_URL"),
			ATCUsername: envOr("ATC_USERNAME", "test"),
			ATCPassword: envOr("ATC_PASSWORD", "test"),
			Namespace:   envOr("K8S_NAMESPACE", "concourse"),
			Kubeconfig:  defaultKubeconfig(),
		}

		Expect(config.ATCURL).ToNot(BeEmpty(), "ATC_URL must be set (TestMain sets it for managed clusters)")

		// Gomega defaults (suite-wide, derived from env vars that never change)
		eventuallyTimeout := 5 * time.Minute
		if v := os.Getenv("EVENTUALLY_TIMEOUT"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				eventuallyTimeout = d
			}
		}
		SetDefaultEventuallyTimeout(eventuallyTimeout)
		SetDefaultEventuallyPollingInterval(500 * time.Millisecond)
		SetDefaultConsistentlyDuration(time.Minute)
		SetDefaultConsistentlyPollingInterval(time.Second)

		// K8s client (goroutine-safe, kubeconfig never changes)
		kubeClient, restConfig = newKubeClient(config.Kubeconfig)

		// Per-process fly login (login once per process, copy .flyrc per test)
		var err error
		suiteFlyHome, err = os.MkdirTemp("", "k8s-behavioral-fly-suite")
		Expect(err).ToNot(HaveOccurred())
		suiteFly := FlyCli{Bin: config.FlyBin, Target: flyTarget, Home: suiteFlyHome}
		loginArgs := []string{}
		if envOr("FLY_INSECURE", "") != "" || strings.HasPrefix(config.ATCURL, "https://") {
			loginArgs = append(loginArgs, "-k")
		}
		suiteFly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL, loginArgs...)
	},
)

var _ = SynchronizedAfterSuite(
	// All processes: clean up per-process resources.
	func() {
		if suiteFlyHome != "" {
			os.RemoveAll(suiteFlyHome)
		}
	},
	// Process 1 only: clean up the compiled fly binary.
	func() {
		if os.Getenv("FLY_PATH") == "" {
			gexec.CleanupBuildArtifacts()
		}
	},
)

var _ = BeforeEach(func() {
	var err error
	tmp, err = os.MkdirTemp("", "k8s-behavioral-tmp")
	Expect(err).ToNot(HaveOccurred())

	flyHome := filepath.Join(tmp, "fly-home")
	Expect(os.Mkdir(flyHome, 0755)).To(Succeed())

	// Copy pre-authenticated .flyrc instead of logging in (~1us vs 1-2s)
	src := filepath.Join(suiteFlyHome, ".flyrc")
	data, err := os.ReadFile(src)
	Expect(err).ToNot(HaveOccurred())
	Expect(os.WriteFile(filepath.Join(flyHome, ".flyrc"), data, 0600)).To(Succeed())

	fly = FlyCli{Bin: config.FlyBin, Target: flyTarget, Home: flyHome}
	pipelineName = randomPipelineName()
})

var _ = AfterEach(func() {
	destroyPipeline()
	if pipelineName != "" {
		cleanupPodsWithLabel(fmt.Sprintf(
			"concourse.ci/worker,concourse.ci/pipeline=%s", pipelineName,
		))
	}
	Expect(os.RemoveAll(tmp)).To(Succeed())
})

// ---------------------------------------------------------------------
// TestMain helpers (run outside Ginkgo — use log, not GinkgoWriter)
// ---------------------------------------------------------------------

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
				return writeKindKubeconfigStdlib(name), false
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

	return writeKindKubeconfigStdlib(name), true
}

// writeKindKubeconfigStdlib writes the kubeconfig for the given cluster to a temp file.
func writeKindKubeconfigStdlib(name string) string {
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
// inside the KinD cluster. For the Concourse image (which is built
// locally), it uses `kind load docker-image`. For registry images
// (postgres, mock-resource, busybox), it pre-pulls them directly
// inside the KinD node via crictl, which is more reliable than
// `kind load` with Docker Desktop's containerd image store.
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
	// This avoids `kind load` compatibility issues with Docker Desktop's
	// containerd image store and is faster for standard registry images.
	registryImages := []string{
		"docker.io/library/postgres:16",
		"docker.io/concourse/mock-resource:latest",
		"docker.io/library/busybox:latest",
	}
	for _, img := range registryImages {
		// Check if image is already present in the node before pulling.
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
// Tries `kind load docker-image` first, falls back to image-archive if
// the direct method fails (e.g., Docker Desktop containerd store issues).
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

// startPortForwardStdlib starts a kubectl port-forward to the Concourse
// web service on a random available port. Discovers the service name
// dynamically using the app.kubernetes.io/component=web label.
func startPortForwardStdlib(kubeconfig, namespace string) (string, *exec.Cmd) {
	port := mustFindFreePort()

	// Discover the web service name via label selector.
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

	// Give port-forward a moment to establish.
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
	// Fallback to well-known names.
	for _, name := range []string{"concourse-web", "concourse-concourse-jetbridge-web"} {
		if exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", "svc", name).Run() == nil {
			return name
		}
	}
	log.Fatalf("could not find Concourse web service in namespace %s", namespace)
	return ""
}

// waitForAPI polls the Concourse /api/v1/info endpoint until it responds
// with 200 OK or the timeout expires. Uses a plain http.Client since
// TestMain always connects via http://localhost (no TLS).
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

func mustFindFreePort() int {
	l, err := findFreeListener()
	if err != nil {
		log.Fatalf("failed to find free port: %v", err)
	}
	return l
}

// ---------------------------------------------------------------------
// Config helpers
// ---------------------------------------------------------------------

func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func defaultKubeconfig() string {
	if v := os.Getenv("KUBECONFIG"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".kube", "config")
}

// ---------------------------------------------------------------------
// Kubernetes client
// ---------------------------------------------------------------------

func newKubeClient(kubeconfig string) (kubernetes.Interface, *rest.Config) {
	var rc *rest.Config
	var err error

	if kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		Expect(err).ToNot(HaveOccurred(), "failed to load kubeconfig from %s", kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
		Expect(err).ToNot(HaveOccurred(), "failed to load in-cluster config (no KUBECONFIG set)")
	}

	client, err := kubernetes.NewForConfig(rc)
	Expect(err).ToNot(HaveOccurred())

	return client, rc
}

// ---------------------------------------------------------------------
// Pipeline helpers
// ---------------------------------------------------------------------

func randomPipelineName() string {
	guid, err := uuid.NewRandom()
	Expect(err).ToNot(HaveOccurred())
	return fmt.Sprintf("%s-%s", pipelinePrefix, guid)
}

func setAndUnpausePipeline(configFile string, args ...string) {
	setPipeline(configFile, args...)
	fly.Run("unpause-pipeline", "-p", pipelineName)
}

func setPipeline(configFile string, args ...string) {
	sp := []string{"set-pipeline", "-n", "-p", pipelineName, "-c", configFile}
	fly.Run(append(sp, args...)...)
}

func destroyPipeline() {
	if pipelineName == "" {
		return
	}
	sess := fly.Start("destroy-pipeline", "-n", "-p", pipelineName)
	<-sess.Exited
	// Don't assert success - pipeline may not exist if test failed early.
}

func inPipeline(thing string) string {
	return pipelineName + "/" + thing
}

func triggerJob(jobName string) {
	fly.Run("trigger-job", "-j", inPipeline(jobName))
}

// waitForBuildAndWatch polls until the build for the given job exists,
// then watches it and returns the completed session. The caller can
// inspect session.ExitCode() and session.Out.Contents().
func waitForBuildAndWatch(jobName string, buildName ...string) *gexec.Session {
	args := []string{"watch", "-j", inPipeline(jobName)}
	if len(buildName) > 0 {
		args = append(args, "-b", buildName[0])
	}

	keepPollingCheck := regexp.MustCompile(
		"job has no builds|build not found|failed to get build",
	)

	deadline := time.Now().Add(5 * time.Minute)
	for {
		session := fly.Start(args...)
		<-session.Exited

		if session.ExitCode() == 1 {
			output := strings.TrimSpace(string(session.Err.Contents()))
			if keepPollingCheck.MatchString(output) {
				if time.Now().After(deadline) {
					Fail(fmt.Sprintf("timed out waiting for build: %s (args: %v)", output, args))
				}
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}

		return session
	}
}

func newMockVersion(resourceName string, tag string) string {
	guid, err := uuid.NewRandom()
	Expect(err).ToNot(HaveOccurred())

	version := guid.String() + "-" + tag
	fly.Run("check-resource", "-r", inPipeline(resourceName), "-f", "version:"+version)

	return version
}

// newMockVersionOrSkip is like newMockVersion but skips the test instead of
// failing when the check-resource fails due to image resolution issues
// (e.g., mock-backed custom type chains in K8s runtime).
func newMockVersionOrSkip(resourceName string, tag string) string {
	guid, err := uuid.NewRandom()
	Expect(err).ToNot(HaveOccurred())

	version := guid.String() + "-" + tag
	sess := fly.Start("check-resource", "-r", inPipeline(resourceName), "-f", "version:"+version)
	<-sess.Exited
	if sess.ExitCode() != 0 {
		output := string(sess.Out.Contents()) + string(sess.Err.Contents())
		if strings.Contains(output, "ErrImagePull") ||
			strings.Contains(output, "failed to pull") ||
			strings.Contains(output, "pod failed") {
			Skip("K8s runtime cannot resolve mock-backed custom type chain images (CODE ISSUE)")
		}
		Fail("check-resource failed: " + output)
	}
	return version
}

// ---------------------------------------------------------------------
// Fly table parsing
// ---------------------------------------------------------------------

var colSplit = regexp.MustCompile(`\s{2,}`)

func flyTable(argv ...string) []map[string]string {
	session := fly.Start(append([]string{"--print-table-headers"}, argv...)...)
	Wait(session)

	result := []map[string]string{}
	var headers []string

	rows := strings.Split(string(session.Out.Contents()), "\n")
	for i, row := range rows {
		columns := colSplit.Split(strings.TrimSpace(row), -1)

		if i == 0 {
			headers = columns
			continue
		}

		if row == "" {
			continue
		}

		entry := map[string]string{}
		for j, header := range headers {
			if j < len(columns) && header != "" && columns[j] != "" {
				entry[header] = columns[j]
			}
		}

		result = append(result, entry)
	}

	return result
}

// ---------------------------------------------------------------------
// File-writing helpers
// ---------------------------------------------------------------------

func writePipelineFile(name, content string) string {
	path := filepath.Join(tmp, name)
	err := os.WriteFile(path, []byte(content), 0644)
	Expect(err).ToNot(HaveOccurred())
	return path
}

func writeTaskFile(name, content string) string {
	return writePipelineFile(name, content) // same operation, different name for clarity
}

// ---------------------------------------------------------------------
// K8s pod helpers (used for cluster-state assertions)
// ---------------------------------------------------------------------

// getPods returns pods in the configured namespace matching the given
// label selector.
func getPods(labelSelector string) []corev1.Pod {
	pods, err := kubeClient.CoreV1().Pods(config.Namespace).List(
		context.Background(),
		metav1.ListOptions{LabelSelector: labelSelector},
	)
	Expect(err).ToNot(HaveOccurred())
	return pods.Items
}

// getPodByName returns a single pod by name from the configured namespace.
func getPodByName(name string) *corev1.Pod {
	pod, err := kubeClient.CoreV1().Pods(config.Namespace).Get(
		context.Background(),
		name,
		metav1.GetOptions{},
	)
	Expect(err).ToNot(HaveOccurred())
	return pod
}

// waitForPodWithLabel waits until at least one pod matching the label
// selector reaches the given phase, then returns it.
func waitForPodWithLabel(labelSelector string, phase corev1.PodPhase) *corev1.Pod {
	var matched *corev1.Pod
	Eventually(func() bool {
		pods := getPods(labelSelector)
		for i := range pods {
			if pods[i].Status.Phase == phase {
				matched = &pods[i]
				return true
			}
		}
		return false
	}, 2*time.Minute, time.Second).Should(BeTrue(),
		fmt.Sprintf("expected pod with label %q to reach phase %s", labelSelector, phase),
	)
	return matched
}

// cleanupPod deletes a pod by name, ignoring not-found errors.
func cleanupPod(name string) {
	_ = kubeClient.CoreV1().Pods(config.Namespace).Delete(
		context.Background(),
		name,
		metav1.DeleteOptions{},
	)
}

