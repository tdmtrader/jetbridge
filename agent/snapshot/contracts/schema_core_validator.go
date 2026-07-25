package contracts

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

// validateDeclaredBody is the composition point: every record contract runs the
// generic core validator BEFORE its own semantic rules, at BOTH gates. It is
// called from the gate-level wrappers rather than from inside the body validators,
// deliberately — a body validator that called it would stop being an independent
// description of the contract, and the parity gate compares exactly those two
// descriptions.
//
// The document is resolved BY TYPE, not by the revision the record's schema digest
// identifies. That is correct while each type has exactly one document: the
// revision-2 documents describe today's validators, and the coordinated descriptor
// bump changes contract identity rather than any rule. The moment a second
// revision of one type is embedded, this resolution has to become revision-aware —
// TestEveryEmbeddedDocumentLoadsForExactlyOneRecordType is the assertion that
// forces the question then.
func validateDeclaredBody(ref snapshot.TypeRef, subjects []Subject, body any) error {
	document, found := recordSchemaDocuments[ref]
	if !found {
		return fmt.Errorf("snapshot contracts: %q has no declared schema", ref)
	}
	if err := document.validateDecodedRecord(subjects, body); err != nil {
		return fmt.Errorf("snapshot contracts: record.json body: %w", err)
	}
	return nil
}

// validateDecodedRecord is the GENERIC CORE VALIDATOR: it enforces the declared
// schema — types, presence, enum membership, score pinning, entity-set identity,
// blob shape, anchor subject resolution — and nothing else. Every semantic rule
// beyond the core stays in Go, and each document lists the ones that apply to it
// in go_only_rules so the declaration cannot be mistaken for the whole contract.
//
// It composes BEFORE the per-type semantic validator at both gates. That order is
// not cosmetic: the semantic rules are written against a body whose shape already
// holds, and a cross-field rule reading a field the core would have rejected is
// reasoning about a value that should never have got that far.
//
// Every error names the field path in the frozen declaration grammar — the same
// spelling the epistemic table keys on — so a reader can look the field up in
// either table from the message alone. Element identity is deliberately not in the
// path: a position is not an address, and not every container has an id.
func (document SchemaDocument) validateDecodedRecord(subjects []Subject, body any) error {
	if err := document.validateDeclaredSubjectSet(subjects); err != nil {
		return err
	}
	declared := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		declared[subject.ID] = struct{}{}
	}
	return document.validateDeclaredValue(bodyFieldPath, reflect.ValueOf(body), declared)
}

// validateDeclaredSubjectSet enforces subject_shape: cardinality, the closed role
// set with its per-role bounds, a pinned subject type, and type uniformity.
//
// It deliberately does NOT enforce `ports`. Where the candidate-port set comes
// from is a Go rule and cannot be a schema rule: at seal time it is the server's
// compiled port declarations, and at read time the sealed candidate-role subjects
// that seal-time admission already certified. A reader holds no declarations.
func (document SchemaDocument) validateDeclaredSubjectSet(subjects []Subject) error {
	shape := document.SubjectShape
	if len(subjects) < shape.Minimum {
		return fmt.Errorf("subjects: %q requires at least %d subject(s), got %d", document.Contract, shape.Minimum, len(subjects))
	}
	if shape.Maximum != nil && len(subjects) > *shape.Maximum {
		return fmt.Errorf("subjects: %q allows at most %d subject(s), got %d", document.Contract, *shape.Maximum, len(subjects))
	}
	counts := make(map[SubjectRole]int, len(shape.Roles))
	for _, subject := range subjects {
		if _, allowed := shape.Roles[subject.Role]; !allowed {
			return fmt.Errorf("subjects/*/role: %q does not allow the %q role", document.Contract, subject.Role)
		}
		counts[subject.Role]++
		if shape.SubjectType != nil && subject.Type != *shape.SubjectType {
			return fmt.Errorf("subjects/*/type: %q requires every subject to be %q, got %q", document.Contract, *shape.SubjectType, subject.Type)
		}
		if shape.UniformSubjectType && subject.Type != subjects[0].Type {
			return fmt.Errorf("subjects/*/type: %q requires every subject to share one snapshot type", document.Contract)
		}
	}
	for _, role := range sortedRoles(shape.Roles) {
		bounds := shape.Roles[role]
		if counts[role] < bounds.Minimum {
			return fmt.Errorf("subjects/*/role: %q requires at least %d subject(s) with the %q role, got %d", document.Contract, bounds.Minimum, role, counts[role])
		}
		if bounds.Maximum != nil && counts[role] > *bounds.Maximum {
			return fmt.Errorf("subjects/*/role: %q allows at most %d subject(s) with the %q role, got %d", document.Contract, *bounds.Maximum, role, counts[role])
		}
	}
	return nil
}

