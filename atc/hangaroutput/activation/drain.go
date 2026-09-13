package activation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Residue is one class of live state that keeps a facet from reaching
// `disabled`, with the count and the reason.
//
// It carries the REASON rather than only the number because a refusal an
// operator cannot act on is a refusal they will work around. "17 rows" invites a
// DELETE; "17 registered generations whose objects are still in the bucket, and
// disabling the facet would leave them with no reclaimer" invites the right
// question.
type Residue struct {
	Class string
	Count int
	Why   string
}

func (residue Residue) String() string {
	return fmt.Sprintf("%s: %d (%s)", residue.Class, residue.Count, residue.Why)
}

// ErrDrainRefused is what a facet with live state answers.
var ErrDrainRefused = fmt.Errorf("%w: the facet still holds state", output.ErrConflict)

// DrainResidue reports every class of live state for one epoch's facet.
//
// It is a READ and it returns everything it finds rather than stopping at the
// first class. An operator draining a plane wants the whole list in one pass:
// discovering the next blocker only after clearing the previous one is how a
// downgrade turns into six hours of incremental surprises.
//
// Requirement 58's shape is here rather than in the CAS: emission stops FIRST
// (that is `draining`, and this predicate does not gate it), and only the
// terminal step asks whether anything is left. Safe releases and settlement
// continue throughout, which is why a `draining` facet is a supported state to
// sit in indefinitely -- claim tombstone purge is outside this track, so a
// deployment may legitimately never reach `disabled`.
func (epochs Epochs) DrainResidue(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet) ([]Residue, error) {
	if _, err := facet.Column(); err != nil {
		return nil, err
	}

	queries := baseResidueQueries
	if facet == FacetOutput {
		queries = outputResidueQueries
	}

	var residue []Residue
	for _, query := range queries {
		var count int
		if err := epochs.DB.QueryRowContext(ctx, query.sql, int64(epoch)).Scan(&count); err != nil {
			return nil, fmt.Errorf("%w: counting %s for epoch %d: %v",
				output.ErrInfrastructure, query.class, epoch, err)
		}
		if count == 0 {
			continue
		}
		residue = append(residue, Residue{Class: query.class, Count: count, Why: query.why})
	}
	sort.Slice(residue, func(i, j int) bool { return residue[i].Class < residue[j].Class })

	return residue, nil
}

// RefuseDrain turns residue into the diagnostic refusal.
func RefuseDrain(epoch executioncontrol.ActivationEpoch, facet Facet, residue []Residue) error {
	if len(residue) == 0 {
		return nil
	}
	lines := make([]string, 0, len(residue))
	for _, one := range residue {
		lines = append(lines, "  - "+one.String())
	}

	return fmt.Errorf("%w: epoch %d's %s facet cannot be disabled:\n%s\n\n"+
		"Unsafe removal is BLOCKED rather than promised after a finite drain. The "+
		"compatibility plane -- the lifecycle controllers, the claim and read-lease APIs, the "+
		"schema -- stays installed while any of this exists, and that is a supported state to "+
		"sit in rather than a failure to finish. Claim tombstone purge is outside this track, "+
		"so a deployment may legitimately never reach `disabled`; it is not a reason to erase "+
		"or reactivate tombstones.",
		ErrDrainRefused, epoch, facet, strings.Join(lines, "\n"))
}

type residueQuery struct {
	class string
	sql   string
	why   string
}

// baseResidueQueries are the base facet's: controlled executions that still
// need an exact finish or stop acknowledgement.
//
// Narrowed by decision F13 to CAPTURE-SELECTED executions. Widening this
// predicate to every controlled execution belongs to the sibling track
// `exact_execution_control`, which is where the non-capture callers live; this
// track has no way to enumerate them and would be asserting an empty set.
var baseResidueQueries = []residueQuery{
	{
		class: "predeclared handoffs with no disposition",
		sql: `SELECT count(*) FROM hangar_handoff_predeclarations predeclaration
		       WHERE predeclaration.activation_epoch = $1
		         AND NOT EXISTS (SELECT 1 FROM hangar_handoff_dispositions disposition
		                          WHERE disposition.handoff_id = predeclaration.handoff_id)`,
		why: "a capture-selected execution was admitted under this epoch and has not reached " +
			"an exact finish or stop acknowledgement; the base protocol is the only thing " +
			"that can still settle it",
	},
	{
		class: "unresolved capture reservations",
		sql: `SELECT count(*) FROM hangar_capture_reservations
		       WHERE activation_epoch = $1 AND state = 'unresolved'`,
		why: "a fenced owner may still verify and register an exact generation, or record a " +
			"terminal failure; taking the protocol away leaves it able to do neither",
	},
}

