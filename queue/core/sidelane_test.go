package core_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("SameAhead: a verdict is used only with the changes it was tested with", func() {
	tested := []string{"a", "b"}

	It("A red tested with exactly the changes that would land before it stands", func() {
		ok, why := core.SameAhead(tested, []string{"b", "a"})
		Expect(ok).To(BeTrue())
		Expect(why).To(BeEmpty())
	})

	It("A red tested without a change that now lands before it is recomposed, not blamed", func() {
		ok, why := core.SameAhead(tested, []string{"a", "b", "c"})
		Expect(ok).To(BeFalse())
		Expect(why).To(Equal("tested with [a b] ahead, but [a b c] would land before it now"))
	})

	It("A red tested with a change that no longer lands before it is recomposed, not blamed", func() {
		ok, why := core.SameAhead(tested, []string{"a"})
		Expect(ok).To(BeFalse())
		Expect(why).To(Equal("tested with [a b] ahead, but [a] would land before it now"))
	})

	It("stands with nothing ahead on either side", func() {
		ok, _ := core.SameAhead(nil, []string{})
		Expect(ok).To(BeTrue())
	})

	It("is recomposed when one change stands in for a different one", func() {
		ok, _ := core.SameAhead([]string{"a", "a"}, []string{"a", "b"})
		Expect(ok).To(BeFalse())
		ok, _ = core.SameAhead([]string{"a", "x"}, []string{"a", "b"})
		Expect(ok).To(BeFalse())
	})

	It("does not reorder either side", func() {
		t, now := []string{"b", "a"}, []string{"a", "b"}
		core.SameAhead(t, now)
		Expect(t).To(Equal([]string{"b", "a"}))
		Expect(now).To(Equal([]string{"a", "b"}))
	})
})