func sortedRoles(roles map[SubjectRole]RoleBounds) []SubjectRole {
	ordered := make([]SubjectRole, 0, len(roles))
	for _, role := range []SubjectRole{
		SubjectRolePrimary, SubjectRoleBase, SubjectRoleEvidence,
		SubjectRoleContext, SubjectRoleCandidate, SubjectRoleReference,
	} {
		if _, found := roles[role]; found {
			ordered = append(ordered, role)
		}
	}
	return ordered
}

// validateDeclaredValue applies one declaration to one value: the presence gate
// first, then the per-kind rules, then descent.
func (document SchemaDocument) validateDeclaredValue(path string, value reflect.Value, subjects map[string]struct{}) error {
	declaration, found := document.resolved[path]
	if !found {
		return fmt.Errorf("%s: no schema declaration; the document and the Go type have drifted apart", path)
	}
	if err := checkDeclaredGoKind(path, declaration, value); err != nil {
		return err
	}
	absent := isAbsentImage(declaration, value)
	switch declaration.Presence {
	case PresenceRequired:
		if absent {
			return fmt.Errorf("%s: is required%s", path, blankNote(declaration))
		}
	case PresenceForbidden:
		if !absent {
			return fmt.Errorf("%s: is forbidden here; the pinning on this field's declaration closes its domain to absence", path)
		}
	}
	if absent {
		return nil
	}
	return document.validateDeclaredPresentValue(path, declaration, value, subjects)
}

func (document SchemaDocument) validateDeclaredPresentValue(
	path string,
	declaration FieldDeclaration,
	value reflect.Value,
	subjects map[string]struct{},
) error {
	switch declaration.Kind {
	case KindString, KindMarkdown:
		if declaration.PathPrefix != "" && !strings.HasPrefix(value.String(), declaration.PathPrefix) {
			return fmt.Errorf("%s: must be beneath %s", path, declaration.PathPrefix)
		}
		return nil
	case KindIdentifier:
		// The rule is recordIdentifierPattern, shared with ValidateIdentifier; only
		// the message is restated, so that every core rejection LEADS with the field
		// path in the frozen grammar rather than embedding it mid-sentence.
		if !recordIdentifierPattern.MatchString(value.String()) {
			return fmt.Errorf(
				"%s: %q must be a non-empty identifier containing only letters, digits, dot, underscore, colon, or hyphen",
				path, value.String(),
			)
		}
		if declaration.resolvesSubject {
			if _, found := subjects[value.String()]; !found {
				return fmt.Errorf("%s: %q is not declared by this record", path, value.String())
			}
		}
		return nil
	case KindEnum:
		return checkEnumMembership(path, declaration, value.String())
	case KindInt:
		return checkIntegerBounds(path, declaration, dereference(value))
	case KindFloat:
		number := dereference(value).Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("%s: must be a finite number", path)
		}
		return nil
	case KindBool:
		return nil
	case KindTimestamp:
		if _, err := time.Parse(time.RFC3339, value.String()); err != nil {
			return fmt.Errorf("%s: must be an RFC 3339 timestamp with an offset", path)
		}
		return nil
	case KindDuration:
		return checkDurationBounds(path, declaration, value.String())
	case KindEntitySet:
		return document.validateEntitySet(path, declaration, value, subjects)
	case KindArray:
		return document.validateArray(path, declaration, value, subjects)
	case KindObject, KindScore, KindBlob, KindAnchor:
		return document.descendStruct(path, value, subjects)
	case KindRecordRef:
		// No field of any of the six types declares a record-ref at revision 2, so
		// there is no Go representation to walk. Refusing loudly beats guessing a
		// shape: the first consumer is the conversion of the existing agent_feedback
		// table, which is where the address grammar gets settled against something
		// real.
		return fmt.Errorf("%s: record-ref has no Go representation at this revision", path)
	default:
		return fmt.Errorf("%s: kind %q is not an enforced core kind", path, declaration.Kind)
	}
}

