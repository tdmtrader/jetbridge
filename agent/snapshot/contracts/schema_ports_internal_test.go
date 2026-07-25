package contracts

import (
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// THE PORTS ORACLE.
//
// `subject_shape.ports` is declared inside bytes that become a frozen contract
// identity, and it is the one declaration the generic core validator deliberately
// does NOT enforce (schema_core_validator.go, validateDeclaredSubjectSet): where
// the candidate-port set comes from is a Go rule and cannot be a schema rule,
// because at seal time it is the server's compiled port declarations and at read
// time it is the sealed candidate-role subjects that admission already certified.
//
// That left it with no oracle in EITHER direction — a value nothing reads and
// nothing checks. Flipping review/v1 to "candidate-ports-exactly" failed no test
// at all, and would have frozen that lie into the descriptor at the bump.
//
// This is the missing oracle, and it is a table on purpose: the fact under test
// is which Go rule sources candidacy for each type, and that is not derivable
// from either side alone. The table is the third pin, so a change to the document
// alone fails the first half and a change to the validator alone fails the
// second. A coordinated change to both still has to come here and say so.
var declaredSubjectPorts = map[snapshot.TypeRef]string{
	reviewType:           PortsAnyExposedInput,
	diagnosisType:        PortsAnyExposedInput,
	validationType:       PortsAnyExposedInput,
	repositoryChangeType: PortsAnyExposedInput,
	measurementsType:     PortsAnyExposedInput,

	// The integrity property, and the only entry that is not the default: a judge
	// may only select from what it was exposed to AS A CANDIDATE. One subject per
	// declared candidate port, and no others.
	selectionType: PortsCandidatePortsExactly,
}

// recordBodySealGates is each type's SEAL-TIME body gate, wired to the production
// entry point the registered validator calls.
//
// The asymmetry in these signatures IS the Go rule the table describes.
// SelectionBody.AdmitForSeal takes the step's declarations because candidacy is a
// server-side declaration a producer must not influence; every other body gate
// takes only the subjects and therefore CANNOT consult ports, whatever anyone
// later believes about it. Changing that is changing a signature, which cannot be
// done quietly here.
var recordBodySealGates = map[snapshot.TypeRef]func([]Subject, any, snapshot.ValidationContext) error{
	reviewType: func(subjects []Subject, body any, _ snapshot.ValidationContext) error {
		return body.(ReviewBody).Validate(subjects)
	},
	diagnosisType: func(subjects []Subject, body any, _ snapshot.ValidationContext) error {
		return body.(DiagnosisBody).Validate(subjects)
	},
	validationType: func(subjects []Subject, body any, _ snapshot.ValidationContext) error {
		return body.(ValidationBody).Validate(subjects)
	},
	repositoryChangeType: func(subjects []Subject, body any, _ snapshot.ValidationContext) error {
		return body.(RepositoryChangeBody).Validate(subjects)
	},
	measurementsType: func(subjects []Subject, body any, _ snapshot.ValidationContext) error {
		return body.(MeasurementsBody).Validate(subjects)
	},
	selectionType: func(subjects []Subject, body any, declarations snapshot.ValidationContext) error {
		return body.(SelectionBody).AdmitForSeal(subjects, declarations)
	},
}

func TestEveryDocumentDeclaresThePortsRuleItsGoGateActuallyImplements(t *testing.T) {
	for ref, document := range recordSchemaDocuments {
		want, tabled := declaredSubjectPorts[ref]
		if !tabled {
			t.Errorf("%q has a schema document but no entry in the ports table, so its declared ports value is checked against nothing", ref)
			continue
		}
		if document.SubjectShape.Ports != want {
			t.Errorf(
				"%q declares ports %q but the Go rule sourcing its candidacy is %q; the declaration is about to be frozen into a descriptor, so it has to be the true one",
				ref, document.SubjectShape.Ports, want,
			)
		}
		if _, wired := recordBodySealGates[ref]; !wired {
			t.Errorf("%q has no body seal gate wired, so nothing exercises its ports behaviour", ref)
		}
	}
	for ref := range declaredSubjectPorts {
		if _, found := recordSchemaDocuments[ref]; !found {
			t.Errorf("the ports table names %q, which has no schema document", ref)
		}
	}
}

// TestGoGatesEnforcePortsExactlyAsDeclared is the behavioural half. It varies ONE
// thing — the declared candidate-port set — and asserts the verdict changes for
// exactly the types whose document says it should.
//
// Three probes, because "candidate ports exactly" is two rules and not one:
// every subject must be a declared candidate port, and every declared candidate
// port must have a subject. A gate that enforced only the first would still let a
// judge silently ignore a candidate it was handed.
func TestGoGatesEnforcePortsExactlyAsDeclared(t *testing.T) {
	for _, fixture := range recordFixtures() {
		exact := declaredSubjectPorts[fixture.ref] == PortsCandidatePortsExactly
		gate := recordBodySealGates[fixture.ref]
		if gate == nil {
			t.Fatalf("%s has no body seal gate", fixture.name)
		}
		t.Run(fixture.name, func(t *testing.T) {
			ports := subjectPorts(fixture.subjects)

			if err := gate(fixture.subjects, fixture.body, candidacyDeclaring(t, fixture.subjects, ports...)); err != nil {
				t.Fatalf("the seal gate rejected a fixture whose candidate ports are exactly its subjects: %v", err)
			}

			if len(ports) > 0 {
				withdrawn := gate(fixture.subjects, fixture.body, candidacyDeclaring(t, fixture.subjects, ports[1:]...))
				assertPortSensitivity(t, exact, withdrawn, fmt.Sprintf(
					"a subject bound to the exposed input %q, which is not a declared candidate port", ports[0],
				))
			}

			phantom := gate(fixture.subjects, fixture.body, candidacyDeclaring(t, fixture.subjects, append(ports, unclaimedPortName)...))
			assertPortSensitivity(t, exact, phantom, fmt.Sprintf(
				"a declared candidate port %q with no subject", unclaimedPortName,
			))
		})
	}
}

// unclaimedPortName is exposed to the step and declared a candidate, but no
// subject claims it — the "and no others" half of candidate-ports-exactly, read
// from the other end.
const unclaimedPortName = "unclaimed-candidate"

func assertPortSensitivity(t *testing.T, exact bool, verdict error, what string) {
	t.Helper()
	switch {
	case exact && verdict == nil:
		t.Errorf(
			"the document declares %q but the Go gate accepted %s; the declaration promises an integrity property the validator is not keeping",
			PortsCandidatePortsExactly, what,
		)
	case !exact && verdict != nil:
		t.Errorf(
			"the document declares %q but the Go gate rejected %s, so it is port-sensitive after all: %v",
			PortsAnyExposedInput, what, verdict,
		)
	}
}

func subjectPorts(subjects []Subject) []string {
	ports := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		ports = append(ports, subject.Input)
	}
	return ports
}

// candidacyDeclaring builds the server-side declarations for a step exposing
// every subject's input plus one spare, and declaring the named subset of them
// candidate ports. The spare is always exposed and only sometimes a candidate,
// which is what lets the probes vary candidacy without varying exposure.
func candidacyDeclaring(t *testing.T, subjects []Subject, candidatePorts ...string) snapshot.ValidationContext {
	t.Helper()
	inputs := map[string]snapshot.SnapshotRef{
		unclaimedPortName: {ID: 1, Type: "repository/v1", Digest: fixtureDigestAt(15)},
	}
	for index, subject := range subjects {
		inputs[subject.Input] = snapshot.SnapshotRef{
			ID: snapshot.SnapshotID(index + 2), Type: subject.Type, Digest: subject.Digest,
		}
	}
	declarations, err := snapshot.NewValidationContext(inputs, nil, snapshot.WithCandidatePorts(candidatePorts...))
	if err != nil {
		t.Fatalf("NewValidationContext(candidate ports %s): %v", strings.Join(candidatePorts, ", "), err)
	}
	return declarations
}
