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
	exists := exec.Command("docker", "image", "inspect", image).Run() == nil

	// Build when absent, or when a rebuild is forced. Reusing a stale local
	// image silently tests old code (the image tag is reused as-is), so
	// CONCOURSE_REBUILD_IMAGE=1 is the escape hatch for local iteration. CI
	// pre-builds this image in the pipeline task, so the default path reuses it.
	if !exists || os.Getenv("CONCOURSE_REBUILD_IMAGE") == "1" {
		log.Printf("Building Concourse image %q from source...", image)
		root := mustRepoRoot()
		cmd := exec.Command("docker", "build", "-f", "Dockerfile.build", "-t", image, root)
		cmd.Dir = root
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("failed to build Concourse image: %v", err)
		}
	}

	// Log the image's id + creation time so a stale reuse is diagnosable from
	// test output — otherwise a stale binary deploy is invisible in the logs.
	if out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}} created={{.Created}}", image).Output(); err == nil {
		log.Printf("Using Concourse image %q: %s", image, strings.TrimSpace(string(out)))
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

	images := []string{
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

	// Build and load the oom-trigger image used by pod_resilience_test.go.
	// This is a tiny static Go binary that reliably triggers the OOM killer
	// by allocating large heap slices — shell-based approaches (awk, dd)
	// don't reliably count against the container memory cgroup in K3s.
	buildAndLoadOOMTriggerImage(ctx)
}

// buildAndLoadOOMTriggerImage compiles cmd/oom-trigger as a static binary,
// packages it into a scratch Docker image, and loads it into the K3s cluster.
func buildAndLoadOOMTriggerImage(ctx context.Context) {
	const imageName = "oom-trigger:latest"

	// Check if image already exists (e.g. built by CI pipeline).
	if err := exec.Command("docker", "image", "inspect", imageName).Run(); err == nil {
		log.Printf("oom-trigger image already exists, loading into K3s...")
		if err := k3sContainer.LoadImages(ctx, imageName); err != nil {
			log.Printf("warning: failed to load %s into K3s: %v", imageName, err)
		}
		return
	}

	root := mustRepoRoot()
	tmpDir, err := os.MkdirTemp("", "oom-trigger-build-*")
	if err != nil {
		log.Printf("warning: failed to create temp dir for oom-trigger: %v", err)
		return
	}
	defer os.RemoveAll(tmpDir)

	log.Println("Building oom-trigger binary...")
	binPath := filepath.Join(tmpDir, "oom-trigger")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/oom-trigger")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		log.Printf("warning: failed to build oom-trigger: %v", err)
		return
	}

	dockerfile := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\nCOPY oom-trigger /oom-trigger\nENTRYPOINT [\"/oom-trigger\"]\n"), 0644); err != nil {
		log.Printf("warning: failed to write oom-trigger Dockerfile: %v", err)
		return
	}

	log.Println("Building oom-trigger Docker image...")
	dockerBuild := exec.Command("docker", "build", "-t", imageName, tmpDir)
	dockerBuild.Stdout = os.Stderr
	dockerBuild.Stderr = os.Stderr
	if err := dockerBuild.Run(); err != nil {
		log.Printf("warning: failed to build oom-trigger image: %v", err)
		return
	}

	log.Println("Loading oom-trigger into K3s cluster...")
	if err := k3sContainer.LoadImages(ctx, imageName); err != nil {
		log.Printf("warning: failed to load %s into K3s: %v", imageName, err)
	}
}

// resolveCapabilitySecretName is the Secret the chart is pointed at to arm
// signed resolve capabilities. The chart never generates key material —
// generating it needs Helm `lookup`, which returns empty under `helm template`
// and so re-mints the key on every GitOps render — so the key is created here,
// once, exactly as an operator would.
const resolveCapabilitySecretName = "concourse-resolve-capability"

// createResolveCapabilitySecret puts a 32-byte key in the namespace. Content is
// fixed rather than random: a test cluster is thrown away, and a deterministic
// key makes a failure reproducible.
func createResolveCapabilitySecret(kubeconfig, namespace string) {
	log.Printf("Creating resolve-capability Secret %s in %s...", resolveCapabilitySecretName, namespace)

	key := strings.Repeat("k", 32)
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig,
		"-n", namespace,
		"create", "secret", "generic", resolveCapabilitySecretName,
		"--from-literal=resolve.key="+key,
		"--dry-run=client", "-o", "yaml")
	manifest, err := cmd.Output()
	if err != nil {
		// Fatal, not a warning: without the Secret the pods mount a volume
		// that does not exist and the suite dies later as an unrelated
		// pod-readiness timeout, naming the wrong cause.
		log.Fatalf("could not render resolve-capability Secret: %v", err)
	}

	// Apply rather than create: this runs on every install, including upgrades
	// of a cluster that already has it.
	apply := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "apply", "-f", "-")
	apply.Stdin = strings.NewReader(string(manifest))
	apply.Stdout = os.Stderr
	apply.Stderr = os.Stderr
	if err := apply.Run(); err != nil {
		log.Fatalf("could not apply resolve-capability Secret: %v", err)
	}
}

