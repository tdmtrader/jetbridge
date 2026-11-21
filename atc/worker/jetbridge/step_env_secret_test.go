package jetbridge

import (
	"bytes"
	"context"
	"encoding/json"
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

// The params a task step's pod is given: a credential with the kubelet's
// expansion syntax in it, an empty one, and one that overrides the step's own
// BUILD_ID.
const stepSecretValue = "s3cr3t-$(HOME)-$$-tail"

func taskParams() []runtime.Param {
	return runtime.ParamsFrom(atc.TaskEnv{"TOKEN": stepSecretValue, "EMPTY": "", "BUILD_ID": "from-a-param"})
}

func taskSpecWithParams() runtime.ContainerSpec {
	return runtime.ContainerSpec{
		Dir:      "/tmp/build/task",
		Env:      []string{"BUILD_ID=42", "ATC_EXTERNAL_URL=https://ci.example"},
		Params:   taskParams(),
		Sidecars: []atc.SidecarConfig{{Name: "db", Image: "postgres:16", Env: []atc.SidecarEnvVar{{Name: "PGDATA", Value: "/data"}}}},
	}
}

// execRuntimeContainer is a step's container on the exec step runtime.
func execRuntimeContainer(t *testing.T, clientset *fake.Clientset, kind db.ContainerType, spec runtime.ContainerSpec) *Container {
	t.Helper()
	spec.Type = kind
	if spec.ImageSpec.ImageURL == "" {
		spec.ImageSpec.ImageURL = "docker:///busybox"
	}
	metadata := db.ContainerMetadata{Type: kind, PipelineName: "p", JobName: "j", BuildName: "1", StepName: "s"}
	return newContainer("step-handle", metadata, spec, nil, clientset, NewConfig("test-ns", ""), "worker",
		&destroyingExecutor{}, nil, nil, stepPodIdentities{}, false, false)
}

// podUIDs gives every pod the fake creates a UID, as the API server does.
func podUIDs(clientset *fake.Clientset) {
	clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		if pod.UID == "" {
			pod.UID = types.UID("pod-uid-" + pod.Name)
		}
		return false, nil, nil
	})
}

// secretActions is the clientset's calls on Secrets, as "verb name".
func secretActions(clientset *fake.Clientset) []string {
	var out []string
	for _, action := range clientset.Actions() {
		if action.GetResource().Resource != "secrets" {
			continue
		}
		out = append(out, action.GetVerb())
	}
	return out
}

// verbsOn is the clientset's calls as "verb resource", in order, for the
// resources named.
func verbsOn(clientset *fake.Clientset, resources ...string) []string {
	var out []string
	for _, action := range clientset.Actions() {
		for _, resource := range resources {
			if action.GetResource().Resource == resource && action.GetSubresource() == "" && action.GetVerb() != "get" && action.GetVerb() != "list" && action.GetVerb() != "watch" {
				out = append(out, action.GetVerb()+" "+resource)
			}
		}
	}
	return out
}

// assertNoLiteralParam fails on any param value in the pod's containers,
// init containers or sidecars: env values, command or args.
func assertNoLiteralParam(t *testing.T, what string, spec corev1.PodSpec) {
	t.Helper()
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"s3cr3t", "from-a-param"} {
		if strings.Contains(string(encoded), needle) {
			t.Errorf("%s carries the param value %q: %s", what, needle, encoded)
		}
	}
}

