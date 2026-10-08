// hangar_live only, never live: this contract relabels a cluster node
// (concourse.dev/artifact-cache and the output plane's two readiness labels)
// to schedule its generated Pod, which is fine on a disposable cluster with a
// cluster-admin service account and wrong on the deployed cluster the live
// tier runs against, where the task's namespaced account cannot list nodes
// and must not relabel the production node even if it could. Its CI home is
// the hangar-generated-pod-contract job in deploy/k8s-e2e-pipeline.yml, which
// stands up a throwaway K3s cluster for it and hands it a cluster-admin
// kubeconfig; build-and-vet only compiles it.
//go:build hangar_live

package jetbridge

// The repository-wide live tag retains this contract alongside the older live
// suite. The narrower hangar_live tag lets CI compile and run this internal
// generated-Pod contract independently while that external suite finishes its
// PostgreSQL harness migration.

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	atcruntime "github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// liveFlowFixtureImage terminates TLS in front of the fixture's shell
// handler. The managed-input init dials its node's artifact daemon over
// https, as it does in every deployment, so a plain `nc` cannot stand in for
// the daemon here the way it does for the capture-hold route beside this
// contract; socat's OPENSSL-LISTEN can. The job that runs this contract loads
// the image into the cluster alongside busybox and alpine.
const liveFlowFixtureImage = "alpine/socat:latest"

