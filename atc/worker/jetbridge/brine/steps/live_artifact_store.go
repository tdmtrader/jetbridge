package steps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
	// root is the path every production pod and daemon uses. dataRoot is the
	// anchor's emptyDir under the kubelet; root is dataRoot itself, or -- in
	// a multi-node fixture -- the shared link that resolves to it (link).
	root, dataRoot, ownerRoot string
	// suffix tells this node's fixture pods apart in a shared namespace.
	suffix        string
	linker        *corev1.Pod
	observerReady bool
	cleaned       bool
}

func newLiveArtifactStore(ctx context.Context, rec *brine.Recorder) (*liveArtifactStore, error) {
	cluster, err := newLiveArtifactCluster(ctx, rec, 4, 1)
	if err != nil {
		return nil, err
	}
	node, err := approvedArtifactNode(ctx, cluster.Clientset)
	if err != nil {
		return nil, err
	}
	return newLiveArtifactStoreOn(ctx, rec, cluster, node.Name, "")
}

// newLiveArtifactCluster is an owned namespace granted the hostPath exception
// this fixture needs, with room for podLimit pods.
func newLiveArtifactCluster(ctx context.Context, rec *brine.Recorder, podLimit, nodes int64) (liveKubernetes, error) {
	if os.Getenv("BRINE_ALLOW_HOSTPATH_TESTS") != "1" {
		return liveKubernetes{}, fmt.Errorf("live artifact storage requires explicit BRINE_ALLOW_HOSTPATH_TESTS=1 approval")
	}
	cluster, err := newLiveKubernetesBounded(ctx, rec, podLimit, nodes)
	if err != nil {
		return liveKubernetes{}, err
	}
	// Only the newly created, UID-matched namespace receives this exception.
	ns, err := cluster.Clientset.CoreV1().Namespaces().Get(ctx, cluster.Namespace, metav1.GetOptions{})
	if err != nil {
		return liveKubernetes{}, err
	}
	if string(ns.UID) != cluster.Marker || ns.Labels["app.kubernetes.io/managed-by"] != "brine-runtime-tests" {
		return liveKubernetes{}, fmt.Errorf("refusing hostPath exception for an unowned namespace")
	}
	ns.Labels["pod-security.kubernetes.io/enforce"] = "privileged"
	ns.Annotations = map[string]string{"brine.dev/hostpath-purpose": "owned artifact handoff migration"}
	if _, err := cluster.Clientset.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
		return liveKubernetes{}, err
	}
	return cluster, nil
}

// newLiveArtifactStoreOn builds the owned store on one node of cluster:
// an anchor whose emptyDir is the store, and an observer that reads it
// through the kubelet path. suffix keeps several nodes' pods apart.
func newLiveArtifactStoreOn(ctx context.Context, rec *brine.Recorder, cluster liveKubernetes, nodeName, suffix string) (*liveArtifactStore, error) {
	s := &liveArtifactStore{cluster: cluster, executor: jetbridge.NewSPDYExecutor(cluster.Clientset, cluster.Config), suffix: suffix}
	TrackDisposer(rec, "the live artifact store"+suffix, func() error {
		clean, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return s.close(clean)
	})
	var err error
	anchor := s.pod("artifact-store-owner"+suffix, nodeName, nil, nil)
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
	s.dataRoot = filepath.Join(s.ownerRoot, "volumes", "kubernetes.io~empty-dir", "artifacts")
	s.root = s.dataRoot
	directory := corev1.HostPathDirectory // Never create an assumed kubelet path.
	observer := s.pod("artifact-store-observer"+suffix, s.anchor.Spec.NodeName,
		[]corev1.Volume{
			{Name: "data", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: s.dataRoot, Type: &directory}}},
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

// link publishes this node's store at linkRoot -- the same path on every node
// of a multi-node fixture, because the runtime takes one artifact host path
// for all of them -- as a symlink into the anchor's emptyDir. The kubelet still
// reclaims the data with the anchor; close removes the link, and a later run's
// link sweeps one a killed run left behind. Afterwards root is linkRoot.
func (s *liveArtifactStore) link(ctx context.Context, linkRoot string) error {
	dir, name := filepath.Dir(linkRoot), filepath.Base(linkRoot)
	if !filepath.IsAbs(linkRoot) || dir == "/" || name != linkName(s.cluster) {
		return fmt.Errorf("link root %q must be an absolute path named after the owned namespace", linkRoot)
	}
	create := corev1.HostPathDirectoryOrCreate
	linker := s.pod("artifact-store-linker"+s.suffix, s.anchor.Spec.NodeName,
		[]corev1.Volume{{Name: "links", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: dir, Type: &create}}}},
		[]corev1.VolumeMount{{Name: "links", MountPath: "/links"}})
	created, err := s.cluster.Clientset.CoreV1().Pods(s.cluster.Namespace).Create(ctx, linker, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	s.linker = created
	if s.linker, err = awaitLiveStoragePod(ctx, s, created); err != nil {
		s.linker = created
		return err
	}
	if err := s.sweepStaleLinks(ctx); err != nil {
		return err
	}
	if _, err := s.exec(ctx, s.linker.Name, []string{"ln", "-sfn", s.dataRoot, "/links/" + name}, nil); err != nil {
		return err
	}
	s.root = linkRoot
	fmt.Printf("linked %s -> %s on node %s\n", linkRoot, s.dataRoot, s.anchor.Spec.NodeName)
	return nil
}

