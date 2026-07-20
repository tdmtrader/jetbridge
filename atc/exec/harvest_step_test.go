package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/agent/api/metrics/metricsfakes"
	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/outcomes/outcomesfakes"
	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/agent/harvest"
	"github.com/concourse/concourse/agent/schema"
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

// stubPlatformUserResolver is the §1.13 platform-user lookup fake: the
// harvest_judge ledger row must carry the platform service user.
type stubPlatformUserResolver struct {
	userID   int
	userName string
	found    bool
	err      error
	lastSub  string
}

func (s *stubPlatformUserResolver) UserBySub(sub string) (int, string, bool, error) {
	s.lastSub = sub
	return s.userID, s.userName, s.found, s.err
}

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
					// Attachable lets a second Run (the web-restart resume
					// specs) re-attach instead of needing a second ProcessDef.
					runtimetest.ProcessStub{ExitStatus: exitStatus, Attachable: true},
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

	Describe("flight ingestion (Slice F)", func() {
		var (
			fakeStreamer     *execfakes.FakeStreamer
			fakeMetricsStore *metricsfakes.FakeStore
			fakeChecker      *budgetfakes.FakeChecker
			reviewsStore     *reviews.MemoryStore
			platformResolver *stubPlatformUserResolver

			resultsJSON string
			eventLines  []string
			reviewJSON  string

			streamFullFlight func(context.Context, runtime.Artifact, string) (io.ReadCloser, error)

			step exec.Step
		)

		BeforeEach(func() {
			fakeStreamer = new(execfakes.FakeStreamer)
			fakeMetricsStore = new(metricsfakes.FakeStore)
			fakeChecker = new(budgetfakes.FakeChecker)
			reviewsStore = reviews.NewMemoryStore()
			platformResolver = &stubPlatformUserResolver{userID: 99, userName: "platform", found: true}

			// Task 9 shapes (NOT plan 09's).
			resultsJSON = `{"schema_version":"1.0","status":"pass","confidence":1,"summary":"1 gate(s) ok; judge 7.5/10 (pass); pushed agent/ticket-42","artifacts":[],"metadata":{"pushed_branch":"agent/ticket-42","head_sha":"abc123"}}`
			eventLines = []string{
				`{"ts":"2026-07-17T12:00:00Z","event":"step.start","data":{"step_name":"harvest","build_id":1234,"plan_id":"h1"}}`,
				`{"ts":"2026-07-17T12:00:01Z","event":"gate.result","data":{"gate":"test","scope":"full","status":"ok","duration_seconds":3}}`,
				`{"ts":"2026-07-17T12:00:02Z","event":"judge.score","data":{"rubric_hash":"h","dimensions":[{"name":"correctness","score":7.5,"max":10,"rationale":"r"}],"total":7.5,"max_total":10,"model":"m1"}}`,
				`{"ts":"2026-07-17T12:00:03Z","event":"cost.record","data":{"source":"harvest_judge","provider":"anthropic","model":"m1","input_tokens":10,"output_tokens":5,"turns":1,"cost_usd":0.2}}`,
				// A foreign-source cost.record (contract §5 invites other
				// producers) must NOT roll into the harvest step's own cost.
				`{"ts":"2026-07-17T12:00:04Z","event":"cost.record","data":{"source":"agent_step","provider":"anthropic","model":"m9","input_tokens":1000,"output_tokens":500,"turns":9,"cost_usd":5.0}}`,
				`{"ts":"2026-07-17T12:00:05Z","event":"push.done","data":{"branch":"agent/ticket-42","sha":"abc123"}}`,
				`{"ts":"2026-07-17T12:00:06Z","event":"step.end","data":{"step_name":"harvest","status":"ok","summary":"done","wall_time_seconds":42,"cost_usd":0.2,"turns":1}}`,
			}
			// The payload's own repo is deliberately WRONG: the pod-written
			// flight volume is attacker-writable, so the stored row must pin
			// the PLAN's repo, never the payload's (D9).
			reviewJSON = `{
				"schema_version": "harvest/1",
				"metadata": {"repo": "attacker/other-repo", "commit": "abc123", "branch": "agent/ticket-42", "agent_model": "m1", "duration_seconds": 42},
				"score": {"value": 7.5, "max": 10, "pass": true},
				"proven_issues": [],
				"observations": [{"id": "judge-correctness-1", "title": "unclear naming", "category": "correctness"}],
				"summary": "judge 7.5/10 (pass)",
				"gates": [],
				"judge": {"rubric_hash": "h", "dimensions": [{"name": "correctness", "score": 7.5, "max": 10, "rationale": "r"}], "total": 7.5, "max_total": 10, "pass": true, "model": "m1"}
			}`

			// The stub honors ctx cancellation, matching the production
			// Streamer: only the detached ingestCtx (F4) keeps reads live
			// once the step's timeout-scoped ctx expired.
			streamFullFlight = func(ctx context.Context, _ runtime.Artifact, path string) (io.ReadCloser, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				switch path {
				case "results.json":
					return io.NopCloser(strings.NewReader(resultsJSON)), nil
				case "events.ndjson":
					return io.NopCloser(strings.NewReader(strings.Join(eventLines, "\n") + "\n")), nil
				case "review.json":
					return io.NopCloser(strings.NewReader(reviewJSON)), nil
				default:
					return nil, fmt.Errorf("no fixture for %q", path)
				}
			}
			fakeStreamer.StreamFileStub = streamFullFlight

			// first ingestion of a (build_id, plan_id) is always a fresh insert
			fakeMetricsStore.UpsertReturningInsertedReturns(true, nil, nil)
		})

		JustBeforeEach(func() {
			step = newStep(harvestPlan,
				exec.WithHarvestPlatformTokenSecret("agent-platform-credential"),
				exec.WithHarvestRunVerifier(fakeRunVerifier),
				exec.WithHarvestStreamer(fakeStreamer),
				exec.WithHarvestMetricsStore(fakeMetricsStore),
				exec.WithHarvestReviewsStore(reviewsStore),
				exec.WithHarvestBudgetRecorder(fakeChecker),
				exec.WithHarvestPlatformUserResolver(platformResolver),
			)
		})

		It("upserts a RunMetrics row via UpsertReturningInserted before Run returns", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
			rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
			Expect(rm.Status).To(Equal("ok"))
			Expect(rm.BuildID).To(Equal(stepMetadata.BuildID))
			Expect(rm.PlanID).To(Equal(string(planID)))
			Expect(rm.StepName).To(Equal("harvest"))
			Expect(*rm.TicketID).To(Equal(42))
			Expect(*rm.PipelineRunID).To(Equal(7))
			// only harvest_judge-source cost.records roll up (§1.13)
			Expect(rm.CostUSD).To(BeNumerically("~", 0.2, 1e-9))
			Expect(rm.Usage.InputTokens).To(Equal(int64(10)))
			Expect(rm.Usage.OutputTokens).To(Equal(int64(5)))
			Expect(rm.Turns).To(Equal(1))
			Expect(rm.Model).To(Equal("m1"))
			Expect(rm.EventCounts).To(HaveKeyWithValue("gate.result", 1))
			Expect(rm.EventCounts).To(HaveKeyWithValue("judge.score", 1))
			Expect(rm.WallTimeSeconds).To(Equal(42)) // step.end wins over the wall clock
			Expect(rm.EventsArtifact).To(Equal("flight-volume"))
			Expect(rm.Results).To(MatchJSON(resultsJSON))
			Expect(rm.Summary).To(ContainSubstring("judge 7.5/10"))
		})

		It("upserts the evidence row with server-side trust pins (D9)", func() {
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			recs, err := reviewsStore.GetByBuild(stepMetadata.BuildID)
			Expect(err).ToNot(HaveOccurred())
			Expect(recs).To(HaveLen(1))
			rec := recs[0]
			Expect(rec.BuildID).To(Equal(stepMetadata.BuildID))
			// Repo comes from the PLAN — the pod-written payload's repo is
			// attacker-writable display data.
			Expect(rec.Repo).To(Equal("tdmtrader/concourse"))
			Expect(rec.CommitSha).To(Equal("abc123"))
			Expect(rec.Branch).To(Equal("agent/ticket-42"))
			Expect(rec.Score).To(Equal(7.5))
			Expect(rec.MaxScore).To(Equal(10.0))
			Expect(rec.Pass).To(BeTrue())
			Expect(rec.ProvenCount).To(BeZero())
			Expect(rec.ObservationCount).To(Equal(1))
			Expect(rec.AgentModel).To(Equal("m1"))
			Expect(rec.DurationSeconds).To(Equal(42))
			Expect(rec.Review).To(MatchJSON(reviewJSON))
			Expect(*rec.TicketID).To(Equal(42))
			Expect(*rec.PipelineRunID).To(Equal(7))
			Expect(rec.SubmittedBy).To(Equal("harvest"))
		})

		It("records the harvest_judge ledger entry with §1.13 platform attribution on a fresh insert", func() {
			step.Run(ctx, state)

			Expect(fakeChecker.RecordCallCount()).To(Equal(1))
			entry := fakeChecker.RecordArgsForCall(0)
			Expect(entry.Source).To(Equal(budget.SourceHarvestJudge))
			Expect(entry.Provider).To(Equal("anthropic"))
			Expect(entry.Model).To(Equal("m1"))
			Expect(entry.CostUSD).To(BeNumerically("~", 0.2, 1e-9))
			Expect(entry.InputTokens).To(Equal(int64(10)))
			Expect(entry.OutputTokens).To(Equal(int64(5)))
			Expect(entry.Turns).To(Equal(1))
			Expect(*entry.TicketID).To(Equal(42))
			Expect(*entry.PipelineRunID).To(Equal(7))
			Expect(entry.BuildID).To(Equal(stepMetadata.BuildID))
			Expect(entry.StepName).To(Equal("harvest"))
			// FROZEN §1.13: harvest_judge spend carries the platform user.
			Expect(platformResolver.lastSub).To(Equal("agent-platform"))
			Expect(*entry.UserID).To(Equal(99))
			Expect(entry.UserName).To(Equal("platform"))
		})

		It("records no ledger entry when the events carry no harvest_judge cost", func() {
			// drop the harvest_judge cost.record; the foreign one remains
			eventLines = append(eventLines[:3], eventLines[4:]...)
			step.Run(ctx, state)
			Expect(fakeMetricsStore.UpsertReturningInsertedArgsForCall(0).CostUSD).To(BeZero())
			Expect(fakeChecker.RecordCallCount()).To(BeZero())
		})

		It("still records the ledger, unattributed, when no platform user is resolvable", func() {
			platformResolver.found = false
			step.Run(ctx, state)
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))
			entry := fakeChecker.RecordArgsForCall(0)
			Expect(entry.UserID).To(BeNil())
			Expect(entry.UserName).To(BeEmpty())
		})

		// --- F3 regression: two-pass idempotency across a web-restart resume ---
		It("appends the ledger once across two Run invocations with identical flight data", func() {
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))

			// resume: the second ingestion UPDATES the row; prev carries the
			// identical counters, so ledgerCost == 0 and the append is skipped.
			fakeMetricsStore.UpsertReturningInsertedReturns(false, &schema.RunMetrics{
				CostUSD: 0.2,
				Usage:   schema.Usage{InputTokens: 10, OutputTokens: 5},
				Turns:   1,
			}, nil)
			_, err = step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeChecker.RecordCallCount()).To(Equal(1)) // charged ONCE
		})

		It("charges exactly the delta when the first pass inserted a zero-cost row", func() {
			// Run 1: the runner died before flushing cost.record/step.end.
			full := eventLines
			eventLines = eventLines[:2]
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeMetricsStore.UpsertReturningInsertedArgsForCall(0).CostUSD).To(BeZero())
			Expect(fakeChecker.RecordCallCount()).To(BeZero()) // nothing measured yet

			// Run 2: the resume reads the real spend; prev is the zero-cost row.
			eventLines = full
			fakeMetricsStore.UpsertReturningInsertedReturns(false, &schema.RunMetrics{}, nil)
			_, err = step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))
			entry := fakeChecker.RecordArgsForCall(0)
			Expect(entry.CostUSD).To(BeNumerically("~", 0.2, 1e-9))
			Expect(entry.InputTokens).To(Equal(int64(10)))
		})

		Context("when every flight stream errors (degraded ingestion)", func() {
			BeforeEach(func() {
				fakeStreamer.StreamFileStub = nil
				fakeStreamer.StreamFileReturns(nil, errors.New("no locator for volume"))
				fakeMetricsStore.InsertIfAbsentReturns(true, nil)
			})

			It("records a status=incomplete row via InsertIfAbsent and skips evidence + ledger", func() {
				// L-1 (#41): no flight file read at all is a missing RECORDING,
				// not a failed step — status=incomplete so DeriveOutcome fuses
				// it to amber "unrecorded" on a succeeded build (never red). It
				// still degrades through InsertIfAbsent (F24), never the upsert.
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue()) // exit status still drives step success

				// degraded ingestion never goes through the clobbering upsert (F24)
				Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(BeZero())
				Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))
				rm := fakeMetricsStore.InsertIfAbsentArgsForCall(0)
				Expect(rm.Status).To(Equal(schema.RunStatusIncomplete))
				Expect(rm.Summary).To(ContainSubstring("no flight output"))

				recs, err := reviewsStore.GetByBuild(stepMetadata.BuildID)
				Expect(err).ToNot(HaveOccurred())
				Expect(recs).To(BeEmpty())
				Expect(fakeChecker.RecordCallCount()).To(BeZero())
			})
		})

		// --- F4: the DeadlineExceeded path still ingests pre-timeout spend ---
		Context("when the step times out", func() {
			BeforeEach(func() {
				harvestPlan.Timeout = "50ms"
			})

			It("still ingests and records the ledger", func() {
				// the process blocks until the (real, tiny) deadline fires, so
				// at ingestion time the step ctx is GENUINELY expired and only
				// the detached ingestCtx keeps the ctx-honoring streamer live
				chosenContainer.ProcessDefs[0].Stub.Call = func(ctx context.Context, _ *runtimetest.Process) (runtime.ProcessResult, error) {
					<-ctx.Done()
					return runtime.ProcessResult{}, ctx.Err()
				}

				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeFalse()) // timeout ⇒ step not successful

				Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
				rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
				Expect(rm.CostUSD).To(BeNumerically("~", 0.2, 1e-9)) // pre-timeout spend preserved
				Expect(fakeChecker.RecordCallCount()).To(Equal(1))
			})
		})

		It("never fails the step when the metrics upsert errors", func() {
			fakeMetricsStore.UpsertReturningInsertedReturns(false, nil, errors.New("db down"))
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			// indeterminate upsert (inserted=false, prev=nil) skips the
			// ledger — cannot prove first insert (F3)
			Expect(fakeChecker.RecordCallCount()).To(BeZero())
		})

		It("strips linkage but still writes both rows when the verifier rejects the claim", func() {
			fakeRunVerifier.TicketBelongsToRunReturns(false, nil)
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
			rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
			Expect(rm.TicketID).To(BeNil())
			Expect(rm.PipelineRunID).To(BeNil())

			recs, err := reviewsStore.GetByBuild(stepMetadata.BuildID)
			Expect(err).ToNot(HaveOccurred())
			Expect(recs).To(HaveLen(1))
			Expect(recs[0].TicketID).To(BeNil())
			Expect(recs[0].PipelineRunID).To(BeNil())
			Expect(recs[0].SubmittedBy).To(Equal("harvest"))
		})
	})

	Describe("outcome seeding at push time (Task B4)", func() {
		var (
			fakeStreamer     *execfakes.FakeStreamer
			fakeMetricsStore *metricsfakes.FakeStore
			outcomesStore    *outcomes.MemoryStore

			resultsJSON string
		)

		BeforeEach(func() {
			fakeStreamer = new(execfakes.FakeStreamer)
			fakeMetricsStore = new(metricsfakes.FakeStore)
			outcomesStore = outcomes.NewMemoryStore()

			// The runner's §2.8.1 results document — the shas ride the
			// schema.Results metadata MAP (judge-evidence Task 9 shape).
			resultsJSON = `{"schema_version":"1.0","status":"pass","confidence":1,"summary":"pushed agent/ticket-42","artifacts":[],"metadata":{"pushed_branch":"agent/ticket-42","head_sha":"abc123","base_sha":"def456"}}`
			fakeStreamer.StreamFileStub = func(ctx context.Context, _ runtime.Artifact, path string) (io.ReadCloser, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if path == "results.json" {
					return io.NopCloser(strings.NewReader(resultsJSON)), nil
				}
				return nil, fmt.Errorf("no fixture for %q", path)
			}
			fakeMetricsStore.UpsertReturningInsertedReturns(true, nil, nil)
		})

		judgeless := func() atc.HarvestPlan {
			p := harvestPlan
			p.Judge = nil
			return p
		}

		seedingOpts := func(store outcomes.Store) []exec.HarvestStepOption {
			return []exec.HarvestStepOption{
				exec.WithHarvestRunVerifier(fakeRunVerifier),
				exec.WithHarvestStreamer(fakeStreamer),
				exec.WithHarvestMetricsStore(fakeMetricsStore),
				exec.WithHarvestOutcomesStore(store),
			}
		}

		It("seeds the outcome row with the run-report shas and the plan's repo/branch on exit 0 + push", func() {
			step := newStep(judgeless(), seedingOpts(outcomesStore)...)
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			row, found, err := outcomesStore.Get(42)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			// Repo/Branch are pinned from the PLAN; the shas are the
			// runner's authoritative head/base from results metadata.
			Expect(row.Repo).To(Equal("tdmtrader/concourse"))
			Expect(row.Branch).To(Equal("agent/ticket-42"))
			Expect(row.PushedSha).To(Equal("abc123"))
			Expect(row.BaseSha).To(Equal("def456"))
			Expect(row.MergeState).To(Equal(outcomes.MergeOpen))
		})

		It("still seeds the row (empty shas) when no parseable results document exists", func() {
			// A runner OOM after push but before the results flush leaves no
			// shas — the row is still created (it anchors created_at; the
			// watcher's branch-head fallback supplies the shas later).
			resultsJSON = "not json"
			step := newStep(judgeless(), seedingOpts(outcomesStore)...)
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			row, found, err := outcomesStore.Get(42)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(row.Branch).To(Equal("agent/ticket-42"))
			Expect(row.PushedSha).To(BeEmpty())
			Expect(row.BaseSha).To(BeEmpty())
		})

		It("seeds nothing on exit 1 (no push happened)", func() {
			exitStatus = 1
			step := newStep(judgeless(), seedingOpts(outcomesStore)...)
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse())

			_, found, err := outcomesStore.Get(42)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("seeds nothing when the plan does not push", func() {
			p := judgeless()
			p.Push = false
			step := newStep(p, seedingOpts(outcomesStore)...)
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, found, err := outcomesStore.Get(42)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("never seeds on an unverified ticket claim", func() {
			// Plan env is attacker-writable YAML — a raw TicketID claim must
			// not create an outcome row for a ticket this run never owned.
			fakeRunVerifier.TicketBelongsToRunReturns(false, nil)
			step := newStep(judgeless(), seedingOpts(outcomesStore)...)
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, found, err := outcomesStore.Get(42)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("transitions the ticket BEFORE seeding, and a failing store never fails the step", func() {
			id, err := ticketsStore.Create(&tickets.Ticket{
				Title: "t", Body: "b", Origin: "fly", Repo: "tdmtrader/concourse",
			})
			Expect(err).ToNot(HaveOccurred())
			runID := 7
			Expect(ticketsStore.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})).To(Succeed())
			Expect(ticketsStore.Transition(id, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{PipelineRunID: &runID})).To(Succeed())

			// The failing wrapper doubles as the call-order probe: at Ensure
			// time the ticket must ALREADY be in needs_review
			// (transition-first ordering is load-bearing, §1.11.1).
			fakeOutcomes := new(outcomesfakes.FakeStore)
			var stateAtEnsure tickets.State
			fakeOutcomes.EnsureStub = func(*outcomes.Outcome) error {
				got, found, getErr := ticketsStore.Get(id)
				Expect(getErr).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				stateAtEnsure = got.State
				return errors.New("outcomes db down")
			}

			p := judgeless()
			p.TicketID = id
			step := newStep(p, append(seedingOpts(fakeOutcomes),
				exec.WithHarvestTicketsStore(ticketsStore),
			)...)
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue()) // seeding failure is logged, never fatal

			Expect(fakeOutcomes.EnsureCallCount()).To(Equal(1))
			Expect(stateAtEnsure).To(Equal(tickets.StateNeedsReview))
		})
	})
})
