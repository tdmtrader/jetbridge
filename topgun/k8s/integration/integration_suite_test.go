// K8s Integration Test Suite
//
// This suite is fully self-contained: it creates an ephemeral KinD cluster,
// deploys Concourse via Helm, runs tests, and tears down automatically.
// No external cluster connectivity is supported — tests always run in
// isolation to prevent accidental impact on production clusters.
//
// Prerequisites: docker, helm, kubectl
//
// Basic run (creates a KinD cluster automatically):
//
//   go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m
//
// Focus on a specific Describe block:
//
//   go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m -run TestIntegration/Pod.Cleanup
//
// Environment variables:
//   FLY_PATH           — path to fly binary (builds from source if unset)
//   CONCOURSE_IMAGE    — Docker image to load into KinD (default: concourse-local:latest)
//   SKIP_TEARDOWN      — Set to "1" to keep KinD cluster after tests
//   EVENTUALLY_TIMEOUT — Go duration for Eventually timeout (default: 5m)

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

	atcURL, pfMgr := startPortForward(kubeconfig, namespace)
	log.Printf("Concourse API available at %s", atcURL)

	waitForAPI(atcURL, 5*time.Minute)

	tuneReaperInterval(kubeconfig, namespace, "2s")

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

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "K8s Integration Suite")
}

// suiteConfig holds configuration passed between Ginkgo nodes via
// SynchronizedBeforeSuite. All fields are serialised as JSON.
type suiteConfig struct {
	FlyBin      string `json:"fly_bin"`
	ATCURL      string `json:"atc_url"`
	ATCUsername  string `json:"atc_username"`
	ATCPassword string `json:"atc_password"`
	Namespace   string `json:"namespace"`
	Kubeconfig  string `json:"kubeconfig"`
}

const (
	flyTarget      = "k8s-integration"
	pipelinePrefix = "k8s-int"
)

var (
	config       suiteConfig
	fly          FlyCli
	kubeClient   kubernetes.Interface
	restConfig   *rest.Config
	pipelineName string
	tmp          string
)

var _ = SynchronizedBeforeSuite(func() []byte {
	// All env vars (ATC_URL, KUBECONFIG, K8S_NAMESPACE) are set by TestMain
	// from the ephemeral k3s cluster — no external cluster connectivity.
	cfg := suiteConfig{
		ATCURL:      os.Getenv("ATC_URL"),
		ATCUsername: "test",
		ATCPassword: "test",
		Namespace:   envOr("K8S_NAMESPACE", "concourse"),
		Kubeconfig:  os.Getenv("KUBECONFIG"),
	}

	Expect(cfg.ATCURL).ToNot(BeEmpty(), "ATC_URL must be set (TestMain sets it for the managed k3s cluster)")
	Expect(cfg.Kubeconfig).ToNot(BeEmpty(), "KUBECONFIG must be set (TestMain sets it for the managed k3s cluster)")

	if flyPath := os.Getenv("FLY_PATH"); flyPath != "" {
		cfg.FlyBin = flyPath
	} else {
		cfg.FlyBin = BuildBinary()
	}

	payload, err := json.Marshal(cfg)
	Expect(err).ToNot(HaveOccurred())

	return payload
}, func(data []byte) {
	err := json.Unmarshal(data, &config)
	Expect(err).ToNot(HaveOccurred())
})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if os.Getenv("FLY_PATH") == "" {
		gexec.CleanupBuildArtifacts()
	}
})

var _ = BeforeEach(func() {
	// 2 minutes is plenty for any single operation; the 5-minute default
	// just hides real failures behind long waits.
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)
	SetDefaultConsistentlyDuration(30 * time.Second)
	SetDefaultConsistentlyPollingInterval(time.Second)

	var err error
	tmp, err = os.MkdirTemp("", "k8s-integration-tmp")
	Expect(err).ToNot(HaveOccurred())

	fly = FlyCli{
		Bin:    config.FlyBin,
		Target: flyTarget,
		Home:   filepath.Join(tmp, "fly-home"),
	}

	err = os.Mkdir(fly.Home, 0755)
	Expect(err).ToNot(HaveOccurred())

	fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL)

	pipelineName = randomPipelineName()

	kubeClient, restConfig = newKubeClient(config.Kubeconfig)
})

var _ = AfterEach(func() {
	destroyPipeline()
	cleanupOrphanedPods()
	Expect(os.RemoveAll(tmp)).To(Succeed())
})

// ---------------------------------------------------------------------
// Config helpers
// ---------------------------------------------------------------------

func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
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
	sess := fly.Start("destroy-pipeline", "-n", "-p", pipelineName)
	<-sess.Exited
	// Don't assert success — pipeline may not exist if test failed early.
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
//
// The overall deadline covers both the polling phase (waiting for the
// build to appear) and the streaming phase (fly watch running). This
// prevents tests from hanging indefinitely when a build is stuck.
func waitForBuildAndWatch(jobName string, buildName ...string) *gexec.Session {
	args := []string{"watch", "-j", inPipeline(jobName)}
	if len(buildName) > 0 {
		args = append(args, "-b", buildName[0])
	}

	keepPollingCheck := regexp.MustCompile(
		"job has no builds|build not found|failed to get build",
	)

	deadline := time.Now().Add(3 * time.Minute)
	for {
		session := fly.Start(args...)

		// Wait for fly watch to exit, but enforce the overall deadline.
		// Without this, fly watch can hang forever on a stuck build.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			session.Kill()
			Eventually(session).Should(gexec.Exit())
			Fail(fmt.Sprintf("timed out waiting for build to complete (args: %v)", args))
		}
		select {
		case <-session.Exited:
			// normal exit
		case <-time.After(remaining):
			session.Kill()
			Eventually(session).Should(gexec.Exit())
			Fail(fmt.Sprintf("timed out waiting for build to complete (args: %v)", args))
		}

		if session.ExitCode() == 1 {
			output := strings.TrimSpace(string(session.Err.Contents()))
			if keepPollingCheck.MatchString(output) {
				if time.Now().After(deadline) {
					Fail(fmt.Sprintf("timed out waiting for build: %s (args: %v)", output, args))
				}
				time.Sleep(time.Second)
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

// cleanupOrphanedPods deletes Completed and Failed pods left behind after
// a pipeline is destroyed. This prevents pod accumulation across test runs.
func cleanupOrphanedPods() {
	pods, err := kubeClient.CoreV1().Pods(config.Namespace).List(
		context.Background(),
		metav1.ListOptions{LabelSelector: "concourse.ci/worker"},
	)
	if err != nil {
		return
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			_ = kubeClient.CoreV1().Pods(config.Namespace).Delete(
				context.Background(),
				p.Name,
				metav1.DeleteOptions{},
			)
		}
	}
}
