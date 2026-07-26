package contracts

import (
	"bytes"
	"strconv"
	"testing"
)

// FuzzCanonicalJSON fuzzes the hand-rolled canonical serializer that produces
// every schema descriptor from revision 2 onwards. Those bytes have to be
// byte-stable forever, and the existing tests — worked example, fifty shuffled
// key orderings, idempotence, number literals, the escape dialect, ambiguity
// rejection — are all example-based.
//
// Three properties:
//
//  1. Neither layer panics, and a rejected document yields no payload. Rejection
//     is a first-class result here: every rejection in canonical_json.go exists
//     because it is a way for two distinct inputs to reach one canonical form.
//
//  2. Whatever is accepted is accepted as a FIXED POINT: canonicalizing the
//     canonical payload returns exactly the same bytes. Without a fixed point a
//     stored descriptor could not be recomputed, which is what a descriptor is.
//
//  3. The framing is exactly algorithm + "\n" + decimal length + "\n" + payload,
//     with no slack. The framing is what makes the encoding prefix-free, so a
//     drift in it is a concatenation-confusion bug, not a formatting nit.
func FuzzCanonicalJSON(f *testing.F) {
	f.Add([]byte(workedExampleDocument))
	f.Add([]byte(workedExamplePayload))
	f.Add([]byte(`{"n":1}`))
	f.Add([]byte(`{"n":1.0}`))
	f.Add([]byte(`{"n":1e0}`))
	f.Add([]byte(`{"b":true,"a":[1,2,{"z":null}]}`))
	f.Add([]byte(`"😀"`))
	f.Add([]byte(`{"a":1,"a":2}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(nil))
	f.Add([]byte("not json"))

	entries, err := schemaDocumentSources.ReadDir(schemaDocumentDirectory)
	if err != nil {
		f.Fatalf("read embedded schema documents: %v", err)
	}
	for _, entry := range entries {
		document, err := schemaDocumentSources.ReadFile(schemaDocumentDirectory + "/" + entry.Name())
		if err != nil {
			f.Fatalf("read embedded schema document %q: %v", entry.Name(), err)
		}
		f.Add(document)
	}

	f.Fuzz(func(t *testing.T, document []byte) {
		payload, err := canonicalJSONPayload(document)
		if err != nil {
			if payload != nil {
				t.Fatalf("rejected input returned a payload: %q", payload)
			}
			return
		}

		again, err := canonicalJSONPayload(payload)
		if err != nil {
			t.Fatalf("canonical payload was rejected on re-canonicalization: %v", err)
		}
		if !bytes.Equal(payload, again) {
			t.Fatalf("canonicalization is not a fixed point:\n once %q\ntwice %q", payload, again)
		}

		framed, err := canonicalJSONSerialization(document)
		if err != nil {
			t.Fatalf("payload canonicalized but framing failed: %v", err)
		}
		header := canonicalJSONAlgorithm + "\n" + strconv.Itoa(len(payload)) + "\n"
		if !bytes.HasPrefix(framed, []byte(header)) {
			t.Fatalf("framed serialization does not start with %q: %q", header, framed)
		}
		if len(framed) != len(header)+len(payload) {
			t.Fatalf("framed serialization is %d bytes, want exactly %d header + %d payload",
				len(framed), len(header), len(payload))
		}
		if !bytes.Equal(framed[len(header):], payload) {
			t.Fatal("framed serialization does not end with exactly the canonical payload")
		}
	})
}
