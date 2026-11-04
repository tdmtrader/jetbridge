package db_test

import (
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A Run producer's Pod is released once the node acknowledges its outcome,
// while its build still runs. A resumed build reads the producer's other
// outputs from its container, so the container outlives its Pod until the
// build completes.
var _ = Describe("A missing Run producer container", func() {
	var (
		f        *closureCaptures
		producer db.RunExecutionAdmission
	)

	insertMissing := func(handle string, buildID int, planID string) {
		GinkgoHelper()
		_, err := psql.Insert("containers").SetMap(map[string]any{
			"handle":        handle,
			"state":         atc.ContainerStateCreated,
			"worker_name":   "producer-worker",
			"build_id":      buildID,
			"plan_id":       planID,
			"missing_since": time.Now().Add(-10 * time.Minute),
		}).RunWith(dbConn).Exec()
		Expect(err).NotTo(HaveOccurred())
	}

	exists := func(handle string) bool {
		GinkgoHelper()
		var found bool
		Expect(psql.Select("count(*) > 0").From("containers").Where(sq.Eq{"handle": handle}).
			RunWith(dbConn).QueryRow().Scan(&found)).To(Succeed())
		return found
	}

	BeforeEach(func() {
		f = newClosureCaptures()
		producer = f.start(f.review, f.reviewPlan)
		_, err := psql.Insert("workers").SetMap(map[string]any{"name": "producer-worker", "state": "running"}).
			RunWith(dbConn).Exec()
		Expect(err).NotTo(HaveOccurred())
		insertMissing("producer-handle", f.review.ID(), string(producer.PlanID))
		insertMissing("other-step-handle", f.review.ID(), "some-other-step")
	})

	It("is kept while its build runs, and only it", func() {
		_, err := containerRepository.RemoveMissingContainers(3 * time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists("producer-handle")).To(BeTrue(), "a running build lost its producer's container")
		Expect(exists("other-step-handle")).To(BeFalse(), "a missing container that produced nothing was kept")
	})

	It("is removed once its build completes", func() {
		_, err := dbConn.Exec(`UPDATE builds SET completed = true, status = 'errored' WHERE id = $1`, f.review.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = containerRepository.RemoveMissingContainers(3 * time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists("producer-handle")).To(BeFalse())
	})
})
