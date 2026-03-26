package testflight_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Scratch paths", func() {
	It("provides a writable scratch volume to the task", func() {
		setAndUnpausePipeline("fixtures/scratch-paths.yml")

		watch := fly("trigger-job", "-j", inPipeline("scratch-write"), "-w")
		Expect(watch).To(gbytes.Say("scratch-writable"))
	})

	It("does not preserve scratch data across tasks in the same build", func() {
		setAndUnpausePipeline("fixtures/scratch-paths.yml")

		watch := fly("trigger-job", "-j", inPipeline("scratch-ephemeral"), "-w")
		Expect(watch).To(gbytes.Say("scratch-writable"))
		Expect(watch).To(gbytes.Say("scratch-ephemeral"))
		Expect(watch).NotTo(gbytes.Say("scratch-persisted"))
	})
})
