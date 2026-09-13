package output

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// The receipt is the one artefact that travels from the node that sealed the
// bytes to the transaction that binds them, and every claim in it is something
// a verifier must match exactly (Reqs 25-26). A field with more than one wire
// spelling per instant is therefore a canonicalization hazard there, and only
// there: everywhere else this package's own fixed-width Timestamp is used.
//
// hangar.TreeAttributes.CreatedAt is a time.Time, and Go's RFC 3339 encoding
// trims trailing zeros, so one instant has several spellings. The fix is a wire
// *projection*, not a second attribute model: output.TreeAttributes declares no
// new fact, converts losslessly in both directions, and leaves the foundation's
// type untouched for every in-process use.
//
// Reqs 25, 26.

// nineDigitUTC is the one spelling this protocol has.
var nineDigitUTC = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$`)

// theSameInstantThreeWays are three renderings whose only difference is
// trailing zeros. A wire contract another language must reproduce cannot have
// its verifier guess which one it will be handed.
var theSameInstantThreeWays = []string{
	"2026-09-08T21:50:23Z",
	"2026-09-08T21:50:23.400000000Z",
	"2026-09-08T21:50:23.418927631Z",
}

func foundationAttributes(t *testing.T, at string) hangar.TreeAttributes {
	t.Helper()

	return hangar.TreeAttributes{
		Ref: hangar.TreeRef{
			Scope:      "0192b3c4d5e6f708",
			Digest:     "sha256:9f2c0c4a6a1a7f7f6b1d0f2f3e4d5c6b7a8998a7b6c5d4e3f201122334455667",
			Generation: 1725830823000001,
		},
		StoredBytes:  4096,
		LogicalBytes: 3072,
		CreatedAt:    mustParse(t, at),
	}
}

// createdAtIn pulls the created_at spelling out of an encoded attributes value,
// whichever type produced it.
func createdAtIn(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding %T: %v", value, err)
	}
	var decoded struct {
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("reading created_at back out of %s: %v", encoded, err)
	}

	return decoded.CreatedAt
}

// TestTheFoundationsAttributeTimeHasMoreThanOneSpelling is why the projection
// exists. It asserts the hazard rather than the fix, so that a future change to
// the foundation's encoding retires this projection deliberately instead of
// leaving it as unexplained duplication.
func TestTheFoundationsAttributeTimeHasMoreThanOneSpelling(t *testing.T) {
	spellings := map[string]bool{}
	for _, at := range theSameInstantThreeWays {
		spelling := createdAtIn(t, foundationAttributes(t, at))
		t.Logf("hangar.TreeAttributes: %-32s encodes as %s", at, spelling)
		spellings[spelling] = true
	}

	if len(spellings) == 1 {
		t.Fatal("hangar.TreeAttributes now has one spelling per instant. The wire projection " +
			"below exists only because it did not; retire it rather than keeping a conversion " +
			"nothing needs.")
	}
}

func TestTheReceiptHasOneWireSpellingPerInstant(t *testing.T) {
	for _, at := range theSameInstantThreeWays {
		projected := AttributesFromFoundation(foundationAttributes(t, at))
		spelling := createdAtIn(t, projected)
		if !nineDigitUTC.MatchString(spelling) {
			t.Errorf("output.TreeAttributes encoded %s as %q; the wire has one spelling per "+
				"instant, nine fractional digits and UTC", at, spelling)
		}
	}

	t.Run("and it is the spelling the signed claims carry", func(t *testing.T) {
		claims := ReceiptClaims{Attributes: AttributesFromFoundation(
			foundationAttributes(t, "2026-09-08T21:50:23.400000000Z"))}
		encoded, err := json.Marshal(claims)
		if err != nil {
			t.Fatalf("encoding the claims: %v", err)
		}
		var decoded struct {
			Attributes struct {
				CreatedAt string `json:"created_at"`
			} `json:"attributes"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("reading the claims back: %v", err)
		}
		if !nineDigitUTC.MatchString(decoded.Attributes.CreatedAt) {
			t.Errorf("the signed attributes spell their creation time %q. Every other claim in "+
				"this receipt has exactly one spelling; a verifier must not have to accept two "+
				"widths on one of them.", decoded.Attributes.CreatedAt)
		}
	})
}

