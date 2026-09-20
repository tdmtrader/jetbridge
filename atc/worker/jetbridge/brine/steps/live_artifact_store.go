package steps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A hostPath rooted in this scenario's own kubelet-managed emptyDir. Removing
// the anchor reclaims the storage; deleting an ordinary hostPath pod does not.
// The observer mounts only this anchor's pod directory, never /var/lib/kubelet
// or another pod's files. It verifies deletion through the parent mount, not
// through the data bind mount (which can outlive the deleted directory).
type liveArtifactStore struct {
	cluster          liveKubernetes
	executor         *jetbridge.SPDYExecutor
	anchor, observer *corev1.Pod
	root, ownerRoot  string
	observerReady    bool
	cleaned          bool
}

func newLiveArtifactStore(ctx context.Context, rec *brine.Recorder) (*liveArtifactStore, error) {
	if os.Getenv("BRINE_ALLOW_HOSTPATH_TESTS") != "1" {
		return nil, fmt.Errorf("live artifact storage requires explicit BRINE_ALLOW_HOSTPATH_TESTS=1 approval")
	}
	cluster, err := newLiveKubernetes(ctx, rec, 4)
	if err != nil {
		return nil, err
	}
	s := &liveArtifactStore{cluster: cluster, executor: jetbridge.NewSPDYExecutor(cluster.Clientset, cluster.Config)}
	TrackDisposer(rec, "the live artifact store", func() error {
		clean, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return s.close(clean)
	})
	// Only the newly created, UID-matched namespace receives this exception.
	ns, err := cluster.Clientset.CoreV1().Namespaces().Get(ctx, cluster.Namespace, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if string(ns.UID) != cluster.Marker || ns.Labels["app.kubernetes.io/managed-by"] != "brine-runtime-tests" {
		return nil, fmt.Errorf("refusing hostPath exception for an unowned namespace")
	}
	ns.Labels["pod-security.kubernetes.io/enforce"] = "privileged"
	ns.Annotations = map[string]string{"brine.dev/hostpath-purpose": "owned artifact handoff migration"}
	if _, err := cluster.Clientset.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
		return nil, err
	}

	nodeName := os.Getenv("BRINE_LIVE_ARTIFACT_NODE")
	if nodeName == "" {
		return nil, fmt.Errorf("BRINE_LIVE_ARTIFACT_NODE must name the approved node")
	}
	nodes, err := cluster.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	// Production task pods are scheduled normally. This single-node premise
	// prevents DirectoryOrCreate from making our owned root on another node.
	if len(nodes.Items) != 1 || nodes.Items[0].Name != nodeName ||
		nodes.Items[0].Spec.Unschedulable || nodes.Items[0].Labels["concourse.dev/artifact-cache"] != "ready" {
		return nil, fmt.Errorf("handoff requires the approved, schedulable single node with existing artifact-cache=ready")
	}
	anchor := s.pod("artifact-store-owner", nodeName, nil, nil)
	size := resource.MustParse("16Mi")
	anchor.Spec.Volumes = []corev1.Volume{{Name: "artifacts", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &size}}}}
	anchor.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "artifacts", MountPath: "/store"}}
	anchor.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "OWNER_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}}}
	anchor.Spec.Containers[0].Command = []string{"sh", "-ec", `printf '%s' "$OWNER_UID" > /store/.brine-owner; exec sleep 600`}
	s.anchor, err = cluster.Clientset.CoreV1().Pods(cluster.Namespace).Create(ctx, anchor, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	anchorReady, err := awaitLiveStoragePod(ctx, s, s.anchor)
	if err != nil {
		return nil, err
	}
	s.anchor = anchorReady
	kubeletRoot := os.Getenv("BRINE_KUBELET_ROOT")
	if kubeletRoot == "" {
		kubeletRoot = "/var/lib/kubelet"
	}
	if !filepath.IsAbs(kubeletRoot) || filepath.Clean(kubeletRoot) == "/" || strings.ContainsAny(string(s.anchor.UID), "/\\") {
		return nil, fmt.Errorf("invalid kubelet root or anchor UID")
	}
	s.ownerRoot = filepath.Join(kubeletRoot, "pods", string(s.anchor.UID))
	s.root = filepath.Join(s.ownerRoot, "volumes", "kubernetes.io~empty-dir", "artifacts")
	directory := corev1.HostPathDirectory // Never create an assumed kubelet path.
	observer := s.pod("artifact-store-observer", s.anchor.Spec.NodeName,
		[]corev1.Volume{
			{Name: "data", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: s.root, Type: &directory}}},
			{Name: "owner", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: s.ownerRoot, Type: &directory}}},
		},
		[]corev1.VolumeMount{{Name: "data", MountPath: "/store"}, {Name: "owner", MountPath: "/owner", ReadOnly: true}},
	)
	s.observer, err = cluster.Clientset.CoreV1().Pods(cluster.Namespace).Create(ctx, observer, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	observerReady, err := awaitLiveStoragePod(ctx, s, s.observer)
	if err != nil {
		return nil, err
	}
	s.observer = observerReady
	s.observerReady = true
	// These reads prove both the assumed kubelet path and the later cleanup
	// observation's parent mount before any production task can use this root.
	for _, path := range []string{"/store/.brine-owner", "/owner/volumes/kubernetes.io~empty-dir/artifacts/.brine-owner"} {
		got, err := s.exec(ctx, s.observer.Name, []string{"cat", path}, nil)
		if err != nil || got != string(s.anchor.UID) {
			return nil, fmt.Errorf("owned storage identity at %s: got %q, err=%v", path, got, err)
		}
	}
	fmt.Printf("verified owned live artifact storage %s on node %s; anchor UID %s observer UID %s\n", s.root, s.anchor.Spec.NodeName, s.anchor.UID, s.observer.UID)
	return s, nil
}

