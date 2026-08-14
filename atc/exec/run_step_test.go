package exec_test

import (
	"context"

	"code.cloudfoundry.org/clock"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type runDelegateFactory func(exec.RunState) exec.RunDelegate

func (factory runDelegateFactory) RunDelegate(state exec.RunState) exec.RunDelegate {
	return factory(state)
}

var _ = Describe("RunStep", func() {
	It("reports a successful lifecycle through the build delegate", func() {
		fixture := useExecDB()
		_, _, _, build := createExecJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)
		planID := atc.PlanID("some-run-plan")
		state := exec.NewRunState(noopStepper, vars.StaticVariables{})
		step := exec.NewRunStep(
			planID,
			atc.RunPlan{Message: "some-message", Type: "some-prototype"},
			runDelegateFactory(func(state exec.RunState) exec.RunDelegate {
				return engine.NewBuildStepDelegate(
					build,
					planID,
					state,
					clock.NewClock(),
					policy.NoopChecker{},
					atc.DisableRedactSecrets,
				)
			}),
		)

		succeeded, err := step.Run(context.Background(), state)
		Expect(err).NotTo(HaveOccurred())
		Expect(succeeded).To(BeTrue())
		Expect(execBuildEventTypes(fixture, build)).To(Equal([]atc.EventType{
			event.EventTypeInitialize,
			event.EventTypeLog,
			event.EventTypeStart,
			event.EventTypeLog,
			event.EventTypeFinish,
		}))
		Expect(execBuildFinishEvents(fixture, build)).To(ConsistOf(
			HaveField("Succeeded", true),
		))
	})

	It("writes its warning and prototype message to stderr", func() {
		fixture := useExecDB()
		_, _, _, build := createExecJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)
		planID := atc.PlanID("some-run-plan")
		state := exec.NewRunState(noopStepper, vars.StaticVariables{})
		step := exec.NewRunStep(
			planID,
			atc.RunPlan{Message: "some-message", Type: "some-prototype"},
			runDelegateFactory(func(state exec.RunState) exec.RunDelegate {
				return engine.NewBuildStepDelegate(
					build,
					planID,
					state,
					clock.NewClock(),
					policy.NoopChecker{},
					atc.DisableRedactSecrets,
				)
			}),
		)

		succeeded, err := step.Run(context.Background(), state)
		Expect(err).NotTo(HaveOccurred())
		Expect(succeeded).To(BeTrue())
		Expect(execBuildLog(fixture, build, event.OriginSourceStderr)).To(SatisfyAll(
			ContainSubstring("the run step is not yet implemented"),
			ContainSubstring("pretending to run some-message on prototype some-prototype..."),
		))
		Expect(execBuildLog(fixture, build, event.OriginSourceStdout)).To(BeEmpty())
	})
})
