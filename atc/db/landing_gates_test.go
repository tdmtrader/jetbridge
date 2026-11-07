package db_test

import (
	"context"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("The landing queue's gates", func() {
	var ctx context.Context
	var factory db.LandingQueueFactory
	var queue db.LandingQueue
	var template db.Pipeline
	var intent db.LandingIntent
	var composeRun int
	config := atc.LandingQueueConfig{Repository: "https://example.test/repo.git", Trunk: "core", Compose: "landing-compose", Land: "landing-land",
		Gates: []atc.LandingQueueGate{{Template: "tests"}, {Template: "review", Wait: "40m"}}}

	newRun := func() int {
		GinkgoHelper()
		_, err := db.SetHangarEnabled(ctx, dbConn, true)
		Expect(err).NotTo(HaveOccurred())
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRunInTx(ctx, tx, template, db.RunParams{}, "landing-queue/default-team/trunk", db.RunCreationOpts{HangarOutput: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		return creation.Run.ID()
	}
	inTx := func(do func(tx db.Tx)) {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		do(tx)
		Expect(tx.Commit()).To(Succeed())
	}
	gates := func() []db.LandingGateRow {
		GinkgoHelper()
		gates, err := factory.Gates(ctx, composeRun)
		Expect(err).NotTo(HaveOccurred())
		return gates
	}
	status := func() atc.LandingQueueStatus {
		GinkgoHelper()
		status, err := factory.Status(ctx, queue)
		Expect(err).NotTo(HaveOccurred())
		return status
	}
	reloadQueue := func() {
		GinkgoHelper()
		var found bool
		var err error
		queue, found, err = factory.Queue(ctx, defaultTeam.ID(), "trunk")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	}

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewLandingQueueFactory(dbConn)
		var err error
		template, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "landing-compose"}, atc.Config{
			Template: true,
			Jobs:     atc.JobConfigs{{Name: "compose", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "compose", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.SetQueue(ctx, defaultTeam.ID(), "trunk", config)).To(Succeed())
		reloadQueue()
		_, err = factory.Submit(ctx, queue.ID, atc.LandingSubmission{ID: "fix-1", Commit: strings.Repeat("a", 40)}, "")
		Expect(err).NotTo(HaveOccurred())
		composeRun = newRun()
		inTx(func(tx db.Tx) {
			entry, _, err := factory.NextQueued(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(factory.RecordCompose(ctx, tx, entry, composeRun)).To(Succeed())
			intents, err := factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			intent = intents[0]
		})
	})

	recordGates := func() (int, int) {
		GinkgoHelper()
		tests, review := newRun(), newRun()
		inTx(func(tx db.Tx) {
			recorded, err := factory.RecordGates(ctx, tx, intent, []db.LandingGateAdmission{
				{Ordinal: 0, Template: "tests", Wait: 45 * time.Minute, RunID: tests},
				{Ordinal: 1, Template: "review", Wait: 40 * time.Minute, RunID: review},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded).To(BeTrue())
		})
		return tests, review
	}

	It("A composed candidate's gates are recorded with their Runs and take its intent to gating, once", func() {
		tests, review := recordGates()
		got := gates()
		Expect(got).To(HaveLen(2))
		Expect(got[0].Template).To(Equal("tests"))
		Expect(*got[0].RunID).To(Equal(tests))
		Expect(got[0].Wait).To(Equal(45 * time.Minute))
		Expect(got[0].Attempts).To(Equal(1))
		Expect(got[0].Verdict).To(Equal(atc.LandingGatePending))
		Expect(got[0].Overdue).To(BeFalse())
		Expect(*got[1].RunID).To(Equal(review))
		Expect(got[1].Wait).To(Equal(40 * time.Minute))

		another := newRun()
		inTx(func(tx db.Tx) {
			intents, err := factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(intents[0].State).To(Equal(db.LandingIntentGating))
			again, err := factory.RecordGates(ctx, tx, intent, []db.LandingGateAdmission{{Ordinal: 0, Template: "tests", Wait: time.Minute, RunID: another}})
			Expect(err).NotTo(HaveOccurred())
			Expect(again).To(BeFalse(), "an intent no longer composing records no gates")
		})

		entry := status().Entries[0]
		Expect(entry.Gates).To(HaveLen(2))
		Expect(entry.Gates[0]).To(Equal(atc.LandingGate{Template: "tests", Run: 2, Verdict: atc.LandingGatePending, Attempts: 1}))
	})

	It("A gate passes or fails once for its Run, and a repeated settle changes nothing", func() {
		recordGates()
		got := gates()
		inTx(func(tx db.Tx) {
			passed, err := factory.PassGate(ctx, tx, got[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(passed).To(BeTrue())
			passed, err = factory.PassGate(ctx, tx, got[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(passed).To(BeFalse(), "the same Run settles once")
			failed, err := factory.FailGate(ctx, tx, got[0], "too late")
			Expect(err).NotTo(HaveOccurred())
			Expect(failed).To(BeFalse(), "a passed gate is not failed afterwards")
			failed, err = factory.FailGate(ctx, tx, got[1], "review Run #3 failed")
			Expect(err).NotTo(HaveOccurred())
			Expect(failed).To(BeTrue())
		})
		got = gates()
		Expect(got[0].Verdict).To(Equal(atc.LandingGatePassed))
		Expect(got[1].Verdict).To(Equal(atc.LandingGateFailed))
		Expect(got[1].LastError).To(Equal("review Run #3 failed"))
	})

	It("A gate with no verdict is due another Run, and the next Run is recorded once", func() {
		recordGates()
		first := gates()[0]
		inTx(func(tx db.Tx) {
			retried, err := factory.RetryGate(ctx, tx, first, "tests Run #2 errored")
			Expect(err).NotTo(HaveOccurred())
			Expect(retried).To(BeTrue())
		})
		got := gates()[0]
		Expect(got.RunID).To(BeNil())
		Expect(got.NoVerdicts).To(Equal(1))
		Expect(got.Verdict).To(Equal(atc.LandingGatePending))

		next, another := newRun(), newRun()
		inTx(func(tx db.Tx) {
			recorded, err := factory.RecordGateRun(ctx, tx, got, next)
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded).To(BeTrue())
			recorded, err = factory.RecordGateRun(ctx, tx, got, another)
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded).To(BeFalse(), "a gate that has its Run is not due another")
		})
		got = gates()[0]
		Expect(*got.RunID).To(Equal(next))
		Expect(got.Attempts).To(Equal(2))
	})

	It("A gate's Run that has run past its wait is overdue by the database's clock", func() {
		recordGates()
		_, err := dbConn.Exec(`UPDATE landing_gates SET admitted_at = now() - interval '46 minutes' WHERE ordinal = 0`)
		Expect(err).NotTo(HaveOccurred())
		got := gates()
		Expect(got[0].Overdue).To(BeTrue())
		Expect(got[1].Overdue).To(BeFalse())
	})

	It("A held gate pauses the queue naming it, and resuming clears the pause and makes the gate due a fresh Run", func() {
		recordGates()
		first := gates()[0]
		inTx(func(tx db.Tx) {
			intents, err := factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			held, err := factory.HoldGate(ctx, tx, intents[0], first, "gate tests: Run #4 errored, after one retry")
			Expect(err).NotTo(HaveOccurred())
			Expect(held).To(BeTrue())
		})
		reloadQueue()
		Expect(queue.Paused).To(BeTrue())
		Expect(queue.PauseReason).To(ContainSubstring("gate tests"))
		Expect(status().Paused).To(BeTrue())
		Expect(status().Entries[0].Gates[0].Verdict).To(Equal(atc.LandingGateNoVerdict))

		resumed, err := factory.Resume(ctx, queue.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(resumed).To(BeTrue())
		resumed, err = factory.Resume(ctx, queue.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(resumed).To(BeFalse(), "a queue that is not paused is not resumed")
		reloadQueue()
		Expect(queue.Paused).To(BeFalse())
		Expect(queue.PauseReason).To(BeEmpty())
		got := gates()[0]
		Expect(got.Verdict).To(Equal(atc.LandingGatePending))
		Expect(got.RunID).To(BeNil())
		Expect(got.NoVerdicts).To(BeZero())
		Expect(got.LastError).To(ContainSubstring("errored"), "the last Run's outcome stays readable")
	})

	It("A candidate's gates go with its intent when the compose Run is deleted", func() {
		recordGates()
		_, err := dbConn.Exec(`DELETE FROM landing_intents WHERE compose_run_id = $1`, composeRun)
		Expect(err).NotTo(HaveOccurred())
		Expect(gates()).To(BeEmpty())
	})
})
