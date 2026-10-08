package db_test

import (
	"context"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("The landing queue's tables", func() {
	var ctx context.Context
	var factory db.LandingQueueFactory
	var queue db.LandingQueue
	var template db.Pipeline
	config := atc.LandingQueueConfig{Repository: "https://example.test/repo.git", Trunk: "core", Compose: "landing-compose", Land: "landing-land"}
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)

	// newRun admits a Run of the template the way the port does, so the
	// intent's foreign keys point at real pipeline_runs rows.
	newRun := func() int {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRunInTx(ctx, tx, template, db.RunParams{}, "landing-queue/default-team/trunk", db.RunCreationOpts{ActivationEpoch: 1})
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
		var found bool
		queue, found, err = factory.Queue(ctx, defaultTeam.ID(), "trunk")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	It("A landing queue is set once per team and name, and setting it again replaces its config", func() {
		changed := config
		changed.Trunk = "main"
		Expect(factory.SetQueue(ctx, defaultTeam.ID(), "trunk", changed)).To(Succeed())
		again, found, err := factory.Queue(ctx, defaultTeam.ID(), "trunk")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(again.ID).To(Equal(queue.ID))
		Expect(again.Config.Trunk).To(Equal("main"))
		queues, err := factory.Queues(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(queues).To(HaveLen(1))
	})

	It("A submit of the same id with the same commit changes nothing, and with another commit is refused", func() {
		created, err := factory.Submit(ctx, queue.ID, atc.LandingSubmission{ID: "fix-1", Commit: shaA}, "someone")
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		created, err = factory.Submit(ctx, queue.ID, atc.LandingSubmission{ID: "fix-1", Commit: shaA}, "someone")
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		_, err = factory.Submit(ctx, queue.ID, atc.LandingSubmission{ID: "fix-1", Commit: shaB}, "someone")
		Expect(err).To(MatchError(db.ErrLandingEntryExists))
		_, err = factory.Submit(ctx, queue.ID, atc.LandingSubmission{ID: "../x", Commit: shaA}, "someone")
		Expect(err).To(HaveOccurred())
		status, err := factory.Status(ctx, queue)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Entries).To(HaveLen(1))
		Expect(status.Entries[0].State).To(Equal(atc.LandingEntryQueued))
	})

	It("An entry goes in flight at its compose Run, lands at its land Run, and a repeated settle changes nothing", func() {
		_, err := factory.Submit(ctx, queue.ID, atc.LandingSubmission{ID: "fix-1", Commit: shaA}, "")
		Expect(err).NotTo(HaveOccurred())
		composeRun, landRun := newRun(), newRun()

		var intent db.LandingIntent
		inTx(func(tx db.Tx) {
			entry, found, err := factory.NextQueued(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(factory.RecordCompose(ctx, tx, entry, composeRun)).To(Succeed())
			_, found, err = factory.NextQueued(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "an entry in flight is not queued")
		})
		inTx(func(tx db.Tx) {
			intents, err := factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(intents).To(HaveLen(1))
			intent = intents[0]
			Expect(intent.ComposeRunID).To(Equal(composeRun))
			Expect(intent.State).To(Equal(db.LandingIntentComposing))
			Expect(factory.RecordLand(ctx, tx, intent, landRun)).To(Succeed())
		})
		inTx(func(tx db.Tx) {
			intents, err := factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(*intents[0].LandRunID).To(Equal(landRun))
			landed, err := factory.LandEntry(ctx, tx, intents[0], "landed by Run")
			Expect(err).NotTo(HaveOccurred())
			Expect(landed).To(BeTrue())
			landed, err = factory.LandEntry(ctx, tx, intents[0], "landed by Run")
			Expect(err).NotTo(HaveOccurred())
			Expect(landed).To(BeFalse(), "the same Run settles once")
			intents, err = factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(intents).To(BeEmpty())
		})

		status, err := factory.Status(ctx, queue)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Entries[0].State).To(Equal(atc.LandingEntryLanded))
		Expect(status.Entries[0].ComposeRun).NotTo(BeZero())
		Expect(status.Entries[0].LandRun).NotTo(BeZero())
		Expect(status.Entries[0].SettledAt).NotTo(BeNil())
	})

	It("A land Run that did not land counts as a failed landing and frees the intent; a moved one queues the entry again", func() {
		_, err := factory.Submit(ctx, queue.ID, atc.LandingSubmission{ID: "fix-1", Commit: shaA}, "")
		Expect(err).NotTo(HaveOccurred())
		composeRun, landRun := newRun(), newRun()
		inTx(func(tx db.Tx) {
			entry, _, err := factory.NextQueued(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(factory.RecordCompose(ctx, tx, entry, composeRun)).To(Succeed())
			intents, err := factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(factory.RecordLand(ctx, tx, intents[0], landRun)).To(Succeed())
		})
		inTx(func(tx db.Tx) {
			intents, err := factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			failed, err := factory.FailLand(ctx, tx, intents[0], "push refused")
			Expect(err).NotTo(HaveOccurred())
			Expect(failed).To(BeTrue())
			failed, err = factory.FailLand(ctx, tx, intents[0], "push refused")
			Expect(err).NotTo(HaveOccurred())
			Expect(failed).To(BeFalse(), "the same land Run fails once")
		})
		status, err := factory.Status(ctx, queue)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.FailedLands).To(Equal(1))
		Expect(status.LastError).To(Equal("push refused"))
		Expect(status.Entries[0].State).To(Equal(atc.LandingEntryInFlight))

		inTx(func(tx db.Tx) {
			intents, err := factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(intents[0].LandRunID).To(BeNil())
			Expect(intents[0].Fails).To(Equal(1))
			moved, err := factory.Recompose(ctx, tx, intents[0], "trunk moved")
			Expect(err).NotTo(HaveOccurred())
			Expect(moved).To(BeTrue())
			entry, found, err := factory.NextQueued(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(entry.EntryID).To(Equal("fix-1"))
			Expect(entry.SettledAt).To(BeNil())
		})
	})

	It("An ejected entry keeps the Run that ejected it and is never queued again", func() {
		_, err := factory.Submit(ctx, queue.ID, atc.LandingSubmission{ID: "fix-1", Commit: shaA}, "")
		Expect(err).NotTo(HaveOccurred())
		composeRun := newRun()
		inTx(func(tx db.Tx) {
			entry, _, err := factory.NextQueued(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(factory.RecordCompose(ctx, tx, entry, composeRun)).To(Succeed())
			intents, err := factory.OpenIntents(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			ejected, err := factory.EjectEntry(ctx, tx, intents[0], "compose Run failed")
			Expect(err).NotTo(HaveOccurred())
			Expect(ejected).To(BeTrue())
			_, found, err := factory.NextQueued(ctx, tx, queue.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})
		_, err = factory.Submit(ctx, queue.ID, atc.LandingSubmission{ID: "fix-1", Commit: shaB}, "")
		Expect(err).To(MatchError(db.ErrLandingEntryExists))
		status, err := factory.Status(ctx, queue)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Entries[0].State).To(Equal(atc.LandingEntryEjected))
		Expect(status.Entries[0].SettleReason).To(Equal("compose Run failed"))
		Expect(status.Entries[0].ComposeRun).NotTo(BeZero())
	})
})
