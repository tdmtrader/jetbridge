package core_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Rank", func() {
	It("Suspects are ranked from the failed test names and the files each change touched", func() {
		batch := ents("a", "b", "c")
		changed := map[string][]string{"a": {"web/x.ts", "README.md"}, "b": {"atc/db/pipes.go"}, "c": {"atc/api/h.go", "atc/db/jobs.go"}}
		failed := []string{"atc/db TestPipes", "atc/api TestHandler", "atc/db TestJobs", "TestMain"}
		Expect(core.Rank(failed, batch, changed)).To(Equal([]string{"c", "b"}))
		Expect(core.Rank([]string{"atc/db TestPipes"}, batch, changed)).To(Equal([]string{"b", "c"}))
		Expect(core.Rank([]string{"README TestMain"}, batch, changed)).To(BeEmpty())
		Expect(core.Rank(nil, batch, changed)).To(BeEmpty())
	})
})
