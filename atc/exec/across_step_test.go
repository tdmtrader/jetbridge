package exec_test

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func persistedAcrossSubstepValues(fixture *execDBFixture, build db.Build) [][]any {
	GinkgoHelper()
	var values [][]any
	for _, e := range execBuildEvents(fixture, build) {
		substeps, ok := e.(event.AcrossSubsteps)
		if !ok {
			continue
		}
		for _, raw := range substeps.Substeps {
			var substep struct {
				Values []any `json:"values"`
			}
			Expect(json.Unmarshal(*raw, &substep)).To(Succeed())
			values = append(values, substep.Values)
		}
	}
	return values
}

var _ = Describe("AcrossStep", func() {
	type vals [4]any

	var (
		ctx    context.Context
		cancel func()

		delegateFactory exec.BuildStepDelegateFactory

		fixture *execDBFixture
		dbBuild db.Build

		step exec.AcrossStep

		plan  atc.AcrossPlan
		state exec.RunState

		stepperCount        int64
		stepperFailOnCount  int64
		stepperPanicOnCount int64

		allVals []vals

		started   chan vals
		terminate map[vals]chan error

		stepMetadata = exec.StepMetadata{
			TeamID:       123,
			TeamName:     "some-team",
			BuildID:      42,
			BuildName:    "some-build",
			PipelineID:   4567,
			PipelineName: "some-pipeline",
		}
	)

	stepRun := func(succeeded bool) stepFunc {
		started := started
		terminate := terminate

		return func(ctx context.Context, childState exec.RunState) (bool, error) {
			defer GinkgoRecover()

			By("having the correct var values")
			values := vals{}
			for i, v := range plan.Vars {
				val, found, _ := childState.Get(vars.Reference{Source: ".", Path: v.Var})
				Expect(found).To(BeTrue(), "unset variable "+v.Var)
				values[i] = val
			}

			By("running with a child scope")
			Expect(childState.Parent()).To(Equal(state))

			started <- values
			if c, ok := terminate[values]; ok {
				select {
				case err := <-c:
					return false, err
				case <-ctx.Done():
					return false, ctx.Err()
				}
			}
			return succeeded, nil
		}
	}

	stepper := func(plan atc.Plan) exec.Step {
		curCount := atomic.AddInt64(&stepperCount, 1)

		panics := curCount == stepperPanicOnCount

		if panics {
			return stepFunc(func(_ context.Context, _ exec.RunState) (bool, error) {
				panic("something went wrong")
			})
		}

		successful := curCount != stepperFailOnCount
		return stepRun(successful)
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		ctx = lagerctx.NewContext(ctx, testLogger)

		state = exec.NewRunState(stepper, vars.StaticVariables{})

		fixture = useExecDB()
		_, _, _, dbBuild = createExecJobBuild(
			fixture,
			"across-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)

		delegateFactory = buildStepDelegateFactory(func(state exec.RunState) exec.BuildStepDelegate {
			return engine.NewBuildStepDelegate(dbBuild, "across-plan-id", state, clock.NewClock(), policy.NoopChecker{}, false)
		})

		plan.Vars = []atc.AcrossVar{
			{
				Var:         "var1",
				Values:      []any{"a1", "a2"},
				MaxInFlight: &atc.MaxInFlightConfig{All: true},
			},
			{
				Var:    "var2",
				Values: []any{"b1", "b2"},
			},
			{
				Var:         "var3",
				Values:      []any{"c1", "c2", "c3"},
				MaxInFlight: &atc.MaxInFlightConfig{Limit: 3},
			},
			{
				Var:    "var4",
				Values: []any{"d1", "d2"},
			},
		}
		stepperFailOnCount = -1
		stepperPanicOnCount = -1
		stepperCount = 0

		started = make(chan vals, 24)
		terminate = map[vals]chan error{}

		allVals = []vals{
			{"a1", "b1", "c1", "d1"},
			{"a1", "b1", "c1", "d2"},
			{"a1", "b1", "c2", "d1"},
			{"a1", "b1", "c2", "d2"},
			{"a1", "b1", "c3", "d1"},
			{"a1", "b1", "c3", "d2"},

			{"a1", "b2", "c1", "d1"},
			{"a1", "b2", "c1", "d2"},
			{"a1", "b2", "c2", "d1"},
			{"a1", "b2", "c2", "d2"},
			{"a1", "b2", "c3", "d1"},
			{"a1", "b2", "c3", "d2"},

			{"a2", "b1", "c1", "d1"},
			{"a2", "b1", "c1", "d2"},
			{"a2", "b1", "c2", "d1"},
			{"a2", "b1", "c2", "d2"},
			{"a2", "b1", "c3", "d1"},
			{"a2", "b1", "c3", "d2"},

			{"a2", "b2", "c1", "d1"},
			{"a2", "b2", "c1", "d2"},
			{"a2", "b2", "c2", "d1"},
			{"a2", "b2", "c2", "d2"},
			{"a2", "b2", "c3", "d1"},
			{"a2", "b2", "c3", "d2"},
		}
		plan.FailFast = false
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		step = exec.Across(
			plan,
			delegateFactory,
			stepMetadata,
		)
	})

	It("persists the step lifecycle events", func() {
		step.Run(ctx, state)

		Expect(execBuildEventTypes(fixture, dbBuild)).To(Equal([]atc.EventType{
			event.EventTypeInitialize,
			event.EventTypeStart,
			event.EventTypeAcrossSubsteps,
			event.EventTypeFinish,
		}))
	})

	Context("when a var shadows an existing local var", func() {
		BeforeEach(func() {
			state.AddLocalVar("var2", 123, false)
		})

		It("logs a warning to stderr", func() {
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			Expect(execBuildLog(fixture, dbBuild, event.OriginSourceStderr)).To(ContainSubstring("WARNING: across step shadows local var 'var2'"))
		})
	})

	It("correctly computes the combinations of var values", func() {
		step.Run(ctx, state)

		Expect(persistedAcrossSubstepValues(fixture, dbBuild)).To(Equal([][]any{
			{"a1", "b1", "c1", "d1"},
			{"a1", "b1", "c1", "d2"},
			{"a1", "b1", "c2", "d1"},
			{"a1", "b1", "c2", "d2"},
			{"a1", "b1", "c3", "d1"},
			{"a1", "b1", "c3", "d2"},

			{"a1", "b2", "c1", "d1"},
			{"a1", "b2", "c1", "d2"},
			{"a1", "b2", "c2", "d1"},
			{"a1", "b2", "c2", "d2"},
			{"a1", "b2", "c3", "d1"},
			{"a1", "b2", "c3", "d2"},

			{"a2", "b1", "c1", "d1"},
			{"a2", "b1", "c1", "d2"},
			{"a2", "b1", "c2", "d1"},
			{"a2", "b1", "c2", "d2"},
			{"a2", "b1", "c3", "d1"},
			{"a2", "b1", "c3", "d2"},

			{"a2", "b2", "c1", "d1"},
			{"a2", "b2", "c1", "d2"},
			{"a2", "b2", "c2", "d1"},
			{"a2", "b2", "c2", "d2"},
			{"a2", "b2", "c3", "d1"},
			{"a2", "b2", "c3", "d2"},
		}))
	})

	It("does not merge artifacts produced by across child scopes", func() {
		plan.Vars = []atc.AcrossVar{{Var: "var1", Values: []any{"only"}}}
		step = exec.Across(plan, delegateFactory, stepMetadata)
		state = exec.NewRunState(func(atc.Plan) exec.Step {
			return stepFunc(func(_ context.Context, childState exec.RunState) (bool, error) {
				childState.ArtifactRepository().RegisterArtifact(
					build.ArtifactName("across-output"),
					runtimetest.NewVolume("across-output"),
					false,
				)
				return true, nil
			})
		}, vars.StaticVariables{})

		succeeded, err := step.Run(ctx, state)

		Expect(err).NotTo(HaveOccurred())
		Expect(succeeded).To(BeTrue())
		Expect(state.ArtifactRepository().AsMap()).To(BeEmpty())
	})

	Describe("parallel execution", func() {
		BeforeEach(func() {
			for _, v := range allVals {
				terminate[v] = make(chan error, 1)
			}
		})

		It("steps are run in parallel according to the MaxInFlight for each var", func() {
			go step.Run(ctx, state)

			By("running the first stage")
			var receivedVals []vals
			for i := 0; i < 6; i++ {
				receivedVals = append(receivedVals, <-started)
			}
			Expect(receivedVals).To(ConsistOf(
				vals{"a1", "b1", "c1", "d1"},
				vals{"a1", "b1", "c2", "d1"},
				vals{"a1", "b1", "c3", "d1"},
				vals{"a2", "b1", "c1", "d1"},
				vals{"a2", "b1", "c2", "d1"},
				vals{"a2", "b1", "c3", "d1"},
			))
			Consistently(started).ShouldNot(Receive())

			By("the first stage completing successfully")
			for _, v := range receivedVals {
				terminate[v] <- nil
			}

			By("running the second stage")
			receivedVals = []vals{}
			for i := 0; i < 6; i++ {
				receivedVals = append(receivedVals, <-started)
			}
			Expect(receivedVals).To(ConsistOf(
				vals{"a1", "b1", "c1", "d2"},
				vals{"a1", "b1", "c2", "d2"},
				vals{"a1", "b1", "c3", "d2"},
				vals{"a2", "b1", "c1", "d2"},
				vals{"a2", "b1", "c2", "d2"},
				vals{"a2", "b1", "c3", "d2"},
			))
			Consistently(started).ShouldNot(Receive())

			By("the second stage completing successfully")
			for _, v := range receivedVals {
				terminate[v] <- nil
			}

			By("running the third stage")
			receivedVals = []vals{}
			for i := 0; i < 6; i++ {
				receivedVals = append(receivedVals, <-started)
			}
			Expect(receivedVals).To(ConsistOf(
				vals{"a1", "b2", "c1", "d1"},
				vals{"a1", "b2", "c2", "d1"},
				vals{"a1", "b2", "c3", "d1"},
				vals{"a2", "b2", "c1", "d1"},
				vals{"a2", "b2", "c2", "d1"},
				vals{"a2", "b2", "c3", "d1"},
			))
			Consistently(started).ShouldNot(Receive())

			By("the third stage completing successfully")
			for _, v := range receivedVals {
				terminate[v] <- nil
			}

			By("running the forth stage")
			receivedVals = []vals{}
			for i := 0; i < 6; i++ {
				receivedVals = append(receivedVals, <-started)
			}
			Expect(receivedVals).To(ConsistOf(
				vals{"a1", "b2", "c1", "d2"},
				vals{"a1", "b2", "c2", "d2"},
				vals{"a1", "b2", "c3", "d2"},
				vals{"a2", "b2", "c1", "d2"},
				vals{"a2", "b2", "c2", "d2"},
				vals{"a2", "b2", "c3", "d2"},
			))
			Consistently(started).ShouldNot(Receive())

			By("the forth stage completing successfully")
			for _, v := range receivedVals {
				terminate[v] <- nil
			}
		})

		Context("when fail fast is true", func() {
			BeforeEach(func() {
				plan.FailFast = true

				stepperFailOnCount = 2
			})

			It("stops running steps after a failure", func() {
				// Allow the failed step to terminate
				terminate[allVals[0]] <- nil

				By("running the step")
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeFalse())

				By("ensuring not all steps were started")
				Expect(started).ToNot(HaveLen(24))
			})
		})

		Context("when fail fast is false", func() {
			BeforeEach(func() {
				plan.FailFast = false

				stepperFailOnCount = 2
			})

			It("allows all steps to run before failing", func() {
				for _, v := range allVals {
					terminate[v] <- nil
				}

				By("running the step")
				ok, err := step.Run(ctx, state)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeFalse())

				By("ensuring all steps were run")
				Expect(started).To(HaveLen(24))
			})
		})
	})

	Describe("panic recovery", func() {
		Context("when one step panics", func() {
			BeforeEach(func() {
				stepperPanicOnCount = 2
			})

			It("handles it gracefully", func() {
				_, err := step.Run(ctx, state)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("something went wrong"))
			})
		})
	})
})
