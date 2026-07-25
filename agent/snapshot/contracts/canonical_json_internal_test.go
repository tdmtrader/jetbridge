package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// The canonical serialization is the descriptor bytes for every schema revision
// from 2 onwards, so it has to be byte-stable forever. These tests live inside
// the package because the two layers — canonical-value and the framed
// canonical-serialization — are unexported, and because confusing the two is the
// exact failure the naming exists to prevent.

// workedExampleDocument is the dialect's §10.3 worked example, verbatim, in the
// human-readable form a reviewer diffs.
const workedExampleDocument = `{
  "dialect": "record-schema-dialect/1",
  "contract": "example/v1",
  "envelope": "record/v1",
  "revision": 2,
  "supersedes": 1,
  "description": "A minimal illustration.",
  "subject_shape": {
    "minimum": 1,
    "maximum": 1,
    "roles": { "primary": { "minimum": 1, "maximum": 1 } }
  },
  "fields": {
    "body": { "kind": "object", "presence": "required" },
    "body/note": { "kind": "markdown", "presence": "required" }
  },
  "go_only_rules": []
}`

const workedExamplePayload = `{"contract":"example/v1","description":"A minimal illustration.","dialect":"record-schema-dialect/1","envelope":"record/v1","fields":{"body":{"kind":"object","presence":"required"},"body/note":{"kind":"markdown","presence":"required"}},"go_only_rules":[],"revision":2,"subject_shape":{"maximum":1,"minimum":1,"roles":{"primary":{"maximum":1,"minimum":1}}},"supersedes":1}`

// The framed digest is a descriptor digest; the unframed one is not, and the two
// are not interchangeable. Both are pinned so a change that drops the framing
// cannot pass by re-deriving one of them.
const (
	workedExampleFramedDigest   = "sha256:c314139dfaf43d9ca2fbc9524a359f2443da50cfb3442708a08f76f6ee59c61b"
	workedExampleUnframedDigest = "sha256:fc9930c990b71132260ae8cea99c63023a9702e1360bd8096533e53fb4fbb0b5"
)

func TestCanonicalPayloadMatchesTheWorkedExample(t *testing.T) {
	payload, err := canonicalJSONPayload([]byte(workedExampleDocument))
	if err != nil {
		t.Fatalf("canonicalJSONPayload(): %v", err)
	}
	if string(payload) != workedExamplePayload {
		t.Fatalf("canonical payload mismatch:\n got %s\nwant %s", payload, workedExamplePayload)
	}
	if len(payload) != 371 {
		t.Errorf("canonical payload is %d bytes, want 371", len(payload))
	}
}

func TestCanonicalSerializationFramesThePayloadWithItsLength(t *testing.T) {
	framed, err := canonicalJSONSerialization([]byte(workedExampleDocument))
	if err != nil {
		t.Fatalf("canonicalJSONSerialization(): %v", err)
	}
	wantHeader := canonicalJSONAlgorithm + "\n371\n"
	if !bytes.HasPrefix(framed, []byte(wantHeader)) {
		t.Fatalf("framed serialization does not start with %q:\n%q", wantHeader, framed)
	}
	if len(framed) != 401 {
		t.Errorf("framed serialization is %d bytes, want 401", len(framed))
	}
	if got := schemaDescriptorDigest(string(framed)); got.String() != workedExampleFramedDigest {
		t.Errorf("framed descriptor digest = %s, want %s", got, workedExampleFramedDigest)
	}
	payload, err := canonicalJSONPayload([]byte(workedExampleDocument))
	if err != nil {
		t.Fatalf("canonicalJSONPayload(): %v", err)
	}
	if got := schemaDescriptorDigest(string(payload)); got.String() != workedExampleUnframedDigest {
		t.Errorf("unframed payload digest = %s, want %s", got, workedExampleUnframedDigest)
	}
	if workedExampleFramedDigest == workedExampleUnframedDigest {
		t.Fatal("framed and unframed digests must differ; only the framed one is a descriptor")
	}
}