func (s *liveArtifactStore) pod(name, node string, volumes []corev1.Volume, mounts []corev1.VolumeMount) *corev1.Pod {
	no, yes := false, true
	root := int64(0)
	grace := int64(1)
	q := resource.MustParse
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"brine.dev/storage-owner": s.cluster.Marker}}, Spec: corev1.PodSpec{
		NodeName: node, RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &no, TerminationGracePeriodSeconds: &grace,
		SecurityContext: &corev1.PodSecurityContext{RunAsUser: &root, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Volumes:         volumes,
		Containers: []corev1.Container{{Name: "main", Image: "busybox:1.37.0", Command: []string{"sleep", "600"}, VolumeMounts: mounts,
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: q("10m"), corev1.ResourceMemory: q("16Mi"), corev1.ResourceEphemeralStorage: q("16Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: q("100m"), corev1.ResourceMemory: q("32Mi"), corev1.ResourceEphemeralStorage: q("32Mi")}},
		}},
	}}
}

// writeFile accesses only this owned store through its independent observer.
func (s *liveArtifactStore) writeFile(ctx context.Context, full, content string) error {
	rel, err := filepath.Rel(s.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("artifact path %q leaves owned storage %q", full, s.root)
	}
	path := filepath.Join("/store", rel)
	_, err = s.exec(ctx, s.observer.Name,
		[]string{"sh", "-ec", "mkdir -p \"$(dirname \"$1\")\"; cat > \"$1\"", "write-owned-artifact", path}, strings.NewReader(content))
	return err
}

func (s *liveArtifactStore) exec(ctx context.Context, pod string, command []string, stdin io.Reader) (string, error) {
	var stdout, stderr bytes.Buffer
	err := s.executor.ExecInPod(ctx, s.cluster.Namespace, pod, "main", command, stdin, &stdout, &stderr, false, jetbridge.ExecAttrs{Purpose: "owned-live-artifact-storage"})
	if err != nil {
		return stdout.String(), fmt.Errorf("storage pod %s: %w; stderr=%s", pod, err, stderr.String())
	}
	return stdout.String(), nil
}

