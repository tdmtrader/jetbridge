package contracts

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

// EpistemicStatus answers one question about one field of a sealed record: on
// whose authority does this value stand?
//
// It is a property OF a field, not a field. Nothing here is ever written into a
// record body, into IntrinsicMetadata, or into a schema descriptor, and the
// declaration table below is deliberately outside every canonical digest. That
// placement is the entire point of this vocabulary. A mislabelled status is an
// ordinary bug, correctable in one commit; if it lived inside a digested subtree
// the same correction would bump a descriptor digest, and every record already
// sealed under the old one is re-validated on read (Record.RevalidateSealed,
// reached from agent/publisher/gateway.go, agent/projection/review.go,
// agent/projection/repository_change.go and agent/functions/repositorymerge).
// That read path accepts every accepted digest precisely so a bump stays a
// versioning event; the seal path (Record.AdmitForSeal) accepts only the current
// one, so a producer cannot pick its own contract identity.
//
// This is not hypothetical. Anchor content hashes are deferred, and landing them
// moves several types' anchor fields from EpistemicAsserted to EpistemicDerived.
// Because those moves happen here, they cost nothing.
//
// # Which gate a status describes
//
// Every status describes the SEAL-TIME gate — the moment the platform decided
// whether to certify these bytes at all. It is not a description of read-time
// revalidation, which is deliberately looser because a reader holds none of the
// step declarations that seal-time admission consulted. A reader of a stored
// record inherits the seal's judgment; it does not re-earn it, and reading status
// off the weaker gate would understate the authority of every field whose
// verification needed declarations that no longer exist.
type EpistemicStatus string

const (
	// EpistemicPlatform: the platform supplies the value and the producer only
	// transcribes it. The producer contributes no information: at the seal-time
	// gate exactly one value passes, and every other value is rejected. The record
	// envelope's type and schema digest are handed to the agent verbatim for
	// copying (agent/runner/runner.go:298-313) and then required to be exactly
	// those values (Record.AdmitForSeal via currentSchemaDigestOnly).
	EpistemicPlatform EpistemicStatus = "platform"

	// EpistemicDerived: the platform recomputes the value from bytes it can see
	// — the record's own sealed content, or a bound subject or payload — and
	// rejects any value that does not match the computation. The producer writes
	// it, but cannot choose it.
	//
	// Behaviourally this is indistinguishable from EpistemicPlatform: both mean
	// exactly one value passes. The difference is where that one value comes from,
	// and it is documentary rather than enforceable, which is why the two share one
	// rule in the test that pins these assignments.
	EpistemicDerived EpistemicStatus = "derived"

	// EpistemicConstrained: the validator confines the value to a closed domain
	// — an enumeration, or a set the platform supplies such as the declared
	// input ports or the record's own declared entity ids — and the producer
	// chooses freely within it. Membership is checked; the choice is not.
	//
	// A domain of exactly one value is still a closed domain, so a few entries are
	// constrained where the truth is nearer to platform. That direction understates
	// authority and is safe; the reverse never is.
	EpistemicConstrained EpistemicStatus = "constrained"

	// EpistemicAsserted: the producer said so. Nothing verifies it. Prose,
	// rationales, titles, self-reported scores and, for now, every anchor
	// locator detail.
	EpistemicAsserted EpistemicStatus = "asserted"
)

// EpistemicStatuses returns the whole vocabulary, strongest authority first.
// The order is part of the surface: a consumer ranking or grouping fields must
// not have to hardcode it.
func EpistemicStatuses() []EpistemicStatus {
	return []EpistemicStatus{
		EpistemicPlatform,
		EpistemicDerived,
		EpistemicConstrained,
		EpistemicAsserted,
	}
}

func (status EpistemicStatus) String() string {
	return string(status)
}

func (status EpistemicStatus) Validate() error {
	switch status {
	case EpistemicPlatform, EpistemicDerived, EpistemicConstrained, EpistemicAsserted:
		return nil
	default:
		return fmt.Errorf("epistemic status must be one of platform, derived, constrained, asserted, not %q", status)
	}
}

// ParseEpistemicStatus is the only way to turn an outside string into an
// EpistemicStatus. It is exact: no case folding and no trimming, because a
// vocabulary with two spellings of one value is two vocabularies.
func ParseEpistemicStatus(value string) (EpistemicStatus, error) {
	status := EpistemicStatus(value)
	if err := status.Validate(); err != nil {
		return "", err
	}
	return status, nil
}

// MarshalText and UnmarshalText give the serialized form: the bare lowercase
// token. Both validate, so an out-of-vocabulary value can neither be written nor
// read back without an error.
func (status EpistemicStatus) MarshalText() ([]byte, error) {
	if err := status.Validate(); err != nil {
		return nil, err
	}
	return []byte(status), nil
}

func (status *EpistemicStatus) UnmarshalText(data []byte) error {
	parsed, err := ParseEpistemicStatus(string(data))
	if err != nil {
		return err
	}
	*status = parsed
	return nil
}

