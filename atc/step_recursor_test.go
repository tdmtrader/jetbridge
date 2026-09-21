package atc_test

import (
	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("StepRecursor", func() {
	Describe("OnRunPipeline", func() {
		It("fires for a run_pipeline step nested inside other steps", func() {
			var visited []*atc.RunPipelineStep

			recursor := atc.StepRecursor{
				OnRunPipeline: func(step *atc.RunPipelineStep) error {
					visited = append(visited, step)
					return nil
				},
			}

			step := &atc.RunPipelineStep{
				Name:   "some-pipeline",
				Params: atc.RunParams{"ref": "some-ref"},
			}

			err := (&atc.DoStep{
				Steps: []atc.Step{
					{Config: &atc.TryStep{Step: atc.Step{Config: step}}},
				},
			}).Visit(recursor)
			Expect(err).ToNot(HaveOccurred())

			Expect(visited).To(ConsistOf(step))
		})

		It("does nothing when the hook is not configured", func() {
			err := (&atc.RunPipelineStep{Name: "some-pipeline"}).Visit(atc.StepRecursor{})
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
