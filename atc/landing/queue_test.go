package landing_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/landing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("The landing queue with no gate", func() {
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

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("A submitted change is composed, then landed, by Runs the queue admits as its team", func() {
		submit("fix-1", shaA)
		pass()
		view := entry("fix-1")
		Expect(view.State).To(Equal(atc.LandingEntryInFlight))
		Expect(view.ComposeRun).To(Equal(1))
		Expect(runCount(creator)).To(Equal(1))
		composeRun := runIDByNumber("landing-compose", 1)
		var params string
		Expect(dbConn.QueryRow(`SELECT params::text FROM pipeline_runs WHERE id = $1`, composeRun).Scan(&params)).To(Succeed())
		Expect(params).To(ContainSubstring("fix-1=" + shaA))

		pass()
		Expect(runCount(creator)).To(Equal(1), "a running compose Run admits nothing")

		finishRun(composeRun, atc.RunStatusSucceeded, "candidate", "manifest")
		pass()
		view = entry("fix-1")
		Expect(view.State).To(Equal(atc.LandingEntryInFlight))
		Expect(view.LandRun).To(Equal(1))
		landRun := runIDByNumber("landing-land", 1)
		var caused int
		Expect(dbConn.QueryRow(`SELECT caused_by_run FROM pipeline_runs WHERE id = $1`, landRun).Scan(&caused)).To(Succeed())
		Expect(caused).To(Equal(composeRun))
		var bound int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_run_inputs WHERE run_id = $1`, landRun).Scan(&bound)).To(Succeed())
		Expect(bound).To(Equal(2), "the land Run binds the manifest and the candidate")

		finishRun(landRun, atc.RunStatusSucceeded, "verdict")
		results.verdicts[landRun] = landing.Verdict{Outcome: landing.VerdictLanded, SHA: shaB}
		pass()
		view = entry("fix-1")
		Expect(view.State).To(Equal(atc.LandingEntryLanded))
		Expect(view.Reason).To(ContainSubstring("landed by Run"))
		Expect(view.ComposeRun).To(Equal(1))
		Expect(view.LandRun).To(Equal(1))
		pass()
		Expect(runCount(creator)).To(Equal(2), "a landed entry admits nothing more")
	})

	It("When the trunk moved under the land Run the entry is composed again on the new head and lands", func() {
		submit("fix-1", shaA)
		pass()
		finishRun(runIDByNumber("landing-compose", 1), atc.RunStatusSucceeded, "candidate", "manifest")
		pass()
		first := runIDByNumber("landing-land", 1)
		finishRun(first, atc.RunStatusSucceeded, "verdict")
		results.verdicts[first] = landing.Verdict{Outcome: landing.VerdictMoved}
		pass()
		view := entry("fix-1")
		Expect(view.Reason).To(ContainSubstring("trunk moved"))
		Expect(view.State).To(Equal(atc.LandingEntryInFlight), "composed again in the same pass")
		Expect(view.ComposeRun).To(Equal(2), "a new compose Run, not the old candidate")
		finishRun(runIDByNumber("landing-compose", 2), atc.RunStatusSucceeded, "candidate", "manifest")
		pass()
		second := runIDByNumber("landing-land", 2)
		finishRun(second, atc.RunStatusSucceeded, "verdict")
		results.verdicts[second] = landing.Verdict{Outcome: landing.VerdictLanded, SHA: shaB}
		pass()
		Expect(entry("fix-1").State).To(Equal(atc.LandingEntryLanded))
		Expect(runCount(creator)).To(Equal(4))
	})

	It("A compose Run that fails ejects the entry naming the Run, and the queue goes on to the next", func() {
		submit("fix-1", shaA)
		submit("fix-2", shaB)
		pass()
		finishRun(runIDByNumber("landing-compose", 1), atc.RunStatusFailed)
		pass()
		view := entry("fix-1")
		Expect(view.State).To(Equal(atc.LandingEntryEjected))
		Expect(view.Reason).To(ContainSubstring("compose Run"))
		Expect(view.Reason).To(ContainSubstring("failed"))
		Expect(entry("fix-2").State).To(Equal(atc.LandingEntryInFlight))
		Expect(entry("fix-2").ComposeRun).To(Equal(2))
	})

	It("A restarted web node between compose and land admits no second compose and no second land for the candidate", func() {
		submit("fix-1", shaA)
		pass()
		pass()
		engine = newEngine()
		pass()
		Expect(runCount(creator)).To(Equal(1))
		finishRun(runIDByNumber("landing-compose", 1), atc.RunStatusSucceeded, "candidate", "manifest")
		pass()
		engine = newEngine()
		pass()
		pass()
		Expect(runCount(creator)).To(Equal(2))
		var intents int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM landing_intents`).Scan(&intents)).To(Succeed())
		Expect(intents).To(Equal(1))
	})

	It("A land Run that did not land counts as a failed landing and the same candidate gets another land Run", func() {
		submit("fix-1", shaA)
		pass()
		finishRun(runIDByNumber("landing-compose", 1), atc.RunStatusSucceeded, "candidate", "manifest")
		pass()
		first := runIDByNumber("landing-land", 1)
		finishRun(first, atc.RunStatusErrored)
		pass()
		status, err := queues.Status(ctx, queue)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.FailedLands).To(Equal(1))
		Expect(status.LastError).To(ContainSubstring("errored"))
		Expect(entry("fix-1").State).To(Equal(atc.LandingEntryInFlight))

		pass()
		second := runIDByNumber("landing-land", 2)
		Expect(second).NotTo(Equal(first))
		Expect(entry("fix-1").LandRun).To(Equal(2))
		Expect(entry("fix-1").ComposeRun).To(Equal(1), "the same candidate")
		finishRun(second, atc.RunStatusSucceeded, "verdict")
		results.verdicts[second] = landing.Verdict{Outcome: landing.VerdictFailed, Reason: "push refused"}
		pass()
		status, err = queues.Status(ctx, queue)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.FailedLands).To(Equal(2))
		Expect(status.LastError).To(ContainSubstring("push refused"))
	})

	It("A verdict that cannot be read leaves the intent for the next pass", func() {
		submit("fix-1", shaA)
		pass()
		finishRun(runIDByNumber("landing-compose", 1), atc.RunStatusSucceeded, "candidate", "manifest")
		pass()
		landRun := runIDByNumber("landing-land", 1)
		finishRun(landRun, atc.RunStatusSucceeded, "verdict")
		pass()
		Expect(entry("fix-1").State).To(Equal(atc.LandingEntryInFlight))
		Expect(entry("fix-1").LandRun).To(Equal(1))
		results.verdicts[landRun] = landing.Verdict{Outcome: landing.VerdictLanded}
		pass()
		Expect(entry("fix-1").State).To(Equal(atc.LandingEntryLanded))
	})
})
