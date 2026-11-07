package jetbridge

import (
	"bytes"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func jobRuntimeConfig() Config {
	config := NewConfig("test-ns", "")
	config.StepRuntime = StepRuntimeJob
	config.LauncherImage = "registry.example/jetbridge:1"
	config.StepEnvelopeURL = "http://web.test-ns.svc:8080/runtime/v1/step-envelope"
	config.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	return config
}

func jobRuntimeContainer(t *testing.T, clientset *fake.Clientset, kind db.ContainerType, spec runtime.ContainerSpec) *Container {
	t.Helper()
	spec.Type = kind
	if spec.ImageSpec.ImageURL == "" {
		spec.ImageSpec.ImageURL = "docker:///busybox"
	}
	metadata := db.ContainerMetadata{Type: kind, PipelineName: "p", JobName: "j", BuildName: "1", StepName: "s"}
	container := newContainer("step-handle", metadata, spec, nil, clientset, jobRuntimeConfig(), "worker",
		nil, nil, nil, stepPodIdentities{}, false, false)
	container.envelopes = NewStepEnvelopes()
	return container
}

func TestBuildStepJobShape(t *testing.T) {
	container := jobRuntimeContainer(t, fake.NewSimpleClientset(), db.ContainerTypeTask, runtime.ContainerSpec{
		Dir:      "/tmp/build/task",
		Sidecars: []atc.SidecarConfig{{Name: "db", Image: "postgres:16"}},
	})
	job, err := container.buildStepJob(runtime.ProcessSpec{Path: "sh", Args: []string{"-c", "echo hi"}, TTY: &runtime.TTYSpec{}}, false)
	if err != nil {
		t.Fatalf("buildStepJob: %v", err)
	}
	if job.Spec.Suspend == nil || !*job.Spec.Suspend {
		t.Error("the Job is not created suspended")
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Error("the Job controller may retry: backoffLimit is not 0")
	}
	if job.Name != container.podName {
		t.Errorf("Job name %q is not the container's pod name %q", job.Name, container.podName)
	}
	pod := job.Spec.Template.Spec
	if job.Spec.Template.Labels[runtimeLabelKey] != StepRuntimeJob {
		t.Errorf("pod template lacks the %s=%s label", runtimeLabelKey, StepRuntimeJob)
	}
	if len(pod.InitContainers) == 0 || pod.InitContainers[0].Name != launcherInitName {
		t.Fatalf("the launcher init is not first: %+v", pod.InitContainers)
	}
	if pod.InitContainers[0].Image != "registry.example/jetbridge:1" {
		t.Errorf("launcher init image = %q", pod.InitContainers[0].Image)
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("expected the sidecar to leave the containers list, got %d containers", len(pod.Containers))
	}
	main := pod.Containers[0]
	if strings.Join(main.Command, " ") != "/jb/jb-launcher" {
		t.Errorf("main command = %v", main.Command)
	}
	if got := strings.Join(main.Args, " "); got != "-- sh -c echo hi" {
		t.Errorf("main args = %q", got)
	}
	if !main.TTY {
		t.Error("a task with a TTY spec runs without one")
	}
	for _, volume := range pod.Volumes {
		if volume.Name == stepTokenVolumeName {
			t.Error("a task without stdin mounts a step token")
		}
		if volume.Name == answerVolumeName {
			t.Error("a task mounts an answer directory")
		}
	}
	sidecar := pod.InitContainers[len(pod.InitContainers)-1]
	if sidecar.Name != "db" || sidecar.RestartPolicy == nil || *sidecar.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Errorf("the sidecar is not a native sidecar: %+v", sidecar)
	}
	if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds != stepJobTerminationGrace {
		t.Error("the termination grace is not the launcher's")
	}
}

