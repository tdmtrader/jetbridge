package output

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
)

// The structural twin of the tamper table.
//
// The tamper table (receipt_signing_test.go) is a list somebody writes by hand,
// and a claim that is added to ReceiptClaims and not added to that list is
// invisible: the table still passes, the new claim is outside the signature,
// and the only way anyone finds out is a review that happens to read
// CanonicalReceiptBytes beside the struct. That is exactly how
// Incarnation.NodeUID came to be a validated part of the source identity that a
// verifier would accept edited.
//
// So this walks ReceiptClaims with reflect and requires every exported leaf to
// be accounted for by name. A new field fails here until somebody says, in one
// of three ways, how the signature treats it -- and two of the three are
// assertions rather than declarations:
//
//   - tamper: change this leaf alone to another value the claims still
//     validate with, and the canonical bytes must differ. This is the strong
//     form and most leaves have it.
//
//   - spelling + occurrences: the leaf is pinned by Validate to a constant, or
//     is tied by Validate to another leaf, so no isolating tamper exists. The
//     assertion is then that its value appears as a length-prefixed field
//     exactly that many times -- which still goes red if the emission is
//     removed, because the count drops.
//
//   - unsignedBecause: a deliberate exemption, with the reason written down.
//     There is one class of these and it is a duplicate of a signed leaf.
//
// Reqs 25, 26.

// claimLeafRule says how the signature covers one leaf of ReceiptClaims.
// Exactly one of its three arms is set.
type claimLeafRule struct {
	tamper func(*ReceiptClaims)

	spelling    func(ReceiptClaims) string
	occurrences int

	unsignedBecause string
}

var receiptClaimCoverage = map[string]claimLeafRule{
	// Pinned to the cohort's own constants by Validate, and both spell
	// "hangar-output-v1", so the count is what says both are emitted.
	"ProtocolVersion": {
		spelling:    func(c ReceiptClaims) string { return c.ProtocolVersion },
		occurrences: 2,
	},
	"MarkerVersion": {
		spelling:    func(c ReceiptClaims) string { return c.MarkerVersion },
		occurrences: 2,
	},
	// The receipt version is the domain, and the domain is emitted first, so
	// this string is in the canonical form twice for two different reasons.
	"ReceiptVersion": {
		spelling:    func(c ReceiptClaims) string { return c.ReceiptVersion },
		occurrences: 2,
	},

	// Validate requires Execution.ExecutionID == Incarnation.ExecutionID, so
	// tampering with either alone never reaches the signature check. The
	// signature covers both, and the count is how that is stated.
	"Execution.ExecutionID": {
		spelling:    func(c ReceiptClaims) string { return string(c.Execution.ExecutionID) },
		occurrences: 2,
	},
	"Incarnation.ExecutionID": {
		spelling:    func(c ReceiptClaims) string { return string(c.Incarnation.ExecutionID) },
		occurrences: 2,
	},
	// Likewise Output == Incarnation.Output.
	"Output": {
		spelling:    func(c ReceiptClaims) string { return string(c.Output) },
		occurrences: 2,
	},
	"Incarnation.Output": {
		spelling:    func(c ReceiptClaims) string { return string(c.Incarnation.Output) },
		occurrences: 2,
	},

	"Execution.Fence":      {tamper: func(c *ReceiptClaims) { c.Execution.Fence = 3 }},
	"ActivationEpoch":      {tamper: func(c *ReceiptClaims) { c.ActivationEpoch = 8 }},
	"HandoffID":            {tamper: func(c *ReceiptClaims) { c.HandoffID = "22222222-2222-4222-8222-222222222222" }},
	"ProducerCheckpointID": {tamper: func(c *ReceiptClaims) { c.ProducerCheckpointID = "another-checkpoint" }},
	"ReservationID":        {tamper: func(c *ReceiptClaims) { c.ReservationID = "55555555-5555-4555-8555-555555555555" }},
	"ChallengeNonce":       {tamper: func(c *ReceiptClaims) { c.ChallengeNonce = "nonce-fedcba9876543210" }},
	"ChallengeIssuedAt": {
		tamper: func(c *ReceiptClaims) { c.ChallengeIssuedAt = NewTimestamp(receiptInstant.Add(time.Second)) },
	},
	"Incarnation.NodeUID":          {tamper: func(c *ReceiptClaims) { c.Incarnation.NodeUID = "node-2" }},
	"Incarnation.HandleGeneration": {tamper: func(c *ReceiptClaims) { c.Incarnation.HandleGeneration = 4 }},
	"CaptureFence":                 {tamper: func(c *ReceiptClaims) { c.CaptureFence = 6 }},
	"WriterFence":                  {tamper: func(c *ReceiptClaims) { c.WriterFence = 10 }},
	"Attributes.StoredBytes":       {tamper: func(c *ReceiptClaims) { c.Attributes.StoredBytes = 4096 }},
	"Attributes.LogicalBytes":      {tamper: func(c *ReceiptClaims) { c.Attributes.LogicalBytes = 8192 }},
	"Attributes.CreatedAt": {
		tamper: func(c *ReceiptClaims) { c.Attributes.CreatedAt = NewTimestamp(receiptInstant.Add(time.Second)) },
	},
	"SignedAt": {
		tamper: func(c *ReceiptClaims) { c.SignedAt = NewTimestamp(receiptInstant.Add(time.Second)) },
	},

	// The ref's three parts. Validate ties Attributes.Ref to Ref, so each
	// tamper has to move both -- and the emission that makes the bytes differ
	// is the Ref one, because Attributes.Ref is not emitted at all.
	"Ref.Scope": {
		tamper: func(c *ReceiptClaims) {
			c.Ref.Scope = "o9999999999999999999999999999999999999999"
			c.Attributes.Ref = c.Ref
		},
	},
	"Ref.Digest": {
		tamper: func(c *ReceiptClaims) {
			c.Ref.Digest = hangar.Digest("sha256:" + strings.Repeat("cd", 32))
			c.Attributes.Ref = c.Ref
		},
	},
	"Ref.Generation": {
		tamper: func(c *ReceiptClaims) {
			c.Ref.Generation = 1725830823000002
			c.Attributes.Ref = c.Ref
		},
	},

	// The one exemption class, and it is a duplicate rather than a fact of its
	// own: Validate refuses claims whose Attributes.Ref is not the Ref, so
	// emitting it a second time would sign one fact twice and leave a verifier
	// with two places to look for it.
	"Attributes.Ref.Scope": {
		unsignedBecause: "Validate pins Attributes.Ref equal to Ref, which is canonicalized",
	},
	"Attributes.Ref.Digest": {
		unsignedBecause: "Validate pins Attributes.Ref equal to Ref, which is canonicalized",
	},
	"Attributes.Ref.Generation": {
		unsignedBecause: "Validate pins Attributes.Ref equal to Ref, which is canonicalized",
	},
}

