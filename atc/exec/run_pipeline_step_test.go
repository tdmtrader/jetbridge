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

// digestConflictRefusal stands in for composition.DigestConflictError.
//
// atc/exec may not import the package that raises it -- that is the boundary
// architecture_test.go defends, and a test import breaches it exactly as a
// production one would -- so the spec exercises the path the real error takes
// instead: an error from beyond core that marks itself a refusal through
// runs.Refusal. A plain errors.New here would assert nothing, because the step
// no longer treats an unrecognized error as a refusal.
type digestConflictRefusal struct{}

func (digestConflictRefusal) Error() string {
	return "sealed input digest changed for an already-admitted call: recorded abc, presented def"
}

func (digestConflictRefusal) AdmissionRefusal() {}

// runPipelineRecordingChecker is a policy.Checker wired the way a deployment
// with a policy agent is -- it screens the run_pipeline action -- and it
// allows, so that what the step showed it can be read back.
type runPipelineRecordingChecker struct {
	policy.NoopChecker
	asked  []string
	inputs []policy.PolicyCheckInput
}

func (checker *runPipelineRecordingChecker) ShouldCheckAction(action string) bool {
	checker.asked = append(checker.asked, action)
	return action == policy.ActionRunPipeline
}

func (checker *runPipelineRecordingChecker) Check(input policy.PolicyCheckInput) (policy.PolicyCheckResult, error) {
	checker.inputs = append(checker.inputs, input)
	return policy.PassedPolicyCheck(), nil
}

// runPipelineDenyingChecker screens the same action and blocks it.
type runPipelineDenyingChecker struct {
	policy.NoopChecker
	messages []string
}

func (checker runPipelineDenyingChecker) ShouldCheckAction(action string) bool {
	return action == policy.ActionRunPipeline
}