// Kubernetes expands $(VAR) in a container's args and env values and
// collapses $$ to $. The exec runtime never met that: its command reached the
// pod only through exec. A task script such as `POD="builder-$$"` must reach
// the launcher as written, so every $ the step carries is escaped.
func TestBuildStepJobKeepsDollarSignsLiteral(t *testing.T) {
	container := jobRuntimeContainer(t, fake.NewSimpleClientset(), db.ContainerTypeTask, runtime.ContainerSpec{
		Dir: "/tmp/build/task",
		Env: []string{"FROM_CONTAINER=a$$b$(HOME)"},
	})
	job, err := container.buildStepJob(runtime.ProcessSpec{
		Path: "sh",
		Args: []string{"-exc", `POD="builder-$$"; echo "$(hostname)" $(HOME) $$$`},
		Env:  []string{"FROM_PROCESS=$(PATH)$"},
	}, false)
	if err != nil {
		t.Fatalf("buildStepJob: %v", err)
	}
	main := job.Spec.Template.Spec.Containers[0]
	wantArgs := []string{"--", "sh", "-exc", `POD="builder-$$$$"; echo "$$(hostname)" $$(HOME) $$$$$$`}
	if strings.Join(main.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf("main args = %q, want %q", main.Args, wantArgs)
	}
	want := map[string]string{"FROM_CONTAINER": "a$$$$b$$(HOME)", "FROM_PROCESS": "$$(PATH)$$"}
	for _, e := range main.Env {
		if w, ok := want[e.Name]; ok {
			if e.Value != w {
				t.Errorf("env %s = %q, want %q", e.Name, e.Value, w)
			}
			delete(want, e.Name)
		}
	}
	if len(want) > 0 {
		t.Errorf("env missing: %v", want)
	}
}

func TestBuildStepJobForAResourceCommandCarriesTokenAndAnswer(t *testing.T) {
	container := jobRuntimeContainer(t, fake.NewSimpleClientset(), db.ContainerTypeCheck, runtime.ContainerSpec{})
	job, err := container.buildStepJob(runtime.ProcessSpec{Path: "/opt/resource/check"}, true)
	if err != nil {
		t.Fatalf("buildStepJob: %v", err)
	}
	pod := job.Spec.Template.Spec
	main := pod.Containers[0]
	args := strings.Join(main.Args, " ")
	for _, want := range []string{"--envelope-url=" + container.config.StepEnvelopeURL, "--token-file=/var/run/jetbridge/token", "--stdout-file=/jb/answer/stdout", "-- /opt/resource/check"} {
		if !strings.Contains(args, want) {
			t.Errorf("main args %q lack %q", args, want)
		}
	}
	var token, answer *corev1.Volume
	for i := range pod.Volumes {
		switch pod.Volumes[i].Name {
		case stepTokenVolumeName:
			token = &pod.Volumes[i]
		case answerVolumeName:
			answer = &pod.Volumes[i]
		}
	}
	if token == nil || token.Projected == nil || len(token.Projected.Sources) != 1 || token.Projected.Sources[0].ServiceAccountToken == nil {
		t.Fatal("no projected ServiceAccount token volume")
	}
	projection := token.Projected.Sources[0].ServiceAccountToken
	if projection.Audience != StepTokenAudience || projection.ExpirationSeconds == nil || *projection.ExpirationSeconds != stepTokenExpirySecond {
		t.Errorf("token projection = %+v", projection)
	}
	if answer == nil || answer.HostPath == nil || answer.HostPath.Path != "/var/concourse/artifacts/steps/step-handle/answer" {
		t.Fatalf("answer volume = %+v", answer)
	}
	for _, init := range pod.InitContainers {
		for _, mount := range init.VolumeMounts {
			if mount.Name == stepTokenVolumeName {
				t.Errorf("init container %q mounts the step token", init.Name)
			}
		}
	}
}

