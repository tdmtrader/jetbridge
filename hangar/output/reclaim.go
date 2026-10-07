package output

import (
	"encoding/json"
	"fmt"
)

// ReclaimOutcome is how a reclaim job ended.
//
// Four members, and the difference between the first two is the whole point of
// Req 49. `reclaimed_confirmed` needs an acknowledged conditional delete;
// `reclaimed_inferred` is a durable admitted-delete record whose response was
// lost, plus observed exact absence. They are not the same evidence and this
// plane never lets one stand in for the other, because confirming a deletion it
// did not see acknowledged is how a lifetime violation becomes a normal
// reclamation in the record.
type ReclaimOutcome string

const (
	// ReclaimConfirmed is an acknowledged conditional delete.
	ReclaimConfirmed ReclaimOutcome = "reclaimed_confirmed"

	// ReclaimInferred is a durable admitted delete, a lost response, and exact
	// absence observed afterwards. Absence WITHOUT a prior admitted delete is
	// an out-of-band lifetime violation and never this.
	ReclaimInferred ReclaimOutcome = "reclaimed_inferred"

	// ReclaimConflicted is a replacement generation, metageneration or marker
	// found where the exact one was expected. It becomes debt and never
	// broadens into an unconditional delete.
	ReclaimConflicted ReclaimOutcome = "conflicted"

	// ReclaimAbandoned is a job given up on -- an unauthorized principal, a
	// policy that went at-risk before any delete was admitted. The generation
	// goes back to being protected rather than being deleted on a guess.
	ReclaimAbandoned ReclaimOutcome = "abandoned"
)

func ReclaimOutcomes() []ReclaimOutcome {
	return []ReclaimOutcome{
		ReclaimConfirmed,
		ReclaimInferred,
		ReclaimConflicted,
		ReclaimAbandoned,
	}
}

func ParseReclaimOutcome(value string) (ReclaimOutcome, error) {
	for _, member := range ReclaimOutcomes() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: reclaim outcome %q; the vocabulary is %v",
		ErrUnknownMember, value, ReclaimOutcomes())
}

func (outcome *ReclaimOutcome) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseReclaimOutcome(text)
	if err != nil {
		return err
	}
	*outcome = parsed

	return nil
}

func (outcome ReclaimOutcome) Validate() error {
	_, err := ParseReclaimOutcome(string(outcome))

	return err
}

// PlaneCounts is what the output plane is holding, as an operator reads it,
// and the residue a drain waits on.
//
// Counts and not lists. An operator watching a plane wants to know whether the
// numbers are moving, and a status surface that returned every live generation
// would be the thing that fell over on the deployment where the number mattered.
type PlaneCounts struct {
	// LiveGenerations are registered, adopted or reclaiming objects: the set
	// this plane is responsible for, and the set a downgrade cannot abandon.
	LiveGenerations int

	// NonterminalCaptures may still create an object or still owe a
	// registration. They are what a capture deadline eventually settles:
	// PendingCaptures plus PublishingCaptures.
	NonterminalCaptures int
	PendingCaptures     int
	PublishingCaptures  int

	// UnreleasedCaptures are terminal captures whose step marker the node has
	// not yet acknowledged clearing. The release pass retries them forever.
	UnreleasedCaptures int

	// UnacknowledgedReleases are captures released because their node was
	// gone or re-registered: no node acknowledged clearing the marker, which
	// may remain on a node that returns with the same disk. Reported, not
	// residue: nothing in the plane is waiting on them.
	UnacknowledgedReleases int

	// OpenClaims and OpenReadLeases are the protections consumers hold. Each
	// one keeps reclaim admission refusing for the generation it names, so an
	// open lease nobody is using is an object nothing will ever delete.
	OpenClaims     int
	OpenReadLeases int

	// UnfinalizedReclaimJobs were admitted and have no outcome. Every one of
	// them is an object whose disposition is unknown: the delete may have
	// happened, and the answer may have been lost.
	UnfinalizedReclaimJobs int

	// OpenIntegrityFindings are unresolved findings of any class. The two
	// runtime classes block admission until an operator resolves them.
	OpenIntegrityFindings int
}

// Residue is what a drain waits on: everything that still needs the output
// plane's daemons, or its web passes, to finish.
func (counts PlaneCounts) Residue() int {
	return counts.PendingCaptures + counts.PublishingCaptures + counts.UnreleasedCaptures +
		counts.OpenClaims + counts.OpenReadLeases + counts.UnfinalizedReclaimJobs
}
