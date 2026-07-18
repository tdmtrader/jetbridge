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

	// C3: terminally-disposed agent tickets leave dead dashboard cards; the
	// lifecycler archives every attempt's run instance plus the base template.
	It("archives the runs and templates of terminal tickets", func() {
		run := new(dbfakes.FakePipelineRun)
		factory.RunsForTerminalTicketsReturns([]db.PipelineRun{run}, nil)
		template := new(dbfakes.FakePipeline)
		factory.TemplatesForTerminalTicketsReturns([]db.Pipeline{template}, nil)

		Expect(lifecycler.Run(context.Background())).To(Succeed())
		Expect(run.ArchiveCallCount()).To(Equal(1))
		Expect(template.ArchiveCallCount()).To(Equal(1))
	})

	It("continues past a failing terminal-ticket archive", func() {
		bad := new(dbfakes.FakePipelineRun)
		bad.ArchiveReturns(errors.New("boom"))
		good := new(dbfakes.FakePipelineRun)
		factory.RunsForTerminalTicketsReturns([]db.PipelineRun{bad, good}, nil)
		template := new(dbfakes.FakePipeline)
		factory.TemplatesForTerminalTicketsReturns([]db.Pipeline{template}, nil)

		Expect(lifecycler.Run(context.Background())).To(Succeed())
		Expect(good.ArchiveCallCount()).To(Equal(1))
		Expect(template.ArchiveCallCount()).To(Equal(1))
	})
})
