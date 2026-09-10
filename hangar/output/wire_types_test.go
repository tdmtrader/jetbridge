package output

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A type with a `json` tag has declared itself a wire type. Whether it is
// actually sent anywhere yet is a fact about the current phase; the tag is a
// promise about its shape, and an unfrozen promise is the thing this whole
// testdata directory exists to prevent.
//
// TestProtocolGoldensCoverEveryFixture asserts the easy direction — every
// fixture on disk is claimed by the table. This is the other one: every tagged
// exported struct in the two contract packages is named by a fixture. Without
// it, adding a wire type and forgetting to freeze it is invisible, which is
// exactly how ControlledExecution, DurableOutputCapture, DeletePrecondition and
// ObjectMarker's struct form came to declare wire shapes nothing pinned.
//
// The map points each type at the fixture that covers it, including types
// covered transitively as part of a larger value. That is a stronger statement
// than an exemption list: "ReceiptClaims is frozen inside receipt.json" is
// checkable, and "ReceiptClaims does not need freezing" would not be.
//
// Reqs 22, 25-27, 35-38, 48-54, 58; AC 20.

// baseFixtureDir is where hangar/executioncontrol keeps its half of the
// contract. This file asserts the fixture exists; that package's own
// TestProtocolGoldensCoverEveryFixture asserts something asserts against it.
const baseFixtureDir = "../executioncontrol/testdata/protocol-v1"

var fixturedTypes = map[string]string{
	outputPackageDir + ":SourceIncarnation":               "source-incarnation.json",
	outputPackageDir + ":CaptureAdmission":                "capture-admission.json",
	outputPackageDir + ":ReservedIncarnation":             "reserved-incarnation.json",
	outputPackageDir + ":CaptureAcknowledgement":          "hold-acknowledgement.json",
	outputPackageDir + ":SuccessfulFinishDisposition":     "successful-finish-disposition.json",
	outputPackageDir + ":NoCaptureDisposition":            "no-capture-disposition.json",
	outputPackageDir + ":PreReservationCancelDisposition": "pre-reservation-cancel-disposition.json",
	outputPackageDir + ":ReleaseAcknowledgement":          "release-acknowledgement.json",
	outputPackageDir + ":LogicalResolution":               "logical-resolution.json",
	outputPackageDir + ":Receipt":                         "receipt.json",
	outputPackageDir + ":ReceiptClaims":                   "receipt.json",
	outputPackageDir + ":TreeAttributes":                  "receipt.json",
	outputPackageDir + ":ReceiptAdmission":                "receipt-admission.json",
	outputPackageDir + ":ClaimAcquisition":                "claim-acquire.json",
	outputPackageDir + ":ClaimRelease":                    "claim-release.json",
	outputPackageDir + ":ReadLease":                       "read-lease.json",
	outputPackageDir + ":ClaimRecord":                     "claim-record.json",
	outputPackageDir + ":ReadGrantClaims":                 "read-grant-claims.json",
	outputPackageDir + ":ReadDestination":                 "read-grant-claims.json",
	outputPackageDir + ":LeaseQuestion":                   "lease-question.json",
	outputPackageDir + ":LeaseAnswer":                     "lease-answer.json",
	outputPackageDir + ":DeletePrecondition":              "delete-precondition.json",
	outputPackageDir + ":InventoryCursor":                 "inventory-cursor.json",
	outputPackageDir + ":InventoryDebt":                   "inventory-debt.json",
	outputPackageDir + ":PolicySnapshot":                  "policy-snapshot.json",
	outputPackageDir + ":ExtensionHandshake":              "capture-extension-handshake.json",
	outputPackageDir + ":CallerNamespaceRequest":          "caller-namespace-request.json",
	outputPackageDir + ":WriterAdmission":                 "writer-admission.json",
	outputPackageDir + ":SealRequest":                     "seal-request.json",
	outputPackageDir + ":SealStarted":                     "seal-started.json",
	outputPackageDir + ":DrainedWriter":                   "drained-writer.json",
	outputPackageDir + ":ReleaseIntent":                   "release-intent.json",
	outputPackageDir + ":PublicationRequest":              "publication-request.json",
	outputPackageDir + ":PublicationResult":               "publication-result.json",
	outputPackageDir + ":CanonicalizationResult":          "canonicalization-result.json",

	basePackageDir + ":Identity":                           "identity.json",
	basePackageDir + ":Envelope":                           "envelope.json",
	basePackageDir + ":Acknowledgement":                    "acknowledgement-finish.json",
	basePackageDir + ":ExitOutcome":                        "acknowledgement-finish.json",
	basePackageDir + ":ClassifyRequest":                    "classify-request.json",
	basePackageDir + ":ClassifyResult":                     "classify-result.json",
	basePackageDir + ":ObserveFinishOrStopRequest":         "observe-finish-or-stop-request.json",
	basePackageDir + ":ObserveFinishOrStopResult":          "observe-finish-or-stop-result.json",
	basePackageDir + ":RequestSourcePreservingStopRequest": "request-source-preserving-stop-request.json",
	basePackageDir + ":RequestSourcePreservingStopResult":  "request-source-preserving-stop-result.json",
	basePackageDir + ":DestructiveCleanupEligibleRequest":  "destructive-cleanup-eligible-request.json",
	basePackageDir + ":DestructiveCleanupEligibleResult":   "destructive-cleanup-eligible-result.json",
	basePackageDir + ":Handshake":                          "capability-handshake.json",
}

