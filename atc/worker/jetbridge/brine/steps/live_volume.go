package steps

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func newLiveVolumeSet(rec *brine.Recorder, capture SpanCapture) (VolumeSet, error) {
	set := newVolumeSet()
	ctx, cancel := context.WithTimeout(execLogger("live-volumes"), 90*time.Second)
	rec.RegisterDisposer(cancel)
	cluster, err := newLiveKubernetes(ctx, rec)
	if err != nil {
		return VolumeSet{}, err
	}
	set.Ctx, set.live = ctx, &cluster
	set.execTrace = &volumeExecObservation{bindings: map[string]volumeExecBinding{}, capture: capture}
	set.execTrace.wire, err = newVolumeWireRoute(rec, cluster.Config, cluster.Namespace)
	if err != nil {
		return VolumeSet{}, err
	}
	return set, nil
}

// The kubelet supplies the mount and its filesystem permissions. In particular,
// no host-side helper creates the StreamIn destination's subdirectories.
func addVolume(set VolumeSet, p brine.Params) (VolumeSet, error) {
	name, named := p.GetString(0)
	mount, mounted := p.GetString(1)
	binding, bound := p.GetString(2)
	if !bound || (binding != "direct" && binding != "deferred") {
		return VolumeSet{}, fmt.Errorf("expected direct or deferred volume binding")
	}
	if !named || !mounted || len(validation.IsDNS1123Label(name)) != 0 || !filepath.IsAbs(mount) || !strings.HasPrefix(filepath.Clean(mount), "/tmp/build/") {
		return VolumeSet{}, fmt.Errorf("expected a DNS volume name and a mount below /tmp/build")
	}
	if set.live == nil {
		return VolumeSet{}, fmt.Errorf("mounted volumes require real Kubernetes")
	}
	if _, exists := set.Volumes[name]; exists {
		return VolumeSet{}, fmt.Errorf("volume %q already exists", name)
	}
	nonRoot, noEscalation, readOnly := true, false, true
	user := int64(1000)
	// Idle fixture pods need no lengthy shutdown after completed I/O. This
	// affects only disposable test pods, never production lifecycle behavior.
	grace := int64(1)
	limit := resource.MustParse("16Mi")
	pod, err := set.live.Clientset.CoreV1().Pods(set.live.Namespace).Create(set.Ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "volume-" + name + "-"},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace,
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: &user, RunAsNonRoot: &nonRoot, FSGroup: &user,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "main", Image: "busybox:1.37.0", Command: []string{"sleep", "300"},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &noEscalation, ReadOnlyRootFilesystem: &readOnly,
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: mount}},
			}},
			Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return VolumeSet{}, err
	}
	pod, err = awaitLivePod(set.Ctx, *set.live, pod.Name)
	if err != nil {
		return VolumeSet{}, err
	}
	if pod.Spec.TerminationGracePeriodSeconds == nil || *pod.Spec.TerminationGracePeriodSeconds != grace {
		return VolumeSet{}, fmt.Errorf("live I/O pod lost its bounded shutdown grace")
	}
	volumes := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		volumes[v.Name] = true
	}
	matched := false
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if !volumes[m.Name] {
				return VolumeSet{}, fmt.Errorf("live mount %q has no volume", m.Name)
			}
			matched = matched || m.Name == "data" && m.MountPath == mount
		}
	}
	if !matched {
		return VolumeSet{}, fmt.Errorf("real pod lost the requested volume mount")
	}
	executor := jetbridge.NewSPDYExecutor(set.live.Clientset, set.live.Config)
	var hostname strings.Builder
	if err := executor.ExecInPod(set.Ctx, set.live.Namespace, pod.Name, "main", []string{"hostname"}, nil, &hostname, nil, false,
		jetbridge.ExecAttrs{Purpose: "volume-premise"}); err != nil {
		return VolumeSet{}, err
	}
	if strings.TrimSpace(hostname.String()) != pod.Name {
		return VolumeSet{}, fmt.Errorf("exec reached hostname %q, want %s", hostname.String(), pod.Name)
	}
	route := set.execTrace.wire
	route.allowPod(pod.Name)
	executor = jetbridge.NewSPDYExecutor(route.client, set.execTrace.config(route.config))
	set.execTrace.bindings[name] = volumeExecBinding{namespace: pod.Namespace, pod: pod.Name, container: "main", mount: mount}
	// Binding style is explicit scenario data; both constructors use the same
	// real mounts, executor and independent request/byte assertions.
	var volume *jetbridge.Volume
	if binding == "direct" {
		volume = jetbridge.NewVolume(nil, executor, pod.Name, set.live.Namespace, "main", mount)
	} else {
		volume = jetbridge.NewDeferredVolume(name+"-handle", "k8s-worker-1", executor, set.live.Namespace, "main", mount)
		volume.SetPodName(pod.Name)
	}
	set.Volumes[name] = volume
	fmt.Printf("live volume %s bound to %s/%s UID %s node %s mount %s\n", name, pod.Namespace, pod.Name, pod.UID, pod.Spec.NodeName, mount)
	return set, nil
}

// Observe the kubelet's running container identity; never set pod status.
func awaitLivePod(ctx context.Context, cluster liveKubernetes, name string) (*corev1.Pod, error) {
	for {
		pod, err := cluster.Clientset.CoreV1().Pods(cluster.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if pod.UID != "" && pod.Spec.NodeName != "" && pod.Status.Phase == corev1.PodRunning {
			for _, c := range pod.Status.ContainerStatuses {
				if c.Name == "main" && c.Ready && c.ContainerID != "" && c.State.Running != nil {
					return pod, nil
				}
			}
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return nil, fmt.Errorf("live I/O pod %s ended before exec: %s", name, pod.Status.Phase)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for real pod %s: %w", name, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}
