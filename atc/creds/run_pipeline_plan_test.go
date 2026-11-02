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

	// Only `((.:name))` -- the calling build's own local vars -- resolves.
	// Everything else is a reference that must reach the run header, and the
	// payload, untouched.
	localVars := func(kvs map[string]any) vars.Variables {
		return vars.NamedVariables{".": vars.StaticVariables(kvs)}
	}

	BeforeEach(func() {
		plan = creds.NewRunPipelinePlan(localVars(map[string]any{
			"ref":    "abc123",
			"env":    "staging",
			"region": "us-east-1",
			"nested": map[string]any{"field": "deep"},
		}), atc.RunPipelinePlan{
			Name: "some-((pipeline-name))-ok",
			Params: atc.RunParams{
				"ref": "((.:ref))",
				"deploy": map[string]any{
					"env":     "to-((.:env))",
					"regions": []any{"((.:region))", "eu-west-1"},
				},
				"deep": "((.:nested.field))",
			},
		})
	})

	Describe("Evaluate", func() {
		It("interpolates build-local vars nested inside the params", func() {
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
					"deep": "deep",
				},
			}))
		})

		It("errors when a build-local var is not defined", func() {
			plan = creds.NewRunPipelinePlan(localVars(map[string]any{}), atc.RunPipelinePlan{
				Name:   "some-pipeline",
				Params: atc.RunParams{"ref": "((.:missing))"},
			})

			_, err := plan.Evaluate()
			Expect(err).To(HaveOccurred())
		})

		// The whole point of the contract: a credential reference is a
		// reference on the run header, not the secret it names. It resolves in
		// the payload at build time, the way it would in any pipeline.
		It("passes every non-local reference through verbatim", func() {
			plan = creds.NewRunPipelinePlan(localVars(map[string]any{"v": "local-value"}), atc.RunPipelinePlan{
				Name: "some-pipeline",
				Params: atc.RunParams{
					"secret":      "((vault/x))",
					"sourced":     "((mysource:key))",
					"unqualified": "((plain))",
					"field":       "((a.b))",
					"mixed":       "((.:v))-((vault/x))",
					"nested": map[string]any{
						"list": []any{"((vault/y))", "((.:v))"},
					},
				},
			})

			result, err := plan.Evaluate()
			Expect(err).NotTo(HaveOccurred())

			Expect(result.Params).To(Equal(atc.RunParams{
				"secret":      "((vault/x))",
				"sourced":     "((mysource:key))",
				"unqualified": "((plain))",
				"field":       "((a.b))",
				"mixed":       "local-value-((vault/x))",
				"nested": map[string]any{
					"list": []any{"((vault/y))", "local-value"},
				},
			}))
		})

		// An excluded reference is never looked up, so a credential manager
		// that cannot answer -- or has no such secret -- is not an error here.
		It("does not report a non-local reference as a missing var", func() {
			plan = creds.NewRunPipelinePlan(localVars(map[string]any{}), atc.RunPipelinePlan{
				Name:   "some-pipeline",
				Params: atc.RunParams{"ref": "((vault/nothing-here))"},
			})

			result, err := plan.Evaluate()
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Params).To(Equal(atc.RunParams{"ref": "((vault/nothing-here))"}))
		})
	})
})