// outputResidueQueries are the output facet's. Every one of them is a thing the
// plane would forget how to finish.
var outputResidueQueries = []residueQuery{
	{
		class: "nonterminal capture reservations",
		sql: `SELECT count(*) FROM hangar_capture_reservations
		       WHERE activation_epoch = $1 AND state IN ('unresolved', 'resolved')`,
		why: "a capture may still create an object, or already has one whose generation is " +
			"not yet registered; disabling the facet would leave the object with a reservation " +
			"nothing will resolve and an orphan sweep that cannot adopt it",
	},
	{
		class: "live exact generations",
		sql: `SELECT count(*) FROM hangar_exact_lifecycles
		       WHERE activation_epoch = $1
		         AND state IN ('registered', 'adopted', 'reclaiming')`,
		why: "objects are in the bucket and this plane is what reclaims them. Disabling the " +
			"facet keeps the objects and removes the only thing that would ever delete them",
	},
	{
		class: "active claims",
		sql: `SELECT count(*) FROM hangar_claims claim
		       JOIN hangar_exact_lifecycles lifecycle ON lifecycle.id = claim.lifecycle_id
		       WHERE lifecycle.activation_epoch = $1 AND claim.released_at IS NULL`,
		why: "a consumer holds a claim on a managed output; releasing it is safe and continues " +
			"while draining, but disabling the facet would strand it",
	},
	{
		class: "active read leases",
		sql: `SELECT count(*) FROM hangar_read_leases lease
		       JOIN hangar_exact_lifecycles lifecycle ON lifecycle.id = lease.lifecycle_id
		       WHERE lifecycle.activation_epoch = $1 AND lease.released_at IS NULL`,
		why: "a materialization is in flight or its lease has not been closed; reclaim " +
			"admission refuses while one is open, so an abandoned lease with no plane to close " +
			"it pins its generation forever",
	},
	{
		class: "unfinalized reclaim jobs",
		sql: `SELECT count(*) FROM hangar_reclaim_jobs
		       WHERE activation_epoch = $1 AND finalized_at IS NULL`,
		why: "a conditional delete was admitted and has not been finalized. Already-admitted " +
			"delete work may FINISH while draining -- that is the one mutation a draining " +
			"facet still performs -- but a job with no outcome is an object whose disposition " +
			"is unknown",
	},
	{
		class: "open policy violations",
		sql: `SELECT count(*) FROM hangar_policy_violations
		       WHERE activation_epoch = $1 AND resolved_at IS NULL`,
		why: "the plane is at risk under this epoch and the reconciliation is an operator's. " +
			"Disabling the facet would close the record of a bucket whose lifetime policy " +
			"could not be proved safe, which is the one thing this plane exists to notice",
	},
	{
		class: "inventory debt",
		sql: `SELECT count(*) FROM hangar_inventory_debt
		       WHERE activation_epoch = $1`,
		why: "objects the sweep could not dispose of are recorded and unresolved; they are " +
			"the set nothing else will revisit",
	},
}

// DrainOutcome is what one facet's drain step did and what it found.
//
// It exists because the sequence below used to live in `cmd/hangar-output-
// activate`'s `drain`, interleaved with its printing, and therefore had no test
// at all: `Epochs.Disable` deliberately does not decide emptiness (see its own
// comment), so the ONLY thing standing between a live plane and `disabled` was
// twenty-five lines in package main that nothing exercised. Requirement 58's
// whole claim -- "unsafe removal is blocked rather than promised after a finite
// drain" -- rested on them. Moving the decision here and leaving the formatting
// in the command makes the claim assertable without changing what it does.
type DrainOutcome struct {
	Facet Facet

	// From is the facet's state on entry, and State its state on exit.
	From  string
	State string

	// Drained is true when this step made the enabled -> draining transition.
	// Skipped is true when the facet was neither enabled nor draining, so
	// there was nothing to drain.
	Drained bool
	Skipped bool

	// Residue is every class of live state found AFTER emission stopped.
	Residue []Residue

	// Disabled is true only when the facet reached the terminal state: the
	// caller asked to finalize AND the residue was empty.
	Disabled bool
}

// DrainStep stops new admission first, and only then asks whether anything is
// left.
//
// That ORDER is requirement 58: emission stops before the predicate runs, so the
// set the predicate is counting cannot grow while it counts. A drain that
// checked first and stopped emission afterwards would refuse on state that
// arrived in between and admit state that arrived after the check.
//
// With `finalize` false it reports and stops, which is the common operator
// action and is safe to repeat. With `finalize` true and any residue it returns
// ErrDrainRefused and disables nothing.
func (epochs Epochs) DrainStep(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet, finalize bool) (DrainOutcome, error) {
	outcome := DrainOutcome{Facet: facet}

	state, err := epochs.Read(ctx, epoch)
	if err != nil {
		return outcome, err
	}
	outcome.From = state.Base
	if facet == FacetOutput {
		outcome.From = state.Output
	}
	outcome.State = outcome.From

	if outcome.State == "enabled" {
		if err := epochs.Drain(ctx, epoch, facet); err != nil {
			return outcome, err
		}
		outcome.Drained = true
		outcome.State = "draining"
	}
	if outcome.State != "draining" {
		outcome.Skipped = true

		return outcome, nil
	}

	outcome.Residue, err = epochs.DrainResidue(ctx, epoch, facet)
	if err != nil {
		return outcome, err
	}
	if len(outcome.Residue) != 0 {
		if !finalize {
			return outcome, nil
		}

		return outcome, RefuseDrain(epoch, facet, outcome.Residue)
	}
	if !finalize {
		return outcome, nil
	}

	if err := epochs.Disable(ctx, epoch, facet); err != nil {
		return outcome, err
	}
	outcome.Disabled = true
	outcome.State = "disabled"

	return outcome, nil
}

// DrainFacets is the order `--facet=all` takes them in: OUTPUT first, base
// second. Base is what settles a capture, so a plane that lost exact execution
// control while an output capture was still nonterminal would have removed the
// only thing that could finish it.
func DrainFacets(all bool, facet Facet) []Facet {
	if all {
		return []Facet{FacetOutput, FacetBase}
	}

	return []Facet{facet}
}
