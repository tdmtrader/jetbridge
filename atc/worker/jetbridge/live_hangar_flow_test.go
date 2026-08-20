//go:build live

package jetbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	atcruntime "github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// TestLiveHangarGeneratedPodMaterializesStrictTree is CI-only by construction:
// it needs the repository's live build tag, a Linux Kubernetes node, and a
// kubeconfig (the K3s behavioral tier provides all three). It deliberately does
// not silently fall back to a fake client or host shell.
func TestLiveHangarGeneratedPodMaterializesStrictTree(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("real BusyBox/Linux and K3s execution is CI-only on macOS")
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("live Hangar flow requires KUBECONFIG for the CI/K3s cluster")
	}
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
	for _, key := range []string{"concourse.dev/artifact-cache", "concourse.dev/hangar-v1"} {
		if value, found := node.Labels[key]; found {
			copy := value
			restoreNodeLabels[key] = &copy
		} else {
			restoreNodeLabels[key] = nil
		}
		node.Labels[key] = "ready"
	}
	if _, err := client.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("label K3s node for generated strict Pod: %v", err)
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
	signer, err := hangar.NewGrantSigner(key, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	unique := fmt.Sprintf("hangar-live-%d", time.Now().UnixNano())
	hostRoot := "/tmp/" + unique
	cfg.Namespace = namespace
	cfg.ArtifactDaemonHostPath = hostRoot
	cfg.ArtifactDaemonPort = 7780
	cfg.ArtifactHelperImage = "busybox:latest"
	cfg.HangarEnabled = true
	cfg.HangarGrantSigner = signer

	handle := "strict-consumer"
	container := &Container{
		handle:   handle,
		podName:  unique + "-step",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: atcruntime.ContainerSpec{
			Dir: "/work", Type: db.ContainerTypeTask,
			ImageSpec: atcruntime.ImageSpec{ImageURL: "busybox:latest"},
			Inputs:    []atcruntime.Input{{HangarTree: &ref, DestinationPath: "/work/exact"}},
		},
		config: cfg, storageBackend: NewDaemonSetBackend(cfg, nil, nil), properties: map[string]string{},
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
		t.Fatalf("generate strict task Pod: %v", err)
	}
	assertLivePodMountsResolve(t, pod)
	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != "materialize-hangar-inputs" || pod.Spec.InitContainers[0].Image != "busybox:latest" {
		t.Fatalf("generated strict init = %+v", pod.Spec.InitContainers)
	}
	strictMount := liveMountAt(t, pod.Spec.Containers[0].VolumeMounts, "/work/exact")
	if !strictMount.ReadOnly {
		t.Fatal("generated main strict input mount is writable")
	}
	assertLiveHangarAffinity(t, pod)

	volumeName := strictMount.Name
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
				Name: "daemon-fixture", Image: "busybox:latest",
				Command:      []string{"sh", "-c", "printf '%s' \"$HANDLER\" >/tmp/handler; chmod 700 /tmp/handler; exec nc -ll -p 7780 -e /tmp/handler"},
				Env:          []corev1.EnvVar{{Name: "HANDLER", Value: "#!/bin/sh\n" + fixtureScript}},
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
			t.Fatalf("generated strict Pod failed: %s", strings.Join(diagnostics, "\n"))
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

func assertLiveHangarAffinity(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	want := map[string]bool{"concourse.dev/artifact-cache": false, "concourse.dev/hangar-v1": false}
	required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) == 0 {
		t.Fatal("generated strict Pod has no required node affinity")
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
			t.Fatalf("generated strict Pod missing %s In [ready]", key)
		}
	}
}