// Fifty shuffled orderings of the same document must produce one byte string.
// Object key order is the model-chosen instability the digest exists to defeat,
// so this is the property, not an illustration of it.
func TestShuffledKeyOrderingsProduceIdenticalCanonicalBytes(t *testing.T) {
	for _, name := range schemaDocumentFileNames(t) {
		t.Run(name, func(t *testing.T) {
			source := readSchemaDocumentFile(t, name)
			want, err := canonicalJSONSerialization(source)
			if err != nil {
				t.Fatalf("canonicalJSONSerialization(%s): %v", name, err)
			}
			random := rand.New(rand.NewSource(1))
			for attempt := 0; attempt < 50; attempt++ {
				shuffled := shuffleJSONKeyOrder(t, source, random)
				got, err := canonicalJSONSerialization(shuffled)
				if err != nil {
					t.Fatalf("attempt %d: canonicalJSONSerialization(shuffled): %v", attempt, err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("attempt %d produced different canonical bytes for the same document", attempt)
				}
			}
		})
	}
}

// A whitespace-only reformat must not move the canonical bytes; a one-byte
// content change must. Both halves are the same property from opposite sides.
func TestWhitespaceIsInsignificantAndContentIsNot(t *testing.T) {
	want, err := canonicalJSONSerialization([]byte(workedExampleDocument))
	if err != nil {
		t.Fatalf("canonicalJSONSerialization(): %v", err)
	}
	reformatted := strings.ReplaceAll(workedExampleDocument, "\n", "\n\t ")
	got, err := canonicalJSONSerialization([]byte(reformatted))
	if err != nil {
		t.Fatalf("canonicalJSONSerialization(reformatted): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("a whitespace-only reformat changed the canonical bytes")
	}

	changed := strings.Replace(workedExampleDocument, "A minimal illustration.", "A minimal illustratioo.", 1)
	if changed == workedExampleDocument {
		t.Fatal("the one-byte content change did not apply")
	}
	mutated, err := canonicalJSONSerialization([]byte(changed))
	if err != nil {
		t.Fatalf("canonicalJSONSerialization(changed): %v", err)
	}
	if bytes.Equal(mutated, want) {
		t.Error("a one-byte content change left the canonical bytes unchanged")
	}
}

// Canonicalizing canonical bytes must be a no-op. Anything else means the
// algorithm has no fixed point and a stored descriptor could not be recomputed.
func TestCanonicalPayloadIsIdempotent(t *testing.T) {
	sources := [][]byte{[]byte(workedExampleDocument)}
	for _, name := range schemaDocumentFileNames(t) {
		sources = append(sources, readSchemaDocumentFile(t, name))
	}
	for _, source := range sources {
		once, err := canonicalJSONPayload(source)
		if err != nil {
			t.Fatalf("canonicalJSONPayload(): %v", err)
		}
		twice, err := canonicalJSONPayload(once)
		if err != nil {
			t.Fatalf("canonicalJSONPayload(canonical): %v", err)
		}
		if !bytes.Equal(once, twice) {
			t.Errorf("canonicalJSONPayload is not idempotent:\n once %s\ntwice %s", once, twice)
		}
	}
}

// Number literals are preserved byte for byte, so 1, 1.0 and 1e0 are three
// distinct canonical forms. Re-rendering them from float64 — which is what
// json.Marshal does — would collapse them.
func TestNumberLiteralsArePreservedByteForByte(t *testing.T) {
	forms := []string{`{"n":1}`, `{"n":1.0}`, `{"n":1e0}`, `{"n":1.00}`}
	seen := make(map[string]string, len(forms))
	for _, form := range forms {
		payload, err := canonicalJSONPayload([]byte(form))
		if err != nil {
			t.Fatalf("canonicalJSONPayload(%s): %v", form, err)
		}
		if string(payload) != form {
			t.Errorf("canonicalJSONPayload(%s) = %s, want the literal preserved", form, payload)
		}
		if other, found := seen[string(payload)]; found {
			t.Errorf("%s and %s canonicalize to the same bytes", other, form)
		}
		seen[string(payload)] = form
	}
}

func TestStringEscapingIsExactlyTheDialectSet(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value string
		want  string
	}{
		{"html characters are not escaped", "a<b>c&d", `"a<b>c&d"`},
		{"line and paragraph separators are not escaped", "a\u2028b\u2029c", "\"a\u2028b\u2029c\""},
		{"solidus is not escaped", "content/payload", `"content/payload"`},
		{"quote and backslash are escaped", "a\"b\\c", `"a\"b\\c"`},
		{"named control escapes are used", "\b\t\n\f\r", `"\b\t\n\f\r"`},
		{"other controls use lowercase hex", "\x00\x1f", `"\u0000\u001f"`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := json.Marshal(map[string]string{"k": testCase.value})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			payload, err := canonicalJSONPayload(encoded)
			if err != nil {
				t.Fatalf("canonicalJSONPayload(): %v", err)
			}
			want := `{"k":` + testCase.want + `}`
			if string(payload) != want {
				t.Errorf("canonicalJSONPayload() = %s, want %s", payload, want)
			}
		})
	}
}