// assertParamEnv checks the main container's env: the step's own entries as
// literals first, then each param read from the step's Secret under p<n> in
// name order, so a param still overrides the step's own variable of the same
// name.
func assertParamEnv(t *testing.T, env []corev1.EnvVar) {
	t.Helper()
	want := []struct{ name, value, key string }{
		{"BUILD_ID", "42", ""},
		{"ATC_EXTERNAL_URL", "https://ci.example", ""},
		{"BUILD_ID", "", "p0"},
		{"EMPTY", "", "p1"},
		{"TOKEN", "", "p2"},
	}
	if len(env) != len(want) {
		t.Fatalf("main env = %+v, want %d entries", env, len(want))
	}
	for i, w := range want {
		got := env[i]
		if got.Name != w.name {
			t.Errorf("env[%d] = %q, want %q", i, got.Name, w.name)
		}
		if w.key == "" {
			if got.ValueFrom != nil || got.Value != w.value {
				t.Errorf("env[%d] %s = %+v, want the literal %q", i, w.name, got, w.value)
			}
			continue
		}
		if got.Value != "" || got.ValueFrom == nil || got.ValueFrom.SecretKeyRef == nil {
			t.Errorf("env[%d] %s = %+v, want a secretKeyRef", i, w.name, got)
			continue
		}
		ref := got.ValueFrom.SecretKeyRef
		if ref.Name != "step-env-step-handle" || ref.Key != w.key || (ref.Optional != nil && *ref.Optional) {
			t.Errorf("env[%d] %s reads %+v, want step-env-step-handle/%s, not optional", i, w.name, ref, w.key)
		}
	}
}

// assertStepSecret checks the Secret's contents and its one owner.
func assertStepSecret(t *testing.T, clientset *fake.Clientset, owner metav1.OwnerReference) {
	t.Helper()
	secret, err := clientset.CoreV1().Secrets("test-ns").Get(context.Background(), "step-env-step-handle", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the step's params Secret: %v", err)
	}
	if secret.Immutable == nil || !*secret.Immutable || secret.Type != corev1.SecretTypeOpaque {
		t.Errorf("Secret immutable=%v type=%q, want immutable Opaque", secret.Immutable, secret.Type)
	}
	want := map[string]string{"p0": "from-a-param", "p1": "", "p2": stepSecretValue}
	if len(secret.Data) != len(want) {
		t.Errorf("Secret data = %v, want %v", secret.Data, want)
	}
	for key, value := range want {
		got, ok := secret.Data[key]
		if !ok || string(got) != value {
			t.Errorf("Secret %s = %q (present %v), want %q as written", key, got, ok, value)
		}
	}
	if secret.Labels[handleLabelKey] != "step-handle" || secret.Labels[workerLabelKey] != "worker" {
		t.Errorf("Secret labels = %v, want the step pod's", secret.Labels)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0] != owner {
		t.Errorf("Secret owners = %+v, want only %+v", secret.OwnerReferences, owner)
	}
}

func TestAPausePodReadsItsTaskParamsFromItsOwnSecret(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	podUIDs(clientset)
	container := execRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
	if _, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy", Args: []string{"--quiet"}}, runtime.ProcessIO{Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	pod, err := clientset.CoreV1().Pods("test-ns").Get(context.Background(), container.podName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the pause pod: %v", err)
	}
	assertNoLiteralParam(t, "the pause pod", pod.Spec)
	assertParamEnv(t, pod.Spec.Containers[0].Env)
	if pod.Annotations[stepEnvSecretAnnotation] != "step-env-step-handle" {
		t.Errorf("pod annotations = %v, want the Secret named", pod.Annotations)
	}
	assertStepSecret(t, clientset, metav1.OwnerReference{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID})

	// The Secret before the pod, a stale one of its name deleted first, and
	// the owner set once the pod's UID is known.
	got := verbsOn(clientset, "secrets", "pods")
	want := []string{"delete secrets", "create secrets", "create pods", "patch secrets"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestAReplacedPausePodGetsItsParamsSecretAfresh(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	podUIDs(clientset)
	container := execRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
	ctx := context.Background()
	first, err := container.createPausePod(ctx, runtime.ProcessSpec{Path: "/bin/deploy"})
	if err != nil {
		t.Fatalf("first pause pod: %v", err)
	}
	if err := clientset.CoreV1().Pods("test-ns").Delete(ctx, first.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).UID = "pod-uid-replacement"
		return false, nil, nil
	})
	clientset.ClearActions()
	second, err := container.createPausePod(ctx, runtime.ProcessSpec{Path: "/bin/deploy"})
	if err != nil {
		t.Fatalf("replacement pause pod: %v", err)
	}
	if got := strings.Join(verbsOn(clientset, "secrets", "pods"), ","); got != "delete secrets,create secrets,create pods,patch secrets" {
		t.Errorf("replacement calls = %s", got)
	}
	assertStepSecret(t, clientset, metav1.OwnerReference{APIVersion: "v1", Kind: "Pod", Name: second.Name, UID: "pod-uid-replacement"})
}

