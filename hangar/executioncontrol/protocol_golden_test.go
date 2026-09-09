package executioncontrol

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// protocolFixtureDir holds the language-neutral wire contract this track
// freezes. It is the only description of the protocol that a non-Go
// implementation can read, so the assertions below are byte-exact in both
// directions: every fixture must decode into the Go type with no field left
// over, and re-encoding that value must reproduce the file exactly.
//
// A golden that is only ever compared against itself proves nothing, so the
// suite also refuses to run against an empty or partially covered directory:
// TestProtocolGoldensCoverEveryFixture fails if any file in the directory is
// not named by the table, and the table itself must be non-empty.
const protocolFixtureDir = "testdata/protocol-v1"

// canonicalJSON is the one encoding this protocol has. Two spaces, struct field
// order, trailing newline. It is stated here rather than left to whatever a
// caller happens to pass MarshalIndent, because "the fixture matches" is only
// meaningful if there is exactly one way to produce it.
func canonicalJSON(t *testing.T, value any) []byte {
	t.Helper()

	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("encoding %T: %v", value, err)
	}

	return append(out, '\n')
}

type validator interface {
	Validate() error
}

// decodeExact decodes raw into a fresh T, refusing any field the type does not
// declare. A protocol that silently drops an unknown field cannot tell a new
// version from a typo, which is the failure the closed vocabulary exists to
// prevent.
func decodeExact[T any](t *testing.T, raw []byte) T {
	t.Helper()

	var value T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&value); err != nil {
		t.Fatalf("decoding %T: %v", value, err)
	}
	if dec.More() {
		t.Fatalf("decoding %T: trailing content after the first JSON value", value)
	}

	return value
}

// roundTrip is the assertion every accepted fixture makes.
func roundTrip[T any](t *testing.T, raw []byte) {
	t.Helper()

	value := decodeExact[T](t, raw)

	if v, ok := any(value).(validator); ok {
		if err := v.Validate(); err != nil {
			t.Fatalf("the frozen fixture does not validate as %T: %v", value, err)
		}
	} else {
		t.Fatalf("%T has no Validate method; every protocol value must bound itself", value)
	}

	got := canonicalJSON(t, value)
	if !bytes.Equal(got, raw) {
		t.Errorf("re-encoding %T did not reproduce the frozen fixture.\n--- got ---\n%s\n--- want ---\n%s", value, got, raw)
	}
}

// refuse is the assertion every refusal fixture makes: decoding, or failing
// that validation, must reject it. An unknown enum member that decodes into the
// zero value and validates is exactly the silent acceptance the closed
// vocabulary forbids.
func refuse[T any](t *testing.T, raw []byte) {
	t.Helper()

	var value T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	err := dec.Decode(&value)
	if err == nil {
		if v, ok := any(value).(validator); ok {
			err = v.Validate()
		}
	}
	if err == nil {
		t.Fatalf("%T accepted a fixture the protocol must refuse:\n%s", value, raw)
	}
	t.Logf("refused as required: %v", err)
}

// protocolFixtures names every file in protocolFixtureDir and what it is.
//
// It is a table rather than a directory walk so that adding a fixture is a
// decision about which type it pins, and so that deleting a type cannot quietly
// orphan its contract.
var protocolFixtures = map[string]func(*testing.T, []byte){
	"identity.json": func(t *testing.T, raw []byte) { roundTrip[Identity](t, raw) },
	"envelope.json": func(t *testing.T, raw []byte) { roundTrip[Envelope](t, raw) },

	"classify-request.json":           func(t *testing.T, raw []byte) { roundTrip[ClassifyRequest](t, raw) },
	"classify-result.json":            func(t *testing.T, raw []byte) { roundTrip[ClassifyResult](t, raw) },
	"classify-result-unresolved.json": func(t *testing.T, raw []byte) { roundTrip[ClassifyResult](t, raw) },
	"observe-finish-or-stop-request.json": func(t *testing.T, raw []byte) {
		roundTrip[ObserveFinishOrStopRequest](t, raw)
	},
	"observe-finish-or-stop-result.json": func(t *testing.T, raw []byte) {
		roundTrip[ObserveFinishOrStopResult](t, raw)
	},
	"request-source-preserving-stop-request.json": func(t *testing.T, raw []byte) {
		roundTrip[RequestSourcePreservingStopRequest](t, raw)
	},
	"request-source-preserving-stop-result.json": func(t *testing.T, raw []byte) {
		roundTrip[RequestSourcePreservingStopResult](t, raw)
	},
	"destructive-cleanup-eligible-request.json": func(t *testing.T, raw []byte) {
		roundTrip[DestructiveCleanupEligibleRequest](t, raw)
	},
	"destructive-cleanup-eligible-result.json": func(t *testing.T, raw []byte) {
		roundTrip[DestructiveCleanupEligibleResult](t, raw)
	},

	"acknowledgement-start.json":  func(t *testing.T, raw []byte) { roundTrip[Acknowledgement](t, raw) },
	"acknowledgement-finish.json": func(t *testing.T, raw []byte) { roundTrip[Acknowledgement](t, raw) },
	"acknowledgement-stop.json":   func(t *testing.T, raw []byte) { roundTrip[Acknowledgement](t, raw) },

	"capability-handshake.json": func(t *testing.T, raw []byte) { roundTrip[Handshake](t, raw) },

	"classifications.json":       assertClosedClassifications,
	"acknowledgement-kinds.json": assertClosedAcknowledgementKinds,

	"refusal-unknown-classification.json":       func(t *testing.T, raw []byte) { refuse[ClassifyResult](t, raw) },
	"refusal-empty-classification.json":         func(t *testing.T, raw []byte) { refuse[ClassifyResult](t, raw) },
	"refusal-unknown-protocol-version.json":     func(t *testing.T, raw []byte) { refuse[ClassifyRequest](t, raw) },
	"refusal-unknown-acknowledgement-kind.json": func(t *testing.T, raw []byte) { refuse[Acknowledgement](t, raw) },
}

