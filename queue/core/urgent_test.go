package core_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("The urgent lane", func() {
	var q *core.Queue
	form := func(buildsOn map[string][]string) []string {
		return ids(core.FormBatch(q.SelectBatch(8), buildsOn, nil).Entries())
	}
	BeforeEach(func() { q = &core.Queue{} })
	queue := func(names ...string) {
		for _, n := range names {
			Expect(q.Admit(entry(n))).To(Succeed())
		}
	}

	It("An urgent change goes to the front of the next batch", func() {
		queue("a", "b", "c")
		Expect(q.Promote("c")).To(Succeed())
		Expect(form(nil)).To(Equal([]string{"c", "a", "b"}))
	})

	It("Urgent changes keep their admission order among themselves", func() {
		queue("a", "b", "c", "d")
		Expect(q.Promote("d")).To(Succeed())
		Expect(q.Promote("b")).To(Succeed())
		Expect(form(nil)).To(Equal([]string{"b", "d", "a", "c"}))
	})

	It("An urgent change never skips the queued change it builds on", func() {
		queue("a", "b", "c")
		Expect(q.Promote("c")).To(Succeed())
		Expect(form(map[string][]string{"c": {"b"}})).To(Equal([]string{"b", "c", "a"}))
	})

	It("Only a queued change can be marked urgent", func() {
		queue("a")
		Expect(q.Settle("a", core.Ejected)).To(Succeed())
		Expect(refusedAs(q.Promote("a"))).To(Equal(core.Ejected))
		Expect(q.Promote("z")).To(HaveOccurred())
	})
})