// TestLiveHangarGeneratedPodMaterializesManagedRead is CI-only by
// construction: it needs the hangar_live build tag, a Linux Kubernetes node,
// and a kubeconfig or an in-cluster service account
// (hangar-generated-pod-contract in deploy/k8s-e2e-pipeline.yml provides
// both, on a K3s cluster it creates and destroys). It deliberately does not
// silently fall back to a fake client or host shell.
//
// It needs no Hangar store and no artifact daemon: the output plane's
// POST /read/v1/materialize that the generated Pod's managed-input init calls
// is stood up below as a fixture Pod -- socat terminating TLS in front of a
// shell handler that writes the tree onto the node and answers 204 -- and the
// read warrant is signed here with a Hangar key held in process. What is
// under test is the Pod the runtime generates (its placement on a node ready
// for the output plane, its read-only input mount, its managed-input init)
// and the materialization that init will accept: the canonical tree with
// root mode 555, and the materialization receipt equal to the JSON of the
// tree ref at mode 444. The fixture writes exactly that; it verifies nothing,
// and the contract claims nothing about the daemon's own answer.
func TestLiveHangarGeneratedPodMaterializesManagedRead(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("real Linux execution on K3s is CI-only on macOS")
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	namespace := os.Getenv("K8S_TEST_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	cfg := NewConfig(namespace, kubeconfig)
	client, err := NewClientset(cfg)
	if err != nil {
		t.Fatalf("create live Kubernetes client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		t.Fatalf("list K3s nodes: count=%d err=%v", len(nodes.Items), err)
	}
	node := nodes.Items[0]
	restoreNodeLabels := map[string]*string{}
	for _, key := range []string{"concourse.dev/artifact-cache", executioncontrol.ReadyLabel, output.ReadyLabel} {
		if value, found := node.Labels[key]; found {
			copy := value
			restoreNodeLabels[key] = &copy
		} else {
			restoreNodeLabels[key] = nil
		}
		node.Labels[key] = "ready"
	}
	if _, err := client.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("label K3s node for the generated Pod: %v", err)
	}
	t.Cleanup(func() {
		latest, getErr := client.CoreV1().Nodes().Get(context.Background(), node.Name, metav1.GetOptions{})
		if getErr != nil {
			t.Errorf("restore K3s node labels: %v", getErr)
			return
		}
		for key, value := range restoreNodeLabels {
			if value == nil {
				delete(latest.Labels, key)
			} else {
				latest.Labels[key] = *value
			}
		}
		if _, updateErr := client.CoreV1().Nodes().Update(context.Background(), latest, metav1.UpdateOptions{}); updateErr != nil {
			t.Errorf("restore K3s node labels: %v", updateErr)
		}
	})

	ref := hangar.TreeRef{
		Scope:      "ci",
		Digest:     "sha256:6738ec08b183496d6d90375bbca158e7460d4a8ff61b154c01bead9de12a2fac",
		Generation: 7,
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	signer, err := hangar.NewSigner(key, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	unique := fmt.Sprintf("hangar-live-%d", time.Now().UnixNano())
	hostRoot := "/tmp/" + unique

	// The runtime is configured for TLS to the daemon, as every deployment
	// is. The init presents no certificate and checks none (it dials its
	// node by IP, which is no certificate's SAN), so the fixture's serving
	// certificate and the client triple below are only what the two sides
	// need to speak https at all.
	ca := newLiveDiskCA(t)
	serverCert, serverKey := ca.issue(t, unique+"-daemon", []string{unique + "-daemon"}, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert, clientKey := ca.issue(t, unique+"-web", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	dir := t.TempDir()
	clientCertPath, clientKeyPath, caPath := filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"), filepath.Join(dir, "ca.crt")
	for path, data := range map[string][]byte{clientCertPath: clientCert, clientKeyPath: clientKey, caPath: ca.certPEM} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Namespace = namespace
	cfg.ArtifactDaemonHostPath = hostRoot
	cfg.ArtifactDaemonPort = 31780
	cfg.ArtifactDaemonTLSEnabled = true
	cfg.ArtifactDaemonTLSCert, cfg.ArtifactDaemonTLSKey, cfg.ArtifactDaemonTLSCACert = clientCertPath, clientKeyPath, caPath
	// The chart's own kubernetes.artifactHelperImage: Alpine's wget speaks
	// TLS through ssl_client, as it does in every deployment.
	cfg.ArtifactHelperImage = "alpine:latest"
	cfg.OutputPlaneEnabled = true

	handle := "managed-read-consumer"
	spec := atcruntime.ContainerSpec{
		Dir: "/work", Type: db.ContainerTypeTask,
		ImageSpec: atcruntime.ImageSpec{ImageURL: "busybox:latest"},
		Inputs:    []atcruntime.Input{{HangarTree: &ref, DestinationPath: "/work/exact"}},
	}
	spec.Inputs[0].HangarRead = liveManagedRead(t, signer, executioncontrol.NodeUID(node.UID), handle, inputVolumeName(spec, 0), ref)
	container := &Container{
		handle:        handle,
		podName:       unique + "-step",
		metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: spec,
		config:        cfg, storageBackend: NewDaemonSetBackend(cfg, nil, nil, nil), properties: map[string]string{},
	}
	receipt, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	receiptB64 := base64.StdEncoding.EncodeToString(receipt)
	mainScript := fmt.Sprintf(`set -eu
test "$(cat '/work/exact/literal [x]')" = 'payload'
test "$(cat /work/exact/nested/run.sh)" = 'run'
test -d /work/exact/empty
test ! -L /work/exact/empty
test "$(readlink /work/exact/latest)" = 'nested/run.sh'
test "$(stat -c '%%a' /work/exact)" = 555
test "$(stat -c '%%a' /work/exact/empty)" = 555
test "$(stat -c '%%a' '/work/exact/literal [x]')" = 444
test "$(stat -c '%%a' /work/exact/nested/run.sh)" = 444
test "$(stat -c '%%a' /work/exact/.hangar-materialized)" = 444
printf '%%s' '%s' | base64 -d | cmp - /work/exact/.hangar-materialized
if touch /work/exact/must-not-write 2>/dev/null; then exit 91; fi
`, receiptB64)
	pod, err := container.buildPod(atcruntime.ProcessSpec{}, []string{"sh", "-c", mainScript}, nil)
	if err != nil {
		t.Fatalf("generate the task Pod: %v", err)
	}
	assertLivePodMountsResolve(t, pod)
	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != "materialize-run-input-0" || pod.Spec.InitContainers[0].Image != cfg.ArtifactHelperImage {
		t.Fatalf("generated managed-input init = %+v", pod.Spec.InitContainers)
	}
	inputMount := liveMountAt(t, pod.Spec.Containers[0].VolumeMounts, "/work/exact")
	if !inputMount.ReadOnly {
		t.Fatal("generated main Run input mount is writable")
	}
	assertLiveHangarAffinity(t, pod)

	// The fixture stands in for POST /read/v1/materialize: it reads the
	// request through to the blank line, writes the canonical tree and the
	// materialization receipt where the daemon would -- the step volume
	// beneath the node's steps root -- and answers an empty 204. It never
	// reads the body, so it checks no warrant; that is the daemon's, not the
	// Pod's, and not under test here.
	volumeName := inputMount.Name
	fixtureScript := fmt.Sprintf(`set -eu
ROOT='/host/steps/%s/%s'
mkdir -p "$ROOT/nested" "$ROOT/empty"
printf 'payload' >"$ROOT/literal [x]"
printf 'run' >"$ROOT/nested/run.sh"
ln -s nested/run.sh "$ROOT/latest"
printf '%%s' '%s' | base64 -d >"$ROOT/.hangar-materialized"
chmod 444 "$ROOT/literal [x]" "$ROOT/nested/run.sh" "$ROOT/.hangar-materialized"
chmod 555 "$ROOT/nested" "$ROOT/empty" "$ROOT"
while IFS= read -r line; do [ "$line" = "$(printf '\r')" ] && break; done
printf 'HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n'
`, handle, volumeName, receiptB64)
	hostPathType := corev1.HostPathDirectoryOrCreate
	fixture := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: unique + "-daemon", Namespace: namespace},
		Spec: corev1.PodSpec{
			NodeName: node.Name, HostNetwork: true, RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name: "daemon-fixture", Image: liveFlowFixtureImage,
				Command: []string{"sh", "-c", "printf '%s' \"$HANDLER\" >/tmp/handler; chmod 700 /tmp/handler; " +
					"printf '%s' \"$SERVER_CERT\" >/tmp/server.crt; printf '%s' \"$SERVER_KEY\" >/tmp/server.key; chmod 600 /tmp/server.key; " +
					"exec socat OPENSSL-LISTEN:31780,reuseaddr,fork,cert=/tmp/server.crt,key=/tmp/server.key,verify=0 EXEC:/tmp/handler"},
				// The generated Pod under test is PullIfNotPresent; say the
				// same for the fixture. Left unset, a `:latest` tag defaults
				// to Always, so this scaffolding Pod -- which proves nothing
				// on its own -- would make the contract fail on a registry
				// timeout inside a nested CI cluster that already has the
				// image.
				ImagePullPolicy: corev1.PullIfNotPresent,
				Env: []corev1.EnvVar{
					{Name: "HANDLER", Value: "#!/bin/sh\n" + fixtureScript},
					{Name: "SERVER_CERT", Value: string(serverCert)},
					{Name: "SERVER_KEY", Value: string(serverKey)},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "host", MountPath: "/host"}},
			}},
			Volumes: []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: hostRoot, Type: &hostPathType}}}},
		},
	}
	createLivePod(t, ctx, client, fixture)
	waitLivePodRunning(t, ctx, client, namespace, fixture.Name)
	time.Sleep(time.Second)
	createLivePod(t, ctx, client, pod)
	waitLivePodSucceeded(t, ctx, client, namespace, pod.Name)
}

