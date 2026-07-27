package gc

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
)

// workflowRunTemplateBatchSize keeps one pass short: the collector holds a row
// lock on every candidate for the length of its transaction, and admission
// waits on that lock.
const workflowRunTemplateBatchSize = 100

type workflowRunTemplateCollector struct {
	lifecycle        db.WorkflowRunTemplateLifecycle
	gracePeriod      time.Duration
	retirementPeriod time.Duration
}

func NewWorkflowRunTemplateCollector(
	lifecycle db.WorkflowRunTemplateLifecycle,
	gracePeriod time.Duration,
	retirementPeriod time.Duration,
) *workflowRunTemplateCollector {
	return &workflowRunTemplateCollector{
		lifecycle:        lifecycle,
		gracePeriod:      gracePeriod,
		retirementPeriod: retirementPeriod,
	}
}

func (c *workflowRunTemplateCollector) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("workflow-run-template-collector")

	logger.Debug("start")
	defer logger.Debug("done")

	start := time.Now()
	defer func() {
		metric.WorkflowRunTemplateCollectorDuration{
			Duration: time.Since(start),
		}.Emit(logger)
	}()

	destroyed, err := c.lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, c.gracePeriod, workflowRunTemplateBatchSize)
	if err != nil {
		logger.Error("failed-to-remove-abandoned-workflow-run-templates", err)
		return err
	}
	if destroyed > 0 {
		logger.Info("removed-abandoned-workflow-run-templates", lager.Data{"destroyed": destroyed})
	}

	if c.retirementPeriod > 0 {
		retired, err := c.lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, c.retirementPeriod, workflowRunTemplateBatchSize)
		if err != nil {
			logger.Error("failed-to-remove-retired-workflow-run-templates", err)
			return err
		}
		if retired > 0 {
			logger.Info("removed-retired-workflow-run-templates", lager.Data{"destroyed": retired})
		}
	}

	return nil
}
