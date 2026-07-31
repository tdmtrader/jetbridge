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
	"regexp"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	containername "github.com/google/go-containerregistry/pkg/name"
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

const artifactHelperSourceImage = "docker.io/library/busybox:1.37.0"

var exactArtifactHelperImage = regexp.MustCompile(`^[^@\s]+@sha256:[a-f0-9]{64}$`)

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
		"docker.io/library/alpine:3.19",
		"docker.io/library/alpine:latest",
		"docker.io/library/nginx:alpine",
	}
	helperSource := artifactHelperSourceImage
	if configured := strings.TrimSpace(os.Getenv("ARTIFACT_HELPER_IMAGE")); configured != "" {
		if !exactArtifactHelperImage.MatchString(configured) {
			log.Fatalf("ARTIFACT_HELPER_IMAGE must be an exact @sha256 reference, got %q", configured)
		}
		helperSource = configured
	}
	images = append(images, helperSource)

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

// resolvedArtifactHelperImage returns the immutable helper reference that was
// loaded into this process's K3s cluster. An explicit private helper may be
// supplied, but never as a mutable tag.
func resolvedArtifactHelperImage() string {
	if configured := strings.TrimSpace(os.Getenv("ARTIFACT_HELPER_IMAGE")); configured != "" {
		if !exactArtifactHelperImage.MatchString(configured) {
			log.Fatalf("ARTIFACT_HELPER_IMAGE must be an exact @sha256 reference, got %q", configured)
		}
		return qualifyHelperDigest(configured)
	}
	output, err := exec.Command(
		"docker", "image", "inspect", "--format", "{{index .RepoDigests 0}}", artifactHelperSourceImage,
	).Output()
	if err != nil {
		log.Fatalf("resolve artifact helper digest for %s: %v", artifactHelperSourceImage, err)
	}
	resolved := strings.TrimSpace(string(output))
	if !exactArtifactHelperImage.MatchString(resolved) {
		log.Fatalf("docker returned invalid artifact helper digest %q for %s", resolved, artifactHelperSourceImage)
	}
	return qualifyHelperDigest(resolved)
}

