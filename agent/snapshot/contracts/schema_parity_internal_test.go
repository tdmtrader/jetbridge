package contracts

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// THE PARITY GATE.
//
// The declared schema and the Go validator are two descriptions of one truth.
// This drives generated instances through both and fails on any divergence,
// asymmetrically, because the two directions of divergence are not equally
// dangerous:
//
//   - The declared schema rejecting what Go accepts is OVER-DECLARATION, and it is
//     always a failure. Stored records are re-validated on read, so a schema
//     stricter than the validator retroactively corrupts every record already
//     sealed, and the failure surfaces as corruption on some later read rather
//     than at seal time.
//   - Go rejecting what the declared schema accepts is only a failure where the
//     DECLARATION CLAIMS TO COVER the rule. Everything in §9 of the dialect is
//     deliberately Go-only, and demanding parity there would be demanding that the
//     schema express what it says it cannot.
//
// This is the test that catches drift when someone edits a Go validator without
// the schema document, which is the failure mode a second description exists to
// make visible.
func TestDeclaredSchemaAndGoValidatorAgreeOnMutatedInstances(t *testing.T) {
	cases := 0
	// Which declarations a fixture actually carries a value for, and which of those
	// a mutation exercised. Comparing the two is what makes coverage a checked
	// property of the generator.
	reachable := make(map[string]map[string]struct{})
	mutated := make(map[string]map[string]struct{})
	// families counts how often each mutation family fired, keyed by a distinctive
	// substring of its name.
	families := make(map[string]int)
	for _, fixture := range recordFixtures() {
		document, found := recordSchemaDocuments[fixture.ref]
		if !found {
			t.Fatalf("%q has no schema document", fixture.ref)
		}
		t.Run(fixture.name, func(t *testing.T) {
			for _, path := range sortedFieldPaths(document.resolved) {
				if path == bodyFieldPath {
					// The body root is the one declaration with no meaningful
					// mutation: emptying it mutates every field at once and says
					// nothing about any of them.
					continue
				}
				declaration := document.resolved[path]
				occurrences := len(locateDeclaredValues(deepCopyBody(t, fixture.body), path))
				for occurrence := 0; occurrence < occurrences; occurrence++ {
					pristine := locateDeclaredValues(deepCopyBody(t, fixture.body), path)[occurrence]
					if !pristine.IsZero() || declaration.Presence == PresenceForbidden {
						recordExercisedPath(reachable, fixture.ref.String(), path)
					}
					for _, mutation := range mutationsFor(document, path, declaration, pristine) {
						body := deepCopyBody(t, fixture.body)
						mutation.apply(locateDeclaredValues(body, path)[occurrence])
						label := fmt.Sprintf("%s occurrence %d: %s", path, occurrence, mutation.name)
						assertParity(t, fixture, document, label, path, mutation.covered, fixture.subjects, body.Interface())
						recordExercisedPath(mutated, fixture.ref.String(), path)
						recordMutationFamily(families, mutation.name)
						cases++
					}
				}
			}
			for _, mutation := range subjectMutations(document, fixture.subjects) {
				label := "subjects: " + mutation.name
				assertParity(t, fixture, document, label, "", mutation.covered, mutation.apply(fixture.subjects), fixture.body)
				recordMutationFamily(families, mutation.name)
				cases++
			}
		})
	}
	// A parity gate that silently generated nothing would be the most expensive
	// kind of green. Coverage is asserted two ways, neither of them a case count: a
	// magic minimum would pass a generator that hammered one field and skipped
	// thirty.
	//
	// First, every mutation FAMILY must have fired. A family that silently stops
	// generating — because no fixture has two elements in some entity set, say —
	// removes a whole class of divergence from the gate without failing it.
	for _, family := range mutationFamilies {
		if families[family] == 0 {
			t.Errorf("no mutation of the family %q was generated, so that class of divergence is not being compared at all", family)
		}
	}
	for ref, paths := range reachable {
		for path := range paths {
			if _, exercised := mutated[ref][path]; !exercised {
				t.Errorf("%s field %q is carried by a fixture but no mutation exercises it, so nothing compares the two descriptions there", ref, path)
			}
		}
	}
	t.Logf("drove %d mutated instances through both descriptions", cases)
}

