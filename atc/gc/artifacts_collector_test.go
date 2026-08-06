package gc_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ArtifactCollector", func() {
	var collector GcCollector

	// The cutoff is `created_at < NOW() - interval '12 hours'` in
	// worker_artifact_lifecycle.go:25. Rows are inserted directly, and aged
	// relative to the database's own clock, because there is no factory that
	// lets a caller choose created_at.
	insertArtifact := func(name, age string) {
		_, err := dbConn.Exec(
			"INSERT INTO worker_artifacts(name, created_at) VALUES($1, NOW() - $2::interval)",
			name, age,
		)
		Expect(err).NotTo(HaveOccurred())
	}

	remaining := func() []string {
		rows, err := dbConn.Query("SELECT name FROM worker_artifacts ORDER BY name")
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var names []string
		for rows.Next() {
			var n string
			Expect(rows.Scan(&n)).To(Succeed())
			names = append(names, n)
		}
		return names
	}

	BeforeEach(func() {
		collector = gc.NewArtifactCollector(db.NewArtifactLifecycle(dbConn))
	})

	Describe("Run", func() {
		It("removes artifacts older than twelve hours and keeps the rest", func() {
			insertArtifact("stale", "13 hours")
			insertArtifact("fresh", "1 hour")

			Expect(collector.Run(context.TODO())).To(Succeed())

			Expect(remaining()).To(ConsistOf("fresh"))
		})

		It("keeps an artifact that has not yet reached the cutoff", func() {
			insertArtifact("just-inside", "11 hours 59 minutes")

			Expect(collector.Run(context.TODO())).To(Succeed())

			Expect(remaining()).To(ConsistOf("just-inside"))
		})
	})
})
