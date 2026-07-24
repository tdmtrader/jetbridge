package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/api/metrics/metricsfakes"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"github.com/onsi/gomega/gbytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// resumeAwarePool decorates the fake pool with the production worker.Pool's
// FindWorkerForContainer capability, letting the agent step detect an existing
// container for the step owner on a restart-resume. containerExists controls
// what that lookup reports.
type resumeAwarePool struct {
	*execfakes.FakePool
	containerExists bool
}

func (p *resumeAwarePool) FindWorkerForContainer(lager.Logger, db.ContainerOwner, worker.Spec) (runtime.Worker, bool, error) {
	return nil, p.containerExists, nil
}

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
		fakeRunVerifier     *execfakes.FakeAgentRunVerifier

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
		fakeRunVerifier = new(execfakes.FakeAgentRunVerifier)
		// Default: the claimed run belongs to this build's pipeline and the
		// claimed ticket is dispatched as that run. Specs that exercise the
		// cross-run / cross-ticket guards override these.
		fakeRunVerifier.RunBelongsToPipelineReturns(true, nil)
		fakeRunVerifier.TicketBelongsToRunReturns(true, nil)
		agentStepOptions = []exec.AgentStepOption{
			exec.WithAgentBudgetChecker(fakeChecker),
			exec.WithAgentMetricsStore(fakeMetricsStore),
			exec.WithAgentRunVerifier(fakeRunVerifier),
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
				// A ticket claim is only honored alongside a run id whose
				// linkage to the ticket the server can verify (review
				// finding, 2026-07-11) — renderer-set pipelines always
				// carry both.
				"AGENT_TICKET_ID":       "7",
				"AGENT_PIPELINE_RUN_ID": "42",
				"BASE_REF":              "main",
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
			"AGENT_TICKET_ID=7",
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
			// Deliberately NOT in agentPlan.Inputs: the env rows derive
			// from plan.Skills alone; the renderer owns adding the
			// "skills" input in production, and an unmounted input here
			// would fail the run with MissingInputsError.
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

	Context("when the plan carries a prompt file", func() {
		BeforeEach(func() {
			agentPlan.Prompt = ""
			agentPlan.PromptFile = "repo/prompts/spec.md"
		})

		It("uses AGENT_PROMPT_FILE", func() {
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).To(ContainElement("AGENT_PROMPT_FILE=repo/prompts/spec.md"))
			Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_PROMPT=")))
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
			snapshotMetadata, snapshotContent := snapshotStoresForSealedOutputs(outputSealer.result)
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

			entry, found := repo.ArtifactEntryFor("workspace")
			Expect(found).To(BeTrue())
			Expect(entry.Artifact).To(BeAssignableToTypeOf(&runtime.SnapshotArtifact{}))
			Expect(entry.Snapshot).NotTo(BeNil())
			Expect(*entry.Snapshot).To(Equal(outputSealer.result["workspace"].Snapshot))
			flight, found := repo.ArtifactEntryFor("flight")
			Expect(found).To(BeTrue())
			Expect(flight.Snapshot).To(BeNil())
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

	It("resolves the step slice through the budget checker", func() {
		fakeChecker.StepSliceReturns(budget.Remaining{
			LimitUSD:     2.5,
			SpentUSD:     1.25,
			RemainingUSD: 1.25,
		}, nil)

		_, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())

		Expect(fakeChecker.StepSliceCallCount()).To(Equal(1))
		ticketID, slice := fakeChecker.StepSliceArgsForCall(0)
		Expect(ticketID).To(Equal(7))
		Expect(slice).To(Equal(2.5))

		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Env).To(ContainElement("AGENT_BUDGET_SLICE_USD=1.25"))
	})

	It("preserves sub-cent precision so a positive hard cap never becomes uncapped", func() {
		fakeChecker.StepSliceReturns(budget.Remaining{
			LimitUSD:     0.004321,
			RemainingUSD: 0.004321,
		}, nil)

		_, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Env).To(ContainElement("AGENT_BUDGET_SLICE_USD=0.004321"))
	})

	It("fails without starting when the slice is exhausted", func() {
		fakeChecker.StepSliceReturns(budget.Remaining{Exhausted: true}, nil)

		ok, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeFalse())
		Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())

		Expect(fakeDelegate.ErroredCallCount()).To(Equal(1))
		_, message := fakeDelegate.ErroredArgsForCall(0)
		Expect(message).To(ContainSubstring("budget slice exhausted"))

		Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
		_, exitStatus := fakeDelegate.FinishedArgsForCall(0)
		Expect(exitStatus).To(Equal(exec.ExitStatus(1)))
	})

	// --- review finding (2026-07-12): budget re-resolution runs at the start
	// of EVERY execution, including a restart-resume. If StepSlice reports
	// Exhausted on a resume, the pre-fix early-return fired BEFORE
	// FindOrSelectWorker/attach — orphaning the supervised agent still running
	// in the existing pod: never attached, never killed, never ingested, its
	// spend leaking and never ledgered. The exhaustion early-return must be
	// gated on there being NO existing container for the step owner. ---
	Context("when the slice is exhausted on a restart-resume with an existing container", func() {
		var pool *resumeAwarePool

		JustBeforeEach(func() {
			// The production worker.Pool answers FindWorkerForContainer; the
			// fake does not, so this decorated pool models a resume where a
			// container already exists for the step owner.
			pool = &resumeAwarePool{FakePool: fakePool, containerExists: true}
			fakeChecker.StepSliceReturns(budget.Remaining{Exhausted: true}, nil)
			fakeMetricsStore.InsertIfAbsentReturns(true, nil)
			step = exec.NewAgentStep(
				planID,
				agentPlan,
				atc.ContainerLimits{},
				atc.ContainerLimits{},
				stepMetadata,
				containerMetadata,
				pool,
				fakeStreamer,
				fakeDelegateFactory,
				0,
				agentImage,
				exec.WithAgentBudgetChecker(fakeChecker),
				exec.WithAgentMetricsStore(fakeMetricsStore),
				exec.WithAgentRunVerifier(fakeRunVerifier),
			)
		})

		It("does not early-return: attaches, waits, and ingests the running agent", func() {
			// Model the genuinely-running agent of a restart-resume: the
			// process is already live in the container, so the step ATTACHES
			// to it (the exhausted-resume path must never Run a new one).
			_, err := chosenContainer.Run(context.Background(), agentProcessSpec, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue()) // the resumed agent completed (exit 0)

			// Fell through to the normal path instead of the exhaustion
			// early-return: worker selected and the process attached/ran.
			Expect(pool.FindOrSelectWorkerCallCount()).To(Equal(1))
			Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))

			// The running agent's flight recorder is ingested rather than left
			// unrecorded, and the premature Errored/Finished(1) never fires.
			Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
			Expect(fakeDelegate.ErroredCallCount()).To(BeZero())
			Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
			_, exitStatus := fakeDelegate.FinishedArgsForCall(0)
			Expect(exitStatus).To(Equal(exec.ExitStatus(0)))
		})

		It("fails closed when no prior agent process is attachable, instead of starting a new one", func() {
			// The container DB row exists (stepContainerExists is satisfied by
			// any row state — the pod is created lazily in Run), but nothing is
			// attachable: the web node restarted before the pod ran, or the pod
			// was reaped. Falling through to attachOrRun's Run() fallback here
			// would launch a BRAND-NEW agent and spend the full configured
			// slice against a ticket that is already exhausted.
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse())

			// No new agent process was started against the exhausted ticket.
			Expect(chosenContainer.RunningProcesses()).To(BeEmpty())

			Expect(fakeDelegate.ErroredCallCount()).To(Equal(1))
			_, message := fakeDelegate.ErroredArgsForCall(0)
			Expect(message).To(ContainSubstring("budget slice exhausted"))

			Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
			_, exitStatus := fakeDelegate.FinishedArgsForCall(0)
			Expect(exitStatus).To(Equal(exec.ExitStatus(1)))
		})
	})

	It("keeps the configured slice when the budget checker errors", func() {
		fakeChecker.StepSliceReturns(budget.Remaining{}, context.DeadlineExceeded)

		ok, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeTrue())

		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Env).To(ContainElement("AGENT_BUDGET_SLICE_USD=2.5"))
	})

	// --- review finding (2026-07-12): steps with no budget_slice_usd bypassed
	// the ticket-exhaustion admission check. budget_slice_usd 0 means
	// "uncapped within ticket budget" (§2.8) — the ticket cap still applies —
	// so admission must run for every ticketed step, not only when slice > 0.
	// Pre-fix, a step omitting the slice was admitted (and spent) against a
	// fully exhausted ticket while a sibling WITH a slice was stopped.
	Context("when the plan declares no budget slice (budget_slice_usd 0)", func() {
		BeforeEach(func() {
			agentPlan.BudgetSliceUSD = 0
		})

		It("still fails without starting when the ticket is exhausted", func() {
			fakeChecker.StepSliceReturns(budget.Remaining{
				LimitUSD:     10,
				SpentUSD:     12,
				RemainingUSD: -2,
				Exhausted:    true,
			}, nil)

			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse())
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())

			Expect(fakeChecker.StepSliceCallCount()).To(Equal(1))
			ticketID, slice := fakeChecker.StepSliceArgsForCall(0)
			Expect(ticketID).To(Equal(7))
			Expect(slice).To(BeZero())

			Expect(fakeDelegate.ErroredCallCount()).To(Equal(1))
			_, message := fakeDelegate.ErroredArgsForCall(0)
			Expect(message).To(ContainSubstring("budget slice exhausted"))
			Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
			_, exitStatus := fakeDelegate.FinishedArgsForCall(0)
			Expect(exitStatus).To(Equal(exec.ExitStatus(1)))
		})

		It("fails closed when the budget checker errors, instead of dispatching uncapped", func() {
			// A sliceless step's ONLY cap is the ticket's resolved remaining.
			// A step WITH a configured slice degrades to that slice when the
			// checker errors (acceptable: still capped) — but degrading a
			// sliceless step means dispatching with NO cap at all against a
			// ticket whose state is unknown, the exact bypass the sliceless
			// admission check exists to prevent.
			fakeChecker.StepSliceReturns(budget.Remaining{}, context.DeadlineExceeded)

			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse())
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())

			Expect(fakeDelegate.ErroredCallCount()).To(Equal(1))
			_, message := fakeDelegate.ErroredArgsForCall(0)
			Expect(message).To(ContainSubstring("failed to resolve ticket budget"))
			Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
			_, exitStatus := fakeDelegate.FinishedArgsForCall(0)
			Expect(exitStatus).To(Equal(exec.ExitStatus(1)))
		})

		It("caps the step at a capped ticket's remaining", func() {
			fakeChecker.StepSliceReturns(budget.Remaining{
				LimitUSD:     10,
				SpentUSD:     4,
				RemainingUSD: 6,
			}, nil)

			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).To(ContainElement("AGENT_BUDGET_SLICE_USD=6"))
		})

		It("emits no slice env when the ticket itself is uncapped", func() {
			// Remaining.LimitUSD == 0 means truly uncapped: StepSlice(t, 0)
			// on an uncapped ticket returns only the spend so far.
			fakeChecker.StepSliceReturns(budget.Remaining{SpentUSD: 4}, nil)

			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			Expect(fakeChecker.StepSliceCallCount()).To(Equal(1))

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_BUDGET_SLICE_USD=")))
		})
	})

	// --- review finding (2026-07-11): budget admission gate entirely skippable ---
	// AGENT_TICKET_ID is the same attacker-writable plan env as everything else
	// (F30). Pre-fix, the ONLY admission check was StepSlice(env ticket, slice):
	// omitting or garbling the key skipped admission entirely, and claiming
	// SOMEONE ELSE'S ticket id both admitted against the victim's remaining
	// budget and misattributed this step's spend into their ledger at ingestion.
	// Ticket identity must be server-verified against the verified run's
	// linkage (agent_tickets.pipeline_run_id) before it reaches admission,
	// metrics tagging, or the cost ledger.
	Describe("server-verified ticket identity", func() {
		It("verifies the claimed ticket against the verified run before admission", func() {
			fakeChecker.StepSliceReturns(budget.Remaining{LimitUSD: 2.5, RemainingUSD: 2.5}, nil)

			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeRunVerifier.TicketBelongsToRunCallCount()).To(Equal(1))
			gotTicket, gotRun := fakeRunVerifier.TicketBelongsToRunArgsForCall(0)
			Expect(gotTicket).To(Equal(7))
			Expect(gotRun).To(Equal(42))

			Expect(fakeChecker.StepSliceCallCount()).To(Equal(1))
			ticketID, _ := fakeChecker.StepSliceArgsForCall(0)
			Expect(ticketID).To(Equal(7))
		})

		Context("when the claimed ticket is not dispatched as the verified run (someone else's ticket)", func() {
			BeforeEach(func() {
				fakeRunVerifier.TicketBelongsToRunReturns(false, nil)
			})

			It("fails closed: never admits against, runs on, or charges the victim ticket", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring("refusing to admit against ticket 7")))

				Expect(fakeChecker.StepSliceCallCount()).To(BeZero())
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
				// nothing ran ⇒ nothing can be misattributed into the
				// victim's metrics or ledger
				Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(BeZero())
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(BeZero())
				Expect(fakeChecker.RecordCallCount()).To(BeZero())
			})
		})

		Context("when the ticket-linkage check errors", func() {
			BeforeEach(func() {
				fakeRunVerifier.TicketBelongsToRunReturns(false, errors.New("db down"))
			})

			It("fails the step rather than proceeding without verification", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring("verify ticket 7 linkage to pipeline run 42")))
				Expect(fakeChecker.StepSliceCallCount()).To(BeZero())
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
			})
		})

		Context("when a ticket is claimed without a pipeline-run id", func() {
			BeforeEach(func() {
				delete(agentPlan.Env, "AGENT_PIPELINE_RUN_ID")
			})

			It("fails closed: the claim has no verifiable run linkage", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring("requires a verified AGENT_PIPELINE_RUN_ID")))
				Expect(fakeChecker.StepSliceCallCount()).To(BeZero())
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
			})
		})

		Context("when AGENT_TICKET_ID is non-numeric", func() {
			BeforeEach(func() {
				agentPlan.Env["AGENT_TICKET_ID"] = "bogus"
			})

			It("errors instead of silently skipping admission (the pre-fix bypass)", func() {
				ok, err := step.Run(ctx, state)
				Expect(ok).To(BeFalse())
				Expect(err).To(MatchError(ContainSubstring(`malformed AGENT_TICKET_ID "bogus"`)))
				Expect(fakeChecker.StepSliceCallCount()).To(BeZero())
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
			})
		})

		Context("when no run verifier is wired", func() {
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
					exec.WithAgentBudgetChecker(fakeChecker),
					exec.WithAgentMetricsStore(fakeMetricsStore),
				)
			})

			It("drops the unverifiable claim: no admission, NULL ticket attribution", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue())

				Expect(fakeChecker.StepSliceCallCount()).To(BeZero())

				// degraded ingestion path (no flight fixtures) still runs, but
				// the unverified ticket claim never reaches attribution — the
				// cost ledger (which now carries the ticket/run identity) stays
				// empty (the durable workflow-run id on the row is server
				// metadata, not the attacker's claim).
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				Expect(fakeChecker.RecordCallCount()).To(BeZero())
			})
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
		Context("with auxiliary and gateway sidecars on a workflow run", func() {
			BeforeEach(func() {
				agentPlan.Env["AGENT_PIPELINE_RUN_ID"] = "42"
				agentPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{Name: "auxiliary", Image: "img:v1"}},
					{Config: &atc.SidecarConfig{Name: "gateway", Image: "img:v2"}},
				}
				fakeChecker.StepSliceReturns(budget.Remaining{
					LimitUSD:     2.5,
					SpentUSD:     1.25,
					RemainingUSD: 1.25,
				}, nil)
			})

			It("keeps hermetic sidecars local and never grants credentials by role name", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				// The run id claimed by plan env is verified against THIS
				// build's pipeline before any secret ref is injected (§8.2).
				Expect(fakeRunVerifier.RunBelongsToPipelineCallCount()).To(Equal(1))
				gotRunID, gotPipelineID := fakeRunVerifier.RunBelongsToPipelineArgsForCall(0)
				Expect(gotRunID).To(Equal(42))
				Expect(gotPipelineID).To(Equal(stepMetadata.PipelineID))

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SidecarEnv["auxiliary"]).To(ConsistOf(
					"BUILD_ID=" + strconv.Itoa(stepMetadata.BuildID),
				))
				Expect(spec.SidecarEnv["auxiliary"]).ToNot(ContainElement(HavePrefix("ATC_EXTERNAL_URL=")))
				Expect(spec.SidecarEnv["gateway"]).To(ContainElement(HavePrefix("AGENT_BUDGET_SLICE_USD=")))
				Expect(spec.SidecarSecretEnv).To(BeEmpty())
			})

			// --- review finding: main container never received the token ---
			// §8.1 pins CLAUDE_CODE_OAUTH_TOKEN to the main container from the
			// per-run secret — the ONLY token path into an agent pod (env is
			// static-only and never carries credentials). Without an explicit
			// ref the main claude CLI has no token at all on
			// dispatch-rendered runs and fails at auth.
			It("wires the main container's CLAUDE_CODE_OAUTH_TOKEN from the per-run secret (§8.1)", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).To(HaveKeyWithValue(
					"CLAUDE_CODE_OAUTH_TOKEN", vars.SecretRef{Name: "agent-run-42", Key: "anthropic-token"}))
				Expect(spec.SidecarSecretEnv).To(BeEmpty())
				// secretKeyRef-only — the token must never appear as a literal.
				Expect(spec.Env).ToNot(ContainElement(HavePrefix("CLAUDE_CODE_OAUTH_TOKEN=")))
			})

			// --- review finding: cross-run model credential exfiltration ---
			// AGENT_PIPELINE_RUN_ID is attacker-writable plan YAML (F30). A
			// pipeline that claims a run id it does not own must not receive that
			// run's model credential in its main agent container. The step fails
			// closed before any pod exists; sidecar secret env remains empty.
			Context("when the claimed run id does not belong to this build's pipeline", func() {
				BeforeEach(func() {
					fakeRunVerifier.RunBelongsToPipelineReturns(false, nil)
				})

				It("refuses to run rather than deliver another run's model credential", func() {
					ok, err := step.Run(ctx, state)
					Expect(ok).To(BeFalse())
					Expect(err).To(MatchError(ContainSubstring("does not belong to this build's pipeline")))
					// Fail closed: never reached worker selection, so no pod
					// could ever reference agent-run-42.
					Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
				})
			})

			Context("when the run-ownership check errors", func() {
				BeforeEach(func() {
					fakeRunVerifier.RunBelongsToPipelineReturns(false, errors.New("db down"))
				})

				It("fails the step rather than proceeding without verification", func() {
					ok, err := step.Run(ctx, state)
					Expect(ok).To(BeFalse())
					Expect(err).To(MatchError(ContainSubstring("verify pipeline run 42 ownership")))
					Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
				})
			})

			Context("when no run verifier is wired", func() {
				It("injects no model-secret ref (fails closed)", func() {
					noVerifierStep := exec.NewAgentStep(
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
						exec.WithAgentBudgetChecker(fakeChecker),
						exec.WithAgentMetricsStore(fakeMetricsStore),
					)
					_, err := noVerifierStep.Run(ctx, state)
					Expect(err).ToNot(HaveOccurred())

					_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
					Expect(spec.SidecarSecretEnv).To(BeEmpty())
					Expect(spec.SecretEnv).ToNot(HaveKey("CLAUDE_CODE_OAUTH_TOKEN"))
				})
			})

			It("keeps the platform token off the main container", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).ToNot(HaveKey("AGENT_PRINCIPAL_TOKEN"))
				Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_PRINCIPAL_TOKEN=")))
			})
		})

		Context("on a platform run with no gateway/platform sidecars", func() {
			BeforeEach(func() {
				agentPlan.Env["AGENT_PIPELINE_RUN_ID"] = "42"
				agentPlan.Sidecars = nil
			})

			// The main container's model credential must not depend on sidecar
			// presence. A rendered step with no MCP sidecars still runs claude,
			// which authenticates through this secret-backed env var.
			It("still verifies run ownership and wires the main container token", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeRunVerifier.RunBelongsToPipelineCallCount()).To(Equal(1))

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).To(HaveKeyWithValue(
					"CLAUDE_CODE_OAUTH_TOKEN", vars.SecretRef{Name: "agent-run-42", Key: "anthropic-token"}))
			})

			Context("when the claimed run id does not belong to this build's pipeline", func() {
				BeforeEach(func() {
					fakeRunVerifier.RunBelongsToPipelineReturns(false, nil)
				})

				It("fails closed even though the main container needs the model credential", func() {
					ok, err := step.Run(ctx, state)
					Expect(ok).To(BeFalse())
					Expect(err).To(MatchError(ContainSubstring("does not belong to this build's pipeline")))
					Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
				})
			})
		})

		Context("without any ticket/run identity env (pure CI)", func() {
			BeforeEach(func() {
				delete(agentPlan.Env, "AGENT_TICKET_ID")
				delete(agentPlan.Env, "AGENT_PIPELINE_RUN_ID")
			})

			It("emits no run-secret env and performs no ticket admission", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeRunVerifier.RunBelongsToPipelineCallCount()).To(BeZero())
				Expect(fakeRunVerifier.TicketBelongsToRunCallCount()).To(BeZero())
				Expect(fakeChecker.StepSliceCallCount()).To(BeZero())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SidecarSecretEnv).To(BeEmpty())
				Expect(spec.SecretEnv).ToNot(HaveKey("CLAUDE_CODE_OAUTH_TOKEN"))
				Expect(spec.SidecarEnv["platform"]).To(ContainElement(HavePrefix("BUILD_ID=")))
				Expect(spec.SidecarEnv["platform"]).ToNot(ContainElement(HavePrefix("ATC_EXTERNAL_URL=")))
			})

			// --- pure-CI token path (§8.1) ---
			// A pure-CI agent step has no agent-run-<id> secret, but §8.1 still
			// supports it as a first-class concept. When the operator configures
			// a platform-level agent secret (web flag), the step wires
			// CLAUDE_CODE_OAUTH_TOKEN to it as a secretKeyRef — the token never
			// touches pipeline YAML or the pod's literal env.
			Context("with a platform-level agent secret configured", func() {
				var platformStep exec.Step

				JustBeforeEach(func() {
					platformStep = exec.NewAgentStep(
						planID, agentPlan,
						atc.ContainerLimits{}, atc.ContainerLimits{},
						stepMetadata, containerMetadata,
						fakePool, fakeStreamer, fakeDelegateFactory,
						0, agentImage,
						exec.WithAgentBudgetChecker(fakeChecker),
						exec.WithAgentMetricsStore(fakeMetricsStore),
						exec.WithAgentRunVerifier(fakeRunVerifier),
						exec.WithAgentPlatformTokenSecret("platform-agent-token"),
					)
				})

				It("wires CLAUDE_CODE_OAUTH_TOKEN from the platform secret as a secretKeyRef", func() {
					_, err := platformStep.Run(ctx, state)
					Expect(err).ToNot(HaveOccurred())

					_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
					Expect(spec.SecretEnv).To(HaveKeyWithValue(
						"CLAUDE_CODE_OAUTH_TOKEN", vars.SecretRef{Name: "platform-agent-token", Key: "anthropic-token"}))
					// secretKeyRef-only: never a literal env value.
					Expect(spec.Env).ToNot(ContainElement(HavePrefix("CLAUDE_CODE_OAUTH_TOKEN=")))
				})

				// Workflow-authored sidecar names are never credential grants.
				// Even a sidecar called gateway remains untrusted; only the main
				// agent process receives the configured model token.
				Context("with a gateway sidecar declared", func() {
					BeforeEach(func() {
						agentPlan.Sidecars = []atc.SidecarSource{
							{Config: &atc.SidecarConfig{Name: "platform", Image: "img:v1"}},
							{Config: &atc.SidecarConfig{Name: "gateway", Image: "img:v2"}},
						}
					})

					It("does not wire any credential into the gateway-named sidecar", func() {
						_, err := platformStep.Run(ctx, state)
						Expect(err).ToNot(HaveOccurred())

						_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
						Expect(spec.SidecarSecretEnv).To(BeEmpty())
					})
				})
			})
		})

		Context("on a platform RUN with a platform-level secret also configured", func() {
			BeforeEach(func() {
				agentPlan.Env["AGENT_PIPELINE_RUN_ID"] = "42"
			})

			It("prefers the per-run secret over the platform fallback", func() {
				runStep := exec.NewAgentStep(
					planID, agentPlan,
					atc.ContainerLimits{}, atc.ContainerLimits{},
					stepMetadata, containerMetadata,
					fakePool, fakeStreamer, fakeDelegateFactory,
					0, agentImage,
					exec.WithAgentBudgetChecker(fakeChecker),
					exec.WithAgentMetricsStore(fakeMetricsStore),
					exec.WithAgentRunVerifier(fakeRunVerifier),
					exec.WithAgentPlatformTokenSecret("platform-agent-token"),
				)
				_, err := runStep.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).To(HaveKeyWithValue(
					"CLAUDE_CODE_OAUTH_TOKEN", vars.SecretRef{Name: "agent-run-42", Key: "anthropic-token"}))
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
				Expect(spec.SidecarSecretEnv).To(BeEmpty())
			})
		})
	})

	Context("flight-recorder ingestion", func() {
		var resultsJSON string
		var eventLines []string
		var streamFullFlight func(context.Context, runtime.Artifact, string) (io.ReadCloser, error)
		var flightRunID snapshot.WorkflowRunID

		BeforeEach(func() {
			// The metric's execution identity: the durable workflow run (carried
			// on step.metadata, server-authenticated) and the planned function.
			flightRunID = snapshot.WorkflowRunID(4242)
			stepMetadata.WorkflowRunID = &flightRunID
			agentPlan.FunctionID = "review"

			resultsJSON = `{"schema_version":"1.0","status":"pass","confidence":1,"summary":"done","artifacts":[]}`
			eventLines = []string{
				`{"ts":"2026-07-10T12:00:00Z","event":"step.start","data":{"step_name":"write-spec","build_id":1,"plan_id":"p"}}`,
				`{"ts":"2026-07-10T12:00:01Z","event":"tool.call","data":{"tool":"run_tests"}}`,
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
			Expect(rm.EventCounts).To(HaveKeyWithValue("step.end", 1))
			Expect(rm.EventsArtifact).ToNot(BeEmpty())
			Expect(rm.Results).To(MatchJSON(resultsJSON))
		})

		Context("when events.ndjson has no step.end", func() {
			BeforeEach(func() {
				// events fixture truncated after tool.call
				eventLines = eventLines[:2]
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
					eventLines[0], // step.start
					oversized,
					eventLines[2], // cost.record
					eventLines[3], // step.end
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

		// review finding 2026-07-12 (F#9/F#11): a park-exit stream ends with
		// step.park, not step.end (contracts §3.2), so sawStepEnd is false. The
		// !sawStepEnd "crashed agent → error" override must NOT rewrite a parked
		// results.json to error — that silently defeats the parked status the
		// three-way taxonomy and migration 1773106061 were widened for. This
		// exercises the exec ingest layer that actually calls ThreeWayStatus.
		Context("when results.json is parked and the stream has no step.end", func() {
			BeforeEach(func() {
				resultsJSON = `{"schema_version":"1.0","status":"parked","confidence":1,"summary":"awaiting human","artifacts":[]}`
				eventLines = eventLines[:3] // step.start, tool.call, cost.record — no step.end
			})

			It("records a parked row, not error", func() {
				step.Run(ctx, state)
				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.Status).To(Equal("parked"))
				Expect(rm.Summary).To(Equal("awaiting human"))
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

			It("records an error row via InsertIfAbsent", func() {
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue()) // exit status still drives step success
				Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(BeZero())
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				rm := fakeMetricsStore.InsertIfAbsentArgsForCall(0)
				Expect(rm.Status).To(Equal("error"))
				Expect(rm.Summary).To(ContainSubstring("flight recorder"))
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
			// server-verified identity, never raw plan env (review finding)
			Expect(*entry.TicketID).To(Equal(7))
			Expect(*entry.PipelineRunID).To(Equal(42))
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

		Context("when workflow identity env is present", func() {
			BeforeEach(func() {
				agentPlan.Env["AGENT_WORKFLOW_NAME"] = "spec-writer"
				agentPlan.Env["AGENT_WORKFLOW_VERSION"] = "3"
				agentPlan.Env["AGENT_WORKFLOW_HASH"] = "abc123"
			})

			It("tags the metrics row and rides workflow attribution on ledger metadata", func() {
				// contracts addendum: writers that know their workflow MUST set
				// {"workflow": "<name>@<version>"} in ledger metadata so
				// group_by=workflow rollups can attribute spend.
				step.Run(ctx, state)
				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.WorkflowName).To(Equal("spec-writer"))
				Expect(*rm.WorkflowVersion).To(Equal(3))
				Expect(rm.WorkflowHash).To(Equal("abc123"))

				Expect(fakeChecker.RecordCallCount()).To(Equal(1))
				entry := fakeChecker.RecordArgsForCall(0)
				Expect(string(entry.Metadata)).To(MatchJSON(`{"workflow":"spec-writer@3"}`))
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
			partialEvents := strings.Join(eventLines[:2], "\n") + "\n"
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
		// before exit, understating SpentForTicket and loosening StepSlice
		// admission for every later step. The exec captures the claude CLI
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
					eventLines[2] = `{"ts":"2026-07-10T12:00:02Z","event":"cost.record","data":{"source":"agent_step","provider":"anthropic","model":"m1","input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_creation_tokens":0,"turns":0,"cost_usd":0}}`
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
					// SpentForTicket stays truthful and the next StepSlice
					// admission is not loosened — the pre-fix code charged $0.
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
					Expect(rm.Status).To(Equal("error")) // still a degraded error row
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
				eventLines[2] = `{"ts":"2026-07-10T12:00:02Z","event":"cost.record","data":{"source":"agent_step","provider":"anthropic","model":"m1","input_tokens":100,"output_tokens":50,"cache_read_tokens":1,"cache_creation_tokens":2,"turns":9,"cost_usd":0.50}}`
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
				eventLines[3] = `{"ts":"2026-07-10T12:01:01Z","event":"step.end","data":{"step_name":"write-spec","status":"failed","summary":"tests failed","wall_time_seconds":61,"cost_usd":0.42,"turns":9}}`
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
	})
})
