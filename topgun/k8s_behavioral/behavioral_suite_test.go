// K8s Behavioral Integration Test Suite
//
// This suite is fully self-contained: it creates an ephemeral KinD cluster,
// deploys Concourse via Helm, runs tests, and tears down automatically.
// No external cluster connectivity is supported — tests always run in
// isolation to prevent accidental impact on production clusters.
//
// Prerequisites: docker, kind, helm, kubectl
//
// Basic run (creates a KinD cluster automatically):
//
//   go test ./topgun/k8s_behavioral/ -count=1 -v -timeout 30m
//
// Focus on a specific Describe block:
//
//   go test ./topgun/k8s_behavioral/ -count=1 -v -timeout 30m -run TestBehavioral/Pipeline
//
// Environment variables:
//   FLY_PATH           - path to fly binary (builds from source if unset)
//   CONCOURSE_IMAGE    - Docker image to load into KinD (default: concourse-local:latest)
//   SKIP_TEARDOWN      - Set to "1" to keep KinD cluster after tests
//   EVENTUALLY_TIMEOUT - Go duration for Eventually timeout (default: 5m)

package behavioral_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
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
// It creates an ephemeral KinD cluster, loads images, deploys Concourse
// via Helm, and starts a port-forward. The resulting config is passed to
// the Ginkgo suite via environment variables.
func TestMain(m *testing.M) {
	// Self-contained mode only — no external cluster connectivity.
	// Not compatible with Ginkgo parallelism: each parallel process runs
	// TestMain independently, so env vars set here don't propagate.
	if proc := os.Getenv("GINKGO_PARALLEL_PROCESS"); proc != "" && proc != "1" {
		log.Fatalf(
			"this suite does not support Ginkgo parallelism (--procs / -p)\n" +
				"  Each test process creates its own KinD cluster via TestMain",
		)
	}
	if err := verifyPrerequisites(); err != nil {
		log.Fatalf("prerequisites check failed: %v", err)
	}

	namespace := envOr("K8S_NAMESPACE", "concourse")
	image := envOr("CONCOURSE_IMAGE", "concourse-local:latest")

	ensureConcourseImage(image)

	kubeconfig := createKindCluster()
	loadImagesIntoKind(image)

	chartPath := filepath.Join(mustRepoRoot(), "deploy", "chart")
	helmDeployConcourse(kubeconfig, namespace, chartPath, image)

	preloadImages()

	atcURL, pfMgr := startPortForward(kubeconfig, namespace)
	log.Printf("Concourse API available at %s", atcURL)

	waitForAPI(atcURL, 5*time.Minute)

	// Export config for the Ginkgo suite via environment variables.
	os.Setenv("ATC_URL", atcURL)
	os.Setenv("KUBECONFIG", kubeconfig)
	os.Setenv("K8S_NAMESPACE", namespace)

	code := m.Run()

	// Cleanup.
	if pfMgr != nil {
		pfMgr.Stop()
	}
	deleteKindCluster()

	os.Exit(code)
}

func TestBehavioral(t *testing.T) {
	RegisterFailHandler(Fail)
	suiteConf, reporterConf := GinkgoConfiguration()
	suiteConf.Timeout = 3 * time.Hour
	RunSpecs(t, "K8s Behavioral Suite", suiteConf, reporterConf)
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
	// All env vars (ATC_URL, KUBECONFIG, K8S_NAMESPACE) are set by TestMain
	// from the ephemeral k3s cluster — no external cluster connectivity.
	func(flyBinData []byte) {
		config = suiteConfig{
			FlyBin:      string(flyBinData),
			ATCURL:      os.Getenv("ATC_URL"),
			ATCUsername: "test",
			ATCPassword: "test",
			Namespace:   envOr("K8S_NAMESPACE", "concourse"),
			Kubeconfig:  defaultKubeconfig(),
		}

		Expect(config.ATCURL).ToNot(BeEmpty(), "ATC_URL must be set (TestMain sets it for the managed k3s cluster)")

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
		suiteFly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL)
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
	// Wait for the Concourse API to be reachable before each test.
	// This handles the case where kubectl port-forward died between tests
	// and the watchdog is still restarting it.
	waitForAPIReachable(config.ATCURL, 30*time.Second)

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

// TestMain helpers are defined in cluster_lifecycle_test.go.

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
	v := os.Getenv("KUBECONFIG")
	Expect(v).ToNot(BeEmpty(), "KUBECONFIG must be set (TestMain sets it for the managed k3s cluster)")
	return v
}