// TestTheAttributeProjectionIsLossless is the other half of "declares no new
// fact": it must carry the foundation's value out and back unchanged, so that
// nothing has to choose which of the two types is authoritative.
func TestTheAttributeProjectionIsLossless(t *testing.T) {
	for _, at := range append(theSameInstantThreeWays, "2026-09-08T21:50:23.000000001Z") {
		foundation := foundationAttributes(t, at)
		back := AttributesFromFoundation(foundation).Foundation()

		if back.Ref != foundation.Ref {
			t.Errorf("%s: the projection changed the ref: %v vs %v", at, back.Ref, foundation.Ref)
		}
		if back.StoredBytes != foundation.StoredBytes || back.LogicalBytes != foundation.LogicalBytes {
			t.Errorf("%s: the projection changed the sizes: %d/%d vs %d/%d", at,
				back.StoredBytes, back.LogicalBytes, foundation.StoredBytes, foundation.LogicalBytes)
		}
		// Equal, not ==: the projection normalizes the location to UTC, which
		// is the whole point. It must not move the instant.
		if !back.CreatedAt.Equal(foundation.CreatedAt) {
			t.Errorf("%s: the projection moved the instant: %s vs %s", at,
				back.CreatedAt, foundation.CreatedAt)
		}
		if back.CreatedAt.Location() != time.UTC {
			t.Errorf("%s: the projection returned a non-UTC time", at)
		}
	}
}

// TestTheSignedClaimsValidateTheirOwnAttributes is the other half of the
// projection's reason for existing.
//
// The projection gave TreeAttributes a Validate; ReceiptClaims.Validate
// compared only the ref and never called it, so a receipt whose signed
// attributes reported a zero creation instant or a negative size validated
// cleanly. Req 25 says the signed claims bind "strict tree attributes" and Req
// 26 says every signed claim is matched against the durable checkpoint,
// reservation and fence -- a claims value that validates with attributes that
// do not is a hole in both.
func TestTheSignedClaimsValidateTheirOwnAttributes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "protocol-v1", "receipt.json"))
	if err != nil {
		t.Fatalf("reading the frozen receipt: %v", err)
	}
	var frozen Receipt
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatalf("decoding the frozen receipt: %v", err)
	}
	if err := frozen.Claims.Validate(); err != nil {
		t.Fatalf("the frozen receipt's claims do not validate, so nothing below means anything: %v", err)
	}

	for _, corruption := range []struct {
		name    string
		breakIt func(*ReceiptClaims)
		want    error
	}{
		{
			name:    "a zero creation instant",
			breakIt: func(claims *ReceiptClaims) { claims.Attributes.CreatedAt = Timestamp{} },
			// The base protocol's sentinel, not this package's: Timestamp is
			// re-exported from hangar/executioncontrol so that one instant has
			// one spelling everywhere, and it refuses a zero on its own terms.
			want: executioncontrol.ErrIncomplete,
		},
		{
			name:    "a negative stored size",
			breakIt: func(claims *ReceiptClaims) { claims.Attributes.StoredBytes = -1 },
			want:    ErrCorrupt,
		},
		{
			name:    "a negative logical size",
			breakIt: func(claims *ReceiptClaims) { claims.Attributes.LogicalBytes = -1 },
			want:    ErrCorrupt,
		},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			claims := frozen.Claims
			corruption.breakIt(&claims)

			err := claims.Validate()
			if err == nil {
				t.Fatalf("ReceiptClaims.Validate accepted %s. The attributes are signed; a "+
					"verifier that matches them against durable state must not be handed a "+
					"value the claims themselves never checked.", corruption.name)
			}
			if !errors.Is(err, corruption.want) {
				t.Errorf("refused %s with %v; expected %v", corruption.name, err, corruption.want)
			}
			// And the whole receipt refuses it too, since Receipt.Validate is
			// what a verifier actually calls.
			receipt := frozen
			receipt.Claims = claims
			if receipt.Validate() == nil {
				t.Errorf("Receipt.Validate accepted %s", corruption.name)
			}
		})
	}
}
