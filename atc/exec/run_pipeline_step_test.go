package exec_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/vars"
)

var _ = Describe("RunPipelineStep", func() {
	var (
		ctx        context.Context
		cancel     func()
		testLogger *lagertest.TestLogger

		fixture         *execDBFixture
		currentTeam     db.Team
		currentPipeline db.Pipeline
		currentJob      db.Job
		realBuild       db.Build

		fakeAdmitter    *execfakes.FakeChildRunAdmitter
		delegateFactory exec.RunPipelineStepDelegateFactory

		state exec.RunState

		rpPlan       *atc.RunPipelinePlan
		stepMetadata exec.StepMetadata

		rpStep  exec.Step
		stepOk  bool
		stepErr error

		planID = "58"
	)

	BeforeEach(func() {
		testLogger = lagertest.NewTestLogger("run-pipeline-action-test")
		ctx, cancel = context.WithCancel(context.Background())
		ctx = lagerctx.NewContext(ctx, testLogger)

		fixture = useExecDB()
		currentTeam, currentPipeline, currentJob, realBuild = createExecJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "parent-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)

		state = exec.NewRunState(noopStepper, vars.StaticVariables{
			"source-ref": "deadbeef",
		})

		fakeAdmitter = new(execfakes.FakeChildRunAdmitter)
		fakeAdmitter.AdmitChildRunReturns(exec.ChildRun{RunID: 42, Number: 7}, nil)

		delegateFactory = runPipelineStepDelegateFactory(func(state exec.RunState) exec.RunPipelineStepDelegate {
			return engine.NewRunPipelineStepDelegate(realBuild, atc.PlanID(planID), state, clock.NewClock(), policy.NoopChecker{})
		})

		stepMetadata = exec.StepMetadata{
			TeamID:       currentTeam.ID(),
			TeamName:     currentTeam.Name(),
			JobID:        currentJob.ID(),
			JobName:      currentJob.Name(),
			BuildID:      realBuild.ID(),
			BuildName:    realBuild.Name(),
			PipelineID:   currentPipeline.ID(),
			PipelineName: currentPipeline.Name(),
			ExternalURL:  "https://ci.example.com",
		}

		rpPlan = &atc.RunPipelinePlan{
			Name:   "version-upgrade",
			Params: atc.RunParams{"ref": "((source-ref))"},
		}
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		rpStep = exec.NewRunPipelineStep(
			atc.PlanID(planID),
			*rpPlan,
			stepMetadata,
			delegateFactory,
			fakeAdmitter,
		)

		stepOk, stepErr = rpStep.Run(ctx, state)
	})

	admittedRequest := func() exec.ChildRunRequest {
		GinkgoHelper()
		Expect(fakeAdmitter.AdmitChildRunCallCount()).To(Equal(1))
		_, request := fakeAdmitter.AdmitChildRunArgsForCall(0)
		return request
	}

	Describe("a run the call admitted", func() {
		It("succeeds", func() {
			Expect(stepErr).NotTo(HaveOccurred())
			Expect(stepOk).To(BeTrue())
			Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
			Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeTrue())
		})

		It("names the run and its URL on stdout", func() {
			Expect(execBuildLog(fixture, realBuild, event.OriginSourceStdout)).To(Equal(
				"admitted run #7 of some-team/version-upgrade\n" +
					"https://ci.example.com/teams/some-team/pipelines/version-upgrade/runs/7\n"))
		})

		It("writes nothing to stderr", func() {
			Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).To(BeEmpty())
		})
	})

	Describe("a run the call re-attached to", func() {
		BeforeEach(func() {
			fakeAdmitter.AdmitChildRunReturns(exec.ChildRun{RunID: 42, Number: 7, Replayed: true}, nil)
		})

		It("says it re-attached rather than admitted", func() {
			Expect(stepOk).To(BeTrue())
			Expect(execBuildLog(fixture, realBuild, event.OriginSourceStdout)).To(Equal(
				"re-attached to run #7 of some-team/version-upgrade\n" +
					"https://ci.example.com/teams/some-team/pipelines/version-upgrade/runs/7\n"))
		})
	})

	// Every refusal is a fact about the config or about the template's state,
	// so every one of them fails the step with its message on stderr rather
	// than erroring it. The digest conflict is stated as a plain error here
	// because atc/exec may not import the package that raises it -- that is
	// the boundary architecture_test.go defends, and a test import breaches it
	// exactly as a production one would.
	for name, refusal := range map[string]error{
		"another team's template":     runs.ErrUnauthorized,
		"no such template":            runs.ErrTemplateNotFound,
		"not a template":              runs.ErrNotATemplate,
		"a paused template":           runs.ErrTemplatePaused,
		"an archived template":        runs.ErrTemplateArchived,
		"params the template refuses": runs.InvalidParamsError{Err: errors.New("unknown parameter: nope")},
		"a re-attach whose inputs moved": errors.New(
			"recorded input digest abc does not match presented digest def"),
	} {
		Context("when the port refuses: "+name, func() {
			BeforeEach(func() {
				fakeAdmitter.AdmitChildRunReturns(exec.ChildRun{}, refusal)
			})

			It("fails the step with the refusal on stderr, and does not error it", func() {
				Expect(stepErr).NotTo(HaveOccurred())
				Expect(stepOk).To(BeFalse())
				Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).
					To(Equal(refusal.Error() + "\n"))
				Expect(execBuildLog(fixture, realBuild, event.OriginSourceStdout)).To(BeEmpty())

				finishes := execBuildFinishEvents(fixture, realBuild)
				Expect(finishes).To(HaveLen(1))
				Expect(finishes[0].Succeeded).To(BeFalse())
			})
		})
	}

	Describe("the request the admitter receives", func() {
		It("carries the call's identity", func() {
			request := admittedRequest()
			Expect(request.BuildID).To(Equal(realBuild.ID()))
			Expect(request.PlanID).To(Equal(atc.PlanID(planID)))
		})

		It("names the template on the build's own team", func() {
			request := admittedRequest()
			Expect(request.Template).To(Equal(runs.TemplateRef{
				Team:     "some-team",
				Pipeline: atc.PipelineRef{Name: "version-upgrade"},
			}))
		})

		It("presents the build as the principal", func() {
			request := admittedRequest()
			Expect(request.Principal.Claims).To(BeNil())
			Expect(request.Principal.Build).To(Equal(&runs.BuildPrincipal{
				TeamName:     "some-team",
				PipelineName: "parent-pipeline",
				JobName:      "some-job",
				BuildName:    realBuild.Name(),
				BuildID:      realBuild.ID(),
			}))
		})

		It("carries the interpolated params", func() {
			Expect(admittedRequest().Params).To(Equal(atc.RunParams{"ref": "deadbeef"}))
		})
	})

	Describe("the input digest", func() {
		// The digest is taken after interpolation, so a build whose
		// credentials resolved differently is a different call. Recomputing it
		// from the uninterpolated plan would make it stable across exactly the
		// change that matters.
		digestFor := func(plan atc.RunPipelinePlan, variables vars.Variables) string {
			GinkgoHelper()
			admitter := new(execfakes.FakeChildRunAdmitter)
			admitter.AdmitChildRunReturns(exec.ChildRun{RunID: 42, Number: 7}, nil)

			ok, err := exec.NewRunPipelineStep(
				atc.PlanID(planID),
				plan,
				stepMetadata,
				delegateFactory,
				admitter,
			).Run(ctx, exec.NewRunState(noopStepper, variables))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, request := admitter.AdmitChildRunArgsForCall(0)
			return request.InputDigest
		}

		staticVars := vars.StaticVariables{"source-ref": "deadbeef"}

		It("is not the uninterpolated params", func() {
			Expect(admittedRequest().InputDigest).To(Equal(digestFor(
				atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "deadbeef"}},
				staticVars)))
		})

		// encoding/json sorts map keys, which is the whole reason the params
		// need no canonicalization of their own. Two maps with the same
		// entries built in opposite orders must digest the same.
		It("does not depend on the order the params were written in", func() {
			forwards := atc.RunParams{}
			forwards["alpha"] = "1"
			forwards["beta"] = "2"
			forwards["gamma"] = "3"

			backwards := atc.RunParams{}
			backwards["gamma"] = "3"
			backwards["beta"] = "2"
			backwards["alpha"] = "1"

			Expect(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: forwards}, staticVars)).
				To(Equal(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: backwards}, staticVars)))
		})

		It("changes when a param value changes", func() {
			Expect(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "deadbeef"}}, staticVars)).
				NotTo(Equal(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "cafebabe"}}, staticVars)))
		})

		It("changes when the template changes", func() {
			Expect(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "deadbeef"}}, staticVars)).
				NotTo(Equal(digestFor(atc.RunPipelinePlan{Name: "other-template", Params: atc.RunParams{"ref": "deadbeef"}}, staticVars)))
		})
	})

	Context("when a param references a var that does not resolve", func() {
		BeforeEach(func() {
			rpPlan = &atc.RunPipelinePlan{
				Name:   "version-upgrade",
				Params: atc.RunParams{"ref": "((nope))"},
			}
		})

		// Unlike a refusal, this never reached the port: the step could not
		// state the call at all, which is the errored case set_pipeline uses
		// for the same failure.
		It("errors the step and admits nothing", func() {
			Expect(stepErr).To(HaveOccurred())
			Expect(stepOk).To(BeFalse())
			Expect(fakeAdmitter.AdmitChildRunCallCount()).To(BeZero())
		})
	})

	Context("when the ATC has no external URL", func() {
		BeforeEach(func() {
			stepMetadata.ExternalURL = ""
		})

		It("names the run without pointing at half a URL", func() {
			Expect(execBuildLog(fixture, realBuild, event.OriginSourceStdout)).To(Equal(
				"admitted run #7 of some-team/version-upgrade\n"))
		})
	})
})
