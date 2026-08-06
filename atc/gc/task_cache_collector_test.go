package gc_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/gc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TaskCacheCollector", func() {
	var (
		collector        GcCollector
		taskCacheFactory db.TaskCacheFactory
	)

	cacheExists := func(id int) bool {
		var n int
		Expect(dbConn.QueryRow("SELECT count(*) FROM task_caches WHERE id = $1", id).Scan(&n)).To(Succeed())
		return n > 0
	}

	BeforeEach(func() {
		taskCacheFactory = db.NewTaskCacheFactory(dbConn)
		collector = gc.NewTaskCacheCollector(db.NewTaskCacheLifecycle(dbConn))
	})

	Describe("Run", func() {
		It("collects caches of an archived pipeline and leaves the rest", func() {
			// CleanUpInvalidTaskCaches selects caches whose pipeline is archived,
			// or whose pipeline or job is paused with no next build
			// (task_cache_lifecycle.go:24-32). Archiving is the cheapest of the
			// three to set up and exercises the same join.
			archived := dbtest.Setup(
				builder.WithPipeline(atc.Config{Jobs: []atc.JobConfig{{Name: "doomed-job"}}}),
			)
			live := dbtest.Setup(
				builder.WithPipeline(atc.Config{Jobs: []atc.JobConfig{{Name: "surviving-job"}}}),
			)

			doomed, err := taskCacheFactory.FindOrCreate(archived.Job("doomed-job").ID(), "some-step", "some-path")
			Expect(err).NotTo(HaveOccurred())
			surviving, err := taskCacheFactory.FindOrCreate(live.Job("surviving-job").ID(), "some-step", "some-path")
			Expect(err).NotTo(HaveOccurred())

			Expect(archived.Pipeline.Archive()).To(Succeed())

			Expect(collector.Run(context.TODO())).To(Succeed())

			Expect(cacheExists(doomed.ID())).To(BeFalse(), "cache of an archived pipeline should have been collected")
			Expect(cacheExists(surviving.ID())).To(BeTrue(), "cache of a live pipeline should have survived")
		})

		It("returns the error when the cleanup fails", func() {
			// A closed connection is a real failure the database can be made to
			// produce on demand. Opened separately so the suite's own conn --
			// which AfterEach closes -- is left intact.
			doomed := postgresRunner.OpenConn()
			Expect(doomed.Close()).To(Succeed())

			err := gc.NewTaskCacheCollector(db.NewTaskCacheLifecycle(doomed)).Run(context.TODO())
			Expect(err).To(HaveOccurred())
		})
	})
})
