package core_test

import (
	"context"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("ComposeVerdict", func() {
	var q *core.Queue
	policy := core.Policy{RetryNone: 1}
	BeforeEach(func() {
		q = &core.Queue{}
		for _, n := range []string{"a", "b"} {
			Expect(q.Admit(entry(n))).To(Succeed())
		}
	})
	decide := func(err error, batch []core.Entry, retries int) core.Decision {
		d, derr := core.Decide(core.ComposeVerdict(err, batch), batch, retries, policy)
		Expect(derr).NotTo(HaveOccurred())
		Expect(core.Apply(q, d, batch)).To(Succeed())
		return d
	}

	It("An unreadable compose result pauses the queue and ejects nobody", func() {
		batch := q.SelectBatch(8)
		corrupt := errors.New("compose record: unexpected end of JSON input")
		Expect(decide(corrupt, batch, 0)).To(Equal(core.Retry))
		Expect(decide(corrupt, batch, 1)).To(Equal(core.Pause))
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"a", "b"}))
	})

	It("A named conflict ejects only that change", func() {
		batch := q.SelectBatch(8)
		conflict := fmt.Errorf("compose: %w", core.ConflictError{EntryID: "b"})
		Expect(decide(conflict, batch, 0)).To(Equal(core.Split))
		b, err := core.NewStackBisect(core.FormBatch(batch, nil, nil), policy)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids(b.Next())).To(Equal([]string{"a"}))
		Expect(b.Record(core.Pass)).To(Equal(core.Land)) // "a" composed, and its run passed
		Expect(settle(q, core.Land, batch[:1])).To(Succeed())
		Expect(ids(b.Next())).To(Equal([]string{"b"}))
		Expect(b.Record(core.ComposeVerdict(conflict, b.Next()))).To(Equal(core.Eject))
		Expect(core.Apply(q, core.Eject, batch[1:])).To(Succeed())
		Expect(refusedAs(q.Admit(entry("a")))).To(Equal(core.Landed))
		Expect(refusedAs(q.Admit(entry("b")))).To(Equal(core.Ejected))
	})

	It("counts a timeout, an unknown error or a conflict outside the batch as no verdict", func() {
		batch := q.SelectBatch(8)
		for _, err := range []error{
			context.DeadlineExceeded,
			errors.New("boom"),
			core.ConflictError{EntryID: "z"},
			core.ConflictError{},
		} {
			Expect(core.ComposeVerdict(err, batch)).To(Equal(core.None), err.Error())
		}
		_, err := core.Decide(core.ComposeVerdict(nil, batch), batch, 0, policy)
		Expect(err).To(HaveOccurred(), "a composed candidate must be run, not landed")
	})

	It("a conflict with an empty ID ejects nobody, even when the batch holds an empty-ID entry", func() {
		batch := []core.Entry{{ID: ""}, {ID: "a"}}
		Expect(core.ComposeVerdict(core.ConflictError{}, batch)).To(Equal(core.None))
	})

	It("reads a pointer *ConflictError like the value form", func() {
		Expect(core.ComposeVerdict(fmt.Errorf("x: %w", &core.ConflictError{EntryID: "b"}), q.SelectBatch(8))).To(Equal(core.Fail))
	})
})