func TestAPausePodThatCannotBeCreatedTakesItsSecretWithIt(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Kind: "Pod"}, "p", nil)
	})
	container := execRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
	if _, err := container.createPausePod(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy"}); err == nil {
		t.Fatal("a refused pod create succeeded")
	}
	if _, err := clientset.CoreV1().Secrets("test-ns").Get(context.Background(), "step-env-step-handle", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the Secret outlived its pod's failed create: %v", err)
	}
}

func TestARefusedParamsSecretFailsTheStepNamingTheRole(t *testing.T) {
	forbid := func(clientset *fake.Clientset) {
		clientset.PrependReactor("*", "secrets", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "step-env-step-handle", nil)
		})
	}
	assertNamesTheRole := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("the step ran without its params Secret")
		}
		for _, want := range []string{`"secrets: create, patch, delete"`, `"test-ns"`, "step-env-step-handle"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %s", err, want)
			}
		}
		var transient *TransientError
		if errors.As(err, &transient) {
			t.Errorf("a refusal is retried as transient: %v", err)
		}
	}

	t.Run("exec", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		forbid(clientset)
		container := execRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
		_, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy"}, runtime.ProcessIO{Stderr: &bytes.Buffer{}})
		assertNamesTheRole(t, err)
		if pods, _ := clientset.CoreV1().Pods("test-ns").List(context.Background(), metav1.ListOptions{}); len(pods.Items) != 0 {
			t.Errorf("a pod was made without its Secret: %+v", pods.Items)
		}
	})

	t.Run("job", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod { return terminatedJobPod(job.Name+"-abcde", 0) })
		forbid(clientset)
		container := jobRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
		_, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy"}, runtime.ProcessIO{Stderr: &bytes.Buffer{}})
		assertNamesTheRole(t, err)
		if jobs, _ := clientset.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{}); len(jobs.Items) != 0 {
			t.Errorf("the Job outlived its refused Secret: %+v", jobs.Items)
		}
		if pods, _ := clientset.CoreV1().Pods("test-ns").List(context.Background(), metav1.ListOptions{}); len(pods.Items) != 0 {
			t.Errorf("a pod was made without its Secret: %+v", pods.Items)
		}
	})
}

func TestParamsThatCannotFitOneSecretFailTheStepBeforeAnythingIsMade(t *testing.T) {
	big := runtime.ContainerSpec{Params: []runtime.Param{
		{Name: "HALF", Value: strings.Repeat("a", maxStepEnvSecretBytes/2)},
		{Name: "OTHER", Value: strings.Repeat("b", maxStepEnvSecretBytes/2)},
	}}
	for name, container := range map[string]func(*fake.Clientset) *Container{
		"exec": func(c *fake.Clientset) *Container { return execRuntimeContainer(t, c, db.ContainerTypeTask, big) },
		"job":  func(c *fake.Clientset) *Container { return jobRuntimeContainer(t, c, db.ContainerTypeTask, big) },
	} {
		t.Run(name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			_, err := container(clientset).Run(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy"}, runtime.ProcessIO{Stderr: &bytes.Buffer{}})
			if err == nil || !strings.Contains(err.Error(), "1048580 bytes") || !strings.Contains(err.Error(), "one Kubernetes Secret (1048576 bytes)") {
				t.Fatalf("Run error = %v, want one naming the size and the limit", err)
			}
			for _, action := range clientset.Actions() {
				if action.GetVerb() == "create" || action.GetVerb() == "delete" || action.GetVerb() == "patch" {
					t.Errorf("an oversized step called %s %s", action.GetVerb(), action.GetResource().Resource)
				}
			}
		})
	}
}

