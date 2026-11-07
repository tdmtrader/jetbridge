package landing_test

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/landing"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("The landing queue with gates", func() {
	var ctx context.Context
	const creator = "landing-queue/landing-team/trunk"
	shaA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	submit := func(id, sha string) {
		GinkgoHelper()
		_, err := queues.Submit(ctx, queue.ID, atc.LandingSubmission{ID: id, Commit: sha}, "someone")
		Expect(err).NotTo(HaveOccurred())
	}
	pass := func() {
		GinkgoHelper()
		Expect(engine.Run(ctx)).To(Succeed())
	}
	// gateTemplate is a template shaped like the deploy/ gates: the queue's
	// three params and the composition run input.
	gateTemplate := func(name string) {
		GinkgoHelper()
		savePipeline(name, atc.Config{Template: true,
			Params: []atc.ParamSchema{{Name: "repository", Type: atc.ParamTypeString, Required: true}, {Name: "trunk", Type: atc.ParamTypeString, Required: true}, {Name: "entries", Type: atc.ParamTypeString, Required: true}},
			Jobs: atc.JobConfigs{{Name: "gate", PlanSequence: []atc.Step{
				{Config: &atc.TaskStep{Name: "gate", TaskID: uuid.NewString(),
					RunInputs: []atc.RunInput{{Name: "composition", Input: "composition"}},
					Config:    &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Inputs: []atc.TaskInputConfig{{Name: "composition"}}}}},
			}}}})
	}
	gateRun := func(template string, number int) int {
		GinkgoHelper()
		return runIDByNumber(template, number)
	}
	runsOf := func(template string) int {
		GinkgoHelper()
		var n int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_runs r JOIN pipelines p ON p.id = r.template_pipeline_id WHERE p.name = $1`, template).Scan(&n)).To(Succeed())
		return n
	}
	cancelRequested := func(runID int) bool {
		GinkgoHelper()
		var requested bool
		Expect(dbConn.QueryRow(`SELECT cancel_requested_at IS NOT NULL FROM pipeline_runs WHERE id = $1`, runID).Scan(&requested)).To(Succeed())
		return requested
	}
	status := func() atc.LandingQueueStatus {
		GinkgoHelper()
		reloaded, found, err := queues.Queue(ctx, team.ID(), "trunk")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		queue = reloaded
		status, err := queues.Status(ctx, queue)
		Expect(err).NotTo(HaveOccurred())
		return status
	}
	gatesOf := func(id string) []atc.LandingGate {
		GinkgoHelper()
		for _, e := range status().Entries {
			if e.ID == id {
				return e.Gates
			}
		}
		Fail("no entry " + id)
		return nil
	}
	// composed submits fix-1 and finishes its compose Run with a bindable
	// composition, then passes once: the gates are admitted.
	composed := func() int {
		GinkgoHelper()
		submit("fix-1", shaA)
		pass()
		composeRun := runIDByNumber("landing-compose", 1)
		finishRun(composeRun, atc.RunStatusSucceeded, "composition")
		pass()
		return composeRun
	}
	land := func() {
		GinkgoHelper()
		landRun := runIDByNumber("landing-land", 1)
		finishRun(landRun, atc.RunStatusSucceeded, "verdict")
		results.verdicts[landRun] = landing.Verdict{Outcome: landing.VerdictLanded, SHA: shaB}
		pass()
	}

	BeforeEach(func() {
		ctx = context.Background()
		gateTemplate("gate-tests")
		gateTemplate("gate-review")
		Expect(queues.SetQueue(ctx, team.ID(), "trunk", atc.LandingQueueConfig{
			Repository: "https://example.test/repo.git", Trunk: "core", Compose: "landing-compose", Land: "landing-land",
			Gates: []atc.LandingQueueGate{{Template: "gate-tests"}, {Template: "gate-review", Wait: "40m"}},
		})).To(Succeed())
		status()
	})

	It("After the compose Run succeeds, one Run per gate is admitted at once as the queue, each bound to the composition, and nothing lands yet", func() {
		composeRun := composed()
		Expect(runsOf("gate-tests")).To(Equal(1))
		Expect(runsOf("gate-review")).To(Equal(1))
		Expect(runsOf("landing-land")).To(BeZero())
		for _, template := range []string{"gate-tests", "gate-review"} {
			var bound int
			Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_run_inputs WHERE run_id = $1 AND source_run_id = $2`, gateRun(template, 1), composeRun).Scan(&bound)).To(Succeed())
			Expect(bound).To(Equal(1), "%s binds the compose Run's composition", template)
			var createdBy, params string
			Expect(dbConn.QueryRow(`SELECT created_by, params::text FROM pipeline_runs WHERE id = $1`, gateRun(template, 1)).Scan(&createdBy, &params)).To(Succeed())
			Expect(createdBy).To(Equal(creator))
			Expect(params).To(ContainSubstring("fix-1=" + shaA))
		}
		Expect(gatesOf("fix-1")).To(Equal([]atc.LandingGate{
			{Template: "gate-tests", Run: 1, Verdict: atc.LandingGatePending, Attempts: 1},
			{Template: "gate-review", Run: 1, Verdict: atc.LandingGatePending, Attempts: 1},
		}))
		pass()
		Expect(runCount(creator)).To(Equal(3), "running gates admit nothing more")
	})

	It("When every gate passes the candidate lands, and not before", func() {
		composed()
		finishRun(gateRun("gate-tests", 1), atc.RunStatusSucceeded)
		pass()
		Expect(runsOf("landing-land")).To(BeZero(), "one gate is still running")
		Expect(gatesOf("fix-1")[0].Verdict).To(Equal(atc.LandingGatePassed))
		finishRun(gateRun("gate-review", 1), atc.RunStatusSucceeded)
		pass()
		Expect(runsOf("landing-land")).To(Equal(1))
		Expect(entry("fix-1").LandRun).To(Equal(1))
		land()
		Expect(entry("fix-1").State).To(Equal(atc.LandingEntryLanded))
		Expect(runCount(creator)).To(Equal(4))
	})

	It("A gate Run that fails ejects the entry naming the gate and its Run, stops the other gate, and the queue goes on", func() {
		composed()
		submit("fix-2", shaB)
		finishRun(gateRun("gate-review", 1), atc.RunStatusFailed)
		pass()
		view := entry("fix-1")
		Expect(view.State).To(Equal(atc.LandingEntryEjected))
		Expect(view.Reason).To(Equal("gate gate-review Run #1 failed"))
		Expect(cancelRequested(gateRun("gate-tests", 1))).To(BeTrue(), "the other gate's verdict no longer counts")
		Expect(gatesOf("fix-1")[1].Verdict).To(Equal(atc.LandingGateFailed))
		Expect(runsOf("landing-land")).To(BeZero())
		Expect(entry("fix-2").State).To(Equal(atc.LandingEntryInFlight))
		Expect(entry("fix-2").ComposeRun).To(Equal(2))
	})

	DescribeTable("A gate Run with no verdict gets one more Run, and a second no verdict pauses the queue until it is resumed",
		func(noVerdict func(runID int), says string) {
			composed()
			submit("fix-2", shaB)
			first := gateRun("gate-tests", 1)
			noVerdict(first)
			pass()
			Expect(runsOf("gate-tests")).To(Equal(2), "one retry")
			gates := gatesOf("fix-1")
			Expect(gates[0].Run).To(Equal(2))
			Expect(gates[0].Attempts).To(Equal(2))
			Expect(gates[0].Verdict).To(Equal(atc.LandingGatePending))
			Expect(gates[0].LastError).To(ContainSubstring("gate gate-tests Run #1 " + says))
			Expect(gates[0].LastError).To(ContainSubstring("retrying once"))

			noVerdict(gateRun("gate-tests", 2))
			pass()
			current := status()
			Expect(current.Paused).To(BeTrue())
			Expect(current.PauseReason).To(ContainSubstring("gate gate-tests Run #2 " + says))
			Expect(current.PauseReason).To(ContainSubstring("paused"))
			Expect(gatesOf("fix-1")[0].Verdict).To(Equal(atc.LandingGateNoVerdict))
			Expect(entry("fix-1").State).To(Equal(atc.LandingEntryInFlight), "no verdict is never an ejection")

			finishRun(gateRun("gate-review", 1), atc.RunStatusSucceeded)
			before := runCount(creator)
			pass()
			pass()
			Expect(runCount(creator)).To(Equal(before), "a paused queue admits nothing")
			Expect(gatesOf("fix-1")[1].Verdict).To(Equal(atc.LandingGatePending), "and settles nothing")
			Expect(entry("fix-2").State).To(Equal(atc.LandingEntryQueued))

			resumed, err := queues.Resume(ctx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(resumed).To(BeTrue())
			pass()
			Expect(status().Paused).To(BeFalse())
			Expect(runsOf("gate-tests")).To(Equal(3), "resuming gives the gate a fresh Run")
			Expect(gatesOf("fix-1")[1].Verdict).To(Equal(atc.LandingGatePassed))
			finishRun(gateRun("gate-tests", 3), atc.RunStatusSucceeded)
			pass()
			land()
			Expect(entry("fix-1").State).To(Equal(atc.LandingEntryLanded))
			Expect(entry("fix-2").State).To(Equal(atc.LandingEntryInFlight))
		},
		Entry("errored", func(runID int) { finishRun(runID, atc.RunStatusErrored) }, "errored"),
		Entry("aborted", func(runID int) { finishRun(runID, atc.RunStatusAborted) }, "aborted"),
		Entry("past its wait", func(runID int) {
			_, err := dbConn.Exec(`UPDATE landing_gates SET admitted_at = now() - interval '46 minutes' WHERE run_id = $1`, runID)
			Expect(err).NotTo(HaveOccurred())
		}, "did not finish within 45m0s"),
	)

	It("A gate Run past its wait is asked to stop, and its late outcome counts for nothing", func() {
		composed()
		first := gateRun("gate-review", 1)
		_, err := dbConn.Exec(`UPDATE landing_gates SET admitted_at = now() - interval '41 minutes' WHERE run_id = $1`, first)
		Expect(err).NotTo(HaveOccurred())
		pass()
		Expect(cancelRequested(first)).To(BeTrue())
		Expect(gatesOf("fix-1")[1].LastError).To(ContainSubstring("did not finish within 40m0s"))
		finishRun(first, atc.RunStatusAborted)
		finishRun(gateRun("gate-tests", 1), atc.RunStatusSucceeded)
		pass()
		Expect(runsOf("landing-land")).To(BeZero(), "the retry's verdict is the one that counts")
		Expect(gatesOf("fix-1")[1].Verdict).To(Equal(atc.LandingGatePending))
	})

	It("A composition that is no longer bindable when the gates are admitted is composed again", func() {
		submit("fix-1", shaA)
		pass()
		finishRun(runIDByNumber("landing-compose", 1), atc.RunStatusSucceeded)
		pass()
		view := entry("fix-1")
		Expect(view.Reason).To(ContainSubstring("no longer bindable"))
		Expect(view.ComposeRun).To(Equal(2), "composed again in the same pass")
		Expect(runsOf("gate-tests")+runsOf("gate-review")).To(BeZero(), "no gate Run survives the rolled-back admission")
	})

	It("A composition that is no longer bindable when a gate is retried is composed again", func() {
		composeRun := composed()
		finishRun(gateRun("gate-tests", 1), atc.RunStatusErrored)
		_, err := dbConn.Exec(`UPDATE hangar_claims SET released_at = now() WHERE consumer_binding_id = $1`, fmt.Sprintf("run:%d/composition", composeRun))
		Expect(err).NotTo(HaveOccurred())
		pass()
		view := entry("fix-1")
		Expect(view.Reason).To(ContainSubstring("no longer bindable"))
		Expect(view.ComposeRun).To(Equal(2))
		Expect(runsOf("gate-tests")).To(Equal(1), "no retry for a candidate that moved")
	})

	It("A gate template without the composition run input is a reported defect, and the entry waits", func() {
		savePipeline("gate-short", atc.Config{Template: true,
			Params: []atc.ParamSchema{{Name: "repository", Type: atc.ParamTypeString}, {Name: "trunk", Type: atc.ParamTypeString}, {Name: "entries", Type: atc.ParamTypeString}},
			Jobs: atc.JobConfigs{{Name: "gate", PlanSequence: []atc.Step{
				{Config: &atc.TaskStep{Name: "gate", TaskID: uuid.NewString(), Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}},
			}}}})
		Expect(queues.SetQueue(ctx, team.ID(), "trunk", atc.LandingQueueConfig{
			Repository: "https://example.test/repo.git", Trunk: "core", Compose: "landing-compose", Land: "landing-land",
			Gates: []atc.LandingQueueGate{{Template: "gate-tests"}, {Template: "gate-short"}},
		})).To(Succeed())
		composed()
		Expect(engine.Logger.(*lagertest.TestLogger).Buffer()).To(gbytes.Say(`gate template \\"gate-short\\" does not declare the composition run input`))
		Expect(runsOf("gate-tests")+runsOf("gate-short")).To(BeZero(), "all of a candidate's gates are admitted together or none is")
		Expect(entry("fix-1").State).To(Equal(atc.LandingEntryInFlight))
		Expect(gatesOf("fix-1")).To(BeEmpty())
	})

	It("A restarted web node replays no gate outcome: no second gate Run, verdict or land Run", func() {
		composed()
		engine = newEngine()
		pass()
		Expect(runCount(creator)).To(Equal(3))
		finishRun(gateRun("gate-tests", 1), atc.RunStatusErrored)
		pass()
		engine = newEngine()
		pass()
		Expect(runsOf("gate-tests")).To(Equal(2), "the no verdict was settled once")
		finishRun(gateRun("gate-tests", 2), atc.RunStatusSucceeded)
		finishRun(gateRun("gate-review", 1), atc.RunStatusSucceeded)
		pass()
		engine = newEngine()
		pass()
		pass()
		Expect(runsOf("landing-land")).To(Equal(1))
		var gates int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM landing_gates`).Scan(&gates)).To(Succeed())
		Expect(gates).To(Equal(2))
		Expect(gatesOf("fix-1")[0].Attempts).To(Equal(2))
	})
})