// EpistemicDeclaration is the epistemic status of every field of one record
// type at one schema revision. It is keyed by (type, revision) because a
// revision can change what the validator verifies, and the same field path can
// therefore stand on different authority in two revisions of the same type.
//
// Field keys are DECLARATION paths: "body/findings/*/severity". They are not
// addresses and cannot be compared to one with ==; use StatusFor.
type EpistemicDeclaration struct {
	Type     snapshot.TypeRef           `json:"type"`
	Revision int                        `json:"revision"`
	Fields   map[string]EpistemicStatus `json:"fields"`
}

func (declaration EpistemicDeclaration) Validate() error {
	if err := declaration.Type.Validate(); err != nil {
		return fmt.Errorf("epistemic declaration type: %w", err)
	}
	if declaration.Revision < 1 {
		return fmt.Errorf("epistemic declaration revision must be positive")
	}
	if len(declaration.Fields) == 0 {
		return fmt.Errorf("epistemic declaration for %q revision %d declares no fields", declaration.Type, declaration.Revision)
	}
	for path, status := range declaration.Fields {
		if err := ValidateFieldDeclarationPath(path); err != nil {
			return fmt.Errorf("epistemic declaration for %q revision %d: %w", declaration.Type, declaration.Revision, err)
		}
		if err := status.Validate(); err != nil {
			return fmt.Errorf("epistemic declaration for %q revision %d field %q: %w", declaration.Type, declaration.Revision, path, err)
		}
	}
	return nil
}

func (declaration EpistemicDeclaration) Clone() EpistemicDeclaration {
	fields := make(map[string]EpistemicStatus, len(declaration.Fields))
	for path, status := range declaration.Fields {
		fields[path] = status
	}
	declaration.Fields = fields
	return declaration
}

// StatusFor resolves either form of field path. An exact declaration path wins;
// otherwise the argument is treated as a stored address and matched against the
// wildcard declarations, so a consumer holding "body/findings/f-001/severity"
// needs no knowledge of which segments are entity ids.
//
// If several declarations match and disagree — which the well-formedness tests
// exist to prevent — nothing is returned rather than one of them arbitrarily.
func (declaration EpistemicDeclaration) StatusFor(pathOrAddress string) (EpistemicStatus, bool) {
	if status, found := declaration.Fields[pathOrAddress]; found {
		return status, true
	}
	var matched EpistemicStatus
	found := false
	for path, status := range declaration.Fields {
		if !FieldAddressMatchesDeclaration(path, pathOrAddress) {
			continue
		}
		if found && matched != status {
			return "", false
		}
		matched, found = status, true
	}
	return matched, found
}

type epistemicKey struct {
	ref      snapshot.TypeRef
	revision int
}

// EpistemicDeclarationFor returns the declaration for one exact (type,
// revision). Revisions are numbered from 1.
//
// A consumer reading a stored record does not hold a revision number; it holds
// the record's schema digest. Converting one to the other is SchemaRevisionFor's
// job and nobody else's — in particular the digest's POSITION in the type's
// accepted history is not its revision, because that history is newest-first
// (AcceptedSchemaDigests) while revisions count up from 1. Callers holding a
// stored record should use EpistemicDeclarationForSchemaDigest and never do the
// arithmetic themselves.
func EpistemicDeclarationFor(ref snapshot.TypeRef, revision int) (EpistemicDeclaration, bool) {
	fields, found := epistemicFieldStatuses[epistemicKey{ref: ref, revision: revision}]
	if !found {
		return EpistemicDeclaration{}, false
	}
	return EpistemicDeclaration{Type: ref, Revision: revision, Fields: fields}.Clone(), true
}

// EpistemicDeclarationForSchemaDigest resolves the declaration from what a stored
// record actually carries. This is the entry point for any reader of the corpus,
// and it is what makes a SUPERSEDED revision reachable: a record sealed under an
// older descriptor resolves to the declaration that described the validator which
// admitted it, not to the current one.
func EpistemicDeclarationForSchemaDigest(ref snapshot.TypeRef, digest snapshot.Digest) (EpistemicDeclaration, bool) {
	revision, found := SchemaRevisionFor(ref, digest)
	if !found {
		return EpistemicDeclaration{}, false
	}
	return EpistemicDeclarationFor(ref, revision)
}

// EpistemicStatusForSchemaDigest answers for one field of the revision a stored
// record's schema digest identifies, accepting either a declaration path or a
// stored address.
func EpistemicStatusForSchemaDigest(ref snapshot.TypeRef, digest snapshot.Digest, pathOrAddress string) (EpistemicStatus, bool) {
	declaration, found := EpistemicDeclarationForSchemaDigest(ref, digest)
	if !found {
		return "", false
	}
	return declaration.StatusFor(pathOrAddress)
}

// CurrentEpistemicDeclarationFor returns the declaration for the revision newly
// authored records pin, so a consumer describing the current contract does not
// have to know revision numbers exist.
func CurrentEpistemicDeclarationFor(ref snapshot.TypeRef) (EpistemicDeclaration, bool) {
	revision, found := CurrentSchemaRevisionFor(ref)
	if !found {
		return EpistemicDeclaration{}, false
	}
	return EpistemicDeclarationFor(ref, revision)
}