// fixtureDirFor maps a scanned package to where its fixtures live.
var fixtureDirFor = map[string]string{
	outputPackageDir: protocolFixtureDir,
	basePackageDir:   baseFixtureDir,
}

func TestEveryWireTypeIsFrozen(t *testing.T) {
	fields := contractFields(t, []string{outputPackageDir, basePackageDir})
	if len(fields) == 0 {
		t.Fatal("the field inventory is empty; this guard would pass vacuously")
	}

	tagged := map[string]bool{}
	for _, field := range fields {
		if field.JSONName == "" {
			continue
		}
		tagged[field.Package+":"+field.Owner] = true
	}
	if len(tagged) == 0 {
		t.Fatal("no exported struct in either contract package declares a json tag. Either the " +
			"tag scan broke or the wire contract vanished; either way this guard is asserting " +
			"nothing.")
	}

	names := make([]string, 0, len(tagged))
	for name := range tagged {
		names = append(names, name)
	}
	sort.Strings(names)
	t.Logf("inventoried %d exported structs declaring a wire shape", len(names))

	for _, name := range names {
		fixture, frozen := fixturedTypes[name]
		if !frozen {
			t.Errorf("%s declares json tags and no fixture freezes it. A tag is a promise about "+
				"a shape another implementation will read. Freeze it in %s and name it here, or "+
				"drop the tags — a type that is not on the wire should not say it is.",
				name, protocolFixtureDir)

			continue
		}
		pkg := packageOf(name)
		full := filepath.Join(fixtureDirFor[pkg], fixture)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("%s is frozen in %s, which does not exist: %v", name, full, err)
		}
		if pkg == outputPackageDir {
			if _, covered := protocolFixtures[fixture]; !covered {
				t.Errorf("%s names %s, which protocolFixtures does not assert against. A fixture "+
					"nothing decodes is documentation, not a contract.", name, fixture)
			}
		}
	}

	for name, fixture := range fixturedTypes {
		if !tagged[name] {
			t.Errorf("fixturedTypes says %s is frozen in %s, and no exported struct by that name "+
				"declares a json tag any more. Remove the entry so the map keeps describing the "+
				"wire.", name, fixture)
		}
	}
}

// packageOf splits the package out of a "package:Type" key. The package part
// is a directory, which may itself contain no colon, so the last one wins.
func packageOf(key string) string {
	if index := strings.LastIndex(key, ":"); index >= 0 {
		return key[:index]
	}

	return ""
}
