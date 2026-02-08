package atc_test

import (
	"github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Worker", func() {
	Describe("Validate", func() {
		var worker atc.Worker

		BeforeEach(func() {
			worker = atc.Worker{}
		})

		Context("when version is empty", func() {
			BeforeEach(func() {
				worker.Version = ""
			})

			It("returns no errors", func() {
				Expect(worker.Validate()).To(Succeed())
			})
		})

		Context("when version is contains numeric charactes", func() {
			BeforeEach(func() {
				worker.Version = "1.2.3"
			})

			It("returns no errors", func() {
				Expect(worker.Validate()).To(Succeed())
			})
		})

		Context("when version is contains non-numeric charactes", func() {
			BeforeEach(func() {
				worker.Version = "a.b.c"
			})

			It("returns errors", func() {
				err := worker.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid worker version, only numeric characters are allowed"))
			})
		})
	})
})
