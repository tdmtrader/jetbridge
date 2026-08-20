package gc_test

import (
	"context"
	"errors"
	"time"

	"github.com/concourse/concourse/atc/gc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type fakeRunReclaimLifecycle struct {
	candidates    []int
	candidateErr  error
	destroyed     []int
	destroyResult map[int]bool
	destroyErr    map[int]error
	deferred      map[int]time.Time
	deferErr      map[int]error
	limit         int
}

func (f *fakeRunReclaimLifecycle) ReclaimCandidateRunIDs(limit int) ([]int, error) {
	f.limit = limit
	return f.candidates, f.candidateErr
}

func (f *fakeRunReclaimLifecycle) DestroyReclaimableRun(id int) (bool, error) {
	f.destroyed = append(f.destroyed, id)
	return f.destroyResult[id], f.destroyErr[id]
}

func (f *fakeRunReclaimLifecycle) DeferRunReclaim(id int, retryAt time.Time) error {
	f.deferred[id] = retryAt
	return f.deferErr[id]
}

var _ = Describe("PipelineRunReclaimer", func() {
	var (
		lifecycle *fakeRunReclaimLifecycle
		now       time.Time
		collector GcCollector
	)

	BeforeEach(func() {
		now = time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC)
		lifecycle = &fakeRunReclaimLifecycle{
			destroyResult: map[int]bool{},
			destroyErr:    map[int]error{},
			deferred:      map[int]time.Time{},
			deferErr:      map[int]error{},
		}
		collector = gc.NewPipelineRunReclaimer(lifecycle, func() time.Time { return now })
	})

	It("requests one bounded batch and attempts every candidate", func() {
		lifecycle.candidates = []int{4, 7, 9}
		lifecycle.destroyResult[4] = true
		lifecycle.destroyResult[7] = false
		lifecycle.destroyResult[9] = true

		Expect(collector.Run(context.Background())).To(Succeed())
		Expect(lifecycle.limit).To(Equal(20))
		Expect(lifecycle.destroyed).To(Equal([]int{4, 7, 9}))
		Expect(lifecycle.deferred).To(BeEmpty(), "normal locked recheck misses do not create retry debt")
	})

	It("defers a real candidate error by exactly five minutes and continues", func() {
		lifecycle.candidates = []int{1, 2, 3}
		lifecycle.destroyErr[1] = errors.New("first failed")
		lifecycle.destroyResult[2] = true
		lifecycle.destroyErr[3] = errors.New("third failed")

		err := collector.Run(context.Background())
		Expect(err).To(MatchError(And(ContainSubstring("first failed"), ContainSubstring("third failed"))))
		Expect(lifecycle.destroyed).To(Equal([]int{1, 2, 3}), "one failure must not monopolize the bounded batch")
		Expect(lifecycle.deferred).To(Equal(map[int]time.Time{
			1: now.Add(5 * time.Minute),
			3: now.Add(5 * time.Minute),
		}))
	})

	It("reports a defer error without preventing later candidates", func() {
		lifecycle.candidates = []int{1, 2}
		lifecycle.destroyErr[1] = errors.New("destroy failed")
		lifecycle.deferErr[1] = errors.New("defer failed")
		lifecycle.destroyResult[2] = true

		err := collector.Run(context.Background())
		Expect(err).To(MatchError(And(ContainSubstring("destroy failed"), ContainSubstring("defer failed"))))
		Expect(lifecycle.destroyed).To(Equal([]int{1, 2}))
	})

	It("returns candidate selection errors without attempting stale work", func() {
		lifecycle.candidates = []int{1}
		lifecycle.candidateErr = errors.New("selection failed")

		Expect(collector.Run(context.Background())).To(MatchError("selection failed"))
		Expect(lifecycle.destroyed).To(BeEmpty())
		Expect(lifecycle.deferred).To(BeEmpty())
	})
})
