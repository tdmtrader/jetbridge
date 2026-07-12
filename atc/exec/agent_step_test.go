package exec_test

import (
	"context"
	"strconv"

	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/atc"
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

		agentPlan  atc.AgentPlan
		agentImage string

		state exec.RunState
		repo  *build.Repository

		step exec.Step

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

		state = exec.NewRunState(noopStepper, vars.StaticVariables{"branch": "main"})
		repo = state.ArtifactRepository()

		agentPlan = atc.AgentPlan{
			Name:           "write-spec",
			Prompt:         "do it",
			Model:          "m1",
			MaxTurns:       3,
			BudgetSliceUSD: 2.5,
			Outputs:        []string{"workspace"},
			Env: map[string]string{
				"AGENT_TICKET_ID": "7",
				"BASE_REF":        "main",
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
			exec.WithAgentBudgetChecker(fakeChecker),
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
		Expect(spec.Dir).To(Equal("some-artifact-root"))
		Expect(spec.Env).To(ContainElements(
			"AGENT_STEP_NAME=write-spec",
			"AGENT_PROMPT=do it",
			"AGENT_MODEL=m1",
			"AGENT_MAX_TURNS=3",
			"AGENT_TICKET_ID=7",
			"BASE_REF=main",
			"PLATFORM_MCP_URL=http://127.0.0.1:7781/mcp",
		))
		Expect(spec.Env).To(ContainElement(HavePrefix("AGENT_FLIGHT_DIR=")))
		Expect(spec.Outputs).To(HaveKey("workspace"))
		Expect(spec.Outputs).To(HaveKey("flight"))
		Expect(spec.Sidecars).To(HaveLen(1))
	})

	Context("when plan env carries ((var)) references", func() {
		BeforeEach(func() {
			agentPlan.Env["BASE_REF"] = "((branch))"
		})

		It("interpolates them through the build's var sources", func() {
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).To(ContainElement("BASE_REF=main"))
		})
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

	It("keeps the configured slice when the budget checker errors", func() {
		fakeChecker.StepSliceReturns(budget.Remaining{}, context.DeadlineExceeded)

		ok, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeTrue())

		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Env).To(ContainElement("AGENT_BUDGET_SLICE_USD=2.50"))
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
		Context("with platform and gateway sidecars on a platform run", func() {
			BeforeEach(func() {
				agentPlan.Env["AGENT_PIPELINE_RUN_ID"] = "42"
				agentPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{Name: "platform", Image: "img:v1"}},
					{Config: &atc.SidecarConfig{Name: "gateway", Image: "img:v2"}},
				}
				fakeChecker.StepSliceReturns(budget.Remaining{
					LimitUSD:     2.5,
					SpentUSD:     1.25,
					RemainingUSD: 1.25,
				}, nil)
			})

			It("populates per-sidecar env and secret refs for MCP sidecars (F15)", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SidecarEnv["platform"]).To(ContainElements(
					"ATC_EXTERNAL_URL="+stepMetadata.ExternalURL,
					"BUILD_ID="+strconv.Itoa(stepMetadata.BuildID),
					"AGENT_TICKET_ID=7",
					"AGENT_PIPELINE_RUN_ID=42",
				))
				Expect(spec.SidecarSecretEnv["platform"]).To(HaveKeyWithValue(
					"AGENT_PRINCIPAL_TOKEN", vars.SecretRef{Name: "agent-run-42", Key: "principal-token"}))
				Expect(spec.SidecarEnv["gateway"]).To(ContainElement(HavePrefix("AGENT_BUDGET_SLICE_USD=")))
				Expect(spec.SidecarSecretEnv["gateway"]).To(HaveKeyWithValue(
					"AGENT_PRINCIPAL_TOKEN", vars.SecretRef{Name: "agent-run-42", Key: "principal-token"}))
				Expect(spec.SidecarSecretEnv["gateway"]).To(HaveKeyWithValue(
					"CLAUDE_CODE_OAUTH_TOKEN", vars.SecretRef{Name: "agent-run-42", Key: "anthropic-token"}))
			})

			It("keeps the platform token off the main container", func() {
				_, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())

				_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
				Expect(spec.SecretEnv).ToNot(HaveKey("AGENT_PRINCIPAL_TOKEN"))
				Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_PRINCIPAL_TOKEN=")))
			})
		})

		It("emits no sidecar secret env without a pipeline-run id (pure CI)", func() {
			// plan.Env carries no AGENT_PIPELINE_RUN_ID
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.SidecarSecretEnv).To(BeEmpty())
			Expect(spec.SidecarEnv["platform"]).To(ContainElement(HavePrefix("ATC_EXTERNAL_URL=")))
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
	})
})
