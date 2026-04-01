package creds_test

import (
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("String", func() {
	Describe("Evaluate", func() {
		It("interpolates variables in the string", func() {
			variables := vars.StaticVariables{
				"token": "super-secret",
			}
			s := creds.NewString(variables, "((token))")

			result, err := s.Evaluate()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("super-secret"))
		})

		It("returns the raw string on error", func() {
			variables := vars.StaticVariables{}
			s := creds.NewString(variables, "((missing-var))")

			result, err := s.Evaluate()
			Expect(err).To(HaveOccurred())
			Expect(result).To(Equal("((missing-var))"))
		})

		It("passes through strings without variables", func() {
			variables := vars.StaticVariables{}
			s := creds.NewString(variables, "plain-value")

			result, err := s.Evaluate()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("plain-value"))
		})
	})
})