// mutationFamilies is every class of divergence the generator is expected to
// produce, keyed by a distinctive substring of the mutation's name so a generated
// name carrying a value still buckets correctly.
var mutationFamilies = []string{
	"replaced with its absent image",
	"blanked to whitespace",
	"set outside its declared value set",
	"below its declared minimum",
	"set below its declared minimum duration",
	"given a value the pinning forbids",
	"reordered out of lexicographic id order",
	"given two elements with the same id",
	"reordered out of its declared sort order",
	"set outside the range its scale implies",
	"removed entirely",
	"which the shape does not allow",
	"given a foreign snapshot type",
	"one past a declared maximum",
	"emptied of a role its declared minimum requires",
}

// recordMutationFamily buckets a mutation by the family substrings the coverage
// assertion names, so a generated name carrying a value ("set to -1, below its
// declared minimum") still counts towards its family.
func recordMutationFamily(families map[string]int, name string) {
	for _, family := range mutationFamilies {
		if strings.Contains(name, family) {
			families[family]++
		}
	}
}

func recordExercisedPath(index map[string]map[string]struct{}, ref, path string) {
	paths := index[ref]
	if paths == nil {
		paths = make(map[string]struct{})
		index[ref] = paths
	}
	paths[path] = struct{}{}
}

func assertParity(
	t *testing.T,
	fixture recordFixture,
	document SchemaDocument,
	label, path string,
	covered bool,
	subjects []Subject,
	body any,
) {
	t.Helper()
	declared := document.validateDecodedRecord(subjects, body)
	semantic := fixture.validate(subjects, body)
	switch {
	case declared != nil && semantic == nil:
		t.Errorf("%s: the declared schema rejects what the Go validator accepts; over-declaration retroactively corrupts every stored record shaped like this\n  declared schema: %v", label, declared)
	case declared == nil && semantic != nil && covered:
		t.Errorf("%s: the Go validator rejects what the declared schema accepts, for a rule the declaration claims to cover\n  go validator: %v", label, semantic)
	}
	if declared != nil && covered && path != "" && !strings.Contains(declared.Error(), path) {
		t.Errorf("%s: the declared-schema error does not name the field path in the frozen grammar\n  got: %v", label, declared)
	}
}

// TestEveryDeclaredPathIsPopulatedBySomeFixture closes the reachability hole in
// the gate above.
//
// TestDeclaredSchemaAndGoValidatorAgreeOnMutatedInstances records a path as
// exercised only where a fixture carries a value for it, and then checks that
// every exercised path was mutated. That is the wrong quantifier: a container no
// fixture ever populates contributes NO paths, so every declaration beneath it —
// including a declared-REQUIRED leaf — is silently outside the gate. It is
// neither presence-derived (the presence test only probes declared-optional
// fields) nor parity-compared (no instance ever reaches it), and nothing fails.
//
// The fix is to make fixture completeness the asserted property rather than a
// hoped-for one: every declared path must be populated by some accepted instance,
// so adding a declaration means adding the instance that derives and compares it.
// A `forbidden` declaration is populated by being REACHED — its whole content is
// that the value must not be there — so reaching it counts.
func TestEveryDeclaredPathIsPopulatedBySomeFixture(t *testing.T) {
	populated := make(map[string]map[string]struct{})
	for _, fixture := range recordFixtures() {
		document, found := recordSchemaDocuments[fixture.ref]
		if !found {
			t.Fatalf("%q has no schema document", fixture.ref)
		}
		body := deepCopyBody(t, fixture.body)
		for path, declaration := range document.resolved {
			for _, value := range locateDeclaredValues(body, path) {
				if !value.IsZero() || declaration.Presence == PresenceForbidden {
					recordExercisedPath(populated, fixture.ref.String(), path)
				}
			}
		}
	}
	for ref, document := range recordSchemaDocuments {
		for _, path := range sortedFieldPaths(document.resolved) {
			if _, found := populated[ref.String()][path]; found {
				continue
			}
			t.Errorf(
				"%q declares %q but no accepted instance populates it, so neither its presence nor its parity with the Go validator is derived from anything",
				ref, path,
			)
		}
	}
}

