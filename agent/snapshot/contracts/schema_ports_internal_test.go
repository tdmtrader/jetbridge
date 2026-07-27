package contracts

import (
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// THE PORTS ORACLE.
//
// `subject_shape.ports` is declared inside bytes that become a frozen contract
// identity, and it is the one declaration the generic core validator deliberately
// does NOT enforce (schema_core_validator.go, validateDeclaredSubjectSet): which
// exposed input a subject may bind is a Go rule, judged against step declarations
// a reader of a stored record does not hold.
//
// That left it with no oracle in EITHER direction — a value nothing reads and
// nothing checks. Flipping review/v1 to some other token failed no test at all,
// and would have frozen that lie into the descriptor at the bump.
//
// This is the missing oracle, and it is a table on purpose: the fact under test
// is which Go rule sources a subject's port for each type, and that is not
// derivable from either side alone. The table is the third pin, so a change to
// the document alone fails the first half and a change to the validator alone
// fails the second. A coordinated change to both still has to come here and say
// so.
var declaredSubjectPorts = map[snapshot.TypeRef]string{
	reviewType:           PortsAnyExposedInput,
	diagnosisType:        PortsAnyExposedInput,
	validationType:       PortsAnyExposedInput,
	repositoryChangeType: PortsAnyExposedInput,
	measurementsType:     PortsAnyExposedInput,
}

// recordBodySealGates is each type's SEAL-TIME body gate, wired to the production
// entry point the registered validator calls.
//
// These signatures ARE the Go rule the table describes. Every body gate takes
// only the subjects, so none of them CAN consult the step's port declarations,
// whatever anyone later believes about it — which is exactly what
// "any-exposed-input" claims. Making one port-sensitive means changing this map's
// type, which cannot be done quietly.
var recordBodySealGates = map[snapshot.TypeRef]func([]Subject, any) error{
	reviewType: func(subjects []Subject, body any) error {
		return body.(ReviewBody).Validate(subjects)
	},
	diagnosisType: func(subjects []Subject, body any) error {
		return body.(DiagnosisBody).Validate(subjects)
	},
	validationType: func(subjects []Subject, body any) error {
		return body.(ValidationBody).Validate(subjects)
	},
	repositoryChangeType: func(subjects []Subject, body any) error {
		return body.(RepositoryChangeBody).Validate(subjects)
	},
	measurementsType: func(subjects []Subject, body any) error {
		return body.(MeasurementsBody).Validate(subjects)
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
				"%q declares ports %q but the ports table says its Go gate implements %q",
				ref, document.SubjectShape.Ports, want,
			)
		}
	}
	for ref := range declaredSubjectPorts {
		if _, found := recordSchemaDocuments[ref]; !found {
			t.Errorf("the ports table names %q, which has no schema document", ref)
		}
	}
}

// TestNoBodySealGateIsPortSensitive is the behavioural half. Every fixture is
// driven through its seal gate and must be admitted, and the gate has no port
// declarations to consult at all — so a type that later grew a port rule would
// have to widen the gate signature above, which this file makes loud.
func TestNoBodySealGateIsPortSensitive(t *testing.T) {
	for _, fixture := range recordFixtures() {
		gate := recordBodySealGates[fixture.ref]
		if gate == nil {
			t.Fatalf("%s has no body seal gate", fixture.name)
		}
		t.Run(fixture.name, func(t *testing.T) {
			if err := gate(fixture.subjects, fixture.body); err != nil {
				t.Fatalf("the seal gate rejected a valid fixture: %v", err)
			}
		})
	}
}