func TestAStepJobOwnsItsParamsSecretAndStoresTheValuesAsWritten(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod { return terminatedJobPod(job.Name+"-abcde", 0) })
	container := jobRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
	if _, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy", Args: []string{"--quiet"}}, runtime.ProcessIO{Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	job, err := clientset.BatchV1().Jobs("test-ns").Get(context.Background(), container.podName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the step Job: %v", err)
	}
	encoded, _ := json.Marshal(job)
	if strings.Contains(string(encoded), "s3cr3t") || strings.Contains(string(encoded), "from-a-param") {
		t.Errorf("the Job carries a param value: %s", encoded)
	}
	assertNoLiteralParam(t, "the Job's template", job.Spec.Template.Spec)
	assertParamEnv(t, job.Spec.Template.Spec.Containers[0].Env)
	if job.Spec.Template.Annotations[stepEnvSecretAnnotation] != "step-env-step-handle" {
		t.Errorf("template annotations = %v, want the Secret named", job.Spec.Template.Annotations)
	}
	// Owned by the Job, not its pod, and stored unescaped: the kubelet
	// expands nothing it reads from a Secret.
	assertStepSecret(t, clientset, metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID})

	got := verbsOn(clientset, "secrets", "jobs")
	want := []string{"create jobs", "delete secrets", "create secrets", "patch jobs"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want the Secret between the Job's create and its unsuspend: %v", got, want)
	}
}

func TestAResourceStepMakesNoSecret(t *testing.T) {
	for _, kind := range []db.ContainerType{db.ContainerTypeCheck, db.ContainerTypeGet, db.ContainerTypePut} {
		t.Run(string(kind)+" exec", func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			container := execRuntimeContainer(t, clientset, kind, runtime.ContainerSpec{Env: []string{"BUILD_ID=42"}})
			if _, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/opt/resource/in"}, runtime.ProcessIO{Stdin: strings.NewReader(`{"source":{"password":"s3cr3t"}}`)}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := secretActions(clientset); len(got) != 0 {
				t.Errorf("a %s step touched Secrets: %v", kind, got)
			}
		})
		t.Run(string(kind)+" job", func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod { return terminatedJobPod(job.Name+"-abcde", 0) })
			container := jobRuntimeContainer(t, clientset, kind, runtime.ContainerSpec{Env: []string{"BUILD_ID=42"}})
			if _, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/opt/resource/in"}, runtime.ProcessIO{Stdin: strings.NewReader(`{"source":{"password":"s3cr3t"}}`), Stderr: &bytes.Buffer{}}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := secretActions(clientset); len(got) != 0 {
				t.Errorf("a %s step touched Secrets: %v", kind, got)
			}
		})
	}
}

