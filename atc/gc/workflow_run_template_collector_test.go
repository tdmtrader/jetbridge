package gc_test

import (
	"context"
	"errors"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/gc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("WorkflowRunTemplateCollector", func() {
	var (
		collector GcCollector
		lifecycle *dbfakes.FakeWorkflowRunTemplateLifecycle
	)

	BeforeEach(func() {
		lifecycle = new(dbfakes.FakeWorkflowRunTemplateLifecycle)
		collector = gc.NewWorkflowRunTemplateCollector(lifecycle, time.Hour)
	})

	It("removes abandoned templates with the configured grace period and a bounded batch", func() {
		lifecycle.RemoveAbandonedWorkflowRunTemplatesReturns(3, nil)

		Expect(collector.Run(context.TODO())).To(Succeed())

		Expect(lifecycle.RemoveAbandonedWorkflowRunTemplatesCallCount()).To(Equal(1))
		_, gracePeriod, limit := lifecycle.RemoveAbandonedWorkflowRunTemplatesArgsForCall(0)
		Expect(gracePeriod).To(Equal(time.Hour))
		Expect(limit).To(BeNumerically(">", 0))
		Expect(limit).To(BeNumerically("<=", db.MaxAbandonedWorkflowRunTemplateBatch))
	})

	It("returns the failure so the component runner records it", func() {
		lifecycle.RemoveAbandonedWorkflowRunTemplatesReturns(0, errors.New("nope"))

		Expect(collector.Run(context.TODO())).To(MatchError("nope"))
	})
})
