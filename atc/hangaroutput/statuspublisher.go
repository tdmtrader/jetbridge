package hangaroutput

import (
	"context"
	"strings"

	"code.cloudfoundry.org/lager/v3"
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
// this plane's: a violation class, a debt reason and an operation kind are
// closed sets declared in hangar/output, and atc/metric having opinions about
// them would be a second place they are enumerated.
func statusEvent(status Status) metric.HangarOutputSnapshot {
	snapshot := metric.HangarOutputSnapshot{
		AtRisk:                 status.AtRisk,
		Reasons:                strings.Join(status.Why, ","),
		InventoryCycle:         status.Cycle,
		InventoryAtCycleStart:  status.AtCycleStart,
		DebtTruncated:          status.DebtTruncated,
		LiveGenerations:        status.Counts.LiveGenerations,
		NonterminalCaptures:    status.Counts.NonterminalCaptures,
		OpenClaims:             status.Counts.OpenClaims,
		OpenReadLeases:         status.Counts.OpenReadLeases,
		UnfinalizedReclaimJobs: status.Counts.UnfinalizedReclaimJobs,
		Violations:             map[string]int{},
		Debt:                   map[string]int{},
		LeaseRemainingSeconds:  map[string]float64{},
	}

	// Every member of every closed vocabulary, including the zeroes. A gauge
	// that only appears when it is nonzero is one an alert cannot distinguish
	// from a scrape that did not happen: `hangar_output_policy_violations > 0`
	// fires the same way on a missing series as on a healthy one, which is to
	// say not at all.
	for _, violation := range output.PolicyViolations() {
		snapshot.Violations[string(violation)] = status.Violations[violation]
	}
	for _, reason := range output.DebtReasons() {
		snapshot.Debt[string(reason)] = status.Debt[reason]
	}
	// OwnedOperationKinds and NOT OperationKinds. The vocabulary is nine; the
	// kinds a deployed workload takes a lease for are four. Emitting -1 for the
	// other five made HangarOutputOperationLeaseUnheld fire five permanent
	// warnings ten minutes after a clean install of a healthy plane, each
	// saying "its controller is not running" about a controller that does not
	// exist -- which is the shape of alert an operator silences, taking the
	// four real ones with it. See leaseowners.go for the five and why.
	for _, kind := range OwnedOperationKinds() {
		remaining, held := status.Leases[kind]
		if !held {
			// A kind whose owner holds nothing is reported as a negative term
			// rather than as zero or as an absent series: zero would read as
			// "expired right now" and absence would read as "not scraped". -1
			// is neither, and the alert rule that cares says so.
			snapshot.LeaseRemainingSeconds[string(kind)] = -1

			continue
		}
		snapshot.LeaseRemainingSeconds[string(kind)] = remaining.Seconds()
	}

	return snapshot
}

var _ = lager.Logger(nil)