// assertClosedClassifications pins the closed vocabulary itself, in the order
// the enum declares it. Classifications() is what every other consumer of this
// protocol enumerates; if it and the fixture disagree, one of them changed
// without the other.
func assertClosedClassifications(t *testing.T, raw []byte) {
	t.Helper()

	want := decodeExact[[]Classification](t, raw)
	got := Classifications()

	if len(got) == 0 {
		t.Fatal("Classifications() is empty; the closed vocabulary check would pass vacuously")
	}
	if len(got) != len(want) {
		t.Fatalf("Classifications() has %d members, the frozen fixture has %d: %v vs %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("classification %d: have %q, frozen fixture says %q", i, got[i], want[i])
		}
	}
	if !bytes.Equal(canonicalJSON(t, got), raw) {
		t.Errorf("Classifications() does not re-encode to the frozen fixture:\n%s", canonicalJSON(t, got))
	}
}

func assertClosedAcknowledgementKinds(t *testing.T, raw []byte) {
	t.Helper()

	want := decodeExact[[]AcknowledgementKind](t, raw)
	got := AcknowledgementKinds()

	if len(got) == 0 {
		t.Fatal("AcknowledgementKinds() is empty; the closed vocabulary check would pass vacuously")
	}
	if len(got) != len(want) {
		t.Fatalf("AcknowledgementKinds() has %d members, the frozen fixture has %d: %v vs %v",
			len(got), len(want), got, want)
	}
	if !bytes.Equal(canonicalJSON(t, got), raw) {
		t.Errorf("AcknowledgementKinds() does not re-encode to the frozen fixture:\n%s", canonicalJSON(t, got))
	}
}

func TestProtocolGoldens(t *testing.T) {
	if len(protocolFixtures) == 0 {
		t.Fatal("no fixtures are declared; this suite would pass vacuously")
	}

	names := make([]string, 0, len(protocolFixtures))
	for name := range protocolFixtures {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(protocolFixtureDir, name))
			if err != nil {
				t.Fatalf("reading the frozen fixture: %v", err)
			}
			protocolFixtures[name](t, raw)
		})
	}
}

// TestProtocolGoldensCoverEveryFixture is the non-vacuity half. Without it a
// fixture could be added, or a type deleted along with its table entry, and the
// suite above would still report success over whatever remained.
func TestProtocolGoldensCoverEveryFixture(t *testing.T) {
	entries, err := os.ReadDir(protocolFixtureDir)
	if err != nil {
		t.Fatalf("reading %s: %v", protocolFixtureDir, err)
	}

	onDisk := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		onDisk[entry.Name()] = true
	}

	if len(onDisk) == 0 {
		t.Fatalf("%s contains no JSON fixture; the protocol is not frozen at all", protocolFixtureDir)
	}

	for name := range onDisk {
		if _, covered := protocolFixtures[name]; !covered {
			t.Errorf("%s/%s is not named in protocolFixtures. A frozen fixture nothing "+
				"asserts against is documentation, not a contract.", protocolFixtureDir, name)
		}
	}
	for name := range protocolFixtures {
		if !onDisk[name] {
			t.Errorf("protocolFixtures names %s/%s, which does not exist.", protocolFixtureDir, name)
		}
	}
}
