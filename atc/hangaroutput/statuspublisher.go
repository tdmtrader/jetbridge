package hangaroutput

import (
	"context"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/hangar/output"
)

// StatusPublisher turns one status read into metrics on the existing scrape
// path.
//
// It is a component and not an API endpoint, deliberately. What an operator
// needs from a deletion plane that has gone fail-closed is an ALERT, and an
// alert is a rule over a series somebody is already scraping -- a status page
// nobody has open at three in the morning is the same as no status page. The
// series are named so the chart's alerting rules can be written against them,
// and deploy/chart/tests/alert_metric_drift_test.go reads the emitter's own
// declarations rather than anybody's memory of these names.
//
// It publishes and never decides. Every predicate behind these numbers is one
// the enforcing path calls; a publisher that judged for itself would be a
// second opinion, and the one that is not the enforcement is the one that
// drifts.
type StatusPublisher struct {
	Reader *StatusReader
}

// Run takes one pass and emits it.
//
// A failed read is NOT silence. A status surface that emitted nothing when it
// could not read would be indistinguishable from a healthy plane with nothing
// to report, which is the exact failure the at-risk state exists to make
// visible. So the read failure is logged and the at-risk gauge is left at
// whatever it last said rather than being cleared to zero.
func (publisher *StatusPublisher) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("hangar-output-status")

	status, err := publisher.Reader.Read(ctx)
	if err != nil {
		logger.Error("status-read-failed", err)

		return err
	}

	metric.HangarOutputStatus{Status: statusEvent(status)}.Emit(logger)

	return nil
}

// statusEvent flattens one read into the shape the emitter publishes.
//
// The flattening is here rather than in atc/metric because the vocabulary is
// this plane's: a violation class is a closed set declared in hangar/output,
// and atc/metric having opinions about it would be a second place it is
// enumerated.
func statusEvent(status Status) metric.HangarOutputSnapshot {
	snapshot := metric.HangarOutputSnapshot{
		Enabled:                status.Enabled,
		AtRisk:                 status.AtRisk,
		Reasons:                strings.Join(status.Why, ","),
		LiveGenerations:        status.Counts.LiveGenerations,
		NonterminalCaptures:    status.Counts.NonterminalCaptures,
		PendingCaptures:        status.Counts.PendingCaptures,
		PublishingCaptures:     status.Counts.PublishingCaptures,
		UnreleasedCaptures:     status.Counts.UnreleasedCaptures,
		UnacknowledgedReleases: status.Counts.UnacknowledgedReleases,
		OpenClaims:             status.Counts.OpenClaims,
		OpenIntegrityFindings:  status.Counts.OpenIntegrityFindings,
		Residue:                status.Counts.Residue(),
		Violations:             map[string]int{},
	}

	// Every member of the closed vocabulary, including the zeroes. A gauge
	// that only appears when it is nonzero is one an alert cannot distinguish
	// from a scrape that did not happen.
	for _, violation := range output.IntegrityViolations() {
		snapshot.Violations[string(violation)] = status.Violations[violation]
	}

	return snapshot
}
