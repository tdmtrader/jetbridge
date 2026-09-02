package db_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PipelineLifecycle", func() {
	var (
		pl  db.PipelineLifecycle
		err error
	)

	BeforeEach(func() {
		pl = db.NewPipelineLifecycle(dbConn, lockFactory)
	})

	Describe("ArchiveAbandonedPipelines", func() {
		JustBeforeEach(func() {
			err = pl.ArchiveAbandonedPipelines()
			Expect(err).NotTo(HaveOccurred())
		})

		Context("child pipeline is set by a job in a pipeline", func() {
			var childPipeline db.Pipeline

			BeforeEach(func() {
				setChildBuild, buildErr := defaultJob.CreateBuild(defaultBuildCreatedBy)
				Expect(buildErr).NotTo(HaveOccurred())
				childPipeline, _, buildErr = setChildBuild.SavePipeline(atc.PipelineRef{Name: "child-pipeline"}, defaultTeam.ID(), defaultPipelineConfig, db.ConfigVersion(0), false)
				Expect(buildErr).NotTo(HaveOccurred())
				Expect(setChildBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
			})

			Context("when build that set child pipeline is not most recent", func() {
				BeforeEach(func() {
					laterBuild, buildErr := defaultJob.CreateBuild(defaultBuildCreatedBy)
					Expect(buildErr).NotTo(HaveOccurred())
					Expect(laterBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
				})

				It("should archive child pipeline", func() {
					_, reloadErr := childPipeline.Reload()
					Expect(reloadErr).NotTo(HaveOccurred())
					Expect(childPipeline.Archived()).To(BeTrue())
				})
			})
		})
	})
})