// §5.1's presence predicate, mechanically. `optional` is a statement about a
// quantified set of records — at least one otherwise-valid record has the field's
// absent image, and at least one has a real value — which is precisely why it is a
// test and not a judgment.
//
// A declared-optional field with no absence witness is mis-declared: it is
// `required`, and declaring it optional understates the contract. One with no
// presence witness is either dead or forbidden. A field with no probe at all fails
// too, so adding a field without deriving its presence is not possible.
func TestDeclaredOptionalFieldsHaveWitnessesBothWays(t *testing.T) {
	type witness struct{ absent, present bool }
	witnesses := make(map[string]map[string]witness)
	for _, fixture := range recordFixtures() {
		document := recordSchemaDocuments[fixture.ref]
		if err := fixture.validate(fixture.subjects, fixture.body); err != nil {
			t.Fatalf("%s is not an accepted instance: %v", fixture.name, err)
		}
		perType := witnesses[fixture.ref.String()]
		if perType == nil {
			perType = make(map[string]witness)
			witnesses[fixture.ref.String()] = perType
		}
		body := deepCopyBody(t, fixture.body)
		for path, declaration := range document.resolved {
			if declaration.Presence != PresenceOptional {
				continue
			}
			for _, value := range locateDeclaredValues(body, path) {
				seen := perType[path]
				if isAbsentForWitness(value) {
					seen.absent = true
				} else {
					seen.present = true
				}
				perType[path] = seen
			}
		}
	}
	for ref, document := range recordSchemaDocuments {
		for path, declaration := range document.resolved {
			if declaration.Presence != PresenceOptional {
				continue
			}
			seen, probed := witnesses[ref.String()][path]
			if !probed {
				t.Errorf("%q field %q is declared optional but no fixture reaches it; presence with no probe is a judgment, not a derivation", ref, path)
				continue
			}
			if !seen.absent {
				t.Errorf("%q field %q is declared optional but no accepted instance omits it; that makes it required, and declaring it optional understates the contract", ref, path)
			}
			if !seen.present {
				t.Errorf("%q field %q is declared optional but no accepted instance carries a real value for it", ref, path)
			}
		}
	}
}

// isAbsentForWitness is deliberately NOT the validator's own absence predicate:
// using that would let the implementation define the property it is being
// measured against. It is §5.2's table read straight off the Go representation —
// nil for a pointer or slice, the empty string, zero, false, or a struct with
// every field at its own absent image — which reflect.Value.IsZero computes
// exactly. Blank-but-not-empty strings count as present here, so a whitespace-only
// value is a mutation rather than a witness.
func isAbsentForWitness(value reflect.Value) bool {
	return value.IsZero()
}

type mutationSpec struct {
	name string
	// covered records whether the DECLARATION claims to cover this mutation. It is
	// the whole reason the gate can be strict in one direction without demanding
	// that the schema express the rules §9 reserves for Go.
	covered bool
	apply   func(reflect.Value)
}