// descendStruct walks a struct's JSON fields against their declarations. It is
// what makes a Go field with no declaration a runtime error as well as a
// load-time one, so drift cannot hide behind a value nobody happened to validate.
func (document SchemaDocument) descendStruct(prefix string, value reflect.Value, subjects map[string]struct{}) error {
	structValue := dereference(value)
	if structValue.Kind() != reflect.Struct {
		return fmt.Errorf("%s: must be an object", prefix)
	}
	structType := structValue.Type()
	for index := 0; index < structType.NumField(); index++ {
		field := structType.Field(index)
		if !field.IsExported() {
			continue
		}
		name, encoded := jsonFieldName(field)
		if !encoded {
			continue
		}
		if err := document.validateDeclaredValue(prefix+fieldPathSeparator+name, structValue.Field(index), subjects); err != nil {
			return err
		}
	}
	return nil
}

// validateEntitySet is ValidateEntityIDs' rule, re-stated in the frozen grammar:
// ids that are identifiers, unique, and lexicographically ascending by byte
// comparison of the declared id_field. An addressable element set is a different
// thing from a bag, and only the former can appear in a stored address.
//
// The rule is deliberately not delegated to ValidateEntityIDs, even though that
// helper enforces exactly the same thing for the Go validators. Its messages spell
// a field as `findings[0].id`, which is a SECOND field-path grammar, and the
// dialect freezes one. Agreement between the two statements of the rule is not
// taken on trust: it is what the parity gate checks, on every entity set of every
// fixture, in both directions.
//
// The identifier check itself is left to descent, because `<path>/*/<id_field>` is
// a declared identifier field in its own right and validating it twice would give
// one violation two different messages.
func (document SchemaDocument) validateEntitySet(
	path string,
	declaration FieldDeclaration,
	value reflect.Value,
	subjects map[string]struct{},
) error {
	slice := dereference(value)
	if err := document.descendElements(path, slice, subjects); err != nil {
		return err
	}
	idPath := path + fieldPathSeparator + fieldPathWildcard + fieldPathSeparator + declaration.IDField
	previous := ""
	for index := 0; index < slice.Len(); index++ {
		element := dereference(slice.Index(index))
		field, found := structFieldByJSONFieldName(element, declaration.IDField)
		if !found {
			return fmt.Errorf("%s: elements have no %q field", path, declaration.IDField)
		}
		id := field.String()
		if index > 0 {
			switch strings.Compare(previous, id) {
			case 0:
				return fmt.Errorf("%s: %q is duplicate", idPath, id)
			case 1:
				return fmt.Errorf("%s: must be lexicographically sorted by %s", path, declaration.IDField)
			}
		}
		previous = id
	}
	return nil
}

func (document SchemaDocument) validateArray(
	path string,
	declaration FieldDeclaration,
	value reflect.Value,
	subjects map[string]struct{},
) error {
	slice := dereference(value)
	if slice.Kind() != reflect.Slice && slice.Kind() != reflect.Array {
		return fmt.Errorf("%s: must be an array", path)
	}
	if !isScalarKind(declaration.Element) {
		return document.descendElements(path, slice, subjects)
	}
	// An array of a scalar kind is itself a leaf: the grammar cannot address an
	// element of one, so its elements are validated here rather than through a
	// declaration of their own.
	elementDeclaration := FieldDeclaration{Kind: declaration.Element, Presence: PresenceRequired}
	previous := ""
	seen := make(map[string]struct{}, slice.Len())
	for index := 0; index < slice.Len(); index++ {
		element := slice.Index(index)
		if err := document.validateDeclaredPresentValue(path, elementDeclaration, element, subjects); err != nil {
			return err
		}
		if element.Kind() != reflect.String {
			continue
		}
		if declaration.Unique {
			if _, duplicate := seen[element.String()]; duplicate {
				return fmt.Errorf("%s: %q is duplicate", path, element.String())
			}
			seen[element.String()] = struct{}{}
		}
		if declaration.Sorted && index > 0 && strings.Compare(previous, element.String()) > 0 {
			return fmt.Errorf("%s: must be lexicographically sorted", path)
		}
		previous = element.String()
	}
	return nil
}

