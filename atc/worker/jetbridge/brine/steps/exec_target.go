package steps

import (
	"fmt"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
)

// ExecTargetDefinitions closes the last gap the coverage matrix carried: every
// PodExecutor double in this package declares the CONTAINER NAME parameter as
// `_`, so nothing observed which container a step's command is exec'd into.
//
// resource_test.go covered it by inspecting the recorded call. The conversion
// is the same as PE-08's: localExecutor knows which containers the pod
// actually has and refuses anything else, exactly as the API server does —
// `kubectl exec -c nope` fails with "container nope not found in pod". A step
// exec'd into its sidecar would run its resource script in the wrong image,
// against the wrong filesystem.

// sortedKeys is shared by artifact and pod diagnostics and membership checks.
func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func ExecTargetDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		TransformUsing[brine.Empty, StepOutcome](
			"a resource step runs on a pod whose only container is {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, a Args, res brine.Resources) (StepOutcome, error) {
				cluster, err := NewCluster(res,
					WithExecutor(localExecutor{present: map[string]bool{a.String(0): true}}),
				)
				if err != nil {
					return StepOutcome{}, err
				}
				ctx, namespace, worker := cluster.Ctx, cluster.Namespace, cluster.Worker
				clientset := cluster.Clientset
				handle := "exec-target-handle"

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

				if err := updateTaskPodStatus(ctx, clientset, namespace, handle, func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodRunning
				}); err != nil {
					return StepOutcome{}, err
				}

				result, waitErr := process.Wait(ctx)
				msg := errorMessage(waitErr)
				return StepOutcome{
					Err: waitErr, Message: msg, ExitStatus: result.ExitStatus,
				}, nil
			},
		),

		CheckThat[StepOutcome]("the step ran, so it was exec'd into the container that exists",
			func(in StepOutcome) error {
				if in.Err != nil {
					return fmt.Errorf(
						"the step failed with %q — the runtime exec'd into a container this pod "+
							"does not have, so the resource script would run in the wrong image "+
							"against the wrong filesystem", in.Message)
				}
				return nil
			}),
	}
}