func mutationsFor(document SchemaDocument, path string, declaration FieldDeclaration, value reflect.Value) []mutationSpec {
	var specs []mutationSpec
	required := declaration.Presence == PresenceRequired

	if !value.IsZero() {
		specs = append(specs, mutationSpec{
			name:    "replaced with its absent image",
			covered: required,
			apply:   func(target reflect.Value) { target.Set(reflect.Zero(target.Type())) },
		})
	}
	if value.Kind() == reflect.String && isStringShapedKind(declaration.Kind) && value.String() != "" {
		specs = append(specs, mutationSpec{
			name:    "blanked to whitespace",
			covered: required,
			apply:   func(target reflect.Value) { target.SetString("   ") },
		})
	}
	if declaration.Kind == KindEnum && value.Kind() == reflect.String {
		specs = append(specs, mutationSpec{
			name:    "set outside its declared value set",
			covered: true,
			apply:   func(target reflect.Value) { target.SetString("not-a-declared-value") },
		})
	}
	if declaration.Kind == KindInt && declaration.Minimum != nil {
		below := belowDeclaredIntegerMinimum(declaration)
		specs = append(specs, mutationSpec{
			name:    fmt.Sprintf("set to %d, below its declared minimum", below),
			covered: true,
			apply:   func(target reflect.Value) { setIntLike(target, below) },
		})
	}
	if declaration.Kind == KindDuration && declaration.Minimum != nil {
		specs = append(specs, mutationSpec{
			name:    "set below its declared minimum duration",
			covered: true,
			apply:   func(target reflect.Value) { target.SetString("-1s") },
		})
	}
	if declaration.Presence == PresenceForbidden {
		specs = append(specs, mutationSpec{
			name:    "given a value the pinning forbids",
			covered: true,
			apply:   func(target reflect.Value) { setFloatLike(target, 0.5) },
		})
	}
	if declaration.Kind == KindEntitySet && value.Kind() == reflect.Slice && value.Len() >= 2 {
		idField := declaration.IDField
		specs = append(specs,
			mutationSpec{
				name:    "reordered out of lexicographic id order",
				covered: true,
				apply: func(target reflect.Value) {
					first := reflect.New(target.Index(0).Type()).Elem()
					first.Set(target.Index(0))
					target.Index(0).Set(target.Index(1))
					target.Index(1).Set(first)
				},
			},
			mutationSpec{
				name:    "given two elements with the same id",
				covered: true,
				apply: func(target reflect.Value) {
					source, sourceFound := structFieldByJSONFieldName(target.Index(0), idField)
					destination, destinationFound := structFieldByJSONFieldName(target.Index(1), idField)
					if sourceFound && destinationFound {
						destination.Set(source)
					}
				},
			},
		)
	}
	if declaration.Kind == KindArray && declaration.Sorted && value.Kind() == reflect.Slice && value.Len() >= 2 {
		specs = append(specs, mutationSpec{
			name:    "reordered out of its declared sort order",
			covered: true,
			apply: func(target reflect.Value) {
				first := reflect.New(target.Index(0).Type()).Elem()
				first.Set(target.Index(0))
				target.Index(0).Set(target.Index(1))
				target.Index(1).Set(first)
			},
		})
	}
	// A score's numeric containment is a Go rule, not a schema rule (§6.10), so this
	// case exists to prove the declared schema is not stricter than the validator
	// there — not to demand that it match.
	if strings.HasSuffix(path, fieldPathSeparator+"value") && declaration.Kind == KindFloat {
		if parent, found := document.resolved[strings.TrimSuffix(path, fieldPathSeparator+"value")]; found && parent.Kind == KindScore {
			specs = append(specs, mutationSpec{
				name:    "set outside the range its scale implies",
				covered: false,
				apply:   func(target reflect.Value) { setFloatLike(target, 2) },
			})
		}
	}
	return specs
}

func belowDeclaredIntegerMinimum(declaration FieldDeclaration) int64 {
	minimum, err := strconv.ParseInt(strings.TrimSpace(string(declaration.Minimum)), 10, 64)
	if err != nil {
		return -1
	}
	if below := minimum - 1; below != 0 {
		return below
	}
	// Never mutate to the absent image: that is the drop case, and conflating the
	// two would hide which rule actually fired.
	return -1
}

func setIntLike(target reflect.Value, value int64) {
	if target.Kind() == reflect.Pointer {
		allocated := reflect.New(target.Type().Elem())
		allocated.Elem().SetInt(value)
		target.Set(allocated)
		return
	}
	target.SetInt(value)
}

