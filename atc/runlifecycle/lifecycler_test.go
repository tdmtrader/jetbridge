package runlifecycle_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runlifecycle"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Lifecycler", func() {
	var (
		factory    *dbfakes.FakePipelineRunFactory
		lifecycler *runlifecycle.Lifecycler
	)

	BeforeEach(func() {
		factory = new(dbfakes.FakePipelineRunFactory)
		lifecycler = runlifecycle.NewLifecycler(factory)
	})

	It("finishes complete runs with their aggregate status", func() {
		complete := new(dbfakes.FakePipelineRun)
		complete.CheckCompleteReturns(db.PipelineRunFailed, true, nil)
		incomplete := new(dbfakes.FakePipelineRun)
		incomplete.CheckCompleteReturns("", false, nil)
		factory.RunningRunsReturns([]db.PipelineRun{complete, incomplete}, nil)

		Expect(lifecycler.Run(context.Background())).To(Succeed())

		Expect(complete.FinishCallCount()).To(Equal(1))
		Expect(complete.FinishArgsForCall(0)).To(Equal(db.PipelineRunFailed))
		Expect(incomplete.FinishCallCount()).To(Equal(0))
	})

	It("reopens completed runs with new activity", func() {
		retriggered := new(dbfakes.FakePipelineRun)
		factory.CompletedRunsWithNewActivityReturns([]db.PipelineRun{retriggered}, nil)

		Expect(lifecycler.Run(context.Background())).To(Succeed())
		Expect(retriggered.ReopenCallCount()).To(Equal(1))
	})

	It("archives expired runs", func() {
		expired := new(dbfakes.FakePipelineRun)
		factory.RunsToArchiveReturns([]db.PipelineRun{expired}, nil)

		Expect(lifecycler.Run(context.Background())).To(Succeed())
		Expect(expired.ArchiveCallCount()).To(Equal(1))
	})

	It("continues past per-run errors", func() {
		bad := new(dbfakes.FakePipelineRun)
		bad.CheckCompleteReturns("", false, errors.New("boom"))
		good := new(dbfakes.FakePipelineRun)
		good.CheckCompleteReturns(db.PipelineRunSucceeded, true, nil)
		factory.RunningRunsReturns([]db.PipelineRun{bad, good}, nil)

		Expect(lifecycler.Run(context.Background())).To(Succeed())
		Expect(good.FinishCallCount()).To(Equal(1))
	})

	// Generic run lifecycle is preserved after the ticket-template archival
	// passes were removed (v3-only cleanup): the retired ticket-named
	// per-ticket pipeline lifecycle is gone, but finish, reopen, and retention
	// archival of generic runs must all still run.
	It("still finishes, reopens, and archives generic runs", func() {
		running := new(dbfakes.FakePipelineRun)
		running.CheckCompleteReturns(db.PipelineRunSucceeded, true, nil)
		expired := new(dbfakes.FakePipelineRun)
		factory.RunningRunsReturns([]db.PipelineRun{running}, nil)
		factory.RunsToArchiveReturns([]db.PipelineRun{expired}, nil)
		Expect(lifecycler.Run(context.Background())).To(Succeed())
		Expect(running.FinishCallCount()).To(Equal(1))
		Expect(expired.ArchiveCallCount()).To(Equal(1))
	})
})