func TestReattachingToALivePausePodMakesNothing(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	container := execRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
	pod, err := container.buildPod(runtime.ProcessSpec{Path: "/bin/deploy"}, []string{"sh", "-c", pauseCommand}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pod.UID = "pod-uid-live"
	pod.Status.Phase = corev1.PodRunning
	if err := clientset.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	clientset.ClearActions()
	if _, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy"}, runtime.ProcessIO{Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "create" || action.GetVerb() == "delete" {
			t.Errorf("reattaching called %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
	// Only the owner a web that died before setting it would have left unset.
	if got := secretActions(clientset); strings.Join(got, ",") != "patch" {
		t.Errorf("Secret calls = %v, want the owner patch alone", got)
	}
}

func TestAPodAnOlderWebMadeWithLiteralParamsIsNotGivenASecret(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	container := execRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
	pod, err := container.buildPod(runtime.ProcessSpec{Path: "/bin/deploy"}, []string{"sh", "-c", pauseCommand}, nil)
	if err != nil {
		t.Fatal(err)
	}
	delete(pod.Annotations, stepEnvSecretAnnotation)
	pod.UID = "pod-uid-old"
	pod.Status.Phase = corev1.PodRunning
	if err := clientset.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	if _, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy"}, runtime.ProcessIO{Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := secretActions(clientset); len(got) != 0 {
		t.Errorf("Secret calls for a pod that reads none: %v", got)
	}
}

// The guard both runtimes pass next to the step pod admission: a param that
// would reach the main container as a literal value is refused, so no later
// change to the pod's composition can bring one back unnoticed.
func TestALiteralParamInTheMainContainerIsRefused(t *testing.T) {
	container := execRuntimeContainer(t, fake.NewSimpleClientset(), db.ContainerTypeTask, taskSpecWithParams())
	pod, err := container.buildPod(runtime.ProcessSpec{Path: "/bin/deploy"}, []string{"sh", "-c", pauseCommand}, nil)
	if err != nil {
		t.Fatalf("a well-formed pod is refused: %v", err)
	}
	env := pod.Spec.Containers[0].Env
	for _, extra := range []corev1.EnvVar{
		{Name: "TOKEN", Value: stepSecretValue},
		// The step's own BUILD_ID is a literal only before the param that
		// overrides it; after it, it would be the value the task sees.
		{Name: "BUILD_ID", Value: "from-a-param"},
	} {
		pod.Spec.Containers[0].Env = append(append([]corev1.EnvVar{}, env...), extra)
		if err := container.refuseLiteralParams(pod); err == nil || !strings.Contains(err.Error(), `"`+extra.Name+`"`) {
			t.Errorf("refuseLiteralParams = %v, want a refusal naming %s", err, extra.Name)
		}
	}
	pod.Spec.Containers[0].Env = env[:len(env)-1]
	if err := container.refuseLiteralParams(pod); err == nil || !strings.Contains(err.Error(), `"TOKEN" is not read`) {
		t.Errorf("refuseLiteralParams = %v, want a refusal of a param read from nowhere", err)
	}
}

func TestAStepParamNameNeedNotBeAValidSecretKey(t *testing.T) {
	params := runtime.ParamsFrom(atc.TaskEnv{"weird name!": "v", "_UNDERSCORE": "u"})
	env := stepParamEnv("h", params)
	data, err := stepEnvSecretData(params)
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range env {
		if entry.Name != params[i].Name {
			t.Errorf("env[%d] = %q, want the param's own name %q", i, entry.Name, params[i].Name)
		}
		if _, ok := data[entry.ValueFrom.SecretKeyRef.Key]; !ok {
			t.Errorf("env %q reads key %q, which the Secret lacks", entry.Name, entry.ValueFrom.SecretKeyRef.Key)
		}
	}
	for key := range data {
		if strings.Trim(key, "p0123456789") != "" {
			t.Errorf("Secret key %q is not one of ours", key)
		}
	}
}

// A pod read that fails for any reason but NotFound says nothing about
// whether the pod is there: the step fails, retryable, and the Secret a live
// pod may still be about to read is not touched.
func TestAFailedPodReadOnReattachLeavesTheSecretAlone(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	container := execRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
	pod, err := container.buildPod(runtime.ProcessSpec{Path: "/bin/deploy"}, []string{"sh", "-c", pauseCommand}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodPending
	if err := clientset.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	clientset.PrependReactor("get", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("the API server is restarting")
	})
	_, err = container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy"}, runtime.ProcessIO{Stderr: &bytes.Buffer{}})
	var transient *TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("Run = %v, want a transient error", err)
	}
	if got := secretActions(clientset); len(got) != 0 {
		t.Errorf("a failed pod read touched the Secret: %v", got)
	}
}

func TestTheKubeletMayLagOnTheStepsOwnSecretForABoundedTime(t *testing.T) {
	now := time.Now()
	pod := func(age time.Duration, annotated bool, reason, message string) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-age)), Annotations: map[string]string{}}}
		if annotated {
			p.Annotations[stepEnvSecretAnnotation] = "step-env-h"
		}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: mainContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}}}}
		return p
	}
	for _, tc := range []struct {
		name    string
		pod     *corev1.Pod
		pending bool
	}{
		{"its Secret not found yet", pod(time.Second, true, "CreateContainerConfigError", `secret "step-env-h" not found`), true},
		{"the secret cache not synced", pod(time.Second, true, "CreateContainerConfigError", "failed to sync secret cache: timed out waiting for the condition"), true},
		{"past the grace", pod(stepEnvSecretSyncGrace+time.Second, true, "CreateContainerConfigError", `secret "step-env-h" not found`), false},
		{"another Secret", pod(time.Second, true, "CreateContainerConfigError", `secret "other" not found`), false},
		{"a pod with no params Secret", pod(time.Second, false, "CreateContainerConfigError", "failed to sync secret cache"), false},
		{"another terminal reason", pod(time.Second, true, "ErrImagePull", `secret "step-env-h" not found`), false},
		{"nothing waiting", pod(time.Second, true, "ContainerCreating", ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, pending := stepEnvSecretSyncPending(tc.pod, now)
			if pending != tc.pending {
				t.Errorf("pending = %v, want %v", pending, tc.pending)
			}
			if pending && (left <= 0 || left > stepEnvSecretSyncGrace) {
				t.Errorf("left = %s", left)
			}
		})
	}
}

