package db_test

import (
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Container", func() {
	var (
		creatingContainer db.CreatingContainer
		build             db.Build
	)

	var safelyCloseConection = func() {
		BeforeEach(func() {
			_ = dbConn.Close()
		})
		AfterEach(func() {
			dbConn = postgresRunner.OpenConn()
		})
	}

	BeforeEach(func() {
		var err error
		build, err = defaultTeam.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())

		creatingContainer, err = defaultWorker.CreateContainer(
			db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()),
			fullMetadata,
		)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("Failed", func() {
		var failedContainer db.FailedContainer
		var failedErr error

		Context("db conn is closed", func() {
			safelyCloseConection()
			It("returns an error", func() {
				failedContainer, failedErr = creatingContainer.Failed()
				Expect(failedContainer).To(BeNil())
				Expect(failedErr).To(HaveOccurred())
			})
		})
	})

	Describe("Destroy", func() {
		var destroyErr error

		Context("called on a destroying container", func() {
			var destroyingContainer db.DestroyingContainer
			BeforeEach(func() {
				createdContainer, err := creatingContainer.Created()
				Expect(err).ToNot(HaveOccurred())
				destroyingContainer, err = createdContainer.Destroying()
				Expect(err).ToNot(HaveOccurred())
			})

			JustBeforeEach(func() {
				_, destroyErr = destroyingContainer.Destroy()
			})

			Context("errors", func() {
				Context("when the db connection is closed", func() {
					safelyCloseConection()
					It("returns an error", func() {
						Expect(destroyErr).To(HaveOccurred())
					})
				})
			})
		})

		Context("called on a failed container", func() {
			var failedContainer db.FailedContainer

			BeforeEach(func() {
				var err error
				failedContainer, err = creatingContainer.Failed()
				Expect(err).ToNot(HaveOccurred())
			})

			JustBeforeEach(func() {
				_, destroyErr = failedContainer.Destroy()
			})

			Context("errors", func() {
				Context("when the db connection is closed", func() {
					safelyCloseConection()
					It("returns an error", func() {
						Expect(destroyErr).To(HaveOccurred())
					})
				})
			})
		})
	})
})
