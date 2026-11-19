package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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
	job, _, err := container.buildStepJob(spec, false)
	if err != nil {
		t.Fatalf("buildStepJob: %v", err)
	}
	return map[string]*corev1.PodSpec{"exec": &pod.Spec, "job": &job.Spec.Template.Spec}
}

func TestATaskUserBecomesTheMainContainersUID(t *testing.T) {
	for user, want := range map[string]int64{"root": 0, "0": 0, "1001": 1001, "2147483647": 2147483647, "001001": 1001} {
		t.Run(user, func(t *testing.T) {
			for runtimeName, build := range taskPodBuilders(t, user) {
				t.Run(runtimeName, func(t *testing.T) {
					pod, err := build()
					if err != nil {
						t.Fatal(err)
					}
					got := pod.Containers[0].SecurityContext.RunAsUser
					if got == nil || *got != want {
						t.Errorf("RunAsUser = %v, want %d", got, want)
					}
					if pod.Containers[0].SecurityContext.RunAsNonRoot != nil || pod.SecurityContext.RunAsNonRoot != nil {
						t.Error("RunAsNonRoot must remain unset")
					}
					assertTaskPodMounts(t, pod)
				})
			}
		})
	}
}

func taskPodBuilders(t *testing.T, user string) map[string]func() (*corev1.PodSpec, error) {
	t.Helper()
	container := jobRuntimeContainer(t, fake.NewSimpleClientset(), db.ContainerTypeTask, runtime.ContainerSpec{Dir: "/tmp/build/task"})
	spec := runtime.ProcessSpec{Path: "sh", User: user}
	return map[string]func() (*corev1.PodSpec, error){
		"exec": func() (*corev1.PodSpec, error) {
			pod, err := container.buildPod(spec, []string{"sh", "-c", pauseCommand}, nil)
			if err != nil {
				return nil, err
			}
			return &pod.Spec, nil
		},
		"job": func() (*corev1.PodSpec, error) {
			job, _, err := container.buildStepJob(spec, false)
			if err != nil {
				return nil, err
			}
			return &job.Spec.Template.Spec, nil
		},
	}
}

