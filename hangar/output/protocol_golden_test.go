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

// roundTripAcknowledgementOfKind is roundTrip plus ValidateAs: the fixture must
// be the statement it claims to be. A source-ledger statement whose kind is not
// bound to the state it proves is finding 1's defect on the wire.
func roundTripAcknowledgementOfKind(kind CaptureAcknowledgementKind) func(*testing.T, []byte) {
	return func(t *testing.T, raw []byte) {
		t.Helper()

		roundTrip[CaptureAcknowledgement](t, raw)

		ack := decodeExact[CaptureAcknowledgement](t, raw)
		if err := ack.ValidateAs(kind); err != nil {
			t.Errorf("the frozen fixture is not a %s statement: %v", kind, err)
		}
	}
}

var protocolFixtures = map[string]func(*testing.T, []byte){
	"source-incarnation.json": func(t *testing.T, raw []byte) { roundTrip[SourceIncarnation](t, raw) },
	"capture-admission.json":  func(t *testing.T, raw []byte) { roundTrip[CaptureAdmission](t, raw) },

	// The reservation the ATC repeats into the producing Pod's volume. Its
	// refusal twin is a reservation whose directory does not derive from the
	// incarnation beside it -- a chosen path wearing a server-issued identity,
	// which is the shape Req 7 exists to refuse.
	"reserved-incarnation.json": func(t *testing.T, raw []byte) {
		roundTrip[ReservedIncarnation](t, raw)
	},
	"refusal-chosen-incarnation-directory.json": func(t *testing.T, raw []byte) {
		refuse[ReservedIncarnation](t, raw)
	},

	"hold-acknowledgement.json": func(t *testing.T, raw []byte) {
		roundTrip[CaptureAcknowledgement](t, raw)
	},
	"writer-ticket-acknowledgement.json": func(t *testing.T, raw []byte) {
		roundTrip[CaptureAcknowledgement](t, raw)
	},

	// Sealing's two halves are two statements, so each gets its own frozen
	// fixture and each is asserted to be of its own kind. A single fixture
	// would freeze the merged shape this contract exists not to have.
	"seal-started-acknowledgement.json":   roundTripAcknowledgementOfKind(CaptureSealStarted),
	"seal-confirmed-acknowledgement.json": roundTripAcknowledgementOfKind(CaptureSealConfirmed),

	"successful-finish-disposition.json": func(t *testing.T, raw []byte) {
		roundTrip[SuccessfulFinishDisposition](t, raw)
	},
	"no-capture-disposition.json": func(t *testing.T, raw []byte) {
		roundTrip[NoCaptureDisposition](t, raw)
	},
	"pre-reservation-cancel-disposition.json": func(t *testing.T, raw []byte) {
		roundTrip[PreReservationCancelDisposition](t, raw)
	},
	"release-acknowledgement.json": func(t *testing.T, raw []byte) {
		roundTrip[ReleaseAcknowledgement](t, raw)
	},

	// The node-local control API's request bodies. They are wire in exactly the
	// sense this file means it: another implementation of the output daemon
	// reads them, so their shape is a promise and not an internal detail.
	"writer-admission.json": func(t *testing.T, raw []byte) { roundTrip[WriterAdmission](t, raw) },
	"seal-request.json":     func(t *testing.T, raw []byte) { roundTrip[SealRequest](t, raw) },
	"seal-started.json":     func(t *testing.T, raw []byte) { roundTrip[SealStarted](t, raw) },
	"drained-writer.json":   func(t *testing.T, raw []byte) { roundTrip[DrainedWriter](t, raw) },
	"release-intent.json":   func(t *testing.T, raw []byte) { roundTrip[ReleaseIntent](t, raw) },
	"publication-request.json": func(t *testing.T, raw []byte) {
		roundTrip[PublicationRequest](t, raw)
	},
	"publication-result.json": func(t *testing.T, raw []byte) {
		roundTrip[PublicationResult](t, raw)
	},

	"receipt.json":           func(t *testing.T, raw []byte) { roundTrip[Receipt](t, raw) },
	"receipt-admission.json": func(t *testing.T, raw []byte) { roundTrip[ReceiptAdmission](t, raw) },
	"logical-resolution.json": func(t *testing.T, raw []byte) {
		roundTrip[LogicalResolution](t, raw)
	},
	"claim-acquire.json":       func(t *testing.T, raw []byte) { roundTrip[ClaimAcquisition](t, raw) },
	"claim-release.json":       func(t *testing.T, raw []byte) { roundTrip[ClaimRelease](t, raw) },
	"read-lease.json":          func(t *testing.T, raw []byte) { roundTrip[ReadLease](t, raw) },
	"delete-precondition.json": func(t *testing.T, raw []byte) { roundTrip[DeletePrecondition](t, raw) },
	"inventory-cursor.json":    func(t *testing.T, raw []byte) { roundTrip[InventoryCursor](t, raw) },
	"inventory-debt.json":      func(t *testing.T, raw []byte) { roundTrip[InventoryDebt](t, raw) },
	"policy-snapshot.json":     func(t *testing.T, raw []byte) { roundTrip[PolicySnapshot](t, raw) },
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

	"dispositions.json":                  assertClosedDispositions,
	"capture-acknowledgement-kinds.json": assertClosedCaptureAcknowledgementKinds,
	"no-capture-reasons.json":            assertClosedNoCaptureReasons,
	"debt-reasons.json":                  assertClosedDebtReasons,
	"policy-states.json":                 assertClosedPolicyStates,

	"refusal-unknown-disposition.json": func(t *testing.T, raw []byte) {
		refuse[NoCaptureDisposition](t, raw)
	},
	"refusal-unknown-policy-state.json": func(t *testing.T, raw []byte) {
		refuse[PolicySnapshot](t, raw)
	},
	"refusal-unknown-marker-version.json":   refuseMarker,
	"refusal-unmarked-object-metadata.json": refuseMarker,
}

func assertClosedEnum[T ~string](t *testing.T, raw []byte, got []T, name string) {
	t.Helper()

	want := decodeExact[[]T](t, raw)

	if len(got) == 0 {
		t.Fatalf("%s is empty; the closed vocabulary check would pass vacuously", name)
	}
	if len(got) != len(want) {
		t.Fatalf("%s has %d members, the frozen fixture has %d: %v vs %v", name, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s member %d: have %q, frozen fixture says %q", name, i, got[i], want[i])
		}
	}
	if !bytes.Equal(canonicalJSON(t, got), raw) {
		t.Errorf("%s does not re-encode to the frozen fixture:\n%s", name, canonicalJSON(t, got))
	}
}

func assertClosedDispositions(t *testing.T, raw []byte) {
	assertClosedEnum(t, raw, Dispositions(), "Dispositions()")
}

func assertClosedCaptureAcknowledgementKinds(t *testing.T, raw []byte) {
	assertClosedEnum(t, raw, CaptureAcknowledgementKinds(), "CaptureAcknowledgementKinds()")
}

func assertClosedNoCaptureReasons(t *testing.T, raw []byte) {
	assertClosedEnum(t, raw, NoCaptureReasons(), "NoCaptureReasons()")
}

func assertClosedDebtReasons(t *testing.T, raw []byte) {
	assertClosedEnum(t, raw, DebtReasons(), "DebtReasons()")
}

func assertClosedPolicyStates(t *testing.T, raw []byte) {
	assertClosedEnum(t, raw, PolicyStates(), "PolicyStates()")
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