func (checker runPipelineDenyingChecker) Check(policy.PolicyCheckInput) (policy.PolicyCheckResult, error) {
	return deniedPolicyCheck{messages: checker.messages}, nil
}

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
		policyChecker   policy.Checker
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

		// `((.:source-ref))` is a build-local var -- a load_var result or an
		// across value -- and it is the only kind the step resolves. A
		// credential reference is left alone; see "a param that names a
		// credential" below.
		state = exec.NewRunState(noopStepper, vars.StaticVariables{})
		state.AddLocalVar("source-ref", "deadbeef", false)

		fakeAdmitter = new(execfakes.FakeChildRunAdmitter)
		fakeAdmitter.AdmitChildRunReturns(exec.ChildRun{RunID: 42, Number: 7}, nil)

		policyChecker = policy.NoopChecker{}

		delegateFactory = runPipelineStepDelegateFactory(func(state exec.RunState) exec.RunPipelineStepDelegate {
			return engine.NewRunPipelineStepDelegate(realBuild, atc.PlanID(planID), state, clock.NewClock(), policyChecker)
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
			Params: atc.RunParams{"ref": "((.:source-ref))"},
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

	// A refusal is a fact about the config or about the template's state, so
	// every one of them fails the step with its message on stderr rather than
	// erroring it. runs.IsRefusal is what decides, and its own spec pins the
	// set; these assert that the step acts on the answer.
	for name, refusal := range map[string]error{
		"another team's template":                    runs.ErrUnauthorized,
		"no such template":                           runs.ErrTemplateNotFound,
		"not a template":                             runs.ErrNotATemplate,
		"an instanced pipeline":                      runs.ErrTemplateInstanced,
		"a paused template":                          runs.ErrTemplatePaused,
		"an archived template":                       runs.ErrTemplateArchived,
		"params the template refuses":                runs.InvalidParamsError{Err: errors.New("unknown parameter: nope")},
		"a template config that no longer validates": runs.TemplateConfigInvalidError{Err: errors.New("jobs: identifier is empty")},
		"a re-attach whose inputs moved":             digestConflictRefusal{},
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

	// Everything else is a fault, and a fault errors the step. Erroring is
	// what the engine's abort and error reporting read, and what LogError and
	// RetryError wrap; failing the step instead reports a broken platform as a
	// pipeline the author wrote wrong, and does it in a build log that is
	// anonymously readable on a public pipeline.
	for name, fault := range map[string]error{
		"an ambiguous principal":       runs.ErrPrincipalAmbiguous,
		"an invalid contract key":      runs.ErrInvalidInvocationKey,
		"a run id that names no row":   runs.ErrRunNotFound,
		"a transaction from elsewhere": runs.ForeignTransactionError{},
		"an operator role it will not honour": runs.CustomRolesInvalidError{
			Err: errors.New("viewer may create runs")},
		"a database that is not answering": errors.New("pool exhausted"),
	} {
		Context("when the admission faults: "+name, func() {
			BeforeEach(func() {
				fakeAdmitter.AdmitChildRunReturns(exec.ChildRun{}, fault)
			})

			It("errors the step, and writes nothing to the build log", func() {
				Expect(stepErr).To(MatchError(fault))
				Expect(stepOk).To(BeFalse())
				Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).To(BeEmpty())
				Expect(execBuildLog(fixture, realBuild, event.OriginSourceStdout)).To(BeEmpty())
			})

			// An errored step is finished by the engine, not here. Reporting
			// Finished(false) as well would render the build as one that ran
			// and failed.
			It("does not finish the step as failed", func() {
				Expect(execBuildFinishEvents(fixture, realBuild)).To(BeEmpty())
			})
		})
	}

	// The abort case, called out on its own because it is the one the old
	// behaviour got visibly wrong: atc/engine's finish path tests
	// errors.Is(err, context.Canceled) to record the build as aborted, and an
	// error the step swallowed can never reach it.
	Context("when the build is aborted during the admission", func() {
		BeforeEach(func() {
			fakeAdmitter.AdmitChildRunReturns(exec.ChildRun{}, context.Canceled)
		})

		It("returns the cancellation, intact enough for the engine to read", func() {
			Expect(stepOk).To(BeFalse())
			Expect(errors.Is(stepErr, context.Canceled)).To(BeTrue())
		})

		It("does not finish the step as failed", func() {
			Expect(execBuildFinishEvents(fixture, realBuild)).To(BeEmpty())
			Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).To(BeEmpty())
		})
	})

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

		It("carries the params with the build's own local vars resolved", func() {
			Expect(admittedRequest().Params).To(Equal(atc.RunParams{"ref": "deadbeef"}))
		})
	})

	// The run's params are persisted on the run header and served by the runs
	// API to anyone who can view the team. A credential reference must
	// therefore survive the step untouched: materialisation substitutes it
	// into the payload config verbatim, and the payload resolves it at build
	// time through the credential manager, exactly as a non-template pipeline
	// would. Interpolating it here would write the secret into the database.
	Describe("a param that names a credential", func() {
		BeforeEach(func() {
			rpPlan = &atc.RunPipelinePlan{
				Name: "version-upgrade",
				Params: atc.RunParams{
					"secret": "((vault/x))",
					"local":  "((.:source-ref))",
					"mixed":  "((.:source-ref))-((vault/x))",
				},
			}
		})

		It("reaches the admitter as a reference, while the local var arrives resolved", func() {
			Expect(stepErr).NotTo(HaveOccurred())
			Expect(admittedRequest().Params).To(Equal(atc.RunParams{
				"secret": "((vault/x))",
				"local":  "deadbeef",
				"mixed":  "deadbeef-((vault/x))",
			}))
		})
	})

	Describe("the input digest", func() {
		// The digest seals the call as the run records it: the references the
		// author wrote, plus the build's own local values. It is taken after
		// that resolution, so a build whose `((.:name))` resolved differently
		// is a different call.
		digestFor := func(plan atc.RunPipelinePlan, localVars map[string]any) string {
			GinkgoHelper()
			admitter := new(execfakes.FakeChildRunAdmitter)
			admitter.AdmitChildRunReturns(exec.ChildRun{RunID: 42, Number: 7}, nil)

			digestState := exec.NewRunState(noopStepper, vars.StaticVariables{})
			for name, value := range localVars {
				digestState.AddLocalVar(name, value, false)
			}

			ok, err := exec.NewRunPipelineStep(
				atc.PlanID(planID),
				plan,
				stepMetadata,
				delegateFactory,
				admitter,
			).Run(ctx, digestState)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, request := admitter.AdmitChildRunArgsForCall(0)
			return request.InputDigest
		}

		localVars := map[string]any{"source-ref": "deadbeef"}

		It("does not depend on a local var still being written as a reference", func() {
			Expect(admittedRequest().InputDigest).To(Equal(digestFor(
				atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "deadbeef"}},
				localVars)))
		})

		// A credential reference is not resolved, so rotating the secret it
		// names cannot move the digest -- the same rule a pipeline's config
		// hash follows. If it could, a rerun of a build would be refused for a
		// change its author never made.
		It("is unmoved by what a credential reference resolves to", func() {
			referencing := atc.RunPipelinePlan{
				Name:   "version-upgrade",
				Params: atc.RunParams{"ref": "((vault/x))"},
			}

			Expect(digestFor(referencing, map[string]any{"unrelated": "before"})).
				To(Equal(digestFor(referencing, map[string]any{"unrelated": "after"})))
		})

		It("is not the digest of the secret a reference names", func() {
			Expect(digestFor(
				atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "((vault/x))"}},
				localVars,
			)).NotTo(Equal(digestFor(
				atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "deadbeef"}},
				localVars)))
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

			Expect(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: forwards}, localVars)).
				To(Equal(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: backwards}, localVars)))
		})

		It("changes when a param value changes", func() {
			Expect(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "deadbeef"}}, localVars)).
				NotTo(Equal(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "cafebabe"}}, localVars)))
		})

		It("changes when the template changes", func() {
			Expect(digestFor(atc.RunPipelinePlan{Name: "version-upgrade", Params: atc.RunParams{"ref": "deadbeef"}}, localVars)).
				NotTo(Equal(digestFor(atc.RunPipelinePlan{Name: "other-template", Params: atc.RunParams{"ref": "deadbeef"}}, localVars)))
		})
	})

	Context("when a param names a build-local var that does not resolve", func() {
		BeforeEach(func() {
			rpPlan = &atc.RunPipelinePlan{
				Name:   "version-upgrade",
				Params: atc.RunParams{"ref": "((.:nope))"},
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

	// Creating a run over the API is screened by the policy wrappa. A build
	// creating one reaches no route, so without this the agent that a
	// deployment installed to govern run creation would never see a
	// build-initiated run at all.
	Describe("the policy check", func() {
		var checker *runPipelineRecordingChecker

		BeforeEach(func() {
			checker = &runPipelineRecordingChecker{}
			policyChecker = checker
		})

		It("screens the run_pipeline action", func() {
			Expect(checker.asked).To(Equal([]string{policy.ActionRunPipeline}))
		})

		// The agent is shown the call as the run will record it: the
		// references the author wrote, with the calling build's own local vars
		// resolved. It is not shown credential values, because the run never
		// holds any -- the same deal set_pipeline's check offers.
		It("shows the agent the call as it will be recorded", func() {
			Expect(checker.inputs).To(HaveLen(1))

			input := checker.inputs[0]
			Expect(input.Action).To(Equal(policy.ActionRunPipeline))
			Expect(input.Team).To(Equal("some-team"))
			Expect(input.Pipeline).To(Equal("parent-pipeline"))
			Expect(input.Data).To(Equal(exec.RunPipelinePolicyData{
				Team:     "some-team",
				Pipeline: "version-upgrade",
				Params:   atc.RunParams{"ref": "deadbeef"},
			}))
		})

		Context("when a param names a credential", func() {
			BeforeEach(func() {
				rpPlan = &atc.RunPipelinePlan{
					Name:   "version-upgrade",
					Params: atc.RunParams{"ref": "((vault/x))"},
				}
			})

			It("shows the agent the reference, never the secret", func() {
				Expect(checker.inputs).To(HaveLen(1))
				Expect(checker.inputs[0].Data).To(Equal(exec.RunPipelinePolicyData{
					Team:     "some-team",
					Pipeline: "version-upgrade",
					Params:   atc.RunParams{"ref": "((vault/x))"},
				}))
			})
		})

		It("admits once the agent allows", func() {
			Expect(stepErr).NotTo(HaveOccurred())
			Expect(stepOk).To(BeTrue())
			Expect(fakeAdmitter.AdmitChildRunCallCount()).To(Equal(1))
		})

		Context("when the agent blocks the run", func() {
			BeforeEach(func() {
				policyChecker = runPipelineDenyingChecker{messages: []string{"policy-check-error"}}
			})

			// A blocked run errors the step rather than failing it, exactly as
			// set_pipeline's does: the build did not fail on its own terms, it
			// was stopped by the operator's policy.
			It("errors the step", func() {
				Expect(stepOk).To(BeFalse())
				Expect(stepErr).To(MatchError(policy.PolicyCheckNotPass{
					Messages: []string{"policy-check-error"},
				}))
			})

			It("admits nothing", func() {
				Expect(fakeAdmitter.AdmitChildRunCallCount()).To(BeZero())
			})
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
