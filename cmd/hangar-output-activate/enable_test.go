package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/hangaroutput/activation"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// What an operator is told when the enable step reads the preconditions.
//
// The step itself is driven against real PostgreSQL in atc/hangaroutput, one
// missing precondition at a time; what could not be asserted there is what
// reaches the terminal, because the printing is this binary's. It is asserted
// here over the real value type -- the same struct EnablePreconditions returns
// -- rather than through a double, because the printer is a pure function of
// that list and nothing else.
//
// The refusal error names the UNMET ones on its own. This prints the whole
// list, and that is the part worth pinning: an operator refused for one reason
// needs to see that the other nine were asked, and an operator who succeeds
// needs the record of what was true at the moment they turned the plane on.
func TestEveryPreconditionReachesTheOperatorMetOrNot(t *testing.T) {
	preconditions := []activation.Precondition{
		{
			Name:   "schema migration",
			Met:    true,
			Detail: "migration 1788936403 is applied",
			Why:    "the plane's tables, triggers and typed SQLSTATEs are this migration",
		},
		{
			Name:   "storage integrity",
			Met:    false,
			Detail: "1 unresolved runtime finding",
			Why: "unresolved storage loss blocks new work, and it requires an operator to " +
				"lifecycle rule that deletes this plane's objects goes unnoticed",
		},
	}

	var out bytes.Buffer
	reportPreconditions(&out, executioncontrol.ActivationEpoch(71),
		activation.FacetOutput, preconditions)
	printed := out.String()

	for _, want := range []string{
		"schema migration",
		"migration 1788936403 is applied",
		"storage integrity",
		"1 unresolved runtime finding",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("the operator is never told %q:\n%s", want, printed)
		}
	}

	// The unmet one has to be distinguishable at a glance, and has to carry
	// the reason. A list where the two read the same is a list an operator
	// scans past.
	unmetLine := ""
	for _, line := range strings.Split(printed, "\n") {
		if strings.Contains(line, "storage integrity") {
			unmetLine = line
		}
	}
	if unmetLine == "" || !strings.Contains(unmetLine, "UNMET") {
		t.Errorf("the unmet precondition is not marked as unmet, so it reads like the eight "+
			"that are fine:\n%s", printed)
	}
	if strings.Contains(printed, "the plane's tables, triggers") {
		t.Errorf("a MET precondition printed its why as well, which is nine lines of reasons "+
			"about things nobody has to do:\n%s", printed)
	}
	if !strings.Contains(printed, "lifecycle rule that deletes this plane's objects") {
		t.Errorf("the unmet precondition printed no reason, so the operator is told what is "+
			"false and not what to go and do:\n%s", printed)
	}

	// The non-vacuity: an empty list prints nothing at all rather than a
	// heading claiming a check that did not happen.
	var empty bytes.Buffer
	reportPreconditions(&empty, executioncontrol.ActivationEpoch(71), activation.FacetOutput, nil)
	if empty.Len() != 0 {
		t.Errorf("an empty precondition list announced itself: %q", empty.String())
	}
}
