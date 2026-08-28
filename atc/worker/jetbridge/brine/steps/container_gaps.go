package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
)

// ContainerGapDefinitions closes the two places container_test.go was stronger
// than brine. Both are about the pod a container builds, and both were
// invisible because every existing pod scenario runs on a worker with NO
// storage backend — with none configured, stepVolume returns emptyDir for
// every container type and buildCleanupInitContainer returns nil, so neither
// mutation changes anything a scenario can see.

// draftContainerType defaults an unset draft to a task, which is what every
// scenario written before check containers existed assumes.
func draftContainerType(t db.ContainerType) db.ContainerType {
	if t == "" {
		return db.ContainerTypeTask
	}
	return t
}

func ContainerGapDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// A check container's working directory must be ephemeral even when
		// the worker keeps step data on the node. The same container handle
		// is reused for every check of a resource, so node-local storage
		// would carry one check's state into the next.
		brine.DefineMap[ClusterReady, ContainerDraft](
			"a check container {string} built from image {string}",
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
					Namespace:     in.Namespace,
					Worker:        in.Worker,
					Clientset:     in.Clientset,
					Ctx:           in.Ctx,
					Handle:        handle,
					ImageURL:      image,
					Dir:           "/workdir",
					TeamID:        in.TeamID,
					ContainerType: db.ContainerTypeCheck,
				}, nil
			},
		),

		// A container whose row already exists is REUSED, and a reused
		// container's pod has to clear the workspace the previous run left on
		// the node before anything else starts.
		// An input that carries a real artifact, rather than just a mount
		// path. The backend only emits a fetch init container for inputs it
		// can actually locate.
		Refine[ContainerDraft]("it takes an input at {string} produced by an earlier step",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.ArtifactInputs = append(in.ArtifactInputs, a.String(0))
				return in
			}),

		Refine[ContainerDraft]("the container has run before on this worker",
			func(in ContainerDraft, _ Args) ContainerDraft {
				in.RanBefore = true
				return in
			}),

		CheckThat[PodCreated]("the pod clears the workspace left by the previous run",
			func(in PodCreated) error {
				if !hasInitContainer(in, "cleanup-stale") {
					return fmt.Errorf(
						"the pod for %q has no cleanup init container, so the step starts on top of "+
							"whatever the previous run left in its node-local workspace — the "+
							"\"destination path already exists\" failure", in.Handle)
				}
				return nil
			}),

		// A step reads its inputs from the artifact daemon on its own node, so
		// the pod must not be scheduled anywhere that daemon is not running.
		// Without the requirement the scheduler is free to place it on a node
		// with no artifact cache, where the step cannot read its inputs.
		CheckThat[PodCreated]("the pod is only scheduled where the artifact cache is ready",
			func(in PodCreated) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				aff := in.Pod.Spec.Affinity
				if aff == nil || aff.NodeAffinity == nil ||
					aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
					return fmt.Errorf(
						"the pod for %q carries no node requirement, so the scheduler may place it "+
							"on a node with no artifact daemon — the step then cannot read its "+
							"inputs at all", in.Handle)
				}
				for _, term := range aff.NodeAffinity.
					RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
					for _, expr := range term.MatchExpressions {
						if expr.Key == "concourse.dev/artifact-cache" {
							return nil
						}
					}
				}
				return fmt.Errorf(
					"the pod for %q requires a node, but not one running the artifact cache", in.Handle)
			}),

		// Inputs are fetched into the workspace by init containers before the
		// step's own command starts. Without them the step runs against an
		// empty directory and fails on a file it was handed.
		CheckThat[PodCreated]("the pod fetches its inputs before the step starts",
			func(in PodCreated) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				for _, c := range in.Pod.Spec.InitContainers {
					if c.Name != "cleanup-stale" {
						return nil
					}
				}
				return fmt.Errorf(
					"the pod for %q has no init container to fetch its inputs, so the step starts "+
						"with an empty workspace and fails on a file it was given", in.Handle)
			}),

		CheckThat[PodCreated]("the pod does not clear the workspace",
			func(in PodCreated) error {
				if hasInitContainer(in, "cleanup-stale") {
					return fmt.Errorf(
						"the pod for %q clears its workspace, but nothing ran here before — a fresh "+
							"container has nothing to clean and the extra init container only costs "+
							"an image pull", in.Handle)
				}
				return nil
			}),
	}
}

func hasInitContainer(in PodCreated, name string) bool {
	if in.Pod == nil {
		return false
	}
	for _, c := range in.Pod.Spec.InitContainers {
		if c.Name == name {
			return true
		}
	}
	return false
}
