package atc_test

import (
	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("StepValidator", func() {
	var validator *atc.StepValidator

	BeforeEach(func() {
		validator = atc.NewStepValidator(atc.Config{}, []string{"jobs(some-job)", ".plan"})
	})

	Describe("VisitRunPipeline", func() {
		It("accepts a valid pipeline name", func() {
			err := validator.Validate(atc.Step{
				Config: &atc.RunPipelineStep{
					Name:   "some-pipeline",
					Params: atc.RunParams{"ref": "some-ref"},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(validator.Errors).To(BeEmpty())
			Expect(validator.Warnings).To(BeEmpty())
		})

		It("errors when no pipeline is named", func() {
			err := validator.Validate(atc.Step{
				Config: &atc.RunPipelineStep{},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(validator.Errors).To(ContainElement(
				"jobs(some-job).plan.run_pipeline(): no pipeline specified",
			))
			Expect(validator.Errors).To(ContainElement(ContainSubstring(
				"identifier cannot be an empty string",
			)))
		})

		It("warns when the name is not a valid identifier", func() {
			err := validator.Validate(atc.Step{
				Config: &atc.RunPipelineStep{Name: "Some-Pipeline"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(validator.Errors).To(BeEmpty())
			Expect(validator.Warnings).To(ConsistOf(atc.ConfigWarning{
				Type:    "invalid_identifier",
				Message: "jobs(some-job).plan.run_pipeline(Some-Pipeline): 'Some-Pipeline' is not a valid identifier: must start with a lowercase letter or a number",
			}))
		})

		It("does not validate the params, which are checked at admission time", func() {
			err := validator.Validate(atc.Step{
				Config: &atc.RunPipelineStep{
					Name:   "some-pipeline",
					Params: atc.RunParams{"anything": map[string]any{"at": "all"}},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(validator.Errors).To(BeEmpty())
		})
	})
})