// EpistemicStatusFor answers for one field of one revision, accepting either a
// declaration path or a stored address.
func EpistemicStatusFor(ref snapshot.TypeRef, revision int, pathOrAddress string) (EpistemicStatus, bool) {
	declaration, found := EpistemicDeclarationFor(ref, revision)
	if !found {
		return "", false
	}
	return declaration.StatusFor(pathOrAddress)
}

// EpistemicDeclarations returns every declaration, sorted by (type, revision),
// so a consumer can enumerate the whole table without knowing which record types
// exist.
func EpistemicDeclarations() []EpistemicDeclaration {
	declarations := make([]EpistemicDeclaration, 0, len(epistemicFieldStatuses))
	for key, fields := range epistemicFieldStatuses {
		declarations = append(declarations, EpistemicDeclaration{
			Type: key.ref, Revision: key.revision, Fields: fields,
		}.Clone())
	}
	sort.Slice(declarations, func(i, j int) bool {
		if declarations[i].Type == declarations[j].Type {
			return declarations[i].Revision < declarations[j].Revision
		}
		return declarations[i].Type < declarations[j].Type
	})
	return declarations
}

// recordEpistemicPrototypes is the zero value of every record type's complete
// wire shape, envelope included. It exists so the declaration table can be
// checked for completeness against the Go types themselves rather than against a
// hand-maintained list of field paths, which would go stale in the same commit
// that made it wrong.
var recordEpistemicPrototypes = map[snapshot.TypeRef]any{
	"review/v1":            Record[ReviewBody]{},
	"diagnosis/v1":         Record[DiagnosisBody]{},
	"validation/v1":        Record[ValidationBody]{},
	"repository-change/v1": Record[RepositoryChangeBody]{},
	"selection/v1":         Record[SelectionBody]{},
	"measurements/v1":      Record[MeasurementsBody]{},
}

const maxRecordFieldDepth = 32

// recordLeafFieldPaths enumerates the declaration path of every value-bearing
// field of a record type.
//
// Only leaves are enumerated. Objects and arrays of objects are structure, not
// claims: "body/findings" carries no value of its own, and giving the container
// a status would invite a second, weaker answer to a question the leaves already
// answer. Their shape is the schema's business.
//
// A slice of scalars IS a leaf — "body/actions/*/addresses" is one value, a set
// of ids — because the grammar has no way to address an element of it: scalars
// have no id, and positions are not addresses.
func recordLeafFieldPaths(recordType reflect.Type) ([]string, error) {
	leaves, err := recordLeafFields(recordType)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		paths = append(paths, leaf.path)
	}
	return paths, nil
}

// recordLeaf is one value-bearing field of a record type, with the two facts
// about its Go representation that a declared schema has to agree with: what kind
// of value it holds, and whether absence is distinguishable from the zero value.
//
// pointer is the whole reason `absent_image` exists in the dialect. An explicit 0
// in a *float64 is a real, distinguishable value; an explicit 0 in a float64 is
// indistinguishable from a missing key, and no rule can tell them apart.
type recordLeaf struct {
	path    string
	kind    reflect.Kind
	pointer bool
	element reflect.Kind
}

func recordLeafFields(recordType reflect.Type) ([]recordLeaf, error) {
	var leaves []recordLeaf
	if err := appendLeafFields(recordType, "", &leaves, 0); err != nil {
		return nil, err
	}
	return leaves, nil
}

func appendLeafFields(fieldType reflect.Type, prefix string, leaves *[]recordLeaf, depth int) error {
	if depth > maxRecordFieldDepth {
		return fmt.Errorf("field %q exceeds the maximum record field depth of %d", prefix, maxRecordFieldDepth)
	}
	pointer := false
	for fieldType.Kind() == reflect.Pointer {
		fieldType = fieldType.Elem()
		pointer = true
	}
	switch fieldType.Kind() {
	case reflect.Struct:
		for index := 0; index < fieldType.NumField(); index++ {
			field := fieldType.Field(index)
			if !field.IsExported() {
				continue
			}
			name, encoded := jsonFieldName(field)
			if !encoded {
				continue
			}
			child := name
			if prefix != "" {
				child = prefix + fieldPathSeparator + name
			}
			if err := appendLeafFields(field.Type, child, leaves, depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		element := fieldType.Elem()
		for element.Kind() == reflect.Pointer {
			element = element.Elem()
		}
		if element.Kind() == reflect.Struct {
			return appendLeafFields(element, prefix+fieldPathSeparator+fieldPathWildcard, leaves, depth+1)
		}
		*leaves = append(*leaves, recordLeaf{path: prefix, kind: fieldType.Kind(), element: element.Kind()})
		return nil
	case reflect.Map, reflect.Interface, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return fmt.Errorf("field %q has kind %s, which the frozen field-path grammar cannot address", prefix, fieldType.Kind())
	default:
		*leaves = append(*leaves, recordLeaf{path: prefix, kind: fieldType.Kind(), pointer: pointer})
		return nil
	}
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	tag, tagged := field.Tag.Lookup("json")
	if !tagged {
		return field.Name, true
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", false
	}
	if name == "" {
		return field.Name, true
	}
	return name, true
}
