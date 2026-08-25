package atc_test

import (
	. "github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gbytes"
)

var _ = Describe("Config diff for template metadata", func() {
	It("reports a change when the template flag is the only edit", func() {
		// This fails if `fly set-pipeline` prints "no changes to apply" for a
		// pipeline whose only edit is becoming a template, which skips the PUT
		// and makes a template impossible to create by editing.
		buffer := NewBuffer()
		existing := Config{Jobs: JobConfigs{{Name: "some-job"}}}
		updated := Config{Jobs: JobConfigs{{Name: "some-job"}}, Template: true}

		Expect(existing.Diff(buffer, updated)).To(BeTrue())
		Eventually(buffer).Should(Say("template has changed"))
		Eventually(buffer).Should(Say("template: true"))
	})

	It("reports a change when a run parameter is added", func() {
		// This fails if a template gains a parameter and fly refuses to send it,
		// leaving the stored schema behind the file on disk.
		buffer := NewBuffer()
		existing := Config{Template: true, Jobs: JobConfigs{{Name: "some-job"}}}
		updated := Config{
			Template: true,
			Jobs:     JobConfigs{{Name: "some-job"}},
			Params:   []ParamSchema{{Name: "environment", Type: ParamTypeString, Required: true}},
		}

		Expect(existing.Diff(buffer, updated)).To(BeTrue())
		Eventually(buffer).Should(Say("parameters:"))
		Eventually(buffer).Should(Say("parameter environment has been added"))
	})

	It("reports a change when run retention is added", func() {
		// This fails if a retention policy edit is dropped, leaving runs to be
		// reclaimed under the previous policy.
		buffer := NewBuffer()
		keepLast := 5
		existing := Config{Template: true, Jobs: JobConfigs{{Name: "some-job"}}}
		updated := Config{
			Template:     true,
			Jobs:         JobConfigs{{Name: "some-job"}},
			RunRetention: &RunRetentionConfig{KeepLast: &keepLast},
		}

		Expect(existing.Diff(buffer, updated)).To(BeTrue())
		Eventually(buffer).Should(Say("run retention has been added"))
	})

	It("stays quiet when template metadata is unchanged", func() {
		// This fails if the new arms report a phantom change on every
		// set-pipeline of an unmodified template.
		existingKeepLast := 5
		updatedKeepLast := 5
		existing := Config{
			Template:     true,
			Jobs:         JobConfigs{{Name: "some-job"}},
			Params:       []ParamSchema{{Name: "environment", Type: ParamTypeString}},
			RunRetention: &RunRetentionConfig{KeepLast: &existingKeepLast},
		}
		updated := Config{
			Template:     true,
			Jobs:         JobConfigs{{Name: "some-job"}},
			Params:       []ParamSchema{{Name: "environment", Type: ParamTypeString}},
			RunRetention: &RunRetentionConfig{KeepLast: &updatedKeepLast},
		}

		Expect(existing.Diff(GinkgoWriter, updated)).To(BeFalse())
	})

	It("stays quiet when a stored empty parameter list meets an absent one", func() {
		// This fails if every ordinary pipeline starts reporting a phantom
		// parameter change, because the API returns a params field for
		// pipelines that never declared any.
		existing := Config{Jobs: JobConfigs{{Name: "some-job"}}, Params: []ParamSchema{}}
		updated := Config{Jobs: JobConfigs{{Name: "some-job"}}}

		Expect(existing.Diff(GinkgoWriter, updated)).To(BeFalse())
	})
})