func TestEveryClaimLeafIsAccountedForBySignature(t *testing.T) {
	leaves := receiptClaimLeaves()
	if len(leaves) == 0 {
		t.Fatal("the leaf walk found no fields; this guard would pass vacuously")
	}
	t.Logf("walked %d exported leaves of ReceiptClaims", len(leaves))

	baseline := sampleClaims()
	canonical, err := CanonicalReceiptBytes(baseline, "receipt-key-1")
	if err != nil {
		t.Fatalf("canonicalizing the baseline: %v", err)
	}

	for _, leaf := range leaves {
		rule, accounted := receiptClaimCoverage[leaf]
		if !accounted {
			t.Errorf("ReceiptClaims.%s is not accounted for in receiptClaimCoverage. Every fact "+
				"a later verifier must not have to trust the caller for goes inside the "+
				"signature; add the emission to CanonicalReceiptBytes and a tamper here, or "+
				"record why it is deliberately unsigned. An unlisted leaf is an unsigned "+
				"claim nobody decided to leave out.", leaf)

			continue
		}

		switch {
		case rule.tamper != nil:
			tampered := sampleClaims()
			rule.tamper(&tampered)
			if err := tampered.Validate(); err != nil {
				t.Errorf("the tamper for %s produced claims Validate refuses (%v), so it never "+
					"reaches the signature. Choose a value the claims are still valid with.",
					leaf, err)

				continue
			}
			mutated, err := CanonicalReceiptBytes(tampered, "receipt-key-1")
			if err != nil {
				t.Errorf("canonicalizing the tamper for %s: %v", leaf, err)

				continue
			}
			if string(mutated) == string(canonical) {
				t.Errorf("altering ReceiptClaims.%s did not change the canonical bytes, so the "+
					"signature does not cover it and an attacker may choose it while keeping a "+
					"valid signature", leaf)
			}

		case rule.spelling != nil:
			value := rule.spelling(baseline)
			if value == "" {
				t.Errorf("the spelling for %s is empty; that would match anything", leaf)

				continue
			}
			want := lengthPrefixed(value)
			if got := strings.Count(string(canonical), want); got != rule.occurrences {
				t.Errorf("the canonical form contains %s %d times and %s says %d. This leaf is "+
					"pinned by Validate, so a count is what stands in for a tamper -- a count "+
					"that dropped is an emission somebody removed.", want, got, leaf, rule.occurrences)
			}

		case rule.unsignedBecause != "":
			// Recorded, not asserted: an exemption is a decision, and the
			// decision is that it is written down beside the field.

		default:
			t.Errorf("the rule for %s sets none of tamper, spelling or unsignedBecause", leaf)
		}
	}

	known := map[string]bool{}
	for _, leaf := range leaves {
		known[leaf] = true
	}
	for leaf := range receiptClaimCoverage {
		if !known[leaf] {
			t.Errorf("receiptClaimCoverage names %s, which ReceiptClaims no longer has. Remove "+
				"the entry so the map keeps describing the claims.", leaf)
		}
	}
}

// lengthPrefixed spells one value the way CanonicalReceiptBytes does.
func lengthPrefixed(value string) string {
	return fmt.Sprintf("%d:%s|", len(value), value)
}

// receiptClaimLeaves walks ReceiptClaims and returns every exported leaf's
// dotted path.
//
// A struct is descended into unless it is a Timestamp or a time.Time: those
// two are values in this protocol rather than aggregates, they are emitted as
// one field each, and descending would name their internals instead of them.
func receiptClaimLeaves() []string {
	var (
		timestampType = reflect.TypeOf(Timestamp{})
		timeType      = reflect.TypeOf(time.Time{})
		paths         []string
		walk          func(reflect.Type, string)
	)

	walk = func(typ reflect.Type, prefix string) {
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			if field.PkgPath != "" {
				continue
			}
			path := field.Name
			if prefix != "" {
				path = prefix + "." + field.Name
			}
			if field.Type.Kind() == reflect.Struct &&
				field.Type != timestampType && field.Type != timeType {
				walk(field.Type, path)

				continue
			}
			paths = append(paths, path)
		}
	}

	walk(reflect.TypeOf(ReceiptClaims{}), "")
	sort.Strings(paths)

	return paths
}