// labelNodesForArtifactCache labels all K3s nodes with the label that
// the JetBridge artifact daemon node affinity requires.
func labelNodesForArtifactCache(kubeconfig string) {
	log.Println("Labeling K3s nodes for artifact cache scheduling...")
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig,
		"label", "nodes", "--all", "concourse.dev/artifact-cache=ready", "--overwrite")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("warning: failed to label nodes: %v", err)
	}
}

// waitForCoreDNS waits until K3s's CoreDNS is running and ready.
func waitForCoreDNS(kubeconfig string) {
	log.Println("Waiting for CoreDNS to be ready...")
	waitCmd := exec.Command("kubectl",
		"--kubeconfig", kubeconfig,
		"-n", "kube-system",
		"wait", "--for=condition=ready", "pod",
		"-l", "k8s-app=kube-dns",
		"--timeout=120s",
	)
	waitCmd.Stdout = os.Stderr
	waitCmd.Stderr = os.Stderr
	if err := waitCmd.Run(); err != nil {
		log.Printf("warning: CoreDNS wait failed: %v (proceeding anyway)", err)
	} else {
		log.Println("CoreDNS is ready.")
	}
}

// helmDeployConcourse deploys Concourse via the local Helm chart.
func helmDeployConcourse(kubeconfig, namespace, chartPath, image string) {
	repo, tag := splitImageRef(image)

	waitForCoreDNS(kubeconfig)
	labelNodesForArtifactCache(kubeconfig)

	exec.Command("kubectl", "--kubeconfig", kubeconfig,
		"create", "namespace", namespace).Run()

	createResolveCapabilitySecret(kubeconfig, namespace)

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
		"--set", "cachePvc.enabled=false",
		"--set", "artifactStorePvc.enabled=false",
		"--set", "artifactDaemon.enabled=true",
		// Exercise the SIGNED resolve path; see createResolveCapabilitySecret.
		"--set", "artifactDaemon.resolveCapability.existingSecret=" + resolveCapabilitySecretName,
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

// portForwardGiveUp bounds how long the manager keeps trying to rebuild a
// forwarder that will not come back. A pod that moves is back inside seconds;
// anything still failing after this is a cluster that has gone away.
const portForwardGiveUp = 2 * time.Minute

func (m *portForwardManager) run(initialReady chan<- struct{}) {
	first := true
	var downSince time.Time

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

		started := time.Now()
		err := m.forward(readySig)
		first = false

		select {
		case <-m.done:
			return
		default:
		}

		// A forwarder that carried traffic and then dropped is a transient; only
		// consecutive immediate failures mean the API server is gone.
		if time.Since(started) > 5*time.Second {
			downSince = time.Time{}
		}
		if downSince.IsZero() {
			downSince = time.Now()
			log.Printf("Port-forward died (%v), restarting...", err)
		}

		if time.Since(downSince) > portForwardGiveUp {
			// Retrying once a second until the job's four-hour timeout buries the
			// real failure under thousands of identical lines and reports no
			// verdict at all. Stop here: the next request meets a closed port and
			// fails the spec that asked for it, with the reason attached.
			log.Printf("Port-forward has failed continuously for %s (%v); giving up, the cluster is gone",
				portForwardGiveUp, err)
			dumpDockerDiagnostics("port-forward gave up, cluster presumed gone")
			return
		}

		time.Sleep(time.Second)
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
	// Ginkgo runs a precompiled suite from the binary's own directory, where
	// `git rev-parse` finds nothing. The old fallback answered /src -- whatever
	// source the runner image happened to bake in -- so the suite deployed one
	// commit's chart against another commit's binaries and nobody could see it
	// in the log. Take the checkout from the environment when the caller knows
	// it, and fail loudly rather than substitute a tree nobody asked for.
	if root := os.Getenv("JETBRIDGE_REPO_ROOT"); root != "" {
		return root
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		log.Fatalf("repo root unknown: not a git checkout here and JETBRIDGE_REPO_ROOT is unset: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------
// Diagnostics
//
// The K3s container runs as a privileged Docker container inside a DinD
// task pod with no external log aggregation. When it dies (OOM, disk
// full, engine crash), the only record of why is whatever we capture
// here before the container/pod is gone. Every command below is
// best-effort: a diagnostic that fails to run must never fail or panic
// the suite, it just logs its own failure and moves on.
// ---------------------------------------------------------------------

// dumpDockerDiagnostics captures host-level Docker/K3s state to the suite
// log. Called when the port-forward manager gives up on a dead cluster,
// and again as a fallback in AfterSuite.
func dumpDockerDiagnostics(reason string) {
	log.Printf("=== docker diagnostics (%s) ===", reason)

	runDiagCmd("docker ps -a", "docker", "ps", "-a")

	if k3sContainer != nil {
		if id := k3sContainer.GetContainerID(); id != "" {
			runDiagCmd("k3s container logs (tail 200)", "docker", "logs", "--tail", "200", id)
			runDiagCmd("k3s container inspect state (OOMKilled ExitCode Error)", "docker", "inspect",
				"--format", "{{.State.OOMKilled}} {{.State.ExitCode}} {{.State.Error}}", id)
		}
	}

	runDiagCmd("df /var/lib/docker", "df", "-h", "/var/lib/docker")
	runDiagCmd("free -m", "free", "-m")
	// dmesg has no portable --tail; pipe through the shell instead. Often
	// unreadable without CAP_SYSLOG inside the task pod -- that's fine,
	// runDiagCmd just logs the failure.
	runDiagCmd("dmesg tail 50", "sh", "-c", "dmesg | tail -50")

	log.Printf("=== end docker diagnostics (%s) ===", reason)
}

// runDiagCmd runs a diagnostic command and logs its output (or, if it
// failed to run, logs that failure). Never panics, never fails the suite.
func runDiagCmd(label, name string, args ...string) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		log.Printf("diag %s: failed to run: %v\n%s", label, err, string(out))
		return
	}
	log.Printf("diag %s:\n%s", label, string(out))
}

// resourceStampDone stops the periodic resource-stamp goroutine started by
// startResourceStamp. nil until startResourceStamp runs.
var resourceStampDone chan struct{}

// resourceStampInterval is how often the trend line below is printed.
// Cheap enough to run often; every 5 minutes gives ~30-40 samples across a
// 2-3 hour run, enough to see a slow climb toward the tmpfs limit before a
// death rather than only a "gone" line at the end.
const resourceStampInterval = 5 * time.Minute

// startResourceStamp begins printing a one-line resource trend stamp every
// resourceStampInterval. Call stopResourceStamp to stop it.
func startResourceStamp() {
	resourceStampDone = make(chan struct{})
	go func() {
		ticker := time.NewTicker(resourceStampInterval)
		defer ticker.Stop()
		for {
			select {
			case <-resourceStampDone:
				return
			case <-ticker.C:
				logResourceStamp()
			}
		}
	}()
}

// stopResourceStamp stops the goroutine started by startResourceStamp, if any.
func stopResourceStamp() {
	if resourceStampDone != nil {
		close(resourceStampDone)
		resourceStampDone = nil
	}
}

func logResourceStamp() {
	log.Printf("resource-stamp: time=%s docker-root-use=%s free-mem-avail-mb=%s running-containers=%s",
		time.Now().UTC().Format(time.RFC3339),
		diagDfPercent("/var/lib/docker"), diagFreeAvailMB(), diagRunningContainers())
}

// diagDfPercent returns the use% of the filesystem containing path, or "?"
// if it can't be determined.
func diagDfPercent(path string) string {
	out, err := exec.Command("df", "--output=pcent", path).Output()
	if err != nil {
		return "?"
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "?"
	}
	return fields[len(fields)-1]
}

// diagFreeAvailMB returns the "available" column of `free -m`, or "?" if it
// can't be determined.
func diagFreeAvailMB() string {
	out, err := exec.Command("free", "-m").Output()
	if err != nil {
		return "?"
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 7 && fields[0] == "Mem:" {
			return fields[6]
		}
	}
	return "?"
}

// diagRunningContainers returns the number of running Docker containers, or
// "?" if it can't be determined.
func diagRunningContainers() string {
	out, err := exec.Command("docker", "ps", "-q").Output()
	if err != nil {
		return "?"
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			n++
		}
	}
	return fmt.Sprintf("%d", n)
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