// jobControllerDouble stands in for the Job controller on the fake
// clientset: a created Job gets the pod the caller describes, labelled and
// owned the way the controller does it, and a deleted Job takes its pods.
func jobControllerDouble(clientset *fake.Clientset, podFor func(job *batchv1.Job) *corev1.Pod) {
	clientset.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		job := action.(k8stesting.CreateAction).GetObject().(*batchv1.Job)
		if _, err := clientset.Tracker().Get(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, job.Namespace, job.Name); err == nil {
			return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, job.Name)
		}
		job.UID = types.UID("uid-" + job.Name)
		if err := clientset.Tracker().Add(job); err != nil {
			return true, nil, err
		}
		if pod := podFor(job); pod != nil {
			pod.Namespace = job.Namespace
			pod.Labels = map[string]string{jobNameLabelKey: job.Name, jobUIDLabelKey: string(job.UID), runtimeLabelKey: StepRuntimeJob}
			controller := true
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}}
			if err := clientset.Tracker().Add(pod); err != nil {
				return true, nil, err
			}
		}
		return true, job, nil
	})
	clientset.PrependReactor("delete", "jobs", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		// The tracker, not the clientset: a reactor runs under the fake's
		// lock, and a clientset call from inside it deadlocks.
		name := action.(k8stesting.DeleteAction).GetName()
		listed, _ := clientset.Tracker().List(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, action.GetNamespace(), metav1.ListOptions{})
		if pods, ok := listed.(*corev1.PodList); ok {
			for _, pod := range pods.Items {
				if pod.Labels[jobNameLabelKey] == name {
					_ = clientset.Tracker().Delete(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, pod.Namespace, pod.Name)
				}
			}
		}
		return false, nil, nil
	})
}

func terminatedJobPod(name string, exit int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("pod-uid-" + name)},
		Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: mainContainerName}}},
		Status: corev1.PodStatus{
			Phase: map[bool]corev1.PodPhase{true: corev1.PodSucceeded, false: corev1.PodFailed}[exit == 0],
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  mainContainerName,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit}},
			}},
		},
	}
}

func TestRunAsJobCreatesASuspendedJobAndFollowsItsPod(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	var createdSuspended bool
	jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod {
		createdSuspended = job.Spec.Suspend != nil && *job.Spec.Suspend
		return terminatedJobPod(job.Name+"-abcde", 3)
	})
	container := jobRuntimeContainer(t, clientset, db.ContainerTypeTask, runtime.ContainerSpec{Dir: "/tmp/build/task"})
	stdout := &bytes.Buffer{}
	process, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "sh", Args: []string{"-c", "exit 3"}}, runtime.ProcessIO{Stdout: stdout, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !createdSuspended {
		t.Error("the Job was not created suspended")
	}
	job, err := clientset.BatchV1().Jobs("test-ns").Get(context.Background(), container.podName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the Job was not created: %v", err)
	}
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		t.Error("the Job was never unsuspended")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitStatus != 3 {
		t.Errorf("exit = %d, want 3 from main's terminated status", result.ExitStatus)
	}
	if stdout.String() != "fake logs" {
		t.Errorf("stdout = %q, want the pod's log", stdout.String())
	}
	properties, _ := container.Properties()
	if properties[exitStatusPropertyName] != "3" {
		t.Errorf("exit status property = %q", properties[exitStatusPropertyName])
	}
	pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, container.podName+"-abcde", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the Job's pod is gone: %v", err)
	}
	if pod.Annotations[exitStatusAnnotationKey] != "3" {
		t.Errorf("the Job's pod carries no exit-status annotation: %v", pod.Annotations)
	}
}

func TestRunAsJobReplacesAFinishedJobOnTheSameHandle(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	created := 0
	jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod {
		created++
		return terminatedJobPod(job.Name+"-"+strings.Repeat("x", created), int32(created))
	})
	container := jobRuntimeContainer(t, clientset, db.ContainerTypeCheck, runtime.ContainerSpec{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for want := 1; want <= 2; want++ {
		process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/opt/resource/check"}, runtime.ProcessIO{Stdin: strings.NewReader(`{}`), Stderr: &bytes.Buffer{}})
		if err != nil {
			t.Fatalf("Run %d: %v", want, err)
		}
		result, err := process.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait %d: %v", want, err)
		}
		if result.ExitStatus != want {
			t.Fatalf("run %d reported exit %d: a finished Job's pod was followed again", want, result.ExitStatus)
		}
	}
	if created != 2 {
		t.Errorf("%d Jobs created, want 2", created)
	}
	pods, _ := clientset.CoreV1().Pods("test-ns").List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 1 {
		t.Errorf("%d pods remain, want the second Job's one", len(pods.Items))
	}
}

