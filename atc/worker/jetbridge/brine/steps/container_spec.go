package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContainerSpecDefinitions carries the container-spec family: how a described
// container becomes a pod spec. Migrated from behavioral_runtime_spec_test.go
// (PE-03, PE-05, PE-06).
func ContainerSpecDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// ClusterReady -> ContainerDraft.
		brine.DefineMap[ClusterReady, ContainerDraft](
			"a task container {string} built from image {string}",
			func(in ClusterReady, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a container handle parameter")
				}
				image, ok := p.GetString(1)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected an image parameter")
				}
				return ContainerDraft{
					Namespace: in.Namespace,
					Worker:    in.Worker,
					Clientset: in.Clientset,
					Ctx:       in.Ctx,
					Handle:    handle,
					ImageURL:  image,
					Dir:       "/workdir",
				}, nil
			},
		),

		// Draft refinements: In and Out are the same type, so these compose
		// freely and in any order before the container runs.
		brine.DefineMap[ContainerDraft, ContainerDraft](
			"the container environment sets {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				assignment, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a KEY=VALUE parameter")
				}
				in.ContainerEnv = append(in.ContainerEnv, assignment)
				return in, nil
			},
		),

		brine.DefineMap[ContainerDraft, ContainerDraft](
			"the process environment sets {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				assignment, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a KEY=VALUE parameter")
				}
				in.ProcessEnv = append(in.ProcessEnv, assignment)
				return in, nil
			},
		),

		// ContainerDraft -> PodCreated.
		brine.DefineMap[ContainerDraft, PodCreated](
			"the container runs",
			func(in ContainerDraft, _ brine.Params, _ *brine.Recorder) (PodCreated, error) {
				var inputs []runtime.Input
				for _, path := range in.Inputs {
					inputs = append(inputs, runtime.Input{DestinationPath: path})
				}
				outputs := runtime.OutputPaths{}
				for i, path := range in.Outputs {
					outputs[fmt.Sprintf("output-%d", i)] = path
				}

				spec := runtime.ContainerSpec{
					TeamID:       1,
					Dir:          in.Dir,
					ImageSpec:    runtime.ImageSpec{ImageURL: in.ImageURL, Privileged: in.Privileged},
					Env:          in.ContainerEnv,
					Inputs:       inputs,
					Caches:       in.Caches,
					ScratchPaths: in.Scratch,
					Sidecars:     in.Sidecars,
					Limits: runtime.ContainerLimits{
						CPU:           in.LimitCPU,
						Memory:        in.LimitMemory,
						CPURequest:              in.RequestCPU,
						MemoryRequest:           in.RequestMemory,
						EphemeralStorage:        in.LimitEphemeral,
						EphemeralStorageRequest: in.RequestEphemeral,
					},
				}
				if len(outputs) > 0 {
					spec.Outputs = outputs
				}

				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask, JobID: in.JobID, StepName: in.StepName},
					spec,
					&noopDelegate{},
				)
				if err != nil {
					return PodCreated{}, fmt.Errorf("find or create container %q: %w", in.Handle, err)
				}

				if _, err := container.Run(in.Ctx,
					runtime.ProcessSpec{Path: "/bin/sh", Env: in.ProcessEnv},
					runtime.ProcessIO{},
				); err != nil {
					return PodCreated{}, fmt.Errorf("run container %q: %w", in.Handle, err)
				}

				pods, err := in.Clientset.CoreV1().Pods(in.Namespace).List(in.Ctx, metav1.ListOptions{})
				if err != nil {
					return PodCreated{}, fmt.Errorf("list pods: %w", err)
				}
				if len(pods.Items) != 1 {
					return PodCreated{}, fmt.Errorf("expected exactly 1 pod, found %d", len(pods.Items))
				}

				pod := pods.Items[0]
				return PodCreated{
					Namespace: in.Namespace,
					Ctx:       in.Ctx,
					Handle:    in.Handle,
					Pod:       &pod,
				}, nil
			},
		),

		// Checks over the resulting pod spec.
		brine.DefineCheck[PodCreated](
			"the main container is named {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a name parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				if main.Name != want {
					return fmt.Errorf("expected the main container to be named %q, got %q", want, main.Name)
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the main container image is {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an image parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				if main.Image != want {
					return fmt.Errorf("expected the main container image to be %q, got %q", want, main.Image)
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the main container image pull policy is {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pull policy parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				if string(main.ImagePullPolicy) != want {
					return fmt.Errorf("expected image pull policy %q, got %q", want, main.ImagePullPolicy)
				}
				return nil
			},
		),

		// The effective value: last assignment wins, which is how the runtime
		// expresses process-over-container precedence.
		brine.DefineCheck[PodCreated](
			"the main container environment resolves {string} to {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				key, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an environment key parameter")
				}
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected an expected-value parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}

				var values []string
				for _, e := range main.Env {
					if e.Name == key {
						values = append(values, e.Value)
					}
				}
				if len(values) == 0 {
					return fmt.Errorf("expected %q in the main container environment, found none of %d vars",
						key, len(main.Env))
				}
				if got := values[len(values)-1]; got != want {
					return fmt.Errorf("expected %q to resolve to %q, got %q (all values: %s)",
						key, want, got, strings.Join(values, ", "))
				}
				return nil
			},
		),
	}
}

func mainContainer(pod *corev1.Pod) (corev1.Container, error) {
	for _, c := range pod.Spec.Containers {
		if c.Name == "main" {
			return c, nil
		}
	}
	return corev1.Container{}, fmt.Errorf("pod %q has no container named \"main\"", pod.Name)
}
