package creds_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RunPipelinePlan", func() {
	var plan creds.RunPipelinePlan

	BeforeEach(func() {
		variables := vars.StaticVariables{
			"pipeline-name": "pn-is",
			"ref":           "abc123",
			"env":           "staging",
			"region":        "us-east-1",
		}
		plan = creds.NewRunPipelinePlan(variables, atc.RunPipelinePlan{
			Name: "some-((pipeline-name))-ok",
			Params: atc.RunParams{
				"ref": "((ref))",
				"deploy": map[string]any{
					"env":     "to-((env))",
					"regions": []any{"((region))", "eu-west-1"},
				},
			},
		})
	})

	Describe("Evaluate", func() {
		It("interpolates vars nested inside the params", func() {
			result, err := plan.Evaluate()
			Expect(err).NotTo(HaveOccurred())

			Expect(result).To(Equal(atc.RunPipelinePlan{
				Name: "some-((pipeline-name))-ok", // Name should not be interpolated.
				Params: atc.RunParams{
					"ref": "abc123",
					"deploy": map[string]any{
						"env":     "to-staging",
						"regions": []any{"us-east-1", "eu-west-1"},
					},
				},
			}))
		})

		It("errors when a var is not defined", func() {
			plan = creds.NewRunPipelinePlan(vars.StaticVariables{}, atc.RunPipelinePlan{
				Name:   "some-pipeline",
				Params: atc.RunParams{"ref": "((missing))"},
			})

			_, err := plan.Evaluate()
			Expect(err).To(HaveOccurred())
		})
	})
})