func setFloatLike(target reflect.Value, value float64) {
	if target.Kind() == reflect.Pointer {
		allocated := reflect.New(target.Type().Elem())
		allocated.Elem().SetFloat(value)
		target.Set(allocated)
		return
	}
	switch target.Kind() {
	case reflect.Float32, reflect.Float64:
		target.SetFloat(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		target.SetInt(int64(value))
	case reflect.String:
		target.SetString(strconv.FormatFloat(value, 'f', -1, 64))
	case reflect.Bool:
		target.SetBool(true)
	}
}

func isStringShapedKind(kind SchemaKind) bool {
	switch kind {
	case KindString, KindMarkdown, KindIdentifier, KindTimestamp, KindDuration, KindEnum:
		return true
	default:
		return false
	}
}

type subjectMutationSpec struct {
	name    string
	covered bool
	apply   func([]Subject) []Subject
}

// The subject set is the one envelope constraint a document declares, because it
// is the one that genuinely differs per type. These mutations are what make
// `measurements/v1` constraining nothing at all a checked fact rather than a
// claim in a comment.
func subjectMutations(document SchemaDocument, subjects []Subject) []subjectMutationSpec {
	specs := []subjectMutationSpec{{
		name:    "removed entirely",
		covered: true,
		apply:   func([]Subject) []Subject { return nil },
	}}
	if role, found := roleOutsideShape(document.SubjectShape); found {
		specs = append(specs, subjectMutationSpec{
			name:    fmt.Sprintf("first subject given the %q role, which the shape does not allow", role),
			covered: true,
			apply: func(original []Subject) []Subject {
				mutated := append([]Subject(nil), original...)
				mutated[0].Role = role
				return mutated
			},
		})
	}
	// The cardinality families. They probe from OUTSIDE each declared bound, which
	// is the half TestFixturesWitnessEveryDeclaredSubjectCardinality cannot reach:
	// that test makes the corpus sit exactly ON every bound, so a bound moved
	// inwards stops accepting a valid record. These make a bound moved OUTWARDS —
	// a declared maximum stricter than the validator, a declared minimum the
	// validator does not actually require — stop rejecting an invalid one. Between
	// them every cardinality in subject_shape has a witness on each side.
	if shape := document.SubjectShape; shape.Maximum != nil {
		count := *shape.Maximum + 1
		specs = append(specs, subjectMutationSpec{
			name:    fmt.Sprintf("grown to %d subjects, one past a declared maximum of %d", count, *shape.Maximum),
			covered: true,
			apply:   func(original []Subject) []Subject { return withSubjectCount(document, original, count) },
		})
	}
	for _, role := range sortedRoles(document.SubjectShape.Roles) {
		bounds := document.SubjectShape.Roles[role]
		if bounds.Maximum != nil {
			count := *bounds.Maximum + 1
			specs = append(specs, subjectMutationSpec{
				name:    fmt.Sprintf("given %d subjects in the %q role, one past a declared maximum of %d", count, role, *bounds.Maximum),
				covered: true,
				apply:   func(original []Subject) []Subject { return withRoleCount(original, role, count) },
			})
		}
		if bounds.Minimum > 0 {
			specs = append(specs, subjectMutationSpec{
				name:    fmt.Sprintf("emptied of a role its declared minimum requires, %q", role),
				covered: true,
				apply:   func(original []Subject) []Subject { return withRoleCount(original, role, 0) },
			})
		}
	}
	if len(subjects) > 0 {
		specs = append(specs, subjectMutationSpec{
			name:    "first subject given a foreign snapshot type",
			covered: document.SubjectShape.SubjectType != nil || document.SubjectShape.UniformSubjectType,
			apply: func(original []Subject) []Subject {
				mutated := append([]Subject(nil), original...)
				mutated[0].Type = "opaque/v1"
				return mutated
			},
		})
	}
	return specs
}

// withRoleCount returns the subject set with EXACTLY count subjects in the given
// role and every other subject untouched, so a cardinality mutation moves one
// number and nothing else. Synthesized subjects share the set's snapshot type,
// because a mutation that also broke type uniformity would leave two rules
// competing to explain one rejection.
func withRoleCount(subjects []Subject, role SubjectRole, count int) []Subject {
	kept := make([]Subject, 0, len(subjects)+count)
	inRole := 0
	for _, subject := range subjects {
		if subject.Role != role {
			kept = append(kept, subject)
			continue
		}
		if inRole < count {
			kept = append(kept, subject)
			inRole++
		}
	}
	for index := inRole; index < count; index++ {
		kept = append(kept, synthesizedSubject(subjects, role, index))
	}
	sort.Slice(kept, func(left, right int) bool { return kept[left].ID < kept[right].ID })
	return kept
}

// withSubjectCount returns a subject set of exactly count subjects, adding any it
// needs in the shape's first allowed role.
func withSubjectCount(document SchemaDocument, subjects []Subject, count int) []Subject {
	roles := sortedRoles(document.SubjectShape.Roles)
	if len(roles) == 0 {
		return subjects
	}
	kept := append([]Subject(nil), subjects...)
	if len(kept) > count {
		kept = kept[:count]
	}
	for index := len(kept); index < count; index++ {
		kept = append(kept, synthesizedSubject(subjects, roles[0], index))
	}
	sort.Slice(kept, func(left, right int) bool { return kept[left].ID < kept[right].ID })
	return kept
}

func synthesizedSubject(subjects []Subject, role SubjectRole, index int) Subject {
	snapshotType := snapshot.TypeRef("repository/v1")
	if len(subjects) > 0 {
		snapshotType = subjects[0].Type
	}
	id := fmt.Sprintf("%s-added-%d", role, index)
	return Subject{
		ID: id, Role: role, Input: "input-" + id, Type: snapshotType,
		Digest: fixtureDigestAt(len(subjects) + index),
	}
}

func roleOutsideShape(shape SubjectShape) (SubjectRole, bool) {
	for _, role := range []SubjectRole{
		SubjectRolePrimary, SubjectRoleBase, SubjectRoleEvidence,
		SubjectRoleContext, SubjectRoleCandidate, SubjectRoleReference,
	} {
		if _, allowed := shape.Roles[role]; !allowed {
			return role, true
		}
	}
	return "", false
}

// deepCopyBody round-trips through JSON rather than copying field by field,
// because JSON is the representation the record actually has: a copy that
// preserved something the wire format cannot carry would make the mutation tests
// test something no stored record can be.
func deepCopyBody(t *testing.T, body any) reflect.Value {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal fixture body: %v", err)
	}
	target := reflect.New(reflect.TypeOf(body))
	if err := json.Unmarshal(encoded, target.Interface()); err != nil {
		t.Fatalf("unmarshal fixture body: %v", err)
	}
	return target.Elem()
}

// locateDeclaredValues resolves a DECLARATION path against a decoded body,
// expanding "*" to every element at that depth. It is how a declaration path
// addresses values without the grammar ever needing array positions: the position
// is an index into the result, never part of the path.
func locateDeclaredValues(body reflect.Value, path string) []reflect.Value {
	segments := strings.Split(path, fieldPathSeparator)
	if segments[0] != bodyFieldPath {
		return nil
	}
	current := []reflect.Value{body}
	for _, segment := range segments[1:] {
		var next []reflect.Value
		for _, value := range current {
			for value.Kind() == reflect.Pointer {
				if value.IsNil() {
					value = reflect.Value{}
					break
				}
				value = value.Elem()
			}
			if !value.IsValid() {
				continue
			}
			if segment == fieldPathWildcard {
				if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
					continue
				}
				for index := 0; index < value.Len(); index++ {
					next = append(next, value.Index(index))
				}
				continue
			}
			if value.Kind() != reflect.Struct {
				continue
			}
			field, found := structFieldByJSONFieldName(value, segment)
			if !found {
				continue
			}
			next = append(next, field)
		}
		current = next
	}
	return current
}
