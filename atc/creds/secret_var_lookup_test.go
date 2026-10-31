package creds_test

import (
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/credstest"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VariableLookupFromSecrets", func() {
	var (
		variables vars.Variables
	)

	BeforeEach(func() {
		secrets := &credstest.Secrets{StaticVariables: vars.StaticVariables{
			"a": map[string]any{
				"b": map[any]any{
					"c": "foo",
				},
			},
		}}
		variables = creds.NewVariables(secrets, creds.SecretLookupParams{Team: "team", Pipeline: "pipeline"}, true)
	})

	Describe("Get", func() {
		It("traverses fields", func() {
			result, found, err := variables.Get(vars.Reference{Path: "a", Fields: []string{"b", "c"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(result).To(Equal("foo"))
		})

		Context("when a field is missing", func() {
			It("errors", func() {
				_, _, err := variables.Get(vars.Reference{Path: "a", Fields: []string{"b", "d"}})
				Expect(err).To(HaveOccurred())
			})
		})
	})
})
