package jetbridge

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// A Run-owned exact execution the privilege gate no longer admits is not
// handed back to its step, and is neither deleted nor replaced: it may have
// started, and only the exact path may stop it. A resumed exact step Job's
// envelope is not registered either, so a pod that has not fetched it never
// starts its command. A Run's build holds no grant; here the build is simply
// not found, which the gate refuses the same way.
func TestAnExactExecutionTheGateNoLongerAdmitsIsLeftWhereItIs(t *testing.T) {
	privileged := true
	exactSpec := func() runtime.ContainerSpec {
		return runtime.ContainerSpec{
			Type:      db.ContainerTypeTask,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			ExecutionControl: &runtime.ExecutionControl{
				Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted,
				Endpoint: "https://daemon.invalid", Capability: "base-capability",
			},
		}
	}
	metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, BuildID: 4242, PipelineName: "p", JobName: "j", BuildName: "1", StepName: "s"}
	identities := stepPodIdentities{builds: absentBuilds{}, gated: true}
	privilegedMain := corev1.PodSpec{Containers: []corev1.Container{{Name: mainContainerName, Image: "busybox",
		SecurityContext: &corev1.SecurityContext{Privileged: &privileged}}}, AutomountServiceAccountToken: new(bool)}

	expectRefusal := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("the gate's refusal handed the exact execution back to its step")
		}
		for _, want := range []string{"exact execution", "neither deleted nor replaced", "Build 4242 was not found", `container "main" is privileged`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal %q does not say %q", err, want)
			}
		}
	}

	t.Run("a live pause pod on the exec runtime", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		deletes := countDeletes(clientset)
		config := NewConfig("test-ns", "")
		config.StepPrivilege = StepPrivilegeGranted
		executor := &countingExecutor{}
		container := newContainer("exact-handle", metadata, exactSpec(), nil, clientset, config, "worker",
			executor, nil, nil, identities, true, false)
		container.outputControls = staticOutputControls{}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: container.podName, Namespace: "test-ns", UID: "exact-pod"},
			Spec: privilegedMain, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if _, err := clientset.CoreV1().Pods("test-ns").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}

		_, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{})
		expectRefusal(t, err)
		if _, err := clientset.CoreV1().Pods("test-ns").Get(context.Background(), container.podName, metav1.GetOptions{}); err != nil {
			t.Errorf("the exact execution's pod is gone: %v", err)
		}
		if n := deletes.Load(); n != 0 {
			t.Errorf("%d deletes were issued", n)
		}
		if n := executor.calls.Load(); n != 0 {
			t.Errorf("%d execs reached the refused pod", n)
		}
	})

	t.Run("a resumed step Job on the job runtime", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		deletes := countDeletes(clientset)
		config := jobRuntimeConfig()
		config.StepPrivilege = StepPrivilegeGranted
		container := newContainer("exact-job-handle", metadata, exactSpec(), nil, clientset, config, "worker",
			nil, nil, nil, identities, true, false)
		container.envelopes = NewStepEnvelopes()
		container.routeExactTasksToJobRuntime()
		container.outputControls = staticOutputControls{}
		suspended := true
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: container.podName, Namespace: "test-ns", UID: "exact-job"},
			Spec: batchv1.JobSpec{Suspend: &suspended, Template: corev1.PodTemplateSpec{Spec: privilegedMain}}}
		if _, err := clientset.BatchV1().Jobs("test-ns").Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}

		_, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{Stdin: strings.NewReader("")})
		expectRefusal(t, err)
		left, err := clientset.BatchV1().Jobs("test-ns").Get(context.Background(), container.podName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("the exact execution's Job is gone: %v", err)
		}
		if left.Spec.Suspend == nil || !*left.Spec.Suspend {
			t.Error("the refused Job was unsuspended")
		}
		if n := deletes.Load(); n != 0 {
			t.Errorf("%d deletes were issued", n)
		}
		if _, err := container.envelopes.Take(container.podName, "any-pod"); err == nil {
			t.Error("the refused Job's envelope was registered for its pod to fetch")
		}
	})

	// A Job already unsuspended whose pod's launcher is still asking for its
	// envelope: the envelope route answers it 503 until the launcher's own
	// envelope timeout ends it without running the command, leaving the
	// never-received record (launcher.Run).
	t.Run("a resumed step Job whose pod awaits its envelope", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		deletes := countDeletes(clientset)
		config := jobRuntimeConfig()
		config.StepPrivilege = StepPrivilegeGranted
		container := newContainer("exact-waiting-handle", metadata, exactSpec(), nil, clientset, config, "worker",
			nil, nil, nil, identities, true, false)
		container.envelopes = NewStepEnvelopes()
		container.routeExactTasksToJobRuntime()
		container.outputControls = staticOutputControls{}
		running := false
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: container.podName, Namespace: "test-ns", UID: "exact-waiting-job"},
			Spec: batchv1.JobSpec{Suspend: &running, Template: corev1.PodTemplateSpec{Spec: privilegedMain}}}
		if _, err := clientset.BatchV1().Jobs("test-ns").Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: container.podName + "-abcde", Namespace: "test-ns", UID: "waiting-pod",
			Labels:          map[string]string{jobUIDLabelKey: "exact-waiting-job", runtimeLabelKey: StepRuntimeJob},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: container.podName, UID: "exact-waiting-job"}}},
			Spec: privilegedMain, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if _, err := clientset.CoreV1().Pods("test-ns").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}

		_, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{Stdin: strings.NewReader("")})
		expectRefusal(t, err)
		if _, err := container.envelopes.Serve(context.Background(), container.podName, pod); err != errEnvelopeUnknown {
			t.Errorf("the waiting pod's envelope fetch = %v, want %v (answered 503, never the envelope)", err, errEnvelopeUnknown)
		}
		if n := deletes.Load(); n != 0 {
			t.Errorf("%d deletes were issued", n)
		}
		if _, err := clientset.CoreV1().Pods("test-ns").Get(context.Background(), pod.Name, metav1.GetOptions{}); err != nil {
			t.Errorf("the waiting pod is gone: %v", err)
		}
	})
}

// absentBuilds finds no build.
type absentBuilds struct{}

func (absentBuilds) Build(int) (db.Build, bool, error) { return nil, false, nil }

type countingExecutor struct{ calls atomic.Int32 }

func (e *countingExecutor) ExecInPod(context.Context, string, string, string, []string, io.Reader, io.Writer, io.Writer, bool, ExecAttrs) error {
	e.calls.Add(1)
	return nil
}

func countDeletes(clientset *fake.Clientset) *atomic.Int32 {
	var n atomic.Int32
	clientset.PrependReactor("delete", "*", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		n.Add(1)
		return false, nil, nil
	})
	return &n
}
