package contracts_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// admittedReviewBytes builds an accepted review/v1 record whose summary is a
// unique sentinel, so a test can splice a chosen byte sequence into the summary
// value with a single bytes.Replace and drive the exact bytes through a gate.
func admittedReviewBytes(t *testing.T) ([]byte, snapshot.ValidationContext) {
	t.Helper()
	ref := snapshot.SnapshotRef{ID: 31, Type: "repository-change/v1", Digest: recordDigest('a')}
	validationContext := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": ref})
	record, err := contracts.NewRecord(
		mustTypeRef(t, "review/v1"),
		[]contracts.Subject{contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "change", ref)},
		contracts.ReviewBody{Conclusion: "accept", Summary: "SENTINEL", Findings: []contracts.Finding{}},
	)
	if err != nil {
		t.Fatalf("NewRecord(review/v1): %v", err)
	}
	return marshalRecord(t, record), validationContext
}

// spliceSummary replaces the sentinel summary value with a chosen byte sequence
// and fails the test if nothing was spliced, so a gate can never pass by accident
// on an unmutated document.
func spliceSummary(t *testing.T, encoded, replacement []byte) []byte {
	t.Helper()
	mutated := bytes.Replace(encoded, []byte("SENTINEL"), replacement, 1)
	if bytes.Equal(mutated, encoded) {
		t.Fatalf("sentinel not spliced; the test would not exercise the gate")
	}
	return mutated
}

// A raw 0xff byte inside the summary string literal.
var rawInvalidByteSummary = []byte("bad\xffbad")

// A lone high-surrogate escape in the summary string literal. The backticks keep
// \ud800 six literal characters, so the raw record.json carries the escape that
// encoding/json would silently sanitize.
var unpairedSurrogateSummary = []byte(`lone \ud800 surrogate`)

// TestSealTimeAdmissionRejectsRecordBytesThatAreNotValidUTF8 drives raw,
// non-UTF-8 record.json bytes through the real seal gate. The bad byte survives
// os.WriteFile and readRegularFile, so the only thing that can catch it is a
// byte-level check at admission.
func TestSealTimeAdmissionRejectsRecordBytesThatAreNotValidUTF8(t *testing.T) {
	encoded, validationContext := admittedReviewBytes(t)
	mutated := spliceSummary(t, encoded, rawInvalidByteSummary)

	_, err := validateFiles(t, "review/v1", map[string][]byte{"record.json": mutated}, validationContext)
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("seal gate error = %v, want it to name UTF-8", err)
	}
}

// TestSealTimeAdmissionRejectsAnUnpairedSurrogateEscape drives a lone surrogate
// escape through the seal gate. The bytes are valid UTF-8 — the escape is ASCII —
// so only the surrogate-escape predicate can catch it, which is why the gate
// needs both checks and not just utf8.Valid.
func TestSealTimeAdmissionRejectsAnUnpairedSurrogateEscape(t *testing.T) {
	encoded, validationContext := admittedReviewBytes(t)
	mutated := spliceSummary(t, encoded, unpairedSurrogateSummary)

	_, err := validateFiles(t, "review/v1", map[string][]byte{"record.json": mutated}, validationContext)
	if err == nil || !strings.Contains(err.Error(), "surrogate") {
		t.Fatalf("seal gate error = %v, want it to name a surrogate escape", err)
	}
}

// TestStoredRecordsWithInvalidTextEncodingStillRevalidate is the hard constraint
// made executable: the SAME two byte sequences that the seal gate refuses must
// still READ. Nothing may reject previously-sealed bytes at read time, so the
// read gate is untouched and both stored records revalidate.
func TestStoredRecordsWithInvalidTextEncodingStillRevalidate(t *testing.T) {
	encoded, _ := admittedReviewBytes(t)
	for name, replacement := range map[string][]byte{
		"a raw invalid byte":           rawInvalidByteSummary,
		"an unpaired surrogate escape": unpairedSurrogateSummary,
	} {
		t.Run(name, func(t *testing.T) {
			mutated := spliceSummary(t, encoded, replacement)
			if _, err := revalidateSealedFiles(t, "review/v1", map[string][]byte{"record.json": mutated}, emptyValidationContext(t)); err != nil {
				t.Fatalf("a stored record carrying %s was rejected on read: %v", name, err)
			}
		})
	}
}

// TestTheJSONDecoderSanitizesRatherThanRefusesBadTextEncoding records the
// measurement the byte-level gate is built on: encoding/json SANITIZES invalid
// text encoding to U+FFFD with a nil error rather than refusing it, so a
// utf8.ValidString check on a decoded body field could never fire. The gate has
// to stand on the raw bytes because by the time a string is decoded, the two
// distinct inputs have already collapsed to one.
func TestTheJSONDecoderSanitizesRatherThanRefusesBadTextEncoding(t *testing.T) {
	for name, document := range map[string][]byte{
		"a raw invalid byte":           []byte("{\"summary\":\"bad\xffbad\"}"),
		"an unpaired surrogate escape": []byte(`{"summary":"lone \ud800 surrogate"}`),
	} {
		t.Run(name, func(t *testing.T) {
			var decoded struct {
				Summary string `json:"summary"`
			}
			if err := json.Unmarshal(document, &decoded); err != nil {
				t.Fatalf("decoding %s returned an error, expected silent sanitization: %v", name, err)
			}
			if !utf8.ValidString(decoded.Summary) {
				t.Fatalf("decoded %s is not valid UTF-8, expected U+FFFD sanitization: %q", name, decoded.Summary)
			}
		})
	}
}

// TestEveryDeclaredFreeTextFieldIsCoveredByTheByteLevelTextGate is the coverage
// ledger for the seal-time text gate. The gate is document-wide — it runs over
// the whole record.json byte stream, not field by field — so the two rejection
// tests above already witness every free-text field at once, present and future.
// What a per-field gate would have given in exchange is a place to notice a NEW
// free-text field, so this test supplies exactly that: it enumerates every
// declared string/markdown leaf across all six record types and pins the set.
// Adding a free-text field to any schema turns this test red, which makes the
// addition a deliberate, reviewed act rather than a silent one.
func TestEveryDeclaredFreeTextFieldIsCoveredByTheByteLevelTextGate(t *testing.T) {
	want := []string{
		"diagnosis/v1: body/actions/*/description",
		"diagnosis/v1: body/actions/*/rationale",
		"diagnosis/v1: body/hypotheses/*/statement",
		"diagnosis/v1: body/summary",
		"measurements/v1: body/explanation",
		"review/v1: body/findings/*/description",
		"review/v1: body/findings/*/recommendation",
		"review/v1: body/findings/*/title",
		"review/v1: body/summary",
		"selection/v1: body/candidates/*/summary",
		"selection/v1: body/rationale",
		"validation/v1: body/checks/*/attempts/*/detail",
		"validation/v1: body/checks/*/detail",
		"validation/v1: body/checks/*/name",
		"validation/v1: body/summary",
	}

	var got []string
	for _, raw := range []string{
		"diagnosis/v1", "measurements/v1", "repository-change/v1",
		"review/v1", "selection/v1", "validation/v1",
	} {
		document, found := contracts.SchemaDocumentFor(mustTypeRef(t, raw))
		if !found {
			t.Fatalf("SchemaDocumentFor(%q): not found", raw)
		}
		for path, declaration := range document.Fields {
			if declaration.Kind == contracts.KindString || declaration.Kind == contracts.KindMarkdown {
				got = append(got, fmt.Sprintf("%s: %s", raw, path))
			}
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("declared free-text leaves =\n  %s\nwant\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