// On the job step runtime: a pod that starts into CreateContainerConfigError
// for its own Secret, and then runs, is the step's; one still waiting once the
// grace is spent fails as before, without needing another event.
func TestAStepJobPodWaitsOutTheKubeletsLagOnItsSecret(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     time.Duration
		recover bool
	}{
		{"recovers", 0, true},
		{"never recovers", stepEnvSecretSyncGrace - 2*time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			var podName string
			jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod {
				podName = job.Name + "-abcde"
				p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
					Name: podName, UID: "pod-uid", CreationTimestamp: metav1.NewTime(time.Now().Add(-tc.age)),
					Annotations: map[string]string{stepEnvSecretAnnotation: "step-env-step-handle"},
				}}
				p.Spec = corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: mainContainerName}}}
				p.Status.Phase = corev1.PodPending
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: mainContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "CreateContainerConfigError", Message: `secret "step-env-step-handle" not found`,
				}}}}
				return p
			})
			container := jobRuntimeContainer(t, clientset, db.ContainerTypeTask, runtime.ContainerSpec{Params: taskParams()})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/deploy"}, runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if tc.recover {
				go func() {
					time.Sleep(300 * time.Millisecond)
					p, err := clientset.CoreV1().Pods("test-ns").Get(context.Background(), podName, metav1.GetOptions{})
					if err != nil {
						return
					}
					done := terminatedJobPod(podName, 0)
					p.Status = done.Status
					_, _ = clientset.CoreV1().Pods("test-ns").UpdateStatus(context.Background(), p, metav1.UpdateOptions{})
				}()
			}
			result, err := process.Wait(ctx)
			if tc.recover {
				if err != nil || result.ExitStatus != 0 {
					t.Fatalf("Wait = %+v, %v; want the command's exit once the kubelet read the Secret", result, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "CreateContainerConfigError") {
				t.Fatalf("Wait = %+v, %v; want CreateContainerConfigError once the grace is spent", result, err)
			}
		})
	}
}

// The same on the exec step runtime's pause pod.
func TestAPausePodWaitsOutTheKubeletsLagOnItsSecret(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	podUIDs(clientset)
	container := execRuntimeContainer(t, clientset, db.ContainerTypeTask, runtime.ContainerSpec{Params: taskParams()})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/deploy"}, runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	pods := clientset.CoreV1().Pods("test-ns")
	set := func(status corev1.PodStatus) {
		t.Helper()
		p, err := pods.Get(context.Background(), container.podName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		p.CreationTimestamp = metav1.Now()
		p.Status = status
		if _, err := pods.Update(context.Background(), p, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	set(corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{Name: mainContainerName, State: corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: `secret "step-env-step-handle" not found`},
	}}}})
	go func() {
		time.Sleep(300 * time.Millisecond)
		p, err := pods.Get(context.Background(), container.podName, metav1.GetOptions{})
		if err != nil {
			return
		}
		p.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: mainContainerName, Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
		_, _ = pods.Update(context.Background(), p, metav1.UpdateOptions{})
	}()
	result, err := process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 {
		t.Fatalf("Wait = %+v, %v; want the command's exit once the kubelet read the Secret", result, err)
	}
}

// A pod create that finds the pod already there leaves the Secret it may be
// reading.
func TestAPodCreateThatFindsThePodThereKeepsTheSecret(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "pods"}, "p")
	})
	container := execRuntimeContainer(t, clientset, db.ContainerTypeTask, taskSpecWithParams())
	if _, err := container.createPausePod(context.Background(), runtime.ProcessSpec{Path: "/bin/deploy"}); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("createPausePod = %v, want AlreadyExists", err)
	}
	if _, err := clientset.CoreV1().Secrets("test-ns").Get(context.Background(), "step-env-step-handle", metav1.GetOptions{}); err != nil {
		t.Errorf("the Secret was deleted under a pod that exists: %v", err)
	}
}
