package steps

import (
	"context"
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Only the daemon pod receives this service account. The role is confined to
// its newly owned namespace; it cannot label nodes or change discovery.
func configureLivePeerDiscovery(ctx context.Context, s *liveArtifactStore, pod *corev1.Pod) error {
	const name = "artifact-peer-reader"
	no, yes := false, true
	_, err := s.cluster.Clientset.CoreV1().ServiceAccounts(s.cluster.Namespace).Create(ctx,
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name}, AutomountServiceAccountToken: &no}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	_, err = s.cluster.Clientset.RbacV1().Roles(s.cluster.Namespace).Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"list"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	_, err = s.cluster.Clientset.RbacV1().RoleBindings(s.cluster.Namespace).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: s.cluster.Namespace}},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	pod.Spec.ServiceAccountName, pod.Spec.AutomountServiceAccountToken = name, &yes
	return nil
}

// Provision the peer only after the producer's mount writes are complete.
// Reclaim that producer pod first to stay within the existing four-pod quota.
// Its hostPath data remains in the owned anchor until scenario disposal.
func (in *ArtifactCluster) ensureLiveMirrorPeer() error {
	if in.mirrorRecorder == nil || in.Peer != nil {
		return nil
	}
	d := in.live
	if d == nil {
		return fmt.Errorf("live mirroring requires the real producer daemon")
	}
	pods, err := in.Clientset.CoreV1().Pods(in.Namespace).List(in.Ctx,
		metav1.ListOptions{LabelSelector: "concourse.ci/handle"})
	if err != nil {
		return err
	}
	if len(pods.Items) > 1 {
		return fmt.Errorf("expected at most one owned producer pod, found %d", len(pods.Items))
	}
	for i := range pods.Items {
		if err := deleteLiveStoragePod(in.Ctx, d.store, &pods.Items[i]); err != nil {
			return err
		}
	}
	endpoint, err := startLiveArtifactPeer(in.Ctx, in.mirrorRecorder, d)
	if err != nil {
		return err
	}
	pod, err := in.Clientset.CoreV1().Pods(in.Namespace).Get(in.Ctx, livePeerService, metav1.GetOptions{})
	if err != nil {
		return err
	}
	in.Peer = &realNode{Root: "/opt/brine/store", URL: endpoint, host: pod.Status.PodIP,
		port: int(d.port), store: d.store, ctx: in.Ctx, pod: pod}
	return nil
}

func liveMirrorRecordingDefinition() brine.StepDefinition {
	return brine.DefineMapUsing[brine.Empty, ArtifactCluster](
		"a jetbridge worker whose outputs are mirrored to an independent peer",
		[]string{"jetbridge-db"},
		func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ArtifactCluster, error) {
			database, ok := res.Get("jetbridge-db").(JetbridgeDB)
			if !ok {
				return ArtifactCluster{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
			}
			return newLiveArtifactRecording(database, rec, true)
		},
	)
}
