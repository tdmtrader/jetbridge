package schema_test

import (
	"testing"

	schema "github.com/concourse/concourse/agent/schema"
)

func TestThreeWayStatus(t *testing.T) {
	// maps results.json wire statuses to the DB/API taxonomy
	cases := []struct {
		name          string
		in            schema.Status
		want          string
		wantAbstained bool
	}{
		{"pass", schema.StatusPass, schema.RunStatusOK, false},
		{"fail", schema.StatusFail, schema.RunStatusFailed, false},
		{"error", schema.StatusError, schema.RunStatusError, false},
		{"abstain", schema.StatusAbstain, schema.RunStatusFailed, true},
		// PARK-V2 is gone: 'parked' is now just an unknown wire value.
		{"parked", schema.Status("parked"), schema.RunStatusError, false},
		{"unknown", schema.Status("bogus"), schema.RunStatusError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, abstained := schema.ThreeWayStatus(tc.in)
			requireEqual(t, got, tc.want)
			requireEqual(t, abstained, tc.wantAbstained)
		})
	}
}

func TestRunStatusIncompleteVocabulary(t *testing.T) {
	// L-1 (#41): 'incomplete' is an ingestion-degradation status (no flight
	// output read), never a results.json wire value — ThreeWayStatus must not
	// emit it. It exists only for DeriveOutcome to fuse to amber "unrecorded".
	requireEqual(t, schema.RunStatusIncomplete, "incomplete")
}
