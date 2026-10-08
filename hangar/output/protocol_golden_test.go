package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// protocolFixtureDir holds the language-neutral wire contract for the durable
// output-capture extension. Its base half lives in
// hangar/executioncontrol/testdata/protocol-v1; nothing here re-freezes a base
// value, and the capture-extension fixtures reference the base identity rather
// than restating it.
const protocolFixtureDir = "testdata/protocol-v1"

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

// roundTripMarker is the object-metadata case. GCS custom metadata is a
// string-to-string map on the wire, so the fixture is that map and the
// assertion is that ParseObjectMarker and ObjectMarker.Metadata are exact
// inverses over it. Encoding the Go struct instead would freeze a shape no
// object store ever sees.
func roundTripMarker(t *testing.T, raw []byte) {
	t.Helper()

	metadata := decodeExact[map[string]string](t, raw)

	marker, err := ParseObjectMarker(metadata)
	if err != nil {
		t.Fatalf("the frozen marker metadata does not parse: %v", err)
	}
	if err := marker.Validate(); err != nil {
		t.Fatalf("the frozen marker metadata does not validate: %v", err)
	}

	got := canonicalJSON(t, marker.Metadata())
	if !bytes.Equal(got, raw) {
		t.Errorf("ObjectMarker.Metadata did not reproduce the frozen metadata.\n--- got ---\n%s\n--- want ---\n%s", got, raw)
	}
}

func refuseMarker(t *testing.T, raw []byte) {
	t.Helper()

	metadata := decodeExact[map[string]string](t, raw)

	marker, err := ParseObjectMarker(metadata)
	if err == nil {
		err = marker.Validate()
	}
	if err == nil {
		t.Fatalf("ParseObjectMarker accepted metadata the protocol must refuse:\n%s", raw)
	}
	t.Logf("refused as required: %v", err)
}

var protocolFixtures = map[string]func(*testing.T, []byte){
	// The capture routes and the node marker.
	"capture-key.json":                     func(t *testing.T, raw []byte) { roundTrip[CaptureKey](t, raw) },
	"step-marker.json":                     func(t *testing.T, raw []byte) { roundTrip[StepMarker](t, raw) },
	"capture-hold-request.json":            func(t *testing.T, raw []byte) { roundTrip[CaptureHoldRequest](t, raw) },
	"capture-hold-acknowledgement.json":    func(t *testing.T, raw []byte) { roundTrip[CaptureHoldAcknowledgement](t, raw) },
	"capture-seal-request.json":            func(t *testing.T, raw []byte) { roundTrip[CaptureSealRequest](t, raw) },
	"capture-seal-result.json":             func(t *testing.T, raw []byte) { roundTrip[CaptureSealResult](t, raw) },
	"capture-publish-request.json":         func(t *testing.T, raw []byte) { roundTrip[CapturePublishRequest](t, raw) },
	"capture-publish-result.json":          func(t *testing.T, raw []byte) { roundTrip[CapturePublishResult](t, raw) },
	"capture-release-request.json":         func(t *testing.T, raw []byte) { roundTrip[CaptureReleaseRequest](t, raw) },
	"capture-release-acknowledgement.json": func(t *testing.T, raw []byte) { roundTrip[CaptureReleaseAcknowledgement](t, raw) },
	"capture-stat-request.json":            func(t *testing.T, raw []byte) { roundTrip[CaptureStatRequest](t, raw) },
	"input-stage.json":                     func(t *testing.T, raw []byte) { roundTrip[InputStage](t, raw) },
	"input-publish-request.json":           func(t *testing.T, raw []byte) { roundTrip[InputPublishRequest](t, raw) },
	"input-publication.json":               func(t *testing.T, raw []byte) { roundTrip[InputPublication](t, raw) },

	// The reservation the ATC repeats into the producing Pod's volume. Its
	// refusal twin is a reservation whose directory does not derive from the
	// incarnation beside it -- a chosen path wearing a server-issued identity,
	// which is the shape the no-caller-chosen-path rule exists to refuse.

	// Sealing's two halves are two statements, so each gets its own frozen
	// fixture and each is asserted to be of its own kind. A single fixture
	// would freeze the merged shape this contract exists not to have.

	// The node-local control API's request bodies. They are wire in exactly the
	// sense this file means it: another implementation of the output plane
	// reads them, so their shape is a promise and not an internal detail.

	"claim-acquire.json":       func(t *testing.T, raw []byte) { roundTrip[ClaimAcquisition](t, raw) },
	"claim-release.json":       func(t *testing.T, raw []byte) { roundTrip[ClaimRelease](t, raw) },
	"delete-precondition.json": func(t *testing.T, raw []byte) { roundTrip[DeletePrecondition](t, raw) },
	"gcs-marker-metadata.json": roundTripMarker,

	"capture-extension-handshake.json": func(t *testing.T, raw []byte) {
		roundTrip[ExtensionHandshake](t, raw)
	},

	// The empty object is the whole shape: every field of a
	// CallerNamespaceRequest is a namespace a caller tried to choose, so the
	// only request this plane serves is the one that names none of them. The
	// type carries json tags precisely so that a hostile body decodes into
	// something that can be refused with a message, rather than into fields
	// that are silently dropped.
	"caller-namespace-request.json": func(t *testing.T, raw []byte) {
		roundTrip[CallerNamespaceRequest](t, raw)
	},
	"refusal-caller-chosen-namespace.json": func(t *testing.T, raw []byte) {
		refuse[CallerNamespaceRequest](t, raw)
	},

	// A claim is the one hold: a consumer's (no expiry; this one released)
	// and a reader's (expiring on the database clock) are one record shape.
	"claim-record.json":         func(t *testing.T, raw []byte) { roundTrip[ClaimRecord](t, raw) },
	"claim-record-reader.json":  func(t *testing.T, raw []byte) { roundTrip[ClaimRecord](t, raw) },
	"read-warrant-claims.json":  func(t *testing.T, raw []byte) { roundTrip[ReadWarrantClaims](t, raw) },
	"managed-read-request.json": func(t *testing.T, raw []byte) { roundTrip[ManagedReadRequest](t, raw) },

	"refusal-malformed-marker-metadata.json": refuseMarker,
	"refusal-unmarked-object-metadata.json":  refuseMarker,
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
		t.Fatalf("%s contains no JSON fixture; the extension is not frozen at all", protocolFixtureDir)
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
