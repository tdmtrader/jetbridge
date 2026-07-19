package exec_test

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/harvest"
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

var _ = Describe("HarvestStep", func() {
	var (
		ctx    context.Context
		cancel func()

		stdoutBuf *gbytes.Buffer
		stderrBuf *gbytes.Buffer

		fakePool            *execfakes.FakePool
		fakeDelegate        *execfakes.FakeTaskDelegate
		fakeDelegateFactory *execfakes.FakeTaskDelegateFactory
		fakeRunVerifier     *execfakes.FakeAgentRunVerifier
		ticketsStore        *tickets.MemoryStore

		state exec.RunState
		repo  *build.Repository

		harvestPlan atc.HarvestPlan
		agentImage  string

		stepMetadata  exec.StepMetadata
		containerMeta db.ContainerMetadata
		planID        atc.PlanID
		expectedOwner db.ContainerOwner

		chosenWorker    *runtimetest.Worker
		chosenContainer *runtimetest.WorkerContainer
		exitStatus      int
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		stdoutBuf = gbytes.NewBuffer()
		stderrBuf = gbytes.NewBuffer()

		fakeDelegate = new(execfakes.FakeTaskDelegate)
		fakeDelegate.StdoutReturns(stdoutBuf)
		fakeDelegate.StderrReturns(stderrBuf)
		fakeDelegate.StartSpanReturns(ctx, tracing.NoopSpan)

		fakeDelegateFactory = new(execfakes.FakeTaskDelegateFactory)
		fakeDelegateFactory.TaskDelegateReturns(fakeDelegate)

		fakeRunVerifier = new(execfakes.FakeAgentRunVerifier)
		fakeRunVerifier.RunBelongsToPipelineReturns(true, nil)
		fakeRunVerifier.TicketBelongsToRunReturns(true, nil)

		ticketsStore = tickets.NewMemoryStore()

		state = exec.NewRunState(noopStepper, vars.StaticVariables{})
		repo = state.ArtifactRepository()
		repo.RegisterArtifact("workspace", runtimetest.NewVolume("workspace-volume"), false)

		stepMetadata = exec.StepMetadata{
			TeamID:     123,
			BuildID:    1234,
			JobID:      12345,
			PipelineID: 555,
		}
		containerMeta = db.ContainerMetadata{
			Type:             db.ContainerTypeTask,
			WorkingDirectory: "some-artifact-root",
		}
		planID = atc.PlanID("h1")
		expectedOwner = db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)

		harvestPlan = atc.HarvestPlan{
			Name: "harvest", Workspace: "workspace",
			Repo: "tdmtrader/concourse", TargetBranch: "main",
			TicketID: 42, PipelineRunID: 7, Branch: "agent/ticket-42", Push: true,
			Judge: &harvest.JudgeConfig{
				Rubric:        []harvest.RubricDimension{{Name: "correctness", Weight: 1, Guidance: "g"}},
				PassThreshold: 6.5,
			},
		}
		agentImage = "registry.home/agent-runner:v1"
		exitStatus = 0
	})

	AfterEach(func() {
		cancel()
	})

	// buildWorker wires a worker whose harvest process exits with exitStatus.
	buildWorker := func() {
		chosenWorker = runtimetest.NewWorker("worker").
			WithContainer(
				expectedOwner,
				runtimetest.NewContainer().WithProcess(
					runtime.ProcessSpec{ID: "harvest", Path: "harvest-runner", Dir: "some-artifact-root"},
					runtimetest.ProcessStub{ExitStatus: exitStatus},
				),
				[]runtime.VolumeMount{
					{Volume: runtimetest.NewVolume("workspace-volume"), MountPath: "some-artifact-root/workspace"},
					{Volume: runtimetest.NewVolume("flight-volume"), MountPath: "some-artifact-root/flight"},
				},
			)
		chosenContainer = chosenWorker.Containers[0]
		_ = chosenContainer
		fakePool = new(execfakes.FakePool)
		fakePool.FindOrSelectWorkerReturns(chosenWorker, nil)
	}

	newStep := func(plan atc.HarvestPlan, opts ...exec.HarvestStepOption) exec.Step {
		buildWorker()
		return exec.NewHarvestStep(
			planID, plan, stepMetadata, containerMeta,
			fakePool, fakeDelegateFactory, 0, agentImage,
			opts...,
		)
	}

	workerSpec := func() runtime.ContainerSpec {
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		return spec
	}

	Describe("judge admission (Slice E)", func() {
		It("admits a judge and carries it in HARVEST_CONFIG", func() {
			step := newStep(harvestPlan, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			var cfg harvest.Config
			for _, e := range workerSpec().Env {
				if strings.HasPrefix(e, "HARVEST_CONFIG=") {
					Expect(json.Unmarshal([]byte(strings.TrimPrefix(e, "HARVEST_CONFIG=")), &cfg)).To(Succeed())
				}
			}
			Expect(cfg.Judge).ToNot(BeNil())
			Expect(cfg.Judge.PassThreshold).To(Equal(6.5))
		})

		It("wires the PLATFORM token via SecretEnv only when a judge is declared", func() {
			step := newStep(harvestPlan, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
			step.Run(ctx, state)
			Expect(workerSpec().SecretEnv).To(HaveKeyWithValue("CLAUDE_CODE_OAUTH_TOKEN", vars.SecretRef{
				Name: "agent-platform-credential", Key: "anthropic-token",
			}))
		})

		It("omits the token for judgeless plans", func() {
			judgeless := harvestPlan
			judgeless.Judge = nil
			step := newStep(judgeless, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
			step.Run(ctx, state)
			Expect(workerSpec().SecretEnv).ToNot(HaveKey("CLAUDE_CODE_OAUTH_TOKEN"))
		})

		It("fails closed when a judge is declared but no platform secret is configured", func() {
			step := newStep(harvestPlan) // no WithHarvestPlatformTokenSecret
			_, err := step.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("--agent-platform-token-secret")))
		})

		It("fails closed on an invalid judge config", func() {
			bad := harvestPlan
			bad.Judge = &harvest.JudgeConfig{PassThreshold: 5} // empty rubric
			step := newStep(bad, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
			_, err := step.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("judge config invalid")))
		})

		It("declares the flight output and AGENT_FLIGHT_DIR", func() {
			step := newStep(harvestPlan, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
			step.Run(ctx, state)
			spec := workerSpec()
			Expect(spec.Outputs).To(HaveKey("flight"))
			Expect(spec.Outputs).To(HaveLen(1))
			Expect(spec.Env).To(ContainElement(HavePrefix("AGENT_FLIGHT_DIR=")))
		})

		It("still refuses dev_mcp (unchanged boundary)", func() {
			withDev := harvestPlan
			withDev.DevMCP = &atc.SidecarSource{}
			step := newStep(withDev, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
			_, err := step.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("dev_mcp")))
		})
	})

	Describe("landed v0.5 behavior (pinned while the harness exists)", func() {
		judgeless := func() atc.HarvestPlan {
			p := harvestPlan
			p.Judge = nil
			return p
		}

		It("errors without an agent image configured", func() {
			buildWorker()
			step := exec.NewHarvestStep(
				planID, judgeless(), stepMetadata, containerMeta,
				fakePool, fakeDelegateFactory, 0, "",
			)
			_, err := step.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("agent-step-image")))
		})

		It("admits full-scope gates and refuses any other scope", func() {
			p := judgeless()
			p.GatePolicy = harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "build", Scope: "full"}}}
			step := newStep(p)
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			p.GatePolicy = harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "affected"}}}
			step = newStep(p)
			_, err = step.Run(ctx, state)
			Expect(err).To(MatchError(ContainSubstring("scope")))
		})

		It("mounts the git credential secret on push", func() {
			step := newStep(judgeless())
			step.Run(ctx, state)
			spec := workerSpec()
			Expect(spec.SecretMounts).To(HaveLen(1))
			Expect(spec.SecretMounts[0].SecretName).To(Equal(harvest.GitCredSecretName("tdmtrader/concourse")))
		})

		It("errors on a missing workspace input", func() {
			p := judgeless()
			p.Workspace = "nope"
			step := newStep(p)
			_, err := step.Run(ctx, state)
			Expect(err).To(BeAssignableToTypeOf(exec.MissingInputsError{}))
		})

		It("walks the ticket to needs_review with the branch on exit 0", func() {
			id, err := ticketsStore.Create(&tickets.Ticket{
				Title: "t", Body: "b", Origin: "fly", Repo: "tdmtrader/concourse",
			})
			Expect(err).To(Succeed())
			Expect(id).To(Equal(1))
			runID := 7
			Expect(ticketsStore.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})).To(Succeed())
			Expect(ticketsStore.Transition(id, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{PipelineRunID: &runID})).To(Succeed())

			p := judgeless()
			p.TicketID = id
			step := newStep(p,
				exec.WithHarvestTicketsStore(ticketsStore),
				exec.WithHarvestRunVerifier(fakeRunVerifier),
			)
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			got, found, err := ticketsStore.Get(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.State).To(Equal(tickets.StateNeedsReview))
			Expect(got.Branch).To(Equal("agent/ticket-42"))
		})
	})
})