func TestRunAsJobRegistersTheEnvelopeBeforeTheJobStarts(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	var registeredAtCreate bool
	var container *Container
	jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod {
		_, err := container.envelopes.Take(job.Name, "any")
		registeredAtCreate = err == nil
		return terminatedJobPod(job.Name+"-abcde", 0)
	})
	container = jobRuntimeContainer(t, clientset, db.ContainerTypeGet, runtime.ContainerSpec{Dir: "/tmp/build/get"})
	_, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/opt/resource/in"}, runtime.ProcessIO{Stdin: strings.NewReader(`{"source":{}}`), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !registeredAtCreate {
		t.Error("the envelope was not registered before the Job existed")
	}
	envelope, err := container.envelopes.Take(container.podName, "any")
	if err != nil || string(envelope.Stdin) != `{"source":{}}` {
		t.Errorf("envelope = %q, %v", envelope.Stdin, err)
	}
}

func TestAttachJob(t *testing.T) {
	ctx := context.Background()
	newContainerWithPod := func(pod *corev1.Pod) *Container {
		clientset := fake.NewSimpleClientset()
		container := jobRuntimeContainer(t, clientset, db.ContainerTypeTask, runtime.ContainerSpec{Dir: "/tmp/build/task"})
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: container.podName, Namespace: "test-ns", UID: "job-uid"}}
		if _, err := clientset.BatchV1().Jobs("test-ns").Create(ctx, job, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if pod != nil {
			pod.Namespace = "test-ns"
			pod.Labels = map[string]string{jobNameLabelKey: container.podName, jobUIDLabelKey: "job-uid"}
			if _, err := clientset.CoreV1().Pods("test-ns").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return container
	}
	t.Run("no pod yet is an error so Run follows the Job", func(t *testing.T) {
		if _, err := newContainerWithPod(nil).Attach(ctx, "p", runtime.ProcessIO{}); !errors.Is(err, errJobPodNotFound) {
			t.Errorf("Attach = %v, want the no-pod error", err)
		}
	})
	t.Run("an annotated pod answers from the annotation", func(t *testing.T) {
		pod := terminatedJobPod("job-pod", 0)
		pod.Annotations = map[string]string{exitStatusAnnotationKey: "4"}
		process, err := newContainerWithPod(pod).Attach(ctx, "p", runtime.ProcessIO{})
		if err != nil {
			t.Fatal(err)
		}
		if result, _ := process.Wait(ctx); result.ExitStatus != 4 {
			t.Errorf("exit = %d, want 4", result.ExitStatus)
		}
	})
	t.Run("a terminated pod without an annotation answers from the kubelet", func(t *testing.T) {
		process, err := newContainerWithPod(terminatedJobPod("job-pod", 5)).Attach(ctx, "p", runtime.ProcessIO{})
		if err != nil {
			t.Fatal(err)
		}
		if result, _ := process.Wait(ctx); result.ExitStatus != 5 {
			t.Errorf("exit = %d, want 5", result.ExitStatus)
		}
	})
	t.Run("a running pod is an error so Run follows the Job", func(t *testing.T) {
		pod := terminatedJobPod("job-pod", 0)
		pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
		if _, err := newContainerWithPod(pod).Attach(ctx, "p", runtime.ProcessIO{}); err == nil || !strings.Contains(err.Error(), "still running") {
			t.Errorf("Attach = %v, want a still-running error", err)
		}
	})
}