func assertTaskPodMounts(t *testing.T, pod *corev1.PodSpec) {
	t.Helper()
	volumes := map[string]int{}
	for _, volume := range pod.Volumes {
		volumes[volume.Name]++
	}
	for _, container := range append(append([]corev1.Container{}, pod.Containers...), pod.InitContainers...) {
		for _, mount := range container.VolumeMounts {
			if volumes[mount.Name] != 1 {
				t.Errorf("container %s mount %s resolves to %d volumes", container.Name, mount.Name, volumes[mount.Name])
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

func TestANamedTaskUserKeepsTheImagesUser(t *testing.T) {
	for _, user := range []string{"concourse", "nobody", "1000:1000"} {
		for runtimeName, build := range taskPodBuilders(t, user) {
			t.Run(user+"/"+runtimeName, func(t *testing.T) {
				pod, err := build()
				if err != nil {
					t.Fatal(err)
				}
				if got := pod.Containers[0].SecurityContext.RunAsUser; got != nil {
					t.Errorf("RunAsUser = %d, want unset", *got)
				}
			})
		}
	}
}

func TestAnInvalidTaskUIDIsRefused(t *testing.T) {
	for _, user := range []string{"-1", "2147483648", "4294967296", "9223372036854775807", "99999999999999999999", "9223372036854775808", "+1001"} {
		for runtimeName, build := range taskPodBuilders(t, user) {
			t.Run(user+"/"+runtimeName, func(t *testing.T) {
				_, err := build()
				want := fmt.Sprintf("unsupported task user %q: a numeric uid must be unsigned decimal digits from 0 to 2147483647, the range Kubernetes runAsUser accepts", user)
				if err == nil || err.Error() != want {
					t.Errorf("expected %s, got %v", want, err)
				}
			})
		}
	}
}

func TestANamedTaskUserProducesAWarningOnBothRuntimes(t *testing.T) {
	for _, runtimeName := range []string{StepRuntimeExec, StepRuntimeJob} {
		t.Run(runtimeName, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			// Stop at submission: exercising Run's real routing needs no running pod.
			stop := errors.New("stop before creating a pod or Job")
			client.PrependReactor("create", "*", func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, stop })
			container := jobRuntimeContainer(t, client, db.ContainerTypeTask, runtime.ContainerSpec{Dir: "/tmp/build/task"})
			container.config.StepRuntime = runtimeName
			container.executor = &controlExecutor{}
			logger := lagertest.NewTestLogger("run-user")
			_, err := container.Run(lagerctx.NewContext(context.Background(), logger), runtime.ProcessSpec{Path: "sh", User: "concourse"}, runtime.ProcessIO{})
			if !errors.Is(err, stop) {
				t.Errorf("expected pod/Job submission, got %v", err)
			}
			found := false
			for _, entry := range logger.Logs() {
				if strings.HasSuffix(entry.Message, ".warning-ignored-task-user") && entry.Data["user"] == "concourse" && entry.Data["severity"] == "warning" {
					reason, _ := entry.Data["reason"].(string)
					found = strings.Contains(reason, "Kubernetes runAsUser takes only a numeric uid") && strings.Contains(reason, "image's USER")
				}
			}
			if !found {
				t.Errorf("expected warning naming concourse and explaining the image USER fallback; logs: %s", logger.Buffer().Contents())
			}
		})
	}
}

// The pipeline author reads the build log, not the web log: an ignored name
// is said once on the step's stderr, whichever runtime runs it.
func TestANamedTaskUserIsReportedOnTheStepsStderr(t *testing.T) {
	for _, runtimeName := range []string{StepRuntimeExec, StepRuntimeJob} {
		t.Run(runtimeName, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			stop := errors.New("stop before creating a pod or Job")
			client.PrependReactor("create", "*", func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, stop })
			container := jobRuntimeContainer(t, client, db.ContainerTypeTask, runtime.ContainerSpec{Dir: "/tmp/build/task"})
			container.config.StepRuntime = runtimeName
			container.executor = &controlExecutor{}
			stderr := new(strings.Builder)
			_, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "sh", User: "concourse"}, runtime.ProcessIO{Stderr: stderr})
			if !errors.Is(err, stop) {
				t.Errorf("expected pod/Job submission, got %v", err)
			}
			want := "run.user \"concourse\" is not a numeric uid and was ignored; the image's user is kept\n"
			if stderr.String() != want {
				t.Errorf("stderr = %q, want %q", stderr.String(), want)
			}
		})
	}
}

// A numeric uid writes nothing to the step's stderr.
func TestANumericTaskUserWritesNothingToStderr(t *testing.T) {
	client := fake.NewSimpleClientset()
	stop := errors.New("stop before creating a pod or Job")
	client.PrependReactor("create", "*", func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, stop })
	container := jobRuntimeContainer(t, client, db.ContainerTypeTask, runtime.ContainerSpec{Dir: "/tmp/build/task"})
	stderr := new(strings.Builder)
	_, _ = container.Run(context.Background(), runtime.ProcessSpec{Path: "sh", User: "1001"}, runtime.ProcessIO{Stderr: stderr})
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", stderr.String())
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
	pod, err := container.buildPod(runtime.ProcessSpec{Path: "sh", User: "root"}, []string{"sh"}, nil)
	if err != nil {
		t.Fatalf("buildPod: %v", err)
	}
	context := pod.Spec.Containers[0].SecurityContext
	if context.Privileged == nil || !*context.Privileged || context.RunAsUser == nil || *context.RunAsUser != 0 {
		t.Errorf("a privileged task's security context = %+v, want privileged as uid 0", context)
	}
}
