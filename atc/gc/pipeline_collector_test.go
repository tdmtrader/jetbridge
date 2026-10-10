package gc_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PipelineCollector", func() {
	var collector GcCollector

	// A child pipeline is one set by a build of a job in another pipeline.
	// ArchiveAbandonedPipelines archives children whose parent is gone or
	// archived (pipeline_lifecycle.go:29-41); a pipeline saved directly by a
	// team has no parent job and is never a candidate.
	setChildPipeline := func(name string) db.Pipeline {
		build, err := defaultJob.CreateBuild("some-user")
		Expect(err).NotTo(HaveOccurred())

		child, _, err := build.SavePipeline(
			atc.PipelineRef{Name: name}, defaultTeam.ID(),
			atc.Config{Jobs: atc.JobConfigs{{Name: "child-job"}}},
			db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		return child
	}

	archived := func(p db.Pipeline) bool {
		_, err := p.Reload()
		Expect(err).NotTo(HaveOccurred())
		return p.Archived()
	}

	BeforeEach(func() {
		collector = gc.NewPipelineCollector(db.NewPipelineLifecycle(dbConn, lockFactory))
	})

	Describe("Run", func() {
		It("leaves a child pipeline whose parent is still healthy", func() {
			child := setChildPipeline("child-pipeline")

			Expect(collector.Run(context.TODO())).To(Succeed())

			Expect(archived(child)).To(BeFalse(), "child of a live parent should not be archived")
		})

		It("archives a child pipeline once its parent is archived", func() {
			child := setChildPipeline("orphaned-pipeline")
			Expect(defaultPipeline.Archive()).To(Succeed())

			Expect(collector.Run(context.TODO())).To(Succeed())

			Expect(archived(child)).To(BeTrue(), "child of an archived parent should have been archived")
		})
	})
})