func TestDeleteStepPodGoesThroughTheJob(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod { return terminatedJobPod(job.Name+"-abcde", 0) })
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "step", Namespace: "test-ns"}}
	if _, err := clientset.BatchV1().Jobs("test-ns").Create(ctx, job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "step-abcde", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteStepPod(ctx, clientset, "test-ns", pod.ObjectMeta, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.BatchV1().Jobs("test-ns").Get(ctx, "step", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the Job survived its pod's deletion: %v", err)
	}
	if _, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "step-abcde", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the pod survived: %v", err)
	}

	pause := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pause", Namespace: "test-ns"}}
	if _, err := clientset.CoreV1().Pods("test-ns").Create(ctx, pause, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := deleteStepPod(ctx, clientset, "test-ns", pause.ObjectMeta, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "pause", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the pause pod survived: %v", err)
	}
}

func TestStepEnvelopesAreHandedToOnePod(t *testing.T) {
	envelopes := NewStepEnvelopes()
	if _, err := envelopes.Take("job", "pod-a"); !errors.Is(err, errEnvelopeUnknown) {
		t.Errorf("unknown job: %v", err)
	}
	envelopes.Register("job", StepEnvelope{Stdin: []byte("in")})
	if envelope, err := envelopes.Take("job", "pod-a"); err != nil || string(envelope.Stdin) != "in" {
		t.Errorf("first take: %q %v", envelope.Stdin, err)
	}
	if _, err := envelopes.Take("job", "pod-a"); err != nil {
		t.Errorf("the same pod may take again: %v", err)
	}
	if _, err := envelopes.Take("job", "pod-b"); !errors.Is(err, errEnvelopeTaken) {
		t.Errorf("another pod: %v", err)
	}
	envelopes.Forget("job")
	if _, err := envelopes.Take("job", "pod-a"); !errors.Is(err, errEnvelopeUnknown) {
		t.Errorf("after forget: %v", err)
	}
}

func TestTheReaperTakesAStepJobWithNoPod(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	old := metav1.NewTime(time.Now().Add(-podlessStepJobGrace - time.Minute))
	fresh := metav1.Now()
	for _, job := range []*batchv1.Job{
		{ObjectMeta: metav1.ObjectMeta{Name: "stale-no-pod", Namespace: "test-ns", UID: "u1", CreationTimestamp: old, Labels: map[string]string{workerLabelKey: "k8s-test-ns"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "fresh-no-pod", Namespace: "test-ns", UID: "u2", CreationTimestamp: fresh, Labels: map[string]string{workerLabelKey: "k8s-test-ns"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "stale-with-pod", Namespace: "test-ns", UID: "u3", CreationTimestamp: old, Labels: map[string]string{workerLabelKey: "k8s-test-ns"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other-worker", Namespace: "test-ns", UID: "u4", CreationTimestamp: old, Labels: map[string]string{workerLabelKey: "k8s-other"}}},
	} {
		if _, err := clientset.BatchV1().Jobs("test-ns").Create(ctx, job, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	pod := terminatedJobPod("stale-with-pod-abcde", 0)
	pod.Namespace = "test-ns"
	pod.Labels = map[string]string{jobUIDLabelKey: "u3"}
	if _, err := clientset.CoreV1().Pods("test-ns").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	reaper := &Reaper{clientset: clientset, cfg: NewConfig("test-ns", "")}
	reaper.reapPodlessStepJobs(ctx, lagertest.NewTestLogger("reaper"), "k8s-test-ns")
	remaining, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	var names []string
	for _, job := range remaining.Items {
		names = append(names, job.Name)
	}
	if got := strings.Join(names, ","); got != "fresh-no-pod,other-worker,stale-with-pod" {
		t.Errorf("remaining Jobs = %s, want only the stale pod-less one gone", got)
	}
}

func TestWebImageIsTheWebContainersImage(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "concourse-web-0", Namespace: "cicd"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "sidecar", Image: "registry.example/other:1"},
			{Name: "concourse-web", Image: "registry.example/jetbridge:0.4.5-rc-abc", Args: []string{"web", "--postgres-host=db"}},
		}},
	})
	image, err := WebImage(ctx, clientset, "cicd", "concourse-web-0")
	if err != nil || image != "registry.example/jetbridge:0.4.5-rc-abc" {
		t.Errorf("WebImage = %q, %v", image, err)
	}
	if _, err := WebImage(ctx, clientset, "cicd", "missing"); err == nil {
		t.Error("a missing pod is not an error")
	}
	if _, err := WebImage(ctx, clientset, "cicd", ""); err == nil {
		t.Error("an empty pod name is not an error")
	}
}
