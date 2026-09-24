package db_test

import (
	"context"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runinput"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Replaying an invocation continues a Run that already exists; it is not an
// admission. An admission hold stops new Runs, not running ones (durable Run
// contract, amendment M-2), so a replay needs only the Run activation marker
// at or past the replayed Run's own epoch -- held or not, and whatever epoch
// the replaying node speaks for. A new key is still refused.
var _ = Describe("Run invocation replay under the activation marker", func() {
	var (
		ctx      context.Context
		factory  db.PipelineRunFactory
		template db.Pipeline
		owner    = runinput.PrincipalDigest("local:owner")
	)

	BeforeEach(func() {
		ctx = context.Background()
		setMarker(1, true)
		factory = db.NewPipelineRunFactory(dbConn, lockFactory)
		var err error
		template, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "replayed"},
			atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
	})

	// create asks for key under the epoch the calling node speaks for; zero is
	// a node configured with admission off.
	create := func(key string, nodeEpoch int64) (db.RunCreation, error) {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		creation, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "owner", db.RunCreationOpts{
			ActivationEpoch: nodeEpoch,
			Invocation:      &db.RunInvocationIdentity{PrincipalDigest: owner, KeyDigest: strings.Repeat(key, 64)},
		})
		if err != nil {
			return db.RunCreation{}, err
		}
		Expect(tx.Commit()).To(Succeed())
		return creation, nil
	}

	runs := func() int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_runs`).Scan(&count)).To(Succeed())
		return count
	}

	lastRunNumber := func() int {
		GinkgoHelper()
		var number int
		Expect(dbConn.QueryRow(`SELECT last_run_number FROM pipelines WHERE id=$1`, template.ID()).Scan(&number)).To(Succeed())
		return number
	}

	Context("under an admission hold", func() {
		var admitted db.RunCreation

		BeforeEach(func() {
			var err error
			admitted, err = create("a", 1)
			Expect(err).NotTo(HaveOccurred())
			setMarker(1, false)
		})

		It("replays the Run the key admitted, from a node still at the epoch or one configured off", func() {
			for _, nodeEpoch := range []int64{1, 0} {
				replay, err := create("a", nodeEpoch)
				Expect(err).NotTo(HaveOccurred(), "node epoch %d", nodeEpoch)
				Expect(replay.Replayed).To(BeTrue())
				Expect(replay.Run.ID()).To(Equal(admitted.Run.ID()))
				Expect(replay.Run.ActivationEpoch()).To(BeEquivalentTo(1))
			}
			Expect(runs()).To(Equal(1))
		})

		It("refuses a new key and allocates nothing", func() {
			_, err := create("b", 1)
			Expect(err).To(MatchError(atc.ErrRunResultsUnavailable))
			_, err = create("b", 0)
			Expect(err).To(MatchError(db.ErrRunActivationEpochRequired))
			Expect(runs()).To(Equal(1))
			Expect(lastRunNumber()).To(Equal(1))
		})

		It("still reports changed intent under the key as a conflict", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "owner", db.RunCreationOpts{
				ActivationEpoch: 0,
				Invocation:      &db.RunInvocationIdentity{PrincipalDigest: owner, KeyDigest: strings.Repeat("a", 64)},
				Correlation:     "changed",
			})
			Expect(err).To(MatchError(db.ErrRunInvocationConflict))
		})
	})

	Context("after the Run activation epoch moves forward", func() {
		var admitted db.RunCreation

		BeforeEach(func() {
			var err error
			admitted, err = create("a", 1)
			Expect(err).NotTo(HaveOccurred())
			setMarker(2, true)
		})

		It("replays an older-epoch Run from a node on the new epoch or one still on the old", func() {
			for _, nodeEpoch := range []int64{2, 1} {
				replay, err := create("a", nodeEpoch)
				Expect(err).NotTo(HaveOccurred(), "node epoch %d", nodeEpoch)
				Expect(replay.Replayed).To(BeTrue())
				Expect(replay.Run.ID()).To(Equal(admitted.Run.ID()))
				Expect(replay.Run.ActivationEpoch()).To(BeEquivalentTo(1))
			}
		})

		It("admits a new key only at the marker's epoch", func() {
			_, err := create("b", 1)
			Expect(err).To(MatchError(atc.ErrRunResultsUnavailable))
			fresh, err := create("b", 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(fresh.Replayed).To(BeFalse())
			Expect(fresh.Run.ActivationEpoch()).To(BeEquivalentTo(2))
		})
	})
})

func setMarker(epoch int64, admitting bool) {
	GinkgoHelper()
	_, err := dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=$1, admission_enabled=$2 WHERE singleton`, epoch, admitting)
	Expect(err).NotTo(HaveOccurred())
}
