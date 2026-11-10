package jetbridge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// exactRoutingContainer is a Run-owned exact step on a job-runtime worker.
func exactRoutingContainer(t *testing.T, clientset *fake.Clientset, kind db.ContainerType, gate bool) *Container {
	t.Helper()
	container := jobRuntimeContainer(t, clientset, kind, runtime.ContainerSpec{
		Dir: "/tmp/build/task",
		ExecutionControl: &runtime.ExecutionControl{
			Version:    runtime.ExecutionControlVersion,
			Phase:      runtime.ControlPhaseAdmitted,
			Identity:   executioncontrol.Identity{ExecutionID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", Fence: 1},
			Endpoint:   "https://127.0.0.1:1",
			Capability: "base-capability",
		},
	})
	container.exactTasksOnJobRuntime = gate
	container.outputControls = staticOutputControls{}
	container.executor = &controlExecutor{run: func(int) error { return nil }}
	return container
}

// Production keeps a Run-owned exact execution on a pause pod under the job
// runtime: with the test gate unset, Run creates a pod the web execs into
// and no Job. With it set, only an exact task leaves the pause pod.
func TestAnExactExecutionStaysOnThePausePodUnlessATestRoutesIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  db.ContainerType
		gate  bool
		toJob bool
	}{
		{"an exact task, gate unset", db.ContainerTypeTask, false, false},
		{"an exact check, gate unset", db.ContainerTypeCheck, false, false},
		{"an exact get, gate set", db.ContainerTypeGet, true, false},
		{"an exact task, gate set", db.ContainerTypeTask, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			container := exactRoutingContainer(t, clientset, tc.kind, tc.gate)
			if got := container.jobRuntime(); got != tc.toJob {
				t.Fatalf("jobRuntime() = %v, want %v", got, tc.toJob)
			}
			if tc.toJob {
				return
			}
			process, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}},
				runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if _, ok := process.(*execProcess); !ok {
				t.Errorf("Run returned %T, want the exec runtime's process", process)
			}
			jobs, _ := clientset.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
			if len(jobs.Items) != 0 {
				t.Errorf("%d step Jobs created for an exact execution", len(jobs.Items))
			}
			pod, err := clientset.CoreV1().Pods("test-ns").Get(context.Background(), container.podName, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("no pause pod: %v", err)
			}
			if pod.Labels[runtimeLabelKey] == StepRuntimeJob {
				t.Error("the pause pod carries the job runtime label")
			}
		})
	}
}

// The gate is a test's, never production's. It is an unexported field, so
// only this package can set it, and no file of the package outside its tests
// does: the one assignment copies a worker's value into the container it
// makes. A test sets it on a container (here) or on a worker
// (RouteExactTasksToJobRuntimeForTesting, a _test file), which no production
// binary contains.
func TestNothingInProductionSetsTheExactJobRuntimeGate(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	assignment := regexp.MustCompile(`\bexactTasksOnJobRuntime\s*(=[^=]|:[^=])`)
	const copied = "container.exactTasksOnJobRuntime = w.exactTasksOnJobRuntime"
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(body), "\n") {
			if assignment.MatchString(line) && strings.TrimSpace(line) != copied {
				t.Errorf("%s:%d sets the exact job-runtime gate: only a test may (%s)", file, n+1, strings.TrimSpace(line))
			}
		}
	}
}

// routeExactTasksToJobRuntime is the gate on one container.
func (c *Container) routeExactTasksToJobRuntime() { c.exactTasksOnJobRuntime = true }