// linkName names a run's link after its owned namespace, by name and UID.
// The sweep reads it back with a namespace get -- the live tier's identity
// may get namespaces but not list them -- and a reused name with another UID
// is still stale.
func linkName(cluster liveKubernetes) string {
	return cluster.Namespace + "." + cluster.Marker
}

// sweepStaleLinks removes links whose namespace is gone or is another
// namespace by now: a run killed before its close. Their targets were already
// reclaimed with their anchors. Anything that is not a symlink, or not named
// the way linkName names one, is left alone.
func (s *liveArtifactStore) sweepStaleLinks(ctx context.Context) error {
	listing, err := s.exec(ctx, s.linker.Name, []string{"sh", "-c", `for l in /links/*; do [ -L "$l" ] && basename "$l"; done; true`}, nil)
	if err != nil {
		return err
	}
	for _, entry := range strings.Fields(listing) {
		name, uid, ok := strings.Cut(entry, ".")
		if !ok || !strings.HasPrefix(name, "brine-runtime-") || uid == "" {
			continue
		}
		ns, err := s.cluster.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil && string(ns.UID) == uid:
			continue
		case err != nil && !apierrors.IsNotFound(err):
			return err
		}
		if _, err := s.exec(ctx, s.linker.Name, []string{"rm", "-f", "/links/" + entry}, nil); err != nil {
			return err
		}
		fmt.Printf("swept stale artifact link %s on node %s\n", entry, s.anchor.Spec.NodeName)
	}
	return nil
}

// liveArtifactApproval is the node label that approves a node to carry this
// fixture's hostPath root and host port. Approval travels with the node, so
// the fixture runs on any cluster where someone has labelled one.
const liveArtifactApproval = "brine.dev/live-artifacts"

// approvedArtifactNode picks the node a single-node fixture lives on: the
// first of approvedArtifactNodes.
func approvedArtifactNode(ctx context.Context, cs kubernetes.Interface) (*corev1.Node, error) {
	nodes, err := approvedArtifactNodes(ctx, cs)
	if err != nil {
		return nil, err
	}
	return nodes[0], nil
}

// approvedArtifactNodes are the nodes the fixture may live on now: the one
// BRINE_LIVE_ARTIFACT_NODE names, when set, or else every node labelled
// liveArtifactApproval=approved that can take it -- Ready, schedulable, with a
// ready production artifact cache -- sorted by name. A labelled node that is
// asleep or cordoned is passed over, not waited for; with none left the error
// names each one and why.
func approvedArtifactNodes(ctx context.Context, cs kubernetes.Interface) ([]*corev1.Node, error) {
	if name := os.Getenv("BRINE_LIVE_ARTIFACT_NODE"); name != "" {
		node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("BRINE_LIVE_ARTIFACT_NODE %q: %w", name, err)
		}
		if why := artifactNodeUnfit(node); why != "" {
			return nil, fmt.Errorf("BRINE_LIVE_ARTIFACT_NODE %q %s", name, why)
		}
		return []*corev1.Node{node}, nil
	}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: liveArtifactApproval + "=approved"})
	if err != nil {
		return nil, err
	}
	if len(nodes.Items) == 0 {
		return nil, fmt.Errorf("no node is approved for the live artifact fixture: label one %s=approved, or name it in BRINE_LIVE_ARTIFACT_NODE", liveArtifactApproval)
	}
	sort.Slice(nodes.Items, func(i, j int) bool { return nodes.Items[i].Name < nodes.Items[j].Name })
	var fit []*corev1.Node
	var unfit []string
	for i := range nodes.Items {
		if why := artifactNodeUnfit(&nodes.Items[i]); why != "" {
			unfit = append(unfit, nodes.Items[i].Name+" "+why)
			continue
		}
		fit = append(fit, &nodes.Items[i])
	}
	if len(fit) == 0 {
		return nil, fmt.Errorf("no approved node can take the live artifact fixture now: %s", strings.Join(unfit, "; "))
	}
	if len(unfit) > 0 {
		fmt.Printf("approved artifact nodes passed over: %s\n", strings.Join(unfit, "; "))
	}
	return fit, nil
}

// artifactNodeUnfit says why a node cannot carry the fixture now, or "".
func artifactNodeUnfit(node *corev1.Node) string {
	if node.Spec.Unschedulable {
		return "is cordoned"
	}
	if node.Labels["concourse.dev/artifact-cache"] != "ready" {
		return "has no ready artifact cache"
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				return ""
			}
			break
		}
	}
	return "is not Ready"
}

// runtimeConfig is the jetbridge config every production pod over this store
// is built from. The store's root is a directory on the anchor's node alone,
// so the pods are required there; on any other node their hostPath would
// resolve to a root this fixture does not own.
func (s *liveArtifactStore) runtimeConfig() jetbridge.Config {
	cfg := jetbridge.NewConfig(s.cluster.Namespace, "")
	s.pin(&cfg)
	return cfg
}

// pin requires a config's pods on the store's node; see runtimeConfig.
func (s *liveArtifactStore) pin(cfg *jetbridge.Config) {
	cfg.RequiredStepNode = &jetbridge.StepNodeLabel{Key: corev1.LabelHostname, Value: s.anchor.Spec.NodeName}
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
	if s.linker != nil {
		if s.root != s.dataRoot {
			if _, err := s.exec(ctx, s.linker.Name, []string{"rm", "-f", "/links/" + filepath.Base(s.root)}, nil); err != nil {
				return err
			}
		}
		if err := deleteLiveStoragePod(ctx, s, s.linker); err != nil {
			return err
		}
		s.linker = nil
	}
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
