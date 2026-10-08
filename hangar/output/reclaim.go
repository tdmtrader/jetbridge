package output

// PlaneCounts is what the output plane is holding, as an operator reads it,
// and the residue a drain waits on.
//
// Counts and not lists. An operator watching a plane wants to know whether the
// numbers are moving, and a status surface that returned every live generation
// would be the thing that fell over on the deployment where the number mattered.
type PlaneCounts struct {
	// LiveGenerations are registered objects: the set this plane is
	// responsible for, and the set a downgrade cannot abandon. A reclaimed
	// generation is not one.
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

	// OpenClaims are the live holds on generations: consumers' holds and
	// readers' holds that have neither been released nor expired. Each one
	// keeps the reclaim pass off the generation it names, so an open claim
	// nobody is using is an object nothing will ever delete.
	OpenClaims int

	// OpenIntegrityFindings are unresolved findings of any class. The two
	// runtime classes block admission until an operator resolves them.
	OpenIntegrityFindings int
}

// Residue is what a drain waits on: everything that still needs the output
// plane's daemons, or its web passes, to finish.
func (counts PlaneCounts) Residue() int {
	return counts.PendingCaptures + counts.PublishingCaptures + counts.UnreleasedCaptures +
		counts.OpenClaims
}
