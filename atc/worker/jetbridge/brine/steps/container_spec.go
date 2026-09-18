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

		// Reuse draft refinements and mount assertions with the real worker.
		Transform[WorkerReady, ContainerDraft](
			"the worker prepares task {string} from image {string}",
			func(in WorkerReady, a Args) (ContainerDraft, error) {
				return workerContainerDraft(in, a.String(0), a.String(1), "")
			},
		),

		// Draft refinements: In and Out are the same type, so these compose
		// freely and in any order before the container runs.
		Refine[ContainerDraft]("the container environment sets {string}",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.ContainerEnv = append(in.ContainerEnv, a.String(0))
				return in
			}),

		Refine[ContainerDraft]("the process environment sets {string}",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.ProcessEnv = append(in.ProcessEnv, a.String(0))
				return in
			}),

		// ContainerDraft -> PodCreated.
		brine.DefineMap[ContainerDraft, PodCreated](
			"the container runs",
			func(in ContainerDraft, _ brine.Params, _ *brine.Recorder) (PodCreated, error) {
				spec, err := containerSpecFromDraft(in)
				if err != nil {
					return PodCreated{}, err
				}

				owner := db.NewFixedHandleContainerOwner(in.Handle)
				metadata := db.ContainerMetadata{
					Type: draftContainerType(in.ContainerType), JobID: in.JobID, StepName: in.StepName,
				}

				// A container whose row already exists is reused, and a reused
				// container's pod clears the workspace its last attempt left.
				if in.RanBefore {
					if _, _, err := in.Worker.FindOrCreateContainer(
						in.Ctx, owner, metadata, spec, nil,
					); err != nil {
						return PodCreated{}, fmt.Errorf("pre-create container %q: %w", in.Handle, err)
					}
				}

				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					owner,
					metadata,
					spec,
					nil,
				)
				if err != nil {
					return PodCreated{}, fmt.Errorf("find or create container %q: %w", in.Handle, err)
				}

				process, err := container.Run(in.Ctx,
					runtime.ProcessSpec{Path: "/bin/sh", Env: in.ProcessEnv},
					runtime.ProcessIO{},
				)
				if err != nil {
					return PodCreated{}, fmt.Errorf("run container %q: %w", in.Handle, err)
				}

				if process == nil {
					return PodCreated{}, fmt.Errorf("run container %q returned no process", in.Handle)
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
					Process:   process,
				}, nil
			},
		),

		// Checks over the resulting pod spec. Each says which field it is
		// about and nothing else; the parameter handling, the comparison and
		// the message are the same for all three, so they come from assert.go.
		CheckString[PodCreated]("the pod is named {string}",
			"the pod's name",
			func(in PodCreated) (string, error) {
				if in.Pod == nil {
					return "", fmt.Errorf("no pod was created")
				}
				return in.Pod.Name, nil
			}),

		CheckString[PodCreated]("the main container is named {string}",
			"the main container's name",
			func(in PodCreated) (string, error) {
				main, err := mainContainer(in.Pod)
				return main.Name, err
			}),

		CheckString[PodCreated]("the main container image is {string}",
			"the main container image",
			func(in PodCreated) (string, error) {
				main, err := mainContainer(in.Pod)
				return main.Image, err
			}),

		CheckString[PodCreated]("the main container image pull policy is {string}",
			"the main container image pull policy",
			func(in PodCreated) (string, error) {
				main, err := mainContainer(in.Pod)
				return string(main.ImagePullPolicy), err
			}),

		// The effective value: last assignment wins, which is how the runtime
		// expresses process-over-container precedence. The detail is the point
		// of the check — on a mismatch it lists EVERY value found for the key,
		// which is the evidence a precedence rule is about.
		CheckStringFor[PodCreated]("the main container environment resolves {string} to {string}",
			"the effective value",
			func(in PodCreated, key string) (string, error) {
				values, err := envValues(in, key)
				if err != nil {
					return "", err
				}
				return values[len(values)-1], nil
			},
			func(in PodCreated) string {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return ""
				}
				var all []string
				for _, e := range main.Env {
					all = append(all, e.Name+"="+e.Value)
				}
				return "all values: " + strings.Join(all, ", ")
			}),
	}
}

// envValues returns every value the main container carries for key, in order,
// so the LAST one is the effective one. Absence is an error rather than an
// empty list: a sentence about what a key resolves to presumes the key is set.
func envValues(in PodCreated, key string) ([]string, error) {
	main, err := mainContainer(in.Pod)
	if err != nil {
		return nil, err
	}
	var values []string
	for _, e := range main.Env {
		if e.Name == key {
			values = append(values, e.Value)
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("expected %q in the main container environment, found none of %d vars",
			key, len(main.Env))
	}
	return values, nil
}

func mainContainer(pod *corev1.Pod) (corev1.Container, error) {
	for _, c := range pod.Spec.Containers {
		if c.Name == "main" {
			return c, nil
		}
	}
	return corev1.Container{}, fmt.Errorf("pod %q has no container named \"main\"", pod.Name)
}

// containerSpecFromDraft gives every draft-consuming action the same spec.
// Inputs, cache identity and resource limits must not depend on which action runs.
func containerSpecFromDraft(in ContainerDraft) (runtime.ContainerSpec, error) {
	inputs, err := draftInputs(in)
	if err != nil {
		return runtime.ContainerSpec{}, err
	}
	outputs := runtime.OutputPaths{}
	for i, path := range in.Outputs {
		outputs[fmt.Sprintf("output-%d", i)] = path
	}
	for name, path := range in.NamedOutputs {
		if _, exists := outputs[name]; exists {
			return runtime.ContainerSpec{}, fmt.Errorf("duplicate output name %q", name)
		}
		outputs[name] = path
	}

	spec := runtime.ContainerSpec{
		TeamID:            in.TeamID,
		Dir:               in.Dir,
		ImageSpec:         runtime.ImageSpec{ImageURL: in.ImageURL, Privileged: in.Privileged},
		Env:               in.ContainerEnv,
		Inputs:            inputs,
		Caches:            in.Caches,
		TaskCacheIdentity: in.taskCacheIdentity(),
		ScratchPaths:      in.Scratch,
		Sidecars:          in.Sidecars,
		Limits: runtime.ContainerLimits{
			CPU:                     in.LimitCPU,
			Memory:                  in.LimitMemory,
			CPURequest:              in.RequestCPU,
			MemoryRequest:           in.RequestMemory,
			EphemeralStorage:        in.LimitEphemeral,
			EphemeralStorageRequest: in.RequestEphemeral,
		},
	}
	if len(outputs) > 0 {
		spec.Outputs = outputs
	}
	return spec, nil
}

// workerContainerDraft carries the real worker into each container-kind draft.
func workerContainerDraft(in WorkerReady, handle, image string, kind db.ContainerType) (ContainerDraft, error) {
	if in.ProducerExecutor == nil {
		return ContainerDraft{}, fmt.Errorf("worker has no production execution transport")
	}
	return ContainerDraft{
		Namespace: in.Namespace, Worker: in.Worker, Clientset: in.Clientset,
		Ctx: in.Ctx, TeamID: in.TeamID, Handle: handle, ImageURL: image,
		Dir: "/workdir", ContainerType: kind, MountExecutor: in.ProducerExecutor,
	}, nil
}