func createLivePod(t *testing.T, ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) {
	t.Helper()
	if _, err := client.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Pod %s: %v", pod.Name, err)
	}
	t.Cleanup(func() {
		grace := int64(0)
		_ = client.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &grace})
	})
}

func waitLivePodRunning(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace, name string) {
	t.Helper()
	for {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get Pod %s: %v", name, err)
		}
		if pod.Status.Phase == corev1.PodRunning {
			return
		}
		if pod.Status.Phase == corev1.PodFailed {
			t.Fatalf("Pod %s failed before running", name)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Pod %s running: %v", name, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func waitLivePodSucceeded(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace, name string) {
	t.Helper()
	for {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get Pod %s: %v", name, err)
		}
		if pod.Status.Phase == corev1.PodSucceeded {
			return
		}
		if pod.Status.Phase == corev1.PodFailed {
			var diagnostics []string
			for _, status := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
				logs, _ := client.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{Container: status.Name}).DoRaw(ctx)
				diagnostics = append(diagnostics, status.Name+": "+string(logs))
			}
			t.Fatalf("generated Pod %s failed: %s", name, strings.Join(diagnostics, "\n"))
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Pod %s success: %v", name, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func assertLivePodMountsResolve(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	volumes := map[string]struct{}{}
	for _, volume := range pod.Spec.Volumes {
		volumes[volume.Name] = struct{}{}
	}
	if len(volumes) == 0 {
		t.Fatal("generated Pod has no volumes")
	}
	for _, containers := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, container := range containers {
			for _, mount := range container.VolumeMounts {
				if _, found := volumes[mount.Name]; !found {
					t.Fatalf("container %q mount %q has no Pod volume", container.Name, mount.Name)
				}
			}
		}
	}
}

func liveMountAt(t *testing.T, mounts []corev1.VolumeMount, path string) corev1.VolumeMount {
	t.Helper()
	for _, mount := range mounts {
		if mount.MountPath == path {
			return mount
		}
	}
	t.Fatalf("no generated mount at %q", path)
	return corev1.VolumeMount{}
}

// assertLiveHangarAffinity requires the placement a Pod with a Run input
// gets: a node whose cache daemon is ready and whose output plane serves both
// exact execution control and the capture extension, since a managed read is
// the output plane's route.
func assertLiveHangarAffinity(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	want := map[string]bool{"concourse.dev/artifact-cache": false, executioncontrol.ReadyLabel: false, output.ReadyLabel: false}
	required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) == 0 {
		t.Fatal("generated Pod has no required node affinity")
	}
	for _, term := range required.NodeSelectorTerms {
		for _, expression := range term.MatchExpressions {
			if _, found := want[expression.Key]; found && expression.Operator == corev1.NodeSelectorOpIn && len(expression.Values) == 1 && expression.Values[0] == "ready" {
				want[expression.Key] = true
			}
		}
	}
	for key, found := range want {
		if !found {
			t.Fatalf("generated Pod missing %s In [ready]", key)
		}
	}
}
