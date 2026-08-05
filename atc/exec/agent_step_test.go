package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/api/metrics/metricsfakes"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"github.com/onsi/gomega/gbytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentStep", func() {
	var (
		ctx    context.Context
		cancel func()

		stdoutBuf *gbytes.Buffer
		stderrBuf *gbytes.Buffer

		fakePool            *execfakes.FakePool
		fakeStreamer        *execfakes.FakeStreamer
		fakeDelegate        *execfakes.FakeTaskDelegate
		fakeDelegateFactory *execfakes.FakeTaskDelegateFactory
		fakeChecker         *budgetfakes.FakeChecker
		fakeMetricsStore    *metricsfakes.FakeStore

		agentPlan  atc.AgentPlan
		agentImage string

		state exec.RunState
		repo  *build.Repository

		step             exec.Step
		agentStepOptions []exec.AgentStepOption

		chosenWorker    *runtimetest.Worker
		chosenContainer *runtimetest.WorkerContainer

		containerMetadata = db.ContainerMetadata{
			WorkingDirectory: "some-artifact-root",
			Type:             db.ContainerTypeAgent,
			StepName:         "write-spec",
		}

		stepMetadata = exec.StepMetadata{
			TeamID:      123,
			BuildID:     1234,
			JobID:       12345,
			PipelineID:  555,
			ExternalURL: "http://foo.bar",
		}

		planID = atc.PlanID("42")

		expectedOwner = db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)

		agentProcessSpec = runtime.ProcessSpec{
			ID:   "agent",
			Path: "agent-runner",
			Dir:  "some-artifact-root",
			TTY: &runtime.TTYSpec{
				WindowSize: runtime.WindowSize{
					Columns: 500,
					Rows:    500,
				},
			},
		}
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		stepMetadata.WorkflowDefinitionID = nil
		stepMetadata.WorkflowRunID = nil
		containerMetadata.WorkingDirectory = "some-artifact-root"
		containerMetadata.Attempt = ""

		stdoutBuf = gbytes.NewBuffer()
		stderrBuf = gbytes.NewBuffer()

		fakeStreamer = new(execfakes.FakeStreamer)

		fakeDelegate = new(execfakes.FakeTaskDelegate)
		fakeDelegate.StdoutReturns(stdoutBuf)
		fakeDelegate.StderrReturns(stderrBuf)
		fakeDelegate.StartSpanReturns(ctx, tracing.NoopSpan)

		fakeDelegateFactory = new(execfakes.FakeTaskDelegateFactory)
		fakeDelegateFactory.TaskDelegateReturns(fakeDelegate)

		fakeChecker = new(budgetfakes.FakeChecker)
		fakeMetricsStore = new(metricsfakes.FakeStore)
		agentStepOptions = []exec.AgentStepOption{
			exec.WithAgentBudgetChecker(fakeChecker),
			exec.WithAgentMetricsStore(fakeMetricsStore),
			exec.WithAgentPlatformTokenSecret("agent-platform-credential"),
		}

		state = exec.NewRunState(noopStepper, vars.StaticVariables{"branch": "main"})
		repo = state.ArtifactRepository()

		agentPlan = atc.AgentPlan{
			Name:           "write-spec",
			Hermetic:       true,
			Prompt:         "do it",
			Model:          "m1",
			MaxTurns:       3,
			BudgetSliceUSD: 2.5,
			Outputs:        []string{"workspace"},
			Env: map[string]string{
				"BASE_REF": "main",
				// Capability endpoints reach the main container ONLY as
				// compiler-emitted <CAPABILITY>_MCP_URL plan env; the exec
				// never derives a URL from a sidecar's name.
				"PLATFORM_MCP_URL": "http://127.0.0.1:7781/mcp",
			},
			Sidecars: []atc.SidecarSource{
				{Config: &atc.SidecarConfig{Name: "platform", Image: "img:v1"}},
			},
		}
		agentImage = "registry.home/agent-runner:v1"

		chosenWorker = runtimetest.NewWorker("worker").
			WithContainer(
				expectedOwner,
				runtimetest.NewContainer().WithProcess(
					agentProcessSpec,
					runtimetest.ProcessStub{Attachable: true},
				),
				[]runtime.VolumeMount{
					{
						Volume:    runtimetest.NewVolume("workspace-volume"),
						MountPath: "some-artifact-root/workspace",
					},
					{
						Volume:    runtimetest.NewVolume("flight-volume"),
						MountPath: "some-artifact-root/flight",
					},
				},
			)
		chosenContainer = chosenWorker.Containers[0]

		fakePool = new(execfakes.FakePool)
		fakePool.FindOrSelectWorkerReturns(chosenWorker, nil)
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		step = exec.NewAgentStep(
			planID,
			agentPlan,
			atc.ContainerLimits{},
			atc.ContainerLimits{},
			stepMetadata,
			containerMetadata,
			fakePool,
			fakeStreamer,
			fakeDelegateFactory,
			0,
			agentImage,
			agentStepOptions...,
		)
	})

	It("errors clearly when no agent image is configured", func() {
		noImageStep := exec.NewAgentStep(
			planID,
			atc.AgentPlan{Name: "a", Prompt: "p"},
			atc.ContainerLimits{},
			atc.ContainerLimits{},
			stepMetadata,
			containerMetadata,
			fakePool,
			fakeStreamer,
			fakeDelegateFactory,
			0,
			"",
		)
		_, err := noImageStep.Run(ctx, state)
		Expect(err).To(MatchError(ContainSubstring("--agent-step-image")))
		Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
	})

	Context("with compiler-frozen skills", func() {
		var (
			skillVolume  *runtimetest.Volume
			materializer *skillMaterializingWorker
		)

		BeforeEach(func() {
			agentPlan.Skills = []string{"review"}
			agentPlan.SkillFiles = map[string]string{
				"skills/review/SKILL.md":      "instructions",
				"skills/review/refs/rules.md": "rules",
			}
			skillVolume = runtimetest.NewVolume("frozen-skills")
			materializer = &skillMaterializingWorker{Worker: chosenWorker, volume: skillVolume}
			fakePool.FindOrSelectWorkerReturns(materializer, nil)
		})

		It("mounts the private immutable tree at the logical skills path", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			spec := chosenContainer.Spec
			var skills *runtime.Input
			for index := range spec.Inputs {
				input := &spec.Inputs[index]
				if input.DestinationPath == "some-artifact-root/skills" {
					skills = input
				}
			}
			Expect(skills).ToNot(BeNil())
			Expect(skills.ReadOnly).To(BeTrue())
			Expect(skills.Private).To(BeFalse())
			Expect(skills.Artifact).To(Equal(skillVolume))
			Expect(materializer.calls).To(Equal(1))
			Expect(string(skillVolume.Content["review/SKILL.md"].Data)).To(Equal("instructions"))
			Expect(spec.Env).To(ContainElement("AGENT_SKILLS=review"))
			Expect(spec.Env).To(ContainElement("AGENT_SKILLS_DIR=some-artifact-root/skills"))
		})

		It("fails when the frozen tree cannot be streamed into the worker volume", func() {
			materializer.volume = failingSkillVolume{Volume: skillVolume, err: errors.New("stream in failed")}
			ok, err := step.Run(ctx, state)
			Expect(ok).To(BeFalse())
			Expect(err).To(MatchError(ContainSubstring("materialize compiled skills: stream in failed")))
			Expect(chosenContainer.Spec).To(BeNil())
		})

		It("refuses a logical skills input before selecting a worker", func() {
			collision := exec.NewAgentStep(
				planID,
				atc.AgentPlan{Name: "collision", Prompt: "do it", Hermetic: true, Skills: []string{"review"}, SkillFiles: agentPlan.SkillFiles, Inputs: []string{"skills"}},
				atc.ContainerLimits{}, atc.ContainerLimits{}, stepMetadata, containerMetadata,
				fakePool, fakeStreamer, fakeDelegateFactory, 0, agentImage, agentStepOptions...,
			)
			_, err := collision.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("collide with logical input")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
		})

		It("refuses a logical skills output before selecting a worker", func() {
			collision := exec.NewAgentStep(
				planID,
				atc.AgentPlan{Name: "collision", Prompt: "do it", Hermetic: true, Skills: []string{"review"}, SkillFiles: agentPlan.SkillFiles, Outputs: []string{"skills"}},
				atc.ContainerLimits{}, atc.ContainerLimits{}, stepMetadata, containerMetadata,
				fakePool, fakeStreamer, fakeDelegateFactory, 0, agentImage, agentStepOptions...,
			)
			_, err := collision.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("collide with logical input or output")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
		})
	})

	It("executes the admitted workflow runtime image across a web-node image rollout", func() {
		admitted := "registry.home/agent-runner@sha256:" + strings.Repeat("a", 64)
		mismatchStep := exec.NewAgentStep(
			planID,
			atc.AgentPlan{Name: "a", Prompt: "p", RuntimeImage: admitted},
			atc.ContainerLimits{},
			atc.ContainerLimits{},
			stepMetadata,
			containerMetadata,
			fakePool,
			fakeStreamer,
			fakeDelegateFactory,
			0,
			"registry.home/agent-runner@sha256:"+strings.Repeat("b", 64),
		)
		ok, err := mismatchStep.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.ImageSpec.ImageURL).To(Equal(admitted))
	})

	It("rejects a malformed admitted workflow runtime before selecting a worker", func() {
		invalidStep := exec.NewAgentStep(
			planID,
			atc.AgentPlan{Name: "a", Prompt: "p", RuntimeImage: "registry.home/agent-runner:v1"},
			atc.ContainerLimits{},
			atc.ContainerLimits{},
			stepMetadata,
			containerMetadata,
			fakePool,
			fakeStreamer,
			fakeDelegateFactory,
			0,
			agentImage,
		)
		_, err := invalidStep.Run(ctx, state)
		Expect(err).To(MatchError(ContainSubstring("invalid admitted runtime image")))
		Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
	})

	It("rejects an unverified human-review validation before selecting a worker", func() {
		agentPlan.RuntimeImage = "registry.example/agent@sha256:" + strings.Repeat("e", 64)
		agentPlan.Inputs = []string{"change", "validation"}
		agentPlan.Outputs = []string{"question"}
		agentPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
			"change":     {Type: snapshot.TypeRef("repository-change/v1")},
			"validation": {Type: snapshot.TypeRef("validation/v1")},
		}
		agentPlan.SnapshotOutputs = map[string]atc.SnapshotOutputConfig{
			"question": {Type: snapshot.TypeRef("question/v1")},
		}
		agentPlan.Validation = "validation"
		agentPlan.ReviewValidation = &atc.ReviewValidationRequirement{
			Candidate: "change", Validation: "validation",
		}

		testStep := exec.NewAgentStep(
			planID, agentPlan, atc.ContainerLimits{}, atc.ContainerLimits{}, stepMetadata,
			containerMetadata, fakePool, fakeStreamer, fakeDelegateFactory, 0, agentImage,
			agentStepOptions...,
		)
		ok, err := testStep.Run(ctx, state)
		Expect(ok).To(BeFalse())
		Expect(err).To(MatchError(ContainSubstring("authoritative validation plan is unavailable")))
		Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
	})

	It("rejects workflow-authored recovery transport", func() {
		agentPlan.Env["CONCOURSE_AGENT_RECOVERY"] = `{"mode":"native_resume"}`

		ok, err := step.Run(ctx, state)
		Expect(ok).To(BeFalse())
		Expect(err).To(MatchError("agent recovery transport is platform-owned"))
		Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
	})

	It("builds the container spec per the s8.1 env contract", func() {
		ok, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeTrue())

		Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
		_, owner, spec, workerSpec := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(owner).To(Equal(expectedOwner))
		Expect(workerSpec.TeamID).To(Equal(stepMetadata.TeamID))

		Expect(spec.ImageSpec.ImageURL).To(Equal("registry.home/agent-runner:v1"))
		Expect(spec.TeamID).To(Equal(stepMetadata.TeamID))
		Expect(spec.StepName).To(Equal("write-spec"))
		Expect(spec.Type).To(Equal(db.ContainerTypeAgent))
		Expect(spec.Hermetic).To(BeTrue())
		Expect(spec.Dir).To(Equal("some-artifact-root"))
		Expect(spec.Env).To(ContainElements(
			"AGENT_STEP_NAME=write-spec",
			"AGENT_PLAN_ID=42",
			"AGENT_PROMPT=do it",
			"AGENT_MODEL=m1",
			"AGENT_MAX_TURNS=3",
			"BASE_REF=main",
			"PLATFORM_MCP_URL=http://127.0.0.1:7781/mcp",
		))
		Expect(spec.Env).To(ContainElement(HavePrefix("AGENT_FLIGHT_DIR=")))
		// §8.1: every declared output's absolute in-pod path, so prompts
		// can target outputs deterministically instead of guessing
		// cwd-relative paths (dual-run finding: claude cd'd into the repo
		// input and wrote review.json there, so the output artifact
		// shipped empty). Name mangling: uppercase, dashes to underscores.
		Expect(spec.Env).To(ContainElement("AGENT_OUTPUT_WORKSPACE=some-artifact-root/workspace"))
		Expect(spec.Outputs).To(HaveKey("workspace"))
		Expect(spec.Outputs).To(HaveKey("flight"))
		Expect(spec.Sidecars).To(HaveLen(1))
	})

	Context("with source-format layers on the plan", func() {
		BeforeEach(func() {
			agentPlan.SystemPrompt = "be careful"
			agentPlan.Context = "## context/x.md\n\nbody\n"
			agentPlan.Skills = []string{"tdd", "extra"}
			agentPlan.SkillFiles = map[string]string{
				"skills/tdd/SKILL.md":   "tdd",
				"skills/extra/SKILL.md": "extra",
			}
			// Skills are compiler-owned rather than an authored DAG input.
			// Provide a worker volume so the executable test follows the same
			// frozen materialization path as a rendered workflow.
			fakePool.FindOrSelectWorkerReturns(&skillMaterializingWorker{
				Worker: chosenWorker, volume: runtimetest.NewVolume("source-format-skills"),
			}, nil)
		})

		It("exports them as §8.1 env", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).To(ContainElements(
				"AGENT_SYSTEM_PROMPT=be careful",
				"AGENT_CONTEXT=## context/x.md\n\nbody\n",
				"AGENT_SKILLS=tdd,extra",
			))
			Expect(spec.Env).To(ContainElement("AGENT_SKILLS_DIR=some-artifact-root/skills"))
		})
	})

	// --- review finding: agent env must be static-only (§2.8) ---
	// The exec used to thread env values through the build's var sources
	// (creds.NewString(...).Evaluate()), so `env: {TOKEN: ((vault:...))}`
	// resolved the secret and landed it as a LITERAL pod-spec env var —
	// readable by anyone with pod read and persisted in etcd, violating
	// §8.2's secretKeyRef-only rule. Values are copied verbatim now, and a
	// value still carrying a ((var)) reference fails the step closed.
	Context("when plan env carries an unresolved ((var)) reference", func() {
		BeforeEach(func() {
			// resolvable through the build's var sources (state carries
			// branch=main) — which is exactly what must NOT happen
			agentPlan.Env["BASE_REF"] = "((branch))"
		})

		It("fails closed instead of interpolating through the build's var sources", func() {
			ok, err := step.Run(ctx, state)
			Expect(ok).To(BeFalse())
			Expect(err).To(MatchError(ContainSubstring("agent env BASE_REF contains unresolved var reference ((branch))")))
			Expect(err).To(MatchError(ContainSubstring("static-only")))

			// no pod is ever requested, so no resolved value can leak
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
		})
	})

	Context("when plan env references a runtime var source (secret interpolation)", func() {
		BeforeEach(func() {
			agentPlan.Env["CLAUDE_CODE_OAUTH_TOKEN"] = "((vault:agent/token))"
		})

		It("refuses to run rather than land the resolved secret in the pod spec", func() {
			ok, err := step.Run(ctx, state)
			Expect(ok).To(BeFalse())
			Expect(err).To(MatchError(ContainSubstring("agent env CLAUDE_CODE_OAUTH_TOKEN contains unresolved var reference ((vault:")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
		})
	})

	It("copies static env values verbatim, never through var sources", func() {
		_, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())

		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Env).To(ContainElement("BASE_REF=main"))
	})

	// The pod only ever receives literal prompt text: prompt_file is a
	// workflow-source field the compiler inlines, and no AGENT_PROMPT_FILE
	// row exists any more (the runner never read one).
	Context("when the plan carries no prompt", func() {
		BeforeEach(func() {
			agentPlan.Prompt = ""
		})

		It("emits no AGENT_PROMPT row at all", func() {
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_PROMPT=")))
			Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_PROMPT_FILE=")))
		})
	})

	It("runs agent-runner as the well-known agent process", func() {
		ok, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeTrue())

		processes := chosenContainer.RunningProcesses()
		Expect(processes).To(HaveLen(1))
		Expect(processes[0].Spec.ID).To(Equal("agent"))
		Expect(processes[0].Spec.Path).To(Equal("agent-runner"))
		Expect(processes[0].Spec.Dir).To(Equal("some-artifact-root"))
		Expect(processes[0].Spec.TTY.WindowSize).To(Equal(runtime.WindowSize{
			Columns: 500,
			Rows:    500,
		}))

		Expect(fakeDelegate.InitializingCallCount()).To(Equal(1))
		Expect(fakeDelegate.StartingCallCount()).To(Equal(1))
		Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
		_, exitStatus := fakeDelegate.FinishedArgsForCall(0)
		Expect(exitStatus).To(Equal(exec.ExitStatus(0)))
	})

	It("registers declared outputs plus the implicit flight artifact", func() {
		_, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())

		_, _, found := repo.ArtifactFor(build.ArtifactName("workspace"))
		Expect(found).To(BeTrue())

		_, _, found = repo.ArtifactFor(build.ArtifactName("flight"))
		Expect(found).To(BeTrue())
	})

	Context("with typed snapshot declarations", func() {
		var (
			outputSealer *recordingOutputSealer
			inputVolume  *runtimetest.Volume
			inputRef     snapshot.SnapshotRef
			outputDigest snapshot.Digest
		)

		BeforeEach(func() {
			var err error
			outputDigest, err = snapshot.ParseDigest("sha256:" + strings.Repeat("c", 64))
			Expect(err).NotTo(HaveOccurred())
			stepMetadata.TeamName = "main"
			stepMetadata.SnapshotCreatedBy = "concourse"
			workflowDefinitionID := 88
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			stepMetadata.WorkflowDefinitionID = &workflowDefinitionID
			stepMetadata.WorkflowRunID = &workflowRunID
			containerMetadata.Attempt = "3"
			inputDigest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("d", 64))
			Expect(err).NotTo(HaveOccurred())
			inputRef = snapshot.SnapshotRef{ID: 82, Type: snapshot.TypeRef("repository/v1"), Digest: inputDigest}
			inputVolume = runtimetest.NewVolume("typed-agent-input")
			Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
				"repository": {Artifact: inputVolume, FromCache: true, Snapshot: &inputRef},
			})).To(Succeed())
			agentPlan.Inputs = []string{"repository"}
			agentPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
				"repository": {Type: snapshot.TypeRef("repository/v1")},
			}
			agentPlan.SnapshotOutputs = map[string]atc.SnapshotOutputConfig{
				"workspace": {
					Type: snapshot.TypeRef("repository-change/v1"), Retention: snapshot.RetentionClassWorkflow,
					WorkflowPort: "change", WorkflowDefinitionID: 88, WorkflowRunID: "9007199254740993",
				},
			}
			outputSealer = &recordingOutputSealer{result: map[string]snapshot.SealedOutput{
				"workspace": {
					Port:     snapshot.Port{Name: "workspace", Type: snapshot.TypeRef("repository-change/v1")},
					Snapshot: snapshot.SnapshotRef{ID: 92, Type: snapshot.TypeRef("repository-change/v1"), Digest: outputDigest},
				},
			}}
			snapshotMetadata, snapshotContent := snapshotStoresForSealedOutputs(outputSealer.result, inputRef)
			agentStepOptions = append(agentStepOptions, exec.WithAgentOutputSealer(outputSealer))
			agentStepOptions = append(agentStepOptions, exec.WithAgentSnapshotStores(snapshotMetadata, snapshotContent))
		})

		It("seals and publishes the complete typed output while keeping flight legacy", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(outputSealer.calls).To(HaveLen(1))
			request := outputSealer.calls[0]
			Expect(request.BuildID).To(Equal(1234))
			Expect(request.TeamID).To(Equal(123))
			Expect(request.TeamName).To(Equal("main"))
			Expect(request.CreatedBy).To(Equal("concourse"))
			Expect(request.PlanID).To(Equal("42"))
			Expect(request.Attempt).To(Equal("3"))
			Expect(request.StepKind).To(Equal("agent"))
			Expect(request.StepName).To(Equal("write-spec"))
			Expect(request.InputOrder).To(Equal([]string{"repository"}))
			Expect(request.Inputs).To(Equal(map[string]snapshot.SnapshotRef{"repository": inputRef}))
			Expect(request.OutputDeclarations).To(Equal([]snapshot.Port{{
				Name: "workspace", Type: snapshot.TypeRef("repository-change/v1"),
			}}))
			Expect(request.Outputs).To(HaveLen(1))
			Expect(request.Outputs[0].ClientKey).To(Equal("workspace"))
			Expect(request.Outputs[0].WorkflowPort).To(Equal("change"))
			Expect(*request.WorkflowDefinitionID).To(Equal(88))
			Expect(request.WorkflowRunID.String()).To(Equal("9007199254740993"))
			Expect(chosenContainer.Spec.Inputs).To(ConsistOf(runtime.Input{
				Artifact: inputVolume, DestinationPath: "some-artifact-root/repository", FromCache: true,
			}))
			Expect(chosenContainer.Spec.Env).To(ContainElements(
				"AGENT_INPUT_REPOSITORY_SNAPSHOT_TYPE=repository/v1",
				"AGENT_INPUT_REPOSITORY_SNAPSHOT_DIGEST="+inputRef.Digest.String(),
				"AGENT_OUTPUT_WORKSPACE_RECORD_TYPE=repository-change/v1",
				HavePrefix("AGENT_OUTPUT_WORKSPACE_RECORD_SCHEMA=sha256:"),
			))

			entry, found := repo.ArtifactEntryFor("workspace")
			Expect(found).To(BeTrue())
			Expect(entry.Artifact).To(BeAssignableToTypeOf(&runtime.SnapshotArtifact{}))
			Expect(entry.Snapshot).NotTo(BeNil())
			Expect(*entry.Snapshot).To(Equal(outputSealer.result["workspace"].Snapshot))
			flight, found := repo.ArtifactEntryFor("flight")
			Expect(found).To(BeTrue())
			Expect(flight.Snapshot).To(BeNil())
		})

		Context("with the managed output builder", func() {
			BeforeEach(func() {
				agentPlan.RuntimeImage = "registry.example/agent@sha256:" + strings.Repeat("a", 64)
				containerMetadata.WorkingDirectory = "/work"
				chosenContainer.ProcessDefs[0].Spec.Dir = "/work"
				for index := range chosenContainer.Mounts {
					chosenContainer.Mounts[index].MountPath = strings.Replace(
						chosenContainer.Mounts[index].MountPath,
						"some-artifact-root",
						"/work",
						1,
					)
				}
			})

			It("keeps sealed-record authority in the main env when the managed output builder is injected", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				Expect(chosenContainer.Spec.ManagedOutputBuilder).NotTo(BeNil())
				Expect(chosenContainer.Spec.Env).To(ContainElements(
					"CONCOURSE_OUTPUT_BUILDER_MCP=1",
					"AGENT_INPUT_REPOSITORY_SNAPSHOT_TYPE=repository/v1",
					"AGENT_INPUT_REPOSITORY_SNAPSHOT_DIGEST="+inputRef.Digest.String(),
					"AGENT_OUTPUT_WORKSPACE_RECORD_TYPE=repository-change/v1",
					HavePrefix("AGENT_OUTPUT_WORKSPACE_RECORD_SCHEMA=sha256:"),
				))
			})
		})

		It("captures full-tree exposure lineage at mount time for every typed input", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(outputSealer.calls).To(HaveLen(1))
			Expect(outputSealer.calls[0].InputExposures).To(Equal(map[string]snapshot.InputExposure{
				"repository": snapshot.FullTreeExposure("some-artifact-root/repository", inputRef.Digest),
			}))
			Expect(outputSealer.calls[0].InputExposures["repository"].MountPath).To(
				Equal(chosenContainer.Spec.Inputs[0].DestinationPath),
				"exposure lineage must record the path the input was actually mounted at",
			)
		})

		Context("with node artifact mappings", func() {
			BeforeEach(func() {
				agentPlan.InputMapping = map[string]string{"repository": "selected-repository"}
				agentPlan.OutputMapping = map[string]string{"workspace": "published-workspace"}
				sealed := outputSealer.result["workspace"]
				delete(outputSealer.result, "workspace")
				outputSealer.result["published-workspace"] = sealed
				Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
					"selected-repository": {Artifact: inputVolume, FromCache: true, Snapshot: &inputRef},
				})).To(Succeed())
			})

			It("uses physical repository and sealer keys while retaining logical container paths", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				Expect(chosenContainer.Spec.Inputs).To(ConsistOf(runtime.Input{
					Artifact: inputVolume, DestinationPath: "some-artifact-root/repository", FromCache: true,
				}))
				Expect(outputSealer.calls).To(HaveLen(1))
				Expect(outputSealer.calls[0].Inputs).To(Equal(map[string]snapshot.SnapshotRef{"repository": inputRef}))
				Expect(outputSealer.calls[0].Outputs[0].ClientKey).To(Equal("published-workspace"))
				_, found := repo.ArtifactEntryFor("published-workspace")
				Expect(found).To(BeTrue())
				_, found = repo.ArtifactEntryFor("workspace")
				Expect(found).To(BeFalse())
			})
		})

		Context("when the authenticated workflow producer has only an internal output", func() {
			BeforeEach(func() {
				declaration := agentPlan.SnapshotOutputs["workspace"]
				declaration.Retention = ""
				declaration.WorkflowPort = ""
				declaration.WorkflowDefinitionID = 0
				declaration.WorkflowRunID = ""
				agentPlan.SnapshotOutputs["workspace"] = declaration
			})

			It("passes the exact authenticated workflow association to the seal request", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				Expect(outputSealer.calls).To(HaveLen(1))
				request := outputSealer.calls[0]
				Expect(request.WorkflowDefinitionID).NotTo(BeNil())
				Expect(*request.WorkflowDefinitionID).To(Equal(88))
				Expect(request.WorkflowRunID).NotTo(BeNil())
				Expect(*request.WorkflowRunID).To(Equal(snapshot.WorkflowRunID(9007199254740993)))
				Expect(request.Outputs).To(HaveLen(1))
				Expect(request.Outputs[0].Retention).NotTo(Equal(snapshot.RetentionClassWorkflow))
				Expect(request.Outputs[0].WorkflowPort).To(BeEmpty())
			})
		})

		Context("when an ordinary build claims a workflow-retained output", func() {
			BeforeEach(func() {
				stepMetadata.WorkflowDefinitionID = nil
				stepMetadata.WorkflowRunID = nil
			})

			It("rejects the unauthenticated association before worker selection", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring("not associated with a workflow run")))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
				Expect(outputSealer.calls).To(BeEmpty())
			})
		})

		Context("when sealing fails", func() {
			BeforeEach(func() { outputSealer.err = errors.New("semantic validation failed") })

			It("ingests flight before returning the seal error and exposes no typed entry", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring("semantic validation failed")))
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				entry, found := repo.ArtifactEntryFor("workspace")
				Expect(found).To(BeFalse())
				Expect(entry.Snapshot).To(BeNil())
			})
		})

		Context("when the process exits nonzero", func() {
			BeforeEach(func() { chosenContainer.ProcessDefs[0].Stub.ExitStatus = 2 })

			It("ingests flight but does not seal or publish typed outputs", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeFalse())
				Expect(outputSealer.calls).To(BeEmpty())
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				_, found := repo.ArtifactEntryFor("workspace")
				Expect(found).To(BeFalse())
			})
		})

		Context("when the process wait fails", func() {
			BeforeEach(func() { chosenContainer.ProcessDefs[0].Stub.Err = "worker connection dropped" })

			It("ingests flight but does not seal or publish typed outputs", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError("worker connection dropped"))
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				Expect(outputSealer.calls).To(BeEmpty())
				_, found := repo.ArtifactEntryFor("workspace")
				Expect(found).To(BeFalse())
			})
		})

		Context("when the required typed output mount is missing", func() {
			BeforeEach(func() {
				chosenContainer.Mounts = chosenContainer.Mounts[1:]
			})

			It("ingests flight and rejects the batch before sealing", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring("required typed output")))
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				Expect(outputSealer.calls).To(BeEmpty())
				_, found := repo.ArtifactEntryFor("workspace")
				Expect(found).To(BeFalse())
			})
		})

		Context("when the typed output has duplicate mounts", func() {
			BeforeEach(func() {
				chosenContainer.Mounts = append(chosenContainer.Mounts, runtime.VolumeMount{
					Volume: runtimetest.NewVolume("duplicate-workspace"), MountPath: "some-artifact-root/workspace/",
				})
			})

			It("ingests flight and rejects the batch before sealing", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring("duplicate mounts")))
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				Expect(outputSealer.calls).To(BeEmpty())
				_, found := repo.ArtifactEntryFor("workspace")
				Expect(found).To(BeFalse())
			})
		})

		Context("when one of two required typed output mounts is missing", func() {
			BeforeEach(func() {
				agentPlan.Outputs = append(agentPlan.Outputs, "review")
				agentPlan.SnapshotOutputs["review"] = atc.SnapshotOutputConfig{Type: snapshot.TypeRef("review/v1")}
			})

			It("does not publish the valid first output", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring("required typed output \"review\"")))
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				Expect(outputSealer.calls).To(BeEmpty())
				_, firstFound := repo.ArtifactEntryFor("workspace")
				_, secondFound := repo.ArtifactEntryFor("review")
				Expect(firstFound).To(BeFalse())
				Expect(secondFound).To(BeFalse())
			})
		})

		Context("when an optional typed output mount exists but has no produced marker", func() {
			BeforeEach(func() {
				declaration := agentPlan.SnapshotOutputs["workspace"]
				declaration.Optional = true
				agentPlan.SnapshotOutputs["workspace"] = declaration
				chosenContainer.Mounts = append(chosenContainer.Mounts, runtime.VolumeMount{
					Volume:    runtimetest.NewVolume("typed-output-markers"),
					MountPath: "/tmp/.jetbridge/typed-output-markers/v1",
				})
				outputSealer.result = map[string]snapshot.SealedOutput{}
			})

			It("treats the always-mounted empty output as absent while preserving flight behavior", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				Expect(chosenContainer.Spec.Outputs).To(HaveKeyWithValue(
					"__jetbridge_typed_output_markers_v1",
					"/tmp/.jetbridge/typed-output-markers/v1/",
				))
				Expect(chosenContainer.Spec.Env).To(ContainElement(
					`JETBRIDGE_OPTIONAL_OUTPUT_MARKERS={"workspace":"/tmp/.jetbridge/typed-output-markers/v1/d29ya3NwYWNl"}`,
				))
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				Expect(outputSealer.calls).To(HaveLen(1))
				Expect(outputSealer.calls[0].OutputDeclarations).To(Equal([]snapshot.Port{{
					Name: "workspace", Type: snapshot.TypeRef("repository-change/v1"), Optional: true,
				}}))
				Expect(outputSealer.calls[0].Outputs).To(BeEmpty())
				_, found := repo.ArtifactEntryFor("workspace")
				Expect(found).To(BeFalse())
			})
		})

		Context("when atomic repository publication rejects the batch", func() {
			var existing *runtimetest.Volume

			BeforeEach(func() {
				state = state.NewArtifactScope()
				repo = state.ArtifactRepository()
				existing = runtimetest.NewVolume("existing-workspace")
				repo.RegisterArtifact("workspace", existing, false)
			})

			It("ingests flight and propagates the publication error without replacement", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring(build.ErrArtifactAlreadyRegistered.Error())))
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				Expect(outputSealer.calls).To(HaveLen(1))
				entry, found := repo.ArtifactEntryFor("workspace")
				Expect(found).To(BeTrue())
				Expect(entry.Artifact).To(Equal(existing))
				Expect(entry.Snapshot).To(BeNil())
			})
		})
	})

	Context("with typed snapshot inputs", func() {
		var inputDigest snapshot.Digest

		BeforeEach(func() {
			var err error
			inputDigest, err = snapshot.ParseDigest("sha256:" + strings.Repeat("e", 64))
			Expect(err).NotTo(HaveOccurred())
			agentPlan.Inputs = []string{"repository"}
			agentPlan.Outputs = nil
			agentPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
				"repository": {Type: snapshot.TypeRef("repository/v1")},
			}
			agentStepOptions = append(agentStepOptions, exec.WithAgentOutputSealer(&recordingOutputSealer{}))
		})

		Context("when an optional input is absent", func() {
			BeforeEach(func() {
				declaration := agentPlan.SnapshotInputs["repository"]
				declaration.Optional = true
				agentPlan.SnapshotInputs["repository"] = declaration
			})

			It("runs without mounting or reporting it missing", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
				Expect(chosenContainer.Spec.Inputs).To(BeEmpty())
			})
		})

		Context("when a required input is absent", func() {
			It("returns MissingInputsError before worker selection", func() {
				_, err := step.Run(ctx, state)
				Expect(err).To(BeAssignableToTypeOf(exec.MissingInputsError{}))
				Expect(err.(exec.MissingInputsError).Inputs).To(Equal([]string{"repository"}))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("when the input snapshot type differs", func() {
			BeforeEach(func() {
				ref := snapshot.SnapshotRef{ID: 42, Type: snapshot.TypeRef("review/v1"), Digest: inputDigest}
				Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
					"repository": {Artifact: runtimetest.NewVolume("input"), Snapshot: &ref},
				})).To(Succeed())
			})

			It("fails before worker selection", func() {
				_, err := step.Run(ctx, state)
				Expect(err).To(MatchError(ContainSubstring("does not match declared type")))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})
	})

	Context("when an ordinary agent input resolves to a typed snapshot", func() {
		BeforeEach(func() {
			digest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("d", 64))
			Expect(err).NotTo(HaveOccurred())
			ref := snapshot.SnapshotRef{ID: 41, Type: snapshot.TypeRef("repository/v1"), Digest: digest}
			agentPlan.Inputs = []string{"repository"}
			Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
				"repository": {Artifact: runtimetest.NewVolume("repository"), Snapshot: &ref},
			})).To(Succeed())
		})

		It("fails closed before worker selection", func() {
			_, err := step.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring(`agent input "repository" is a typed snapshot but has no input_types declaration`)))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	Context("when a typed agent declares an extra untyped output", func() {
		BeforeEach(func() {
			agentPlan.Outputs = []string{"workspace", "hidden"}
			agentPlan.SnapshotOutputs = map[string]atc.SnapshotOutputConfig{
				"workspace": {Type: snapshot.TypeRef("repository-change/v1")},
			}
			agentStepOptions = append(agentStepOptions, exec.WithAgentOutputSealer(&recordingOutputSealer{}))
		})

		It("fails exact output coverage before worker selection", func() {
			_, err := step.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("every declared agent output must be typed")))
			Expect(err).To(MatchError(ContainSubstring("hidden")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	Context("with a typed input carrying no snapshot ref", func() {
		BeforeEach(func() {
			stepMetadata.TeamName = "main"
			stepMetadata.SnapshotCreatedBy = "concourse"
			containerMetadata.Attempt = "1"
			agentPlan.Inputs = []string{"repository"}
			agentPlan.Outputs = nil
			agentPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
				"repository": {Type: snapshot.TypeRef("repository/v1")},
			}
			repo.RegisterArtifact("repository", runtimetest.NewVolume("repository"), false)
			agentStepOptions = append(agentStepOptions, exec.WithAgentOutputSealer(&recordingOutputSealer{}))
		})

		It("fails before worker selection", func() {
			_, err := step.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("snapshot ref")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	Context("with typed declarations but no sealer", func() {
		BeforeEach(func() {
			agentPlan.SnapshotOutputs = map[string]atc.SnapshotOutputConfig{
				"workspace": {Type: snapshot.TypeRef("repository-change/v1")},
			}
		})

		It("fails closed before worker selection", func() {
			_, err := step.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("output sealer")))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
		})
	})

	// The plan's declared slice is a hard cap the renderer resolved; the exec
	// neither re-resolves nor re-reserves it (the binder's durable reservation
	// is the single admission authority).
	It("passes the plan's declared budget slice through as the step cap", func() {
		ok, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeTrue())

		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Env).To(ContainElement("AGENT_BUDGET_SLICE_USD=2.5"))
	})

	It("preserves sub-cent precision so a positive hard cap never becomes uncapped", func() {
		agentPlan.BudgetSliceUSD = 0.004321
		step = exec.NewAgentStep(
			planID, agentPlan, atc.ContainerLimits{}, atc.ContainerLimits{},
			stepMetadata, containerMetadata, fakePool, fakeStreamer, fakeDelegateFactory,
			0, agentImage, agentStepOptions...,
		)

		_, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Env).To(ContainElement("AGENT_BUDGET_SLICE_USD=0.004321"))
	})

	Context("when the plan declares no budget slice (budget_slice_usd 0)", func() {
		BeforeEach(func() {
			agentPlan.BudgetSliceUSD = 0
		})

		It("emits no slice env: 0 means uncapped", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_BUDGET_SLICE_USD=")))
		})
	})

	Context("when a declared input is missing from the artifact repository", func() {
		BeforeEach(func() {
			agentPlan.Inputs = []string{"repo"}
		})

		It("fails with MissingInputsError", func() {
			_, err := step.Run(ctx, state)
			Expect(err).To(BeAssignableToTypeOf(exec.MissingInputsError{}))
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
		})
	})

	Describe("runtime seams (F15/F21)", func() {
		Context("with auxiliary and gateway sidecars", func() {
			BeforeEach(func() {
				agentPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{Name: "auxiliary", Image: "img:v1"}},
					{Config: &atc.SidecarConfig{Name: "gateway", Image: "img:v2"}},
				}
			})

			It("keeps hermetic sidecars local and never grants credentials by role name", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SidecarEnv["auxiliary"]).To(ConsistOf(
					"BUILD_ID=" + strconv.Itoa(stepMetadata.BuildID),
				))
				Expect(spec.SidecarEnv["auxiliary"]).ToNot(ContainElement(HavePrefix("ATC_EXTERNAL_URL=")))
			})

			// §8.1 pins CLAUDE_CODE_OAUTH_TOKEN to the MAIN container from the
			// platform secret — the only token path into an agent pod (env is
			// static-only and never carries credentials). A workflow-authored
			// sidecar name is never a credential grant, however suggestive.
			It("wires the main container's model credential and no sidecar's", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).To(HaveKeyWithValue(
					"CLAUDE_CODE_OAUTH_TOKEN",
					vars.SecretRef{Name: "agent-platform-credential", Key: "anthropic-token"}))
				// secretKeyRef-only — the token must never appear as a literal.
				Expect(spec.Env).ToNot(ContainElement(HavePrefix("CLAUDE_CODE_OAUTH_TOKEN=")))
			})
		})

		Context("model credential delivery", func() {
			// The runner picks the claude CLI credential variable from the
			// kind, so it travels beside the token — but as an OPTIONAL ref:
			// an operator-created secret has only "anthropic-token", and a
			// required ref would stop every agent pod from starting.
			It("injects the token kind as an optional secretKeyRef", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).To(HaveKeyWithValue(
					"AGENT_MODEL_TOKEN_KIND",
					vars.SecretRef{Name: "agent-platform-credential", Key: "kind", Optional: true}))
				Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_MODEL_TOKEN_KIND=")))
			})

			It("honors an operator-configured secret name", func() {
				operatorStep := exec.NewAgentStep(
					planID, agentPlan, atc.ContainerLimits{}, atc.ContainerLimits{},
					stepMetadata, containerMetadata, fakePool, fakeStreamer, fakeDelegateFactory,
					0, agentImage,
					exec.WithAgentBudgetChecker(fakeChecker),
					exec.WithAgentMetricsStore(fakeMetricsStore),
					exec.WithAgentPlatformTokenSecret("operator-managed-token"),
				)
				_, err := operatorStep.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).To(HaveKeyWithValue(
					"CLAUDE_CODE_OAUTH_TOKEN",
					vars.SecretRef{Name: "operator-managed-token", Key: "anthropic-token"}))
			})

			// An operator can clear --agent-platform-token-secret. The step
			// must not invent a secret name; the pod simply gets no token.
			It("emits no model-credential refs when no platform secret is configured", func() {
				unconfiguredStep := exec.NewAgentStep(
					planID, agentPlan, atc.ContainerLimits{}, atc.ContainerLimits{},
					stepMetadata, containerMetadata, fakePool, fakeStreamer, fakeDelegateFactory,
					0, agentImage,
					exec.WithAgentBudgetChecker(fakeChecker),
					exec.WithAgentMetricsStore(fakeMetricsStore),
				)
				_, err := unconfiguredStep.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).To(BeEmpty())
			})

			It("keeps the principal token off the main container", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).ToNot(HaveKey("AGENT_PRINCIPAL_TOKEN"))
				Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_PRINCIPAL_TOKEN=")))
			})
		})

		// Sidecars get build correlation only: no run/ticket identity travels
		// into a pod any more, because none of it exists in plan env.
		Context("with a non-hermetic plan", func() {
			BeforeEach(func() {
				agentPlan.Hermetic = false
			})

			It("adds only the external URL to the sidecar correlation rows", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SidecarEnv["platform"]).To(ConsistOf(
					"BUILD_ID="+strconv.Itoa(stepMetadata.BuildID),
					"ATC_EXTERNAL_URL="+stepMetadata.ExternalURL,
				))
			})
		})

		It("sets each unset MCP sidecar WorkingDir to the workspace mount (F21)", func() {
			// plan.Outputs includes "workspace"; sidecar config leaves WorkingDir unset
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Sidecars[0].WorkingDir).To(HaveSuffix("/workspace"))
		})

		Context("without a workspace artifact", func() {
			BeforeEach(func() {
				agentPlan.Outputs = nil
			})

			It("leaves sidecar WorkingDir unset (jetbridge default)", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.Sidecars[0].WorkingDir).To(BeEmpty())
			})
		})

		Context("when the sidecar declares its own WorkingDir", func() {
			BeforeEach(func() {
				agentPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{Name: "platform", Image: "img:v1", WorkingDir: "/custom"}},
				}
			})

			It("preserves the configured WorkingDir", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.Sidecars[0].WorkingDir).To(Equal("/custom"))
			})
		})

		Context("with a custom compiled capability sidecar", func() {
			BeforeEach(func() {
				agentPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{Name: "repository-tools", Image: "img:v1"}},
				}
				agentPlan.Env["REPOSITORY_TOOLS_MCP_URL"] = "http://127.0.0.1:7790/mcp"
			})

			It("receives local build context and the workspace CWD without credentials", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SidecarEnv["repository-tools"]).To(ConsistOf(
					"BUILD_ID=" + strconv.Itoa(stepMetadata.BuildID),
				))
				Expect(spec.Sidecars[0].WorkingDir).To(HaveSuffix("/workspace"))
			})
		})
	})

	Context("flight-recorder ingestion", func() {
		const (
			flightEventStepStart = iota
			flightEventToolCall
			flightEventMCPReady
			flightEventCostRecord
			flightEventStepEnd
		)

		var resultsJSON string
		var eventLines []string
		var streamFullFlight func(context.Context, runtime.Artifact, string) (io.ReadCloser, error)
		var flightRunID snapshot.WorkflowRunID

		BeforeEach(func() {
			// The metric's execution identity: the durable workflow run (carried
			// on step.metadata, server-authenticated) and the planned function.
			flightRunID = snapshot.WorkflowRunID(4242)
			// A durable workflow run always carries its definition too; set both
			// so the authenticated workflow association is complete (task_step.go
			// rejects a run ID without a matching definition ID).
			flightDefinitionID := 4242
			stepMetadata.WorkflowDefinitionID = &flightDefinitionID
			stepMetadata.WorkflowRunID = &flightRunID
			agentPlan.FunctionID = "review"

			resultsJSON = `{"schema_version":"1.0","status":"pass","confidence":1,"summary":"done","artifacts":[]}`
			eventLines = []string{
				`{"ts":"2026-07-10T12:00:00Z","event":"step.start","data":{"step_name":"write-spec","build_id":1,"plan_id":"p"}}`,
				`{"ts":"2026-07-10T12:00:01Z","event":"tool.call","data":{"tool":"run_tests"}}`,
				`{"ts":"2026-07-10T12:00:01Z","event":"mcp.ready","data":{"server":"output-builder","protocol_version":"2024-11-05","tools":["describe_output","validate_output","write_output"]}}`,
				`{"ts":"2026-07-10T12:00:02Z","event":"cost.record","data":{"source":"agent_step","provider":"anthropic","model":"m1","input_tokens":100,"output_tokens":50,"cache_read_tokens":1,"cache_creation_tokens":2,"turns":9,"cost_usd":0.42}}`,
				`{"ts":"2026-07-10T12:01:01Z","event":"step.end","data":{"step_name":"write-spec","status":"ok","summary":"done","wall_time_seconds":61,"cost_usd":0.42,"turns":9}}`,
			}

			// The stub honors ctx cancellation, matching the production
			// Streamer (which threads ctx into the HTTP request): the
			// detached ingestCtx keeps reads live even when the step's
			// timeout-scoped ctx has expired (finding F4), and the pre-fix
			// code (threading the expired ctx) fails these specs.
			streamFullFlight = func(ctx context.Context, artifact runtime.Artifact, path string) (io.ReadCloser, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				switch path {
				case "results.json":
					return io.NopCloser(strings.NewReader(resultsJSON)), nil
				case "events.ndjson":
					return io.NopCloser(strings.NewReader(strings.Join(eventLines, "\n") + "\n")), nil
				default:
					return nil, fmt.Errorf("no fixture for %q", path)
				}
			}
			fakeStreamer.StreamFileStub = streamFullFlight

			// first ingestion of a (build_id, plan_id) is always a fresh insert
			fakeMetricsStore.UpsertReturningInsertedReturns(true, nil, nil)
		})

		It("upserts a RunMetrics row before Run returns", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
			rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
			Expect(rm.Status).To(Equal("ok"))
			Expect(rm.BuildID).To(Equal(stepMetadata.BuildID))
			Expect(rm.PlanID).To(Equal(string(planID)))
			// the durable workflow run and function are the metric's identity;
			// the ticket/run stay on the cost ledger (asserted below). The
			// metric field is the schema-local type converted from metadata.
			Expect(rm.WorkflowRunID).ToNot(BeNil())
			Expect(*rm.WorkflowRunID).To(Equal(schema.WorkflowRunID(int64(flightRunID))))
			Expect(rm.FunctionID).To(Equal("review"))
			Expect(rm.Usage.InputTokens).To(Equal(int64(100)))
			Expect(rm.Usage.OutputTokens).To(Equal(int64(50)))
			Expect(rm.Usage.CacheReadInputTokens).To(Equal(int64(1)))
			Expect(rm.Usage.CacheCreationInputTokens).To(Equal(int64(2)))
			Expect(rm.Model).To(Equal("m1"))
			Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
			Expect(rm.Turns).To(Equal(9))
			Expect(rm.WallTimeSeconds).To(Equal(61))
			Expect(rm.StepName).To(Equal("write-spec"))
			Expect(rm.Summary).To(Equal("done"))
			Expect(rm.EventCounts).To(HaveKeyWithValue("tool.call", 1))
			Expect(rm.EventCounts).To(HaveKeyWithValue("mcp.ready", 1))
			Expect(rm.EventCounts).To(HaveKeyWithValue("step.end", 1))
			Expect(rm.EventsArtifact).ToNot(BeEmpty())
			Expect(rm.Results).To(MatchJSON(resultsJSON))
		})

		Context("when events.ndjson has no step.end", func() {
			BeforeEach(func() {
				// events fixture truncated after tool.call
				eventLines = eventLines[:flightEventToolCall+1]
			})

			It("records an error row", func() {
				step.Run(ctx, state)
				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.Status).To(Equal("error"))
				Expect(rm.EventCounts).To(HaveKeyWithValue("tool.call", 1)) // partial counts kept
			})
		})

		// review finding 2026-07-16: one oversized NDJSON line must not poison
		// the rest of the stream — the cost.record and step.end that FOLLOW it
		// are ledger- and status-relevant. The reader skips the giant line and
		// resyncs; ingestion keeps reading.
		Context("when events.ndjson has an oversized line mid-stream", func() {
			BeforeEach(func() {
				oversized := `{"ts":"2026-07-10T12:00:01Z","event":"tool.call","data":{"blob":"` +
					strings.Repeat("x", 5<<20) + `"}}`
				eventLines = []string{
					eventLines[flightEventStepStart], // step.start
					oversized,
					eventLines[flightEventMCPReady],   // mcp.ready
					eventLines[flightEventCostRecord], // cost.record
					eventLines[flightEventStepEnd],    // step.end
				}
			})

			It("still ingests the cost.record and step.end after the giant line", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue())

				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.Status).To(Equal("ok")) // step.end WAS seen
				Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
				Expect(rm.EventCounts).To(HaveKeyWithValue("step.end", 1))
			})
		})

		// PARK-V2 is gone: the "crashed agent → error" rule has no exceptions.
		// A stream that ends without step.end is an error however cheerful the
		// results.json is, and 'parked' is no longer a status the schema
		// accepts — it decodes as an invalid results document.
		Context("when the stream has no step.end", func() {
			BeforeEach(func() {
				resultsJSON = `{"schema_version":"1.0","status":"pass","confidence":1,"summary":"all good","artifacts":[]}`
				eventLines = eventLines[:flightEventStepEnd] // step.start, tool.call, mcp.ready, cost.record — no step.end
			})

			It("records error, whatever results.json claimed", func() {
				step.Run(ctx, state)
				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.Status).To(Equal("error"))
				Expect(rm.Summary).To(Equal("all good"))
			})
		})

		Context("when the flight files are missing entirely", func() {
			BeforeEach(func() {
				// fakeStreamer returns an error for both files ⇒ no flight data
				// was read ⇒ DEGRADED ingestion goes through InsertIfAbsent
				// (F24), never the clobbering upsert. First run, no existing
				// row ⇒ inserted.
				fakeStreamer.StreamFileStub = func(context.Context, runtime.Artifact, string) (io.ReadCloser, error) {
					return nil, errors.New("no locator for volume")
				}
				fakeMetricsStore.InsertIfAbsentReturns(true, nil)
			})

			It("records an incomplete row via InsertIfAbsent", func() {
				// L-1 (#41): no flight file read at all is a missing RECORDING,
				// not a failed step — status=incomplete so DeriveOutcome fuses
				// it to amber "unrecorded" on a succeeded build (never red). It
				// still degrades through InsertIfAbsent (F24), never the upsert.
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue()) // exit status still drives step success
				Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(BeZero())
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				rm := fakeMetricsStore.InsertIfAbsentArgsForCall(0)
				Expect(rm.Status).To(Equal(schema.RunStatusIncomplete))
				Expect(rm.Summary).To(ContainSubstring("no flight output"))
			})

			// --- review finding (2026-07-12): a StreamFile failure silently
			// degraded ingestion to a status=error row with zero diagnostics.
			// Every read failure must be logged, naming the file, like every
			// other failure in ingestFlightRecorder. ---
			It("logs a diagnostic naming each unreadable flight file", func() {
				logger := lagertest.NewTestLogger("agent-step")
				// The step logs through the context StartSpan returns, so thread
				// the capturing logger there (the base ctx is otherwise swapped
				// out and the diagnostic would go to lager's default sink).
				fakeDelegate.StartSpanReturns(lagerctx.NewContext(ctx, logger), tracing.NoopSpan)

				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(logger.Buffer()).To(gbytes.Say(`failed-to-stream-flight-file.*results\.json`))
				Expect(logger.Buffer()).To(gbytes.Say(`failed-to-stream-flight-file.*events\.ndjson`))
			})
		})

		It("never fails the step when the metrics upsert errors", func() {
			fakeMetricsStore.UpsertReturningInsertedReturns(false, nil, errors.New("db down"))
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			// upsert failure ⇒ inserted=false ⇒ no ledger append (cannot prove first-insert)
			Expect(fakeChecker.RecordCallCount()).To(Equal(0))
		})

		It("records a fire-and-forget ledger entry when cost was incurred", func() {
			step.Run(ctx, state)
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))
			entry := fakeChecker.RecordArgsForCall(0)
			Expect(entry.Source).To(Equal(budget.SourceAgentStep))
			Expect(entry.Provider).To(Equal("anthropic"))
			// Spend attribution is server-owned: the durable workflow run off
			// step metadata and the function id off the immutable plan.
			Expect(entry.WorkflowRunID).ToNot(BeNil())
			Expect(*entry.WorkflowRunID).To(Equal(int64(flightRunID)))
			Expect(entry.FunctionID).To(Equal("review"))
			Expect(entry.BuildID).To(Equal(stepMetadata.BuildID))
			Expect(entry.StepName).To(Equal("write-spec"))
			Expect(entry.Model).To(Equal("m1"))
			Expect(entry.InputTokens).To(Equal(int64(100)))
			Expect(entry.OutputTokens).To(Equal(int64(50)))
			Expect(entry.Turns).To(Equal(9))
			Expect(entry.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
		})

		Context("when results.json reports abstain", func() {
			BeforeEach(func() {
				resultsJSON = `{"schema_version":"1.0","status":"abstain","confidence":0.2,"summary":"cannot judge","artifacts":[]}`
			})

			It("maps abstain results to failed", func() {
				step.Run(ctx, state)
				Expect(fakeMetricsStore.UpsertReturningInsertedArgsForCall(0).Status).To(Equal("failed"))
			})
		})

		// The pod cannot influence attribution: it is never told which workflow
		// run it belongs to, and the ledger row's identity comes only from
		// step metadata and the immutable plan.
		Context("when the step has no durable workflow run", func() {
			BeforeEach(func() {
				stepMetadata.WorkflowDefinitionID = nil
				stepMetadata.WorkflowRunID = nil
			})

			It("records the spend with no workflow-run attribution", func() {
				step.Run(ctx, state)

				Expect(fakeChecker.RecordCallCount()).To(Equal(1))
				entry := fakeChecker.RecordArgsForCall(0)
				Expect(entry.WorkflowRunID).To(BeNil())
				Expect(entry.FunctionID).To(Equal("review"))
				Expect(entry.Metadata).To(BeEmpty())
			})
		})

		// --- findings F3 + F24: web-restart resume neither re-charges the ledger
		// nor clobbers the real metrics row ---
		It("appends the ledger once and never clobbers across two Run invocations (resume)", func() {
			// First Run: flight data reads fine, fresh insert → ledger append fires.
			fakeMetricsStore.UpsertReturningInsertedReturns(true, nil, nil)
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			// Web restart: Step.Run re-executes, re-attaches, re-ingests — but the
			// in-memory volume locator is gone (artifact_locator.go is ephemeral)
			// and exitedProcess never re-records outputs, so BOTH StreamFile reads
			// fail. The degraded re-ingestion must go through InsertIfAbsent
			// (ON CONFLICT DO NOTHING) so its zero-cost error row cannot destroy
			// web-1's real row (F24) — and inserted=false skips the ledger (F3).
			fakeStreamer.StreamFileStub = nil
			fakeStreamer.StreamFileReturns(nil, errors.New("no locator for volume"))
			fakeMetricsStore.InsertIfAbsentReturns(false, nil) // row already exists
			_, err = step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1)) // full ingest only on web-1
			Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))          // degraded path on web-2
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))                       // ledger charged ONCE
		})

		// --- review finding (severed exec, 2026-07-11): a transient exec sever
		// mid-run makes the flight volume readable (F23 records output locations)
		// before the runner wrote cost.record/step.end — it only writes them
		// after claude exits — so the first ingestion INSERTS a zero-cost row.
		// The step then returns a transient error, RetryErrorStep wraps
		// Retriable, the tracker re-runs the step, the supervisor resumes the
		// still-running claude, and the full re-ingestion UPDATES the row
		// (inserted=false). The pre-fix first-insert-only ledger gate skipped
		// that update, so the step's ENTIRE spend never reached
		// agent_cost_ledger and budget admission over-allowed. The delta gate
		// must charge cost - prev.cost = the full real cost. ---
		It("charges the full cost on resume after a severed-exec zero-cost partial ingestion", func() {
			// Run 1: partial flight — events truncated before cost.record and
			// step.end, results.json not written yet. Zero cost ⇒ row inserted,
			// nothing to charge.
			partialEvents := strings.Join(eventLines[:flightEventCostRecord], "\n") + "\n"
			fakeStreamer.StreamFileStub = func(ctx context.Context, _ runtime.Artifact, path string) (io.ReadCloser, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if path == "events.ndjson" {
					return io.NopCloser(strings.NewReader(partialEvents)), nil
				}
				return nil, errors.New("results.json not written yet")
			}
			fakeMetricsStore.UpsertReturningInsertedReturns(true, nil, nil) // fresh insert
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
			Expect(fakeMetricsStore.UpsertReturningInsertedArgsForCall(0).CostUSD).To(BeZero())
			Expect(fakeChecker.RecordCallCount()).To(BeZero()) // nothing measured yet

			// Run 2: the tracker re-runs the step, the resumed claude finishes,
			// and full ingestion reads the real cost. The upsert UPDATES the
			// zero-cost partial row and returns its counters as prev.
			fakeStreamer.StreamFileStub = streamFullFlight
			fakeMetricsStore.UpsertReturningInsertedReturns(false, &schema.RunMetrics{}, nil) // prev = the zero-cost partial
			_, err = step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeChecker.RecordCallCount()).To(Equal(1)) // the spend reaches the ledger exactly once
			entry := fakeChecker.RecordArgsForCall(0)
			Expect(entry.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
			Expect(entry.InputTokens).To(Equal(int64(100)))
			Expect(entry.OutputTokens).To(Equal(int64(50)))
			Expect(entry.Turns).To(Equal(9))
		})

		// --- review finding (degraded first ingestion, 2026-07-12): the
		// one-and-only first insert of a (build_id, plan_id) can be consumed by
		// a ZERO-cost write, and a first-insert-only ledger gate would then skip
		// the healing re-ingestion (an update) too — the step's real spend would
		// permanently miss agent_cost_ledger even though agent_run_metrics
		// healed, silently under-counting budget admission. Two shapes: ---

		It("charges the full cost when the first ingestion was fully degraded and a resume heals it", func() {
			// Run 1: no flight data readable at all (transient artifact-daemon
			// outage, mTLS hiccup) ⇒ flightRead=false ⇒ the degraded path lands
			// InsertIfAbsent's zero-cost error row (inserted=true), and there is
			// nothing to charge yet.
			fakeStreamer.StreamFileStub = nil
			fakeStreamer.StreamFileReturns(nil, errors.New("artifact daemon unreachable"))
			fakeMetricsStore.InsertIfAbsentReturns(true, nil)
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
			Expect(fakeChecker.RecordCallCount()).To(BeZero()) // nothing measured yet

			// Run 2: a re-ingestion (same-web tracker re-run) reads the full
			// flight and UPDATES the degraded row; prev carries its zero
			// counters, so the delta is the entire real spend.
			fakeStreamer.StreamFileStub = streamFullFlight
			fakeMetricsStore.UpsertReturningInsertedReturns(false, &schema.RunMetrics{}, nil) // prev = the zero-cost error row
			_, err = step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeChecker.RecordCallCount()).To(Equal(1)) // the spend reaches the ledger exactly once
			entry := fakeChecker.RecordArgsForCall(0)
			Expect(entry.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
			Expect(entry.InputTokens).To(Equal(int64(100)))
			Expect(entry.Turns).To(Equal(9))
		})

		It("charges the full cost when the first ingestion read results.json but the events stream failed", func() {
			// Run 1: results.json streams fine but the events.ndjson read fails
			// ⇒ flightRead=true, so the zero-cost row goes through the FULL
			// upsert (never InsertIfAbsent) and consumes the first insert with
			// cost 0 — cost only ever comes from events.ndjson.
			fakeStreamer.StreamFileStub = func(ctx context.Context, _ runtime.Artifact, path string) (io.ReadCloser, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if path == "results.json" {
					return io.NopCloser(strings.NewReader(resultsJSON)), nil
				}
				return nil, errors.New("events stream reset by peer")
			}
			fakeMetricsStore.UpsertReturningInsertedReturns(true, nil, nil) // fresh insert
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(BeZero()) // partial read is NOT the degraded path
			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
			Expect(fakeMetricsStore.UpsertReturningInsertedArgsForCall(0).CostUSD).To(BeZero())
			Expect(fakeChecker.RecordCallCount()).To(BeZero()) // nothing measured yet

			// Run 2: the healing re-ingestion reads events too and UPDATES the
			// row; prev.cost==0 ⇒ the delta charges the whole real cost.
			fakeStreamer.StreamFileStub = streamFullFlight
			fakeMetricsStore.UpsertReturningInsertedReturns(false, &schema.RunMetrics{}, nil) // prev = the zero-cost partial
			_, err = step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeChecker.RecordCallCount()).To(Equal(1)) // the spend reaches the ledger exactly once
			entry := fakeChecker.RecordArgsForCall(0)
			Expect(entry.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
			Expect(entry.OutputTokens).To(Equal(int64(50)))
		})

		It("charges only the delta when a prior partial ingestion already appended spend", func() {
			// e.g. a sever AFTER the runner flushed cost.record: the partial
			// ingestion inserted (and appended) 0.30; the resume's full
			// ingestion reads 0.42 and must append only the difference.
			fakeMetricsStore.UpsertReturningInsertedReturns(false, &schema.RunMetrics{
				CostUSD: 0.30,
				Usage:   schema.Usage{InputTokens: 80, OutputTokens: 40},
				Turns:   7,
			}, nil)
			step.Run(ctx, state)
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))
			entry := fakeChecker.RecordArgsForCall(0)
			Expect(entry.CostUSD).To(BeNumerically("~", 0.12, 1e-9))
			Expect(entry.InputTokens).To(Equal(int64(20)))
			Expect(entry.OutputTokens).To(Equal(int64(10)))
			Expect(entry.Turns).To(Equal(2))
		})

		It("skips the ledger when a full re-ingestion reads the same cost (web-restart resume)", func() {
			fakeMetricsStore.UpsertReturningInsertedReturns(false, &schema.RunMetrics{
				CostUSD: 0.42,
				Usage:   schema.Usage{InputTokens: 100, OutputTokens: 50},
				Turns:   9,
			}, nil)
			step.Run(ctx, state)
			Expect(fakeChecker.RecordCallCount()).To(BeZero()) // delta is zero — already charged
		})

		It("skips the ledger when an update returns no previous counters (lost ingestion race)", func() {
			// inserted=false with prev=nil is indeterminate: the concurrent
			// winner charged the ledger, so skipping preserves exactly-once.
			fakeMetricsStore.UpsertReturningInsertedReturns(false, nil, nil)
			step.Run(ctx, state)
			Expect(fakeChecker.RecordCallCount()).To(BeZero())
		})

		// --- review finding (self-reported cost, 2026-07-12): the flight
		// recorder is written inside the agent pod, where claude runs as root —
		// a prompt-injected agent can rewrite events.ndjson with cost_usd 0
		// before exit, understating the global daily cap and every later
		// workflow-run reservation. The exec captures the claude CLI
		// envelope from the LIVE stdout stream and floors ingestion at it. ---
		Context("server-side cost floor (self-reported flight recorder)", func() {
			envelope := `{"type":"result","subtype":"success","is_error":false,"result":"done","model":"m1","num_turns":9,"total_cost_usd":0.42,"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1,"cache_creation_input_tokens":2}}`

			BeforeEach(func() {
				// The agent process streams the envelope over stdout while it
				// runs — the same bytes agent-runner parses in-pod. Mutate the
				// harness-registered stub in place (WithProcess is
				// copy-on-write; finding F37).
				chosenContainer.ProcessDefs[0].Stub.Do = func(_ context.Context, p *runtimetest.Process) error {
					fmt.Fprintln(p.Stdout(), envelope)
					return nil
				}
			})

			Context("when the flight recorder under-reports (tampered events.ndjson)", func() {
				BeforeEach(func() {
					// The agent zeroed its own cost.record before exit.
					eventLines[flightEventCostRecord] = `{"ts":"2026-07-10T12:00:02Z","event":"cost.record","data":{"source":"agent_step","provider":"anthropic","model":"m1","input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_creation_tokens":0,"turns":0,"cost_usd":0}}`
				})

				It("floors the metrics row and the ledger at the observed stdout envelope", func() {
					ok, err := step.Run(ctx, state)
					Expect(err).ToNot(HaveOccurred())
					Expect(ok).To(BeTrue())

					rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
					Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
					Expect(rm.Usage.InputTokens).To(Equal(int64(100)))
					Expect(rm.Usage.OutputTokens).To(Equal(int64(50)))
					Expect(rm.Usage.CacheReadInputTokens).To(Equal(int64(1)))
					Expect(rm.Usage.CacheCreationInputTokens).To(Equal(int64(2)))
					Expect(rm.Turns).To(Equal(9))

					// The falsified spend still reaches agent_cost_ledger, so
					// budget reconstruction stays truthful and the next
					// reservation is not loosened — the pre-fix code charged $0.
					Expect(fakeChecker.RecordCallCount()).To(Equal(1))
					entry := fakeChecker.RecordArgsForCall(0)
					Expect(entry.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
					Expect(entry.InputTokens).To(Equal(int64(100)))
					Expect(entry.Turns).To(Equal(9))
				})
			})

			Context("when the whole flight recorder is unreadable but the envelope streamed", func() {
				BeforeEach(func() {
					fakeStreamer.StreamFileStub = nil
					fakeStreamer.StreamFileReturns(nil, errors.New("no locator for volume"))
					fakeMetricsStore.InsertIfAbsentReturns(true, nil)
				})

				It("carries the observed floor on the degraded insert and ledgers it", func() {
					_, err := step.Run(ctx, state)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
					rm := fakeMetricsStore.InsertIfAbsentArgsForCall(0)
					Expect(rm.Status).To(Equal(schema.RunStatusIncomplete)) // L-1: no flight read ⇒ incomplete (amber), still the observed floor
					Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9))

					Expect(fakeChecker.RecordCallCount()).To(Equal(1))
					Expect(fakeChecker.RecordArgsForCall(0).CostUSD).To(BeNumerically("~", 0.42, 1e-9))
				})
			})

			It("charges exactly once when the flight recorder honestly matches the envelope", func() {
				// The honest path round-trips the identical float64 through the
				// runner's cost.record, so the floor is a no-op — no double count,
				// no inflation.
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
				Expect(fakeChecker.RecordCallCount()).To(Equal(1))
				Expect(fakeChecker.RecordArgsForCall(0).CostUSD).To(BeNumerically("~", 0.42, 1e-9))
			})

			It("trusts a flight recorder that reports MORE than the observed floor", func() {
				// e.g. gateway-external tool spend rolled into cost.record by a
				// future runner: the floor only ever raises, never caps.
				eventLines[flightEventCostRecord] = `{"ts":"2026-07-10T12:00:02Z","event":"cost.record","data":{"source":"agent_step","provider":"anthropic","model":"m1","input_tokens":100,"output_tokens":50,"cache_read_tokens":1,"cache_creation_tokens":2,"turns":9,"cost_usd":0.50}}`
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeChecker.RecordArgsForCall(0).CostUSD).To(BeNumerically("~", 0.50, 1e-9))
			})
		})

		// --- finding F4: timed-out step still records pre-timeout cost + ledger ---
		Context("when the step times out", func() {
			BeforeEach(func() {
				// A real (tiny) timeout: MaybeTimeout wraps ctx with a deadline,
				// the process blocks until it fires and surfaces
				// context.DeadlineExceeded — so at ingestion time the step ctx is
				// GENUINELY expired and the ctx-honoring streamer stub above
				// rejects any read threaded through it. Only the detached
				// ingestCtx (WithoutCancel) keeps the reads live. The flight
				// volume stays fully populated — the runner flushed before the
				// kill. Mutate the harness-registered process definition's stub
				// in place (runtimetest's WithProcess is copy-on-write; finding
				// F37).
				agentPlan.Timeout = "50ms"
				chosenContainer.ProcessDefs[0].Stub.Call = func(ctx context.Context, _ *runtimetest.Process) (runtime.ProcessResult, error) {
					<-ctx.Done()
					return runtime.ProcessResult{}, ctx.Err()
				}
			})

			It("still ingests cost/tokens and records the ledger", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeFalse()) // timeout ⇒ step not successful

				Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9)) // pre-timeout cost preserved
				Expect(rm.Turns).To(Equal(9))
				Expect(rm.Usage.InputTokens).To(Equal(int64(100)))
				Expect(rm.EventCounts).To(HaveKeyWithValue("tool.call", 1))
				Expect(fakeChecker.RecordCallCount()).To(Equal(1)) // ledger entry recorded despite timeout
			})
		})

		// --- review finding (2026-07-12): the failed-agent path — agent-runner
		// exits 2, results.json says fail — had zero exec-level coverage, so
		// ingestion-on-failure was unpinned: a mutation gating ingestion on
		// `result.ExitStatus == 0` kept the whole suite green (the timeout
		// spec's zero-value ProcessResult also carries ExitStatus 0) while
		// silently dropping every "agent did badly" metrics row — the rows the
		// scorecard/budget three-way ok/failed/error taxonomy exists for. ---
		Context("when the agent fails (agent-runner exits non-zero)", func() {
			BeforeEach(func() {
				// Mutate the harness-registered process definition's stub in
				// place (runtimetest's WithProcess is copy-on-write; finding
				// F37). Exit 2 is agent-runner's "agent failed" code.
				chosenContainer.ProcessDefs[0].Stub.ExitStatus = 2

				resultsJSON = `{"schema_version":"1.0","status":"fail","confidence":0.9,"summary":"tests failed","artifacts":[]}`
				eventLines[flightEventStepEnd] = `{"ts":"2026-07-10T12:01:01Z","event":"step.end","data":{"step_name":"write-spec","status":"failed","summary":"tests failed","wall_time_seconds":61,"cost_usd":0.42,"turns":9}}`
			})

			It("fails the step without erroring and still ingests the failed row", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeFalse())

				Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
				_, exitStatus := fakeDelegate.FinishedArgsForCall(0)
				Expect(exitStatus).To(Equal(exec.ExitStatus(2)))

				// ingestion is unconditional on exit status: scorecards and
				// budget admission depend on the failed rows being written
				Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.Status).To(Equal("failed"))
				Expect(rm.Summary).To(Equal("tests failed"))
				Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9))

				// the spend was real even though the agent failed
				Expect(fakeChecker.RecordCallCount()).To(Equal(1))
				Expect(fakeChecker.RecordArgsForCall(0).CostUSD).To(BeNumerically("~", 0.42, 1e-9))
			})
		})

		// Companion to the exit-2 spec: a non-deadline runErr (worker transport
		// dropped mid-Wait) surfaces the error to the caller, but only AFTER
		// ingestion — the flight volume may hold real spend that must reach the
		// metrics row and the ledger before the build errors.
		Context("when process.Wait returns a transport error", func() {
			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.Err = "worker connection dropped"
			})

			It("still ingests, then surfaces the error without Finished", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError("worker connection dropped"))

				// errored, not finished — no exit status to report
				Expect(fakeDelegate.FinishedCallCount()).To(BeZero())

				Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
				Expect(fakeChecker.RecordCallCount()).To(Equal(1))
			})
		})

		// An interruption is retriable, but only so far. Nothing on the
		// Retriable path counts anything -- the engine returns without
		// finishing and the build tracker re-picks the build up -- so for an
		// agent whose pod is gone, every cycle would wipe the workspace and
		// start a fresh agent at full token spend until a human aborted the
		// build. The durable ingestion sequence is what bounds it.
		DescribeTable("when process.Wait reports an interruption", func(reason runtime.InterruptionReason) {
			chosenContainer.ProcessDefs[0].Stub.Call = func(context.Context, *runtimetest.Process) (runtime.ProcessResult, error) {
				return runtime.ProcessResult{}, runtime.NewInterruptionError(reason, errors.New("pod lifecycle ended"))
			}

			ok, err := step.Run(ctx, state)
			Expect(ok).To(BeFalse())

			By("surfacing the interruption so RetryError can mark the step retriable")
			Expect(err).To(HaveOccurred())
			var interruption runtime.InterruptionError
			Expect(errors.As(err, &interruption)).To(BeTrue())
			Expect(interruption.InterruptionReason()).To(Equal(reason))
			Expect(fakeDelegate.FinishedCallCount()).To(BeZero())
		},
			Entry("pod deletion", runtime.InterruptionPodDeleted),
			Entry("eviction", runtime.InterruptionEvicted),
			Entry("node loss", runtime.InterruptionNodeLost),
			Entry("preemption", runtime.InterruptionPreempted),
		)

		It("stops retrying an interruption once the step has burned its restart cap", func() {
			chosenContainer.ProcessDefs[0].Stub.Call = func(context.Context, *runtimetest.Process) (runtime.ProcessResult, error) {
				return runtime.ProcessResult{}, runtime.NewInterruptionError(runtime.InterruptionPreempted, errors.New("pod lifecycle ended"))
			}
			// The durable row says this step has already been ingested three
			// times: the original execution plus two restarts. Which write
			// path records that is an ingestion detail, so both report it.
			fakeMetricsStore.InsertIfAbsentStub = func(rm *schema.RunMetrics) (bool, error) {
				rm.IngestionSeq = 3
				return false, nil
			}
			fakeMetricsStore.UpsertReturningInsertedStub = func(rm *schema.RunMetrics) (bool, *schema.RunMetrics, error) {
				rm.IngestionSeq = 3
				return false, nil, nil
			}

			ok, err := step.Run(ctx, state)
			Expect(ok).To(BeFalse())

			By("failing closed rather than starting a fourth agent")
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeDelegate.FinishedCallCount()).To(BeZero())
			Expect(fakeDelegate.ErroredCallCount()).To(Equal(1))
			_, message := fakeDelegate.ErroredArgsForCall(0)
			Expect(message).To(ContainSubstring("preempted"))
			Expect(message).To(ContainSubstring("restart"))
		})

		// The runner-captured transcript (flight/transcript.ndjson) is produced
		// by THIS step's own runner: the agent step's flight volume is the only
		// place it ever lives. Ingestion therefore belongs in
		// ingestFlightRecorder, keyed identically to the metrics row on
		// (build_id, plan_id) and carrying the SAME server-owned v3 identity
		// (workflow run + function id) — never anything read out of the
		// attacker-writable recorder or plan env.
		Context("with a transcript store wired", func() {
			var (
				transcriptStore *stubTranscriptStore
				transcriptRaw   string
			)

			BeforeEach(func() {
				transcriptStore = &stubTranscriptStore{}
				transcriptRaw = `{"type":"system","subtype":"init"}` + "\n" +
					`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/w/main.go"}}]}}` + "\n" +
					`{"type":"result","total_cost_usd":0.42}` + "\n"

				// Delegate every other path to the outer results.json/events.ndjson
				// fixture; only transcript.ndjson is new here.
				fakeStreamer.StreamFileStub = func(ctx context.Context, artifact runtime.Artifact, path string) (io.ReadCloser, error) {
					if path == "transcript.ndjson" {
						if err := ctx.Err(); err != nil {
							return nil, err
						}
						return io.NopCloser(strings.NewReader(transcriptRaw)), nil
					}
					return streamFullFlight(ctx, artifact, path)
				}

				agentStepOptions = append(agentStepOptions, exec.WithAgentStepTranscriptStore(transcriptStore))
			})

			It("upserts the transcript keyed by build/plan and the v3 run identity, from the agent step's OWN flight", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue())

				Expect(transcriptStore.upserted).To(HaveLen(1))
				tr := transcriptStore.upserted[0]
				Expect(tr.BuildID).To(Equal(stepMetadata.BuildID))
				Expect(tr.PlanID).To(Equal(string(planID)))
				Expect(tr.StepName).To(Equal("write-spec"))
				// the durable workflow run off step.metadata and the function id
				// off the immutable plan — the same pair the metrics row carries
				Expect(tr.WorkflowRunID).ToNot(BeNil())
				Expect(*tr.WorkflowRunID).To(Equal(flightRunID))
				Expect(tr.FunctionID).To(Equal("review"))
				Expect(tr.NDJSON).To(Equal(transcriptRaw))
				Expect(tr.ByteLen).To(Equal(len(transcriptRaw)))
				Expect(tr.Truncated).To(BeFalse())
			})

			Context("on a pure-CI agent step with no durable workflow run", func() {
				BeforeEach(func() {
					stepMetadata.WorkflowDefinitionID = nil
					stepMetadata.WorkflowRunID = nil
				})

				It("leaves the workflow run unset rather than inventing one", func() {
					ok, err := step.Run(ctx, state)
					Expect(err).ToNot(HaveOccurred())
					Expect(ok).To(BeTrue())

					Expect(transcriptStore.upserted).To(HaveLen(1))
					Expect(transcriptStore.upserted[0].WorkflowRunID).To(BeNil())
					Expect(transcriptStore.upserted[0].FunctionID).To(Equal("review"))
				})
			})

			It("bounds an over-512KiB transcript to the tail with the truncation marker", func() {
				var b strings.Builder
				for b.Len() < 600*1024 {
					b.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"padding padding padding"}]}}` + "\n")
				}
				b.WriteString(`{"type":"result","total_cost_usd":9.9}` + "\n")
				transcriptRaw = b.String()

				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue())

				Expect(transcriptStore.upserted).To(HaveLen(1))
				tr := transcriptStore.upserted[0]
				Expect(tr.Truncated).To(BeTrue())
				Expect(tr.ByteLen).To(BeNumerically("<=", 512*1024+256))
				Expect(tr.ByteLen).To(Equal(len(tr.NDJSON)))
				Expect(tr.NDJSON).To(HavePrefix(`{"type":"transcript_truncated","dropped_bytes":`))
				// the head-truncation drops a partial line, so the first data
				// line is always whole
				Expect(strings.Split(tr.NDJSON, "\n")[1]).To(HavePrefix(`{"type":"assistant"`))
				// the diagnostic tail (the final result line) survives
				Expect(tr.NDJSON).To(ContainSubstring(`"total_cost_usd":9.9`))
			})

			It("never fails the step when the transcript upsert errors", func() {
				transcriptStore.err = errors.New("db down")
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue())
				Expect(transcriptStore.upserted).To(HaveLen(1)) // attempted despite the error
			})
		})

		// The base step in this Context (outer JustBeforeEach) wires NO
		// transcript store. Ingestion must stay fully nil-guarded — not even
		// the extra StreamFile call fires.
		It("does not stream transcript.ndjson when no transcript store is configured", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			for i := 0; i < fakeStreamer.StreamFileCallCount(); i++ {
				_, _, path := fakeStreamer.StreamFileArgsForCall(i)
				Expect(path).ToNot(Equal("transcript.ndjson"))
			}
		})
	})
})

type skillMaterializingWorker struct {
	runtime.Worker
	volume runtime.Volume
	err    error
	calls  int
}

func (worker *skillMaterializingWorker) CreateVolumeForArtifact(_ context.Context, _ int) (runtime.Volume, db.WorkerArtifact, error) {
	worker.calls++
	return worker.volume, nil, worker.err
}

type failingSkillVolume struct {
	runtime.Volume
	err error
}

func (volume failingSkillVolume) StreamIn(_ context.Context, _ string, _ compression.Compression, _ float64, _ io.Reader) error {
	return volume.err
}

// stubTranscriptStore records the transcripts the agent step's flight
// ingestion upserts, and can fail on demand (observability is best-effort:
// a store error must never fail the step).
type stubTranscriptStore struct {
	upserted []db.AgentRunTranscript
	err      error
}

func (s *stubTranscriptStore) Upsert(t db.AgentRunTranscript) error {
	s.upserted = append(s.upserted, t)
	return s.err
}
