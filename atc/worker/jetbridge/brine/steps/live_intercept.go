package steps

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These fixtures test lookup/interception of an existing pod, not creation of
// a task pod by the worker. PID 1 can be asked to finish; only the kubelet
// reports Running/Succeeded/Failed. All files belong to its emptyDir.
func createInterceptPod(w WorkerReady, name string, labels map[string]string) error {
	return createInterceptPodWithImage(w, name, labels, "busybox:1.37.0")
}

func createInterceptPodWithImage(w WorkerReady, name string, labels map[string]string, image string) error {
	nonRoot, noEscalation, readOnly := true, false, true
	user, grace := int64(1000), int64(1)
	limit := resource.MustParse("16Mi")
	_, err := w.Clientset.CoreV1().Pods(w.Namespace).Create(w.Ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace,
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: &user, RunAsNonRoot: &nonRoot, FSGroup: &user,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "main", Image: image,
				Command: []string{"sh", "-c", `while [ ! -f /tmp/intercept-exit ]; do sleep 1; done; exit "$(cat /tmp/intercept-exit)"`},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &noEscalation, ReadOnlyRootFilesystem: &readOnly,
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts: []corev1.VolumeMount{{Name: "scratch", MountPath: "/tmp"}}}},
			Volumes: []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}}},
		}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	pod, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, name)
	if err != nil {
		return err
	}
	volumes := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		volumes[v.Name] = true
	}
	scratch := false
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if !volumes[m.Name] {
				return fmt.Errorf("intercept pod mount %q has no volume", m.Name)
			}
			scratch = scratch || c.Name == "main" && m.Name == "scratch" && m.MountPath == "/tmp"
		}
	}
	if !scratch {
		return fmt.Errorf("intercept pod has no owned scratch mount")
	}
	var hostname strings.Builder
	if err := w.Executor.ExecInPod(w.Ctx, w.Namespace, name, "main", []string{"hostname"}, nil, &hostname, nil, false,
		jetbridge.ExecAttrs{Purpose: "intercept-premise"}); err != nil {
		return err
	}
	if strings.TrimSpace(hostname.String()) != name {
		return fmt.Errorf("intercept premise reached %q, want %q", hostname.String(), name)
	}
	fmt.Printf("live intercept pod %s/%s UID %s node %s hostname verified\n", w.Namespace, name, pod.UID, pod.Spec.NodeName)
	return nil
}

func finishInterceptPod(w WorkerReady, status string) error {
	code, err := strconv.Atoi(status)
	if err != nil || code < 0 || code > 255 {
		return fmt.Errorf("invalid pod exit status %q", status)
	}
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	original, err := pods.Get(w.Ctx, w.StepPodName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	// Let the production supervisor and execProcess write completion before
	// PID 1 exits. The later interception must preserve that real record.
	out, err := runTask(TaskCluster{WorkerReady: w, metadata: w.StepMetadata},
		w.StepHandle, "exit "+strconv.Itoa(code), nil)
	if err != nil {
		return fmt.Errorf("run completing task: %w", err)
	}
	if out.Err != nil || out.ExitStatus != code {
		return fmt.Errorf("completing task returned status %d, want %d: %v", out.ExitStatus, code, out.Err)
	}
	if err := requirePodSupervisorState(w, w.StepPodName); err != nil {
		return err
	}
	if err := w.Executor.ExecInPod(w.Ctx, w.Namespace, w.StepPodName, "main",
		[]string{"sh", "-c", `printf '%s\n' "$1" > /tmp/intercept-exit`, "finish", status},
		nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "intercept-finish"}); err != nil {
		return err
	}
	for {
		pod, err := pods.Get(w.Ctx, w.StepPodName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.UID != original.UID {
			return fmt.Errorf("intercept pod was replaced while finishing")
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			var terminated *corev1.ContainerStateTerminated
			for _, c := range pod.Status.ContainerStatuses {
				if c.Name == "main" {
					terminated = c.State.Terminated
				}
			}
			if terminated == nil || int(terminated.ExitCode) != code {
				return fmt.Errorf("expected actual main exit %d, got %+v", code, terminated)
			}
			fmt.Printf("live intercept pod %s/%s UID %s actually finished exit %d phase %s\n", w.Namespace, pod.Name, pod.UID, code, pod.Status.Phase)
			return nil
		}
		select {
		case <-w.Ctx.Done():
			return w.Ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
