package pagination_test

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/pagination"
)

func TestCursorRoundTripIsStableAndNormalizesTheInstant(t *testing.T) {
	instant := time.Date(2026, time.July, 22, 12, 34, 56, 789123000, time.FixedZone("offset", -7*60*60))
	cursor := pagination.Cursor{CreatedAt: instant, ID: 9007199254740993}

	encoded, err := pagination.Encode(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(encoded, "=+/") {
		t.Fatalf("cursor is not an unpadded URL-safe token: %q", encoded)
	}
	decoded, err := pagination.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.CreatedAt.Equal(instant) || decoded.CreatedAt.Location() != time.UTC || decoded.ID != cursor.ID {
		t.Fatalf("decoded cursor = %#v, want instant %s and ID %d", decoded, instant, cursor.ID)
	}
	reencoded, err := pagination.Encode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if reencoded != encoded {
		t.Fatalf("re-encoded cursor = %q, want stable %q", reencoded, encoded)
	}
}

func TestCursorRejectsMalformedNonCanonicalAndOutOfRangeValues(t *testing.T) {
	valid, err := pagination.Encode(pagination.Cursor{
		CreatedAt: time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC),
		ID:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"",
		"not-a-cursor",
		valid + "=",
		valid + "x",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := pagination.Decode(raw); err == nil {
				t.Fatalf("Decode(%q) succeeded", raw)
			}
		})
	}
	for _, cursor := range []pagination.Cursor{
		{CreatedAt: time.Time{}, ID: 1},
		{CreatedAt: time.Now(), ID: 0},
		{CreatedAt: time.Now(), ID: -1},
		{CreatedAt: time.Unix(1, 1), ID: 1},
	} {
		if _, err := pagination.Encode(cursor); err == nil {
			t.Fatalf("Encode(%#v) succeeded", cursor)
		}
	}
}
