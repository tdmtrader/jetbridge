package jetbridge

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Both runtimes compose their pod through composePod, so a task's run.user
// reaches the main container whichever runtime runs it.
func taskPodsFor(t *testing.T, user string) map[string]*corev1.PodSpec {
	t.Helper()
	container := jobRuntimeContainer(t, fake.NewSimpleClientset(), db.ContainerTypeTask, runtime.ContainerSpec{Dir: "/tmp/build/task"})
	spec := runtime.ProcessSpec{Path: "sh", Args: []string{"-c", "id -u"}, User: user}

	pod, err := container.buildPod(spec, []string{"sh", "-c", pauseCommand}, nil)
	if err != nil {
		t.Fatalf("buildPod: %v", err)
	}
	job, err := container.buildStepJob(spec, false)
	if err != nil {
		t.Fatalf("buildStepJob: %v", err)
	}
	return map[string]*corev1.PodSpec{"exec": &pod.Spec, "job": &job.Spec.Template.Spec}
}

func TestATaskUserBecomesTheMainContainersUID(t *testing.T) {
	for user, want := range map[string]int64{"root": 0, "0": 0, "1000": 1000, "2147483647": 2147483647} {
		for runtimeName, pod := range taskPodsFor(t, user) {
			got := pod.Containers[0].SecurityContext.RunAsUser
			if got == nil || *got != want {
				t.Errorf("%s runtime, user %q: RunAsUser = %v, want %d", runtimeName, user, got, want)
			}
		}
	}
}

func TestATaskWithNoUserKeepsTheImagesUser(t *testing.T) {
	for runtimeName, pod := range taskPodsFor(t, "") {
		if got := pod.Containers[0].SecurityContext.RunAsUser; got != nil {
			t.Errorf("%s runtime: RunAsUser = %d, want it unset", runtimeName, *got)
		}
	}
}

func TestATaskUserThatIsNotAUIDIsRefused(t *testing.T) {
	container := jobRuntimeContainer(t, fake.NewSimpleClientset(), db.ContainerTypeTask, runtime.ContainerSpec{Dir: "/tmp/build/task"})
	for _, user := range []string{"concourse", "-1", "2147483648", "1000:1000"} {
		spec := runtime.ProcessSpec{Path: "sh", User: user}
		if _, err := container.buildPod(spec, []string{"sh"}, nil); err == nil || !strings.Contains(err.Error(), `unsupported task user "`+user+`"`) {
			t.Errorf("exec runtime, user %q: err = %v", user, err)
		}
		if _, err := container.buildStepJob(spec, false); err == nil || !strings.Contains(err.Error(), `unsupported task user "`+user+`"`) {
			t.Errorf("job runtime, user %q: err = %v", user, err)
		}
	}
}

// A uid is not privilege: a task that runs as root keeps the unprivileged
// security policy, and a privileged task keeps its privilege.
func TestATaskUserLeavesThePrivilegePolicyAlone(t *testing.T) {
	for runtimeName, pod := range taskPodsFor(t, "root") {
		context := pod.Containers[0].SecurityContext
		if context.Privileged != nil || context.AllowPrivilegeEscalation == nil || *context.AllowPrivilegeEscalation {
			t.Errorf("%s runtime: a root task's security context = %+v, want escalation denied", runtimeName, context)
		}
	}
	container := jobRuntimeContainer(t, fake.NewSimpleClientset(), db.ContainerTypeTask, runtime.ContainerSpec{
		Dir: "/tmp/build/task", ImageSpec: runtime.ImageSpec{Privileged: true},
	})
	pod, err := container.buildPod(runtime.ProcessSpec{Path: "sh", User: "1000"}, []string{"sh"}, nil)
	if err != nil {
		t.Fatalf("buildPod: %v", err)
	}
	context := pod.Spec.Containers[0].SecurityContext
	if context.Privileged == nil || !*context.Privileged || context.RunAsUser == nil || *context.RunAsUser != 1000 {
		t.Errorf("a privileged task's security context = %+v, want privileged as uid 1000", context)
	}
}
