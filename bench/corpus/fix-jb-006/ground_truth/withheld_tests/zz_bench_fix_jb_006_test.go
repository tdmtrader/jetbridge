// WITHHELD grading material for bench case fix-jb-006. Never exposed.
//
// The harness copies this file into the candidate tree as a NEW file:
//
//	cp ground_truth/withheld_tests/zz_bench_fix_jb_006_test.go \
//	   <tree>/atc/configvalidate/zz_bench_fix_jb_006_test.go
//
// It is deliberately self-contained (its own local job fixture and validate
// helper, no package-level identifiers) so it cannot collide with — or be
// clobbered by — the spec the task asks the candidate to add to
// atc/configvalidate/validate_test.go. Do NOT grade by overwriting or patching
// validate_test.go: that file is part of the candidate's answer.
//
// Assertions mirror ground_truth/reference.diff's spec exactly; the spec name is
// namespaced so -ginkgo.focus selects only this spec, never a candidate-authored
// one with a similar name.

package configvalidate_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"

	// load dummy credential manager
	_ "github.com/concourse/concourse/atc/creds/dummy"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("bench-graded fix-jb-006", func() {
	benchValidJob := atc.JobConfig{
		Name: "entry",
		PlanSequence: []atc.Step{
			{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
		},
	}

	benchValidate := func(c atc.Config) []string {
		_, errorMessages := configvalidate.Validate(c)
		return errorMessages
	}

	It("bench-graded: rejects negative run_retention values", func() {
		errs := benchValidate(atc.Config{
			Template:     true,
			RunRetention: &atc.RunRetentionConfig{KeepLast: -1},
			Jobs:         atc.JobConfigs{benchValidJob},
		})
		Expect(errs).To(ContainElement(ContainSubstring("run_retention.keep_last must not be negative")))

		errs = benchValidate(atc.Config{
			Template:     true,
			RunRetention: &atc.RunRetentionConfig{TTLDays: -7},
			Jobs:         atc.JobConfigs{benchValidJob},
		})
		Expect(errs).To(ContainElement(ContainSubstring("run_retention.ttl_days must not be negative")))

		errs = benchValidate(atc.Config{
			Template:     true,
			RunRetention: &atc.RunRetentionConfig{KeepLast: -2, TTLDays: -3},
			Jobs:         atc.JobConfigs{benchValidJob},
		})
		Expect(errs).To(ContainElement(ContainSubstring("run_retention.keep_last must not be negative")))
		Expect(errs).To(ContainElement(ContainSubstring("run_retention.ttl_days must not be negative")))
	})
})
