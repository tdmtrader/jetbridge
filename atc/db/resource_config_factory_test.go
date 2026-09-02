package db_test

import (
	"sync"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceConfigFactory", func() {
	var build db.Build

	BeforeEach(func() {
		var err error
		job, found, err := defaultPipeline.Job("some-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		build, err = job.CreateBuild(defaultBuildCreatedBy)
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when the resource config is concurrently created", func() {
		BeforeEach(func() {
			Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
			Expect(build.SetInterceptible(false)).To(Succeed())
		})

		It("consistently is able to be created", func() {
			// enable concurrent use of database. this is set to 1 by default to
			// ensure methods don't require more than one in a single connection,
			// which can cause deadlocking as the pool is limited.
			dbConn.SetMaxOpenConns(2)

			done := make(chan struct{})

			wg := new(sync.WaitGroup)
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()

				for {
					select {
					case <-done:
						return
					default:
						_, err := resourceConfigFactory.FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"some": "unique-source"}, nil)
						Expect(err).ToNot(HaveOccurred())
					}
				}
			}()

			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer close(done)
				defer wg.Done()

				for i := 0; i < 100; i++ {
					_, err := resourceConfigFactory.FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"some": "unique-source"}, nil)
					Expect(err).ToNot(HaveOccurred())
				}
			}()

			wg.Wait()
		})
	})
})
