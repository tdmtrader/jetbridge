package steps

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ExecTargetDefinitions closes the last gap the coverage matrix carried: every
// PodExecutor double in this package declares the CONTAINER NAME parameter as
// `_`, so nothing observed which container a step's command is exec'd into.
//
// resource_test.go covered it by inspecting the recorded call. The conversion
// is the same as PE-08's: containerAwareAdapter knows which containers the pod
// actually has and refuses anything else, exactly as the API server does —
// `kubectl exec -c nope` fails with "container nope not found in pod". A step
// exec'd into its sidecar would run its resource script in the wrong image,
// against the wrong filesystem.

// containerAwareAdapter is a REAL PodExecutor that honours the container name
// rather than recording it.
type containerAwareAdapter struct {
	present map[string]bool
}

func (a containerAwareAdapter) ExecInPod(
	ctx context.Context,
	_, _ string,
	containerName string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	_ bool,
	_ jetbridge.ExecAttrs,
) error {
	if !a.present[containerName] {
		// What the API server says when the pod has no such container.
		return fmt.Errorf(
			"container %q not found in pod (has: %s)",
			containerName, strings.Join(sortedKeys(a.present), ", "))
	}
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return execExitError(err)
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func ExecTargetDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, StepOutcome](
			"a resource step runs on a pod whose only container is {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (StepOutcome, error) {
				only, ok := p.GetString(0)
				if !ok {
					return StepOutcome{}, fmt.Errorf("expected a container name parameter")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return StepOutcome{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return StepOutcome{}, err
				}

				ctx := context.Background()
				namespace := "test-namespace"
				handle := "exec-target-handle"
				clientset := fake.NewSimpleClientset()
				worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig(namespace, ""))
				worker.SetExecutor(containerAwareAdapter{present: map[string]bool{only: true}})

				container, _, err := worker.FindOrCreateContainer(ctx,
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeGet},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/get",
						ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
						Type:      db.ContainerTypeGet,
					},
					&noopDelegate{},
				)
				if err != nil {
					return StepOutcome{}, fmt.Errorf("find or create container: %w", err)
				}

				process, err := container.Run(ctx,
					runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}},
					runtime.ProcessIO{
						Stdin:  strings.NewReader("{}"),
						Stdout: new(strings.Builder),
						Stderr: new(strings.Builder),
					},
				)
				if err != nil {
					return StepOutcome{}, fmt.Errorf("run container: %w", err)
				}

				pods := clientset.CoreV1().Pods(namespace)
				pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
				if err != nil {
					return StepOutcome{}, fmt.Errorf("get pod: %w", err)
				}
				pod.Status.Phase = corev1.PodRunning
				if _, err := pods.UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
					return StepOutcome{}, fmt.Errorf("update pod: %w", err)
				}

				result, waitErr := process.Wait(ctx)
				msg := ""
				if waitErr != nil {
					msg = waitErr.Error()
				}
				return StepOutcome{
					Err: waitErr, Message: msg, ExitStatus: result.ExitStatus,
				}, nil
			},
		),

		brine.DefineCheck[StepOutcome](
			"the step ran, so it was exec'd into the container that exists",
			func(in StepOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Err != nil {
					return fmt.Errorf(
						"the step failed with %q — the runtime exec'd into a container this pod "+
							"does not have, so the resource script would run in the wrong image "+
							"against the wrong filesystem", in.Message)
				}
				return nil
			},
		),
	}
}