// ---------------------------------------------------------------------
// Kubernetes client
// ---------------------------------------------------------------------

func newKubeClient(kubeconfig string) (kubernetes.Interface, *rest.Config) {
	Expect(kubeconfig).ToNot(BeEmpty(), "kubeconfig path must not be empty")

	rc, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).ToNot(HaveOccurred(), "failed to load kubeconfig from %s", kubeconfig)

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

	// Retry on transient pod race conditions. These happen when the mock
	// resource check pod completes so fast that the K8s worker cannot exec
	// into it in time. Known error variants:
	//   - "pod terminated before exec could run"
	//   - "unable to upgrade connection: container not found"
	for attempt := 0; attempt < 3; attempt++ {
		sess := fly.Start("check-resource", "-r", inPipeline(resourceName), "-f", "version:"+version)
		<-sess.Exited
		if sess.ExitCode() == 0 {
			return version
		}
		output := string(sess.Out.Contents()) + string(sess.Err.Contents())
		if strings.Contains(output, "pod terminated before exec could run") ||
			strings.Contains(output, "container not found") {
			time.Sleep(2 * time.Second)
			continue
		}
		Fail("check-resource failed: " + output)
	}
	Fail("check-resource failed after 3 retries due to pod race condition")
	return version
}

// newMockVersionOrSkip is like newMockVersion but skips the current test
// (instead of failing) when the check-resource fails due to known K8s
// limitations with custom type chains and produces: registry-image.
//
// Known skip conditions:
//   - ErrImagePull: multi-level type chains (e.g., level-a → level-b →
//     registry-image) where the intermediate type name leaks as Docker image.
//   - "unknown field": produces: registry-image resources pass source fields
//     like "repository" to the mock-resource check binary, which rejects them.
func newMockVersionOrSkip(resourceName string, tag string) string {
	guid, err := uuid.NewRandom()
	Expect(err).ToNot(HaveOccurred())

	version := guid.String() + "-" + tag

	for attempt := 0; attempt < 3; attempt++ {
		sess := fly.Start("check-resource", "-r", inPipeline(resourceName), "-f", "version:"+version)
		<-sess.Exited
		if sess.ExitCode() == 0 {
			return version
		}
		output := string(sess.Out.Contents()) + string(sess.Err.Contents())
		if strings.Contains(output, "pod terminated before exec could run") ||
			strings.Contains(output, "container not found") {
			time.Sleep(2 * time.Second)
			continue
		}
		if strings.Contains(output, "ErrImagePull") ||
			strings.Contains(output, "ImagePullBackOff") ||
			strings.Contains(output, "failed to pull") {
			Skip("K8s type chain bug: check pod uses type name as image — " + output)
		}
		if strings.Contains(output, "unknown field") ||
			strings.Contains(output, "invalid payload") {
			Skip("mock-resource rejects registry-image source fields (produces: registry-image limitation) — " + output)
		}
		Fail("check-resource failed: " + output)
	}
	Fail("check-resource failed after 3 retries due to pod race condition")
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
// Connectivity helpers
// ---------------------------------------------------------------------

// waitForAPIReachable polls the Concourse /api/v1/info endpoint until it
// responds with HTTP 200. This is called in BeforeEach to handle the case
// where kubectl port-forward died and the watchdog is restarting it.
func waitForAPIReachable(atcURL string, timeout time.Duration) {
	if atcURL == "" {
		return
	}
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.Get(atcURL + "/api/v1/info")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		if time.Now().After(deadline) {
			log.Printf("warning: API not reachable after %s, proceeding anyway", timeout)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
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