// Every one of these is a way for two distinct inputs to map to one canonical
// form, which is precisely what a content digest must never allow.
func TestCanonicalPayloadRejectsAmbiguousInput(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		document []byte
		want     string
	}{
		{"duplicate key", []byte(`{"a":1,"a":2}`), "duplicate"},
		{"invalid utf-8", []byte("{\"a\":\"\xff\"}"), "UTF-8"},
		{"unpaired high surrogate", []byte(`{"a":"\ud800"}`), "surrogate"},
		{"unpaired low surrogate", []byte(`{"a":"\udc00x"}`), "surrogate"},
		{"trailing content", []byte(`{"a":1} {"b":2}`), "one JSON value"},
		{"empty document", []byte(""), "one JSON value"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			payload, err := canonicalJSONPayload(testCase.document)
			if err == nil {
				t.Fatalf("canonicalJSONPayload(%q) = %s, want an error", testCase.document, payload)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("canonicalJSONPayload(%q) error = %v, want it to mention %q", testCase.document, err, testCase.want)
			}
		})
	}
}

// A paired surrogate escape is legal and must round-trip to the astral
// character it denotes.
func TestPairedSurrogateEscapeIsAccepted(t *testing.T) {
	payload, err := canonicalJSONPayload([]byte(`{"a":"\ud83d\ude00"}`))
	if err != nil {
		t.Fatalf("canonicalJSONPayload(): %v", err)
	}
	if want := "{\"a\":\"\U0001F600\"}"; string(payload) != want {
		t.Errorf("canonicalJSONPayload() = %s, want %s", payload, want)
	}
}

func TestCanonicalPayloadSortsKeysByUnsignedByteComparison(t *testing.T) {
	// "Z" (0x5a) < "a" (0x61) < "~" (0x7e), which a case-folding or
	// locale-aware comparison would reorder.
	payload, err := canonicalJSONPayload([]byte(`{"a":1,"~":2,"Z":3}`))
	if err != nil {
		t.Fatalf("canonicalJSONPayload(): %v", err)
	}
	if want := `{"Z":3,"a":1,"~":2}`; string(payload) != want {
		t.Errorf("canonicalJSONPayload() = %s, want %s", payload, want)
	}
}

func TestCanonicalPayloadPreservesArrayOrder(t *testing.T) {
	payload, err := canonicalJSONPayload([]byte(`{"a":["c","b","a"]}`))
	if err != nil {
		t.Fatalf("canonicalJSONPayload(): %v", err)
	}
	if want := `{"a":["c","b","a"]}`; string(payload) != want {
		t.Errorf("canonicalJSONPayload() = %s, want array order preserved: %s", payload, want)
	}
}

// shuffleJSONKeyOrder re-renders a document with every object's keys in a random
// order, preserving array order and number literals. It is the generator for the
// key-order property: Go's own encoder cannot produce it, because it sorts map
// keys.
func shuffleJSONKeyOrder(t *testing.T, document []byte, random *rand.Rand) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	var buffer bytes.Buffer
	renderShuffled(t, &buffer, value, random)
	return buffer.Bytes()
}

func renderShuffled(t *testing.T, buffer *bytes.Buffer, value any, random *rand.Rand) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		random.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				t.Fatalf("marshal key %q: %v", key, err)
			}
			buffer.Write(encodedKey)
			buffer.WriteString(" : ")
			renderShuffled(t, buffer, typed[key], random)
		}
		buffer.WriteByte('}')
	case []any:
		buffer.WriteByte('[')
		for index, element := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			renderShuffled(t, buffer, element, random)
		}
		buffer.WriteByte(']')
	case json.Number:
		buffer.WriteString(typed.String())
	case string:
		encoded, err := json.Marshal(typed)
		if err != nil {
			t.Fatalf("marshal string: %v", err)
		}
		buffer.Write(encoded)
	case bool:
		buffer.WriteString(fmt.Sprintf("%t", typed))
	case nil:
		buffer.WriteString("null")
	default:
		t.Fatalf("unexpected decoded type %T", value)
	}
}