// descendElements walks the elements of an array or entity-set against the
// declarations at "<path>/*". An element that EXISTS is present by definition, so
// the element itself never goes through the presence gate — only its fields do.
func (document SchemaDocument) descendElements(path string, slice reflect.Value, subjects map[string]struct{}) error {
	prefix := path + fieldPathSeparator + fieldPathWildcard
	for index := 0; index < slice.Len(); index++ {
		if err := document.descendStruct(prefix, slice.Index(index), subjects); err != nil {
			return err
		}
	}
	return nil
}

func checkEnumMembership(path string, declaration FieldDeclaration, value string) error {
	for _, allowed := range declaration.Values {
		if allowed.Value == value {
			return nil
		}
	}
	permitted := make([]string, 0, len(declaration.Values))
	for _, allowed := range declaration.Values {
		permitted = append(permitted, allowed.Value)
	}
	return fmt.Errorf("%s: %q is not one of %s", path, value, strings.Join(permitted, ", "))
}

func checkIntegerBounds(path string, declaration FieldDeclaration, value reflect.Value) error {
	number := value.Int()
	if declaration.Minimum != nil {
		minimum, err := strconv.ParseInt(strings.TrimSpace(string(declaration.Minimum)), 10, 64)
		if err == nil && number < minimum {
			return fmt.Errorf("%s: %d is below the declared minimum %d", path, number, minimum)
		}
	}
	if declaration.Maximum != nil {
		maximum, err := strconv.ParseInt(strings.TrimSpace(string(declaration.Maximum)), 10, 64)
		if err == nil && number > maximum {
			return fmt.Errorf("%s: %d is above the declared maximum %d", path, number, maximum)
		}
	}
	return nil
}

func checkDurationBounds(path string, declaration FieldDeclaration, value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s: %q is not a Go duration", path, value)
	}
	if minimum, err := durationAttribute("", path, "minimum", declaration.Minimum); err == nil && minimum != nil {
		if int64(duration) < *minimum {
			return fmt.Errorf("%s: %q is below the declared minimum %s", path, value, time.Duration(*minimum))
		}
	}
	if maximum, err := durationAttribute("", path, "maximum", declaration.Maximum); err == nil && maximum != nil {
		if int64(duration) > *maximum {
			return fmt.Errorf("%s: %q is above the declared maximum %s", path, value, time.Duration(*maximum))
		}
	}
	return nil
}

// isAbsentImage is §5.2's table. The record decoder cannot distinguish absence
// from the type's zero value — encoding/json leaves a missing key and an explicit
// null at the same value without error — so presence is a statement about the
// decoded value and never about the JSON key.
//
// For every string-shaped kind, blank IS absent: `required` on a string is the
// same statement as "the validator rejects blank", which is how every string the
// Go validators require is actually enforced.
func isAbsentImage(declaration FieldDeclaration, value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	if value.Kind() == reflect.Pointer {
		return value.IsNil()
	}
	switch value.Kind() {
	case reflect.String:
		return strings.TrimSpace(value.String()) == ""
	case reflect.Slice, reflect.Array, reflect.Map:
		return value.Len() == 0
	default:
		return value.IsZero()
	}
}

func blankNote(declaration FieldDeclaration) string {
	switch declaration.Kind {
	case KindString, KindMarkdown, KindIdentifier, KindTimestamp, KindDuration, KindEnum:
		// Absence and blankness are one condition here, and saying so in the message
		// saves a reader the trip to §5.2.
		return " and must not be blank; a missing key and a blank value decode identically"
	default:
		return ""
	}
}

func checkDeclaredGoKind(path string, declaration FieldDeclaration, value reflect.Value) error {
	if !value.IsValid() {
		return fmt.Errorf("%s: has no value", path)
	}
	kind := value.Kind()
	if kind == reflect.Pointer {
		kind = value.Type().Elem().Kind()
	}
	for _, want := range goKindsFor(declaration) {
		if kind == want {
			return nil
		}
	}
	return fmt.Errorf("%s: is declared %q but holds a %s", path, declaration.Kind, kind)
}

func dereference(value reflect.Value) reflect.Value {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return value
		}
		value = value.Elem()
	}
	return value
}

func structFieldByJSONFieldName(structValue reflect.Value, name string) (reflect.Value, bool) {
	structType := structValue.Type()
	for index := 0; index < structType.NumField(); index++ {
		field := structType.Field(index)
		if !field.IsExported() {
			continue
		}
		encoded, ok := jsonFieldName(field)
		if !ok || encoded != name {
			continue
		}
		return structValue.Field(index), true
	}
	return reflect.Value{}, false
}
