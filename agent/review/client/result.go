package client

import (
	"context"
	"io/fs"

	"github.com/concourse/concourse/agent/detached"
	"github.com/concourse/concourse/agent/review"
)

// Result retrieves the typed review report for a completed Run. The shared
// client verifies the archive against the Run's immutable binding before the
// report is parsed, and the report's run_id against the Run after.
func (c *Client) Result(ctx context.Context, handle Handle, name string) (*review.Report, error) {
	parsed, err := c.Client.Result(ctx, handle, name, parseReviewReport)
	if err != nil {
		return nil, err
	}
	return parsed.(publishedReport).report, nil
}

type publishedReport struct{ report *review.Report }

func (p publishedReport) RunID() int {
	if p.report.RunID == nil {
		return 0
	}
	return *p.report.RunID
}

func parseReviewReport(tree fs.FS) (detached.RunIdentified, error) {
	body, err := detached.ReadResultFile(tree, "review.json", 16<<20)
	if err != nil {
		return nil, err
	}
	report, err := review.ParsePublishedReport(body)
	if err != nil {
		return nil, err
	}
	return publishedReport{report}, nil
}