// qualifyHelperDigest expands a digest reference to its fully-qualified form
// before it reaches Helm.
//
// exactArtifactHelperImage (and the chart's identical regex) both accept a
// reference with no registry, but the web parses this flag with
// name.StrictValidation, which requires one — so "busybox@sha256:..." renders
// and deploys happily and then crash-loops the web with "strict validation
// requires the registry to be explicitly defined". That is precisely what
// `docker image inspect --format {{index .RepoDigests 0}}` returns for an
// official image: Docker prints the short form regardless of how the image was
// pulled, so the fully-qualified constant above does not survive the round trip.
//
// Normalizing through the same library the validator uses yields
// "index.docker.io/library/busybox@sha256:...", and is a no-op for references
// that already carry a registry.
func qualifyHelperDigest(reference string) string {
	digest, err := containername.NewDigest(reference)
	if err != nil {
		log.Fatalf("artifact helper reference %q is not a valid digest: %v", reference, err)
	}
	qualified := digest.Name()
	if err := atc.ValidatePinnedOCIImage(qualified); err != nil {
		log.Fatalf("artifact helper reference %q is not deployable: %v", qualified, err)
	}
	return qualified
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

const (
	// hangarEmulatorBucket is the Hangar bucket these tests seal snapshots
	// into. It exists as a directory under the emulator's filesystem root —
	// fake-gcs-server treats each top-level directory as a bucket, so creating
	// it is a mkdir rather than an API call that would have to be retried until
	// the server is up.
	hangarEmulatorBucket = "concourse-hangar"

	// hangarEmulatorImage matches the emulator the live cluster runs, so a
	// snapshot that seals here seals the same way in production.
	hangarEmulatorImage = "fsouza/fake-gcs-server:1.52.3"
)

// hangarEmulatorEndpoint is the GCS-compatible JSON endpoint the artifact
// daemon talks to. The daemon passes it to the GCS client verbatim, which is
// why it carries the /storage/v1/ suffix while the emulator's own external URL
// does not.
func hangarEmulatorEndpoint(namespace string) string {
	return fmt.Sprintf("http://fake-gcs.%s.svc:4443/storage/v1/", namespace)
}

// deployHangarEmulator stands up a GCS-compatible object store for the artifact
// daemon to seal agent snapshots into.
//
// The daemon builds its Hangar client at startup and calls os.Exit(1) if that
// fails, before it ever binds its listener. With agentSnapshots enabled and no
// endpoint configured it reaches for production application-default
// credentials, which do not exist in an ephemeral K3s cluster — so the daemon
// dies, its hostPort answers nothing, and every step that fetches an artifact
// fails with "connection refused" from an init container that looks like the
// culprit. An in-cluster emulator is what keeps that failure from being the
// suite's normal state.
//
// Storage is an emptyDir: snapshots live exactly as long as the cluster does,
// and nothing here should outlive the run that wrote it.
func deployHangarEmulator(kubeconfig, namespace string) {
	log.Printf("Deploying Hangar GCS emulator into namespace %s...", namespace)

	manifest := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hangar-fake-gcs
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hangar-fake-gcs
  template:
    metadata:
      labels:
        app: hangar-fake-gcs
    spec:
      initContainers:
        - name: create-bucket
          image: %[2]s
          command: ["/bin/sh", "-c", "mkdir -p /data/%[3]s"]
          volumeMounts:
            - name: data
              mountPath: /data
      containers:
        - name: fake-gcs
          image: %[2]s
          args:
            - -scheme=http
            - -port=4443
            - -backend=filesystem
            - -filesystem-root=/data
            - -external-url=http://fake-gcs.%[1]s.svc:4443
          ports:
            - name: http
              containerPort: 4443
          readinessProbe:
            tcpSocket:
              port: 4443
            initialDelaySeconds: 1
            periodSeconds: 2
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: fake-gcs
  namespace: %[1]s
spec:
  selector:
    app: hangar-fake-gcs
  ports:
    - name: http
      port: 4443
      targetPort: 4443
`, namespace, hangarEmulatorImage, hangarEmulatorBucket)

	apply := exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-f", "-")
	apply.Stdin = strings.NewReader(manifest)
	apply.Stdout = os.Stderr
	apply.Stderr = os.Stderr
	if err := apply.Run(); err != nil {
		log.Fatalf("failed to deploy Hangar GCS emulator: %v", err)
	}

	wait := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace,
		"wait", "--for=condition=available", "deployment/hangar-fake-gcs", "--timeout=180s")
	wait.Stdout = os.Stderr
	wait.Stderr = os.Stderr
	if err := wait.Run(); err != nil {
		describePods(kubeconfig, namespace)
		log.Fatalf("timed out waiting for the Hangar GCS emulator: %v", err)
	}
	log.Println("Hangar GCS emulator is ready.")
}

// waitForArtifactDaemon blocks until the artifact daemon is serving on every
// node, and explains itself when it is not.
//
// Waiting only for web lets the suite start against a daemon that is still
// crash-looping, and the daemon is not something a spec ever names: it surfaces
// dozens of specs later as a `fetch-inputs` init container that cannot reach
// a host port, which reads as a broken artifact protocol rather than a daemon
// that never started. Failing here instead, with the daemon's own logs,
// names the actual problem.
func waitForArtifactDaemon(kubeconfig, namespace string) {
	log.Println("Waiting for the artifact daemon to be ready...")
	wait := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace,
		"wait", "--for=condition=ready", "pod",
		"-l", "app.kubernetes.io/component=artifact-daemon",
		"--timeout=180s")
	wait.Stdout = os.Stderr
	wait.Stderr = os.Stderr
	if err := wait.Run(); err == nil {
		log.Println("Artifact daemon is ready.")
		return
	} else {
		log.Printf("artifact daemon did not become ready: %v", err)
	}

	describe := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace,
		"describe", "pods", "-l", "app.kubernetes.io/component=artifact-daemon")
	describe.Stdout = os.Stderr
	describe.Stderr = os.Stderr
	describe.Run()
	logs := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace,
		"logs", "-l", "app.kubernetes.io/component=artifact-daemon",
		"--all-containers", "--tail=80")
	logs.Stdout = os.Stderr
	logs.Stderr = os.Stderr
	logs.Run()
	log.Fatalf("artifact daemon never became ready; every artifact fetch would fail with connection refused")
}

// describePods dumps pod state for diagnosis when a wait times out.
func describePods(kubeconfig, namespace string) {
	desc := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "describe", "pods")
	desc.Stdout = os.Stderr
	desc.Stderr = os.Stderr
	desc.Run()
}

// helmDeployConcourse deploys Concourse via the local Helm chart.
func helmDeployConcourse(kubeconfig, namespace, chartPath, image string) {
	repo, tag := splitImageRef(image)
	artifactHelperImage := resolvedArtifactHelperImage()

	waitForCoreDNS(kubeconfig)
	labelNodesForArtifactCache(kubeconfig)

	exec.Command("kubectl", "--kubeconfig", kubeconfig,
		"create", "namespace", namespace).Run()

	deployHangarEmulator(kubeconfig, namespace)

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
		"--set-string", "kubernetes.artifactHelperImage=" + artifactHelperImage,
		"--set", "postgresql.persistence.enabled=false",
		// Keep the artifact daemon explicit so the test documents the required
		// runtime mode even though it is also the chart default.
		"--set", "artifactDaemon.enabled=true",
		"--set", "artifactDaemon.tls.enabled=true",
		"--set", "agentSnapshots.enabled=true",
		"--set", "agentSnapshots.replicationFactor=1",
		// Snapshots need durable Hangar storage, and the daemon exits at
		// startup rather than run without the bucket it was told to use. Point
		// it at the in-cluster emulator; production credentials do not exist
		// here and reaching for them is what kills the daemon.
		"--set", "artifactDaemon.hangar.bucket=" + hangarEmulatorBucket,
		"--set", "artifactDaemon.hangar.endpoint=" + hangarEmulatorEndpoint(namespace),
		"--set", "agentExperiments.runnerEnabled=true",
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
		describePods(kubeconfig, namespace)
		log.Fatalf("timed out waiting for concourse-web pod: %v", err)
	}

	waitForArtifactDaemon(kubeconfig, namespace)
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
		// Fallback for CI images where source is copied (not a git clone).
		if _, statErr := os.Stat("/src/go.mod"); statErr == nil {
			return "/src"
		}
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