func deleteLiveStoragePod(ctx context.Context, s *liveArtifactStore, pod *corev1.Pod) error {
	if pod == nil {
		return nil
	}
	zero := int64(0)
	pods := s.cluster.Clientset.CoreV1().Pods(s.cluster.Namespace)
	if err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	for {
		current, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			// Deletion visibility can precede quota-controller accounting.
			// Wait for the owned quota to stop counting an absent pod before
			// its replacement is admitted; never raise the resource limits.
			return waitForLiveStorageQuota(ctx, s)
		}
		if err != nil {
			return err
		}
		if current.UID != pod.UID {
			return fmt.Errorf("refusing to delete replacement pod %s", pod.Name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func waitForLiveStorageQuota(ctx context.Context, s *liveArtifactStore) error {
	waited := false
	for {
		pods, err := s.cluster.Clientset.CoreV1().Pods(s.cluster.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		quota, err := s.cluster.Clientset.CoreV1().ResourceQuotas(s.cluster.Namespace).Get(ctx, "bounded-test", metav1.GetOptions{})
		if err != nil {
			return err
		}
		used, known := quota.Status.Used[corev1.ResourcePods]
		if known && used.Value() <= int64(len(pods.Items)) {
			if waited {
				fmt.Printf("owned storage quota caught up with pod deletion: namespace %s actual pods %d accounted pods %d\n", s.cluster.Namespace, len(pods.Items), used.Value())
			}
			return nil
		}
		if !waited {
			fmt.Printf("waiting for owned storage quota after confirmed deletion: namespace %s actual pods %d accounted pods %d known=%t\n", s.cluster.Namespace, len(pods.Items), used.Value(), known)
		}
		waited = true
		select {
		case <-ctx.Done():
			return fmt.Errorf("owned pod quota did not observe deletion: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (s *liveArtifactStore) close(ctx context.Context) error {
	if s.cleaned {
		return nil
	}
	// Callers register their task-pod disposers after this store, so those pods
	// are gone before the anchor is removed. The observer must remain alive.
	if !s.observerReady {
		if err := deleteLiveStoragePod(ctx, s, s.observer); err != nil {
			return err
		}
		return deleteLiveStoragePod(ctx, s, s.anchor)
	}
	if err := deleteLiveStoragePod(ctx, s, s.anchor); err != nil {
		return err
	}
	for {
		_, err := s.exec(ctx, s.observer.Name, []string{"test", "!", "-e", "/owner/volumes/kubernetes.io~empty-dir/artifacts"}, nil)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return fmt.Errorf("kubelet did not reclaim owned storage %s: %w", s.root, ctx.Err())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	fmt.Printf("verified kubelet removed owned live artifact storage %s for anchor UID %s\n", s.root, s.anchor.UID)
	if err := deleteLiveStoragePod(ctx, s, s.observer); err != nil {
		return err
	}
	s.cleaned = true
	return nil
}

// Preserve the created UID for cleanup even when waiting returns no pod. Read
// failure evidence with a fresh, bounded context before disposers remove it.
func awaitLiveStoragePod(ctx context.Context, s *liveArtifactStore, created *corev1.Pod) (*corev1.Pod, error) {
	ready, err := awaitLivePod(ctx, s.cluster, created.Name)
	if err == nil {
		if ready.UID != created.UID {
			return nil, fmt.Errorf("storage pod %s changed UID %s -> %s", created.Name, created.UID, ready.UID)
		}
		return ready, nil
	}
	inspect, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, readErr := s.cluster.Clientset.CoreV1().Pods(s.cluster.Namespace).Get(inspect, created.Name, metav1.GetOptions{})
	events, eventErr := s.cluster.Clientset.CoreV1().Events(s.cluster.Namespace).List(inspect, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + string(created.UID)})
	var status any = "unavailable"
	if snapshot != nil {
		status = snapshot.Status
	}
	var notes []string
	if events != nil {
		for _, event := range events.Items {
			notes = append(notes, event.Reason+": "+event.Message)
		}
	}
	return nil, fmt.Errorf("%w; created UID %s; status=%+v; events=%v; diagnostic errors=%v/%v", err, created.UID, status, notes, readErr, eventErr)
}
