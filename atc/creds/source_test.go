package creds_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Evaluate", func() {
	var source creds.Source

	BeforeEach(func() {
		variables := vars.StaticVariables{
			"some-param": "lol",
		}
		source = creds.NewSource(variables, atc.Source{
			"some": map[string]any{
				"source-key": "((some-param))",
			},
		})
	})

	Describe("Evaluate", func() {
		It("parses variables", func() {
			result, err := source.Evaluate()
			Expect(err).NotTo(HaveOccurred())

			Expect(result).To(Equal(atc.Source{
				"some": map[string]any{
					"source-key": "lol",
				},
			}))
		})
	})
})
var _ = Describe("EvaluateWithReferenceExclusion", func() {
	It("leaves an excluded template parameter in place instead of demanding a credential", func() {
		// This fails if `fly set-pipeline --check-creds` on a template reports
		// the template's own declared parameters as missing credentials, which
		// makes every such request 400.
		source := creds.NewSource(vars.StaticVariables{}, atc.Source{
			"repository": "example/((environment))",
			"tag":        "((image_tag))",
		})

		result, err := source.EvaluateWithReferenceExclusion(vars.NewExactReferenceExclusion([]string{"environment", "image_tag"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(atc.Source{
			"repository": "example/((environment))",
			"tag":        "((image_tag))",
		}))
	})

	It("still reports a genuinely missing credential", func() {
		// This fails if the exclusion turns the credential check into a no-op.
		source := creds.NewSource(vars.StaticVariables{}, atc.Source{"tag": "((secret))"})

		_, err := source.EvaluateWithReferenceExclusion(vars.NewExactReferenceExclusion([]string{"environment"}))
		Expect(err).To(HaveOccurred())
	})
})
