package configvalidate_test

import (
	"os"
	"path/filepath"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The K8s behavioral tier sets these configs on a cluster only CI can run,
// ~20 minutes in. A config the API refuses fails there and nowhere earlier
// (build 239: a resource no job used), so the unit tier holds them to the
// same gates the API applies (atc/api/configserver/save.go) and fly execute
// applies to a task file (atc.NewTaskConfig). topgun is outside the unit
// tier, so the check lives here, beside the validator.
var _ = Describe("the k8s-behavioral runtime switch configs", func() {
	dir := filepath.Join("..", "..", "topgun", "k8s_behavioral", "testdata", "runtime-switch")

	// A name of the shape the suite gives a pipeline (randomPipelineName).
	const pipelineName = "k8s-beh-3f1c2a9e-1b2c-4d5e-8f90-0123456789ab"

	DescribeTable("a pipeline config the API saves",
		func(file string) {
			payload, err := os.ReadFile(filepath.Join(dir, file))
			Expect(err).ToNot(HaveOccurred())

			var config atc.Config
			Expect(atc.UnmarshalConfig(payload, &config)).To(Succeed())

			_, errorMessages := configvalidate.Validate(config)
			Expect(errorMessages).To(BeEmpty())

			_, err = atc.ValidateIdentifier(pipelineName, "pipeline")
			Expect(err).ToNot(HaveOccurred())
			Expect(configvalidate.ValidateTemplateDeclaration(atc.PipelineRef{Name: pipelineName}, config)).To(Succeed())
		},
		Entry("the privilege spec's pipeline", "step-privilege.yml"),
		Entry("the switch spec's pipeline", "runtime-switch.yml"),
	)

	It("loads the privilege spec's one-off task as fly execute does", func() {
		payload, err := os.ReadFile(filepath.Join(dir, "privileged-one-off.yml"))
		Expect(err).ToNot(HaveOccurred())
		_, err = atc.NewTaskConfig(payload)
		Expect(err).ToNot(HaveOccurred())
	})
})
