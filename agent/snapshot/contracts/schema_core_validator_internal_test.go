package contracts

import (
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// One rejection per rule of the enforced core, each asserting two things: that the
// core rule fires at all, and that the message names the field in the FROZEN
// declaration grammar. The second half matters as much as the first — a message
// spelling a field any other way introduces a second field-path grammar, and the
// dialect freezes one.
func TestGenericCoreValidatorEnforcesTheCoreAndNamesTheFieldPath(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		fixture  string
		mutate   func(reflect.Value)
		wantPath string
		wantWhy  string
	}{
		{
			name:    "a required markdown field left blank",
			fixture: "review/changes-required",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/summary", func(value reflect.Value) { value.SetString("  \t ") })
			},
			wantPath: "body/summary",
			wantWhy:  "must not be blank",
		},
		{
			name:    "an enum value outside its declared set",
			fixture: "review/changes-required",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/findings/*/severity", func(value reflect.Value) { value.SetString("catastrophic") })
			},
			wantPath: "body/findings/*/severity",
			wantWhy:  "observation, low, medium, high, critical",
		},
		{
			name:    "an identifier outside the identifier charset",
			fixture: "review/changes-required",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/findings/*/category", func(value reflect.Value) { value.SetString("not an identifier") })
			},
			wantPath: "body/findings/*/category",
			wantWhy:  "identifier",
		},
		{
			name:    "an anchor subject the record does not declare",
			fixture: "review/changes-required",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/findings/*/evidence/*/subject", func(value reflect.Value) { value.SetString("ghost") })
			},
			wantPath: "body/findings/*/evidence/*/subject",
			wantWhy:  "not declared by this record",
		},
		{
			name:    "an entity set out of lexicographic id order",
			fixture: "review/changes-required",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/findings", func(value reflect.Value) {
					first := reflect.New(value.Index(0).Type()).Elem()
					first.Set(value.Index(0))
					value.Index(0).Set(value.Index(1))
					value.Index(1).Set(first)
				})
			},
			wantPath: "body/findings",
			wantWhy:  "lexicographically sorted by id",
		},
		{
			name:    "an integer below its declared minimum",
			fixture: "diagnosis/identified",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/hypotheses/*/rank", func(value reflect.Value) { value.SetInt(-3) })
			},
			wantPath: "body/hypotheses/*/rank",
			wantWhy:  "below the declared minimum 1",
		},
		{
			name:    "a score bound the pinning forbids",
			fixture: "diagnosis/identified",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/hypotheses/*/confidence/minimum", func(value reflect.Value) {
					allocated := reflect.New(value.Type().Elem())
					allocated.Elem().SetFloat(0)
					value.Set(allocated)
				})
			},
			wantPath: "body/hypotheses/*/confidence/minimum",
			wantWhy:  "forbidden",
		},
		{
			name:    "a score scale outside the pinned value",
			fixture: "diagnosis/identified",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/hypotheses/*/confidence/scale", func(value reflect.Value) { value.SetString("bounded") })
			},
			wantPath: "body/hypotheses/*/confidence/scale",
			wantWhy:  "unit-interval",
		},
		{
			name:    "a scalar array out of its declared sort order",
			fixture: "diagnosis/identified",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/actions/*/addresses", func(value reflect.Value) {
					value.Set(reflect.ValueOf([]string{"h-2", "h-1"}))
				})
			},
			wantPath: "body/actions/*/addresses",
			wantWhy:  "lexicographically sorted",
		},
		{
			name:    "a duration below its declared minimum",
			fixture: "validation/failed",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/checks/*/attempts/*/duration", func(value reflect.Value) { value.SetString("-4s") })
			},
			wantPath: "body/checks/*/attempts/*/duration",
			wantWhy:  "below the declared minimum",
		},
		{
			name:    "a duration that is not a duration",
			fixture: "validation/failed",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/checks/*/attempts/*/duration", func(value reflect.Value) { value.SetString("a while") })
			},
			wantPath: "body/checks/*/attempts/*/duration",
			wantWhy:  "not a Go duration",
		},
		{
			name:    "a blob path outside its declared prefix",
			fixture: "repository-change/patch",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/payload/path", func(value reflect.Value) { value.SetString("artifacts/change.patch") })
			},
			wantPath: "body/payload/path",
			wantWhy:  "beneath content/",
		},
		{
			name:    "a required blob leaf left absent",
			fixture: "repository-change/patch",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/payload/media_type", func(value reflect.Value) { value.SetString("") })
			},
			wantPath: "body/payload/media_type",
			wantWhy:  "is required",
		},
		{
			name:    "an entity set with two elements sharing one id",
			fixture: "measurements/measured",
			mutate: func(body reflect.Value) {
				setLocated(body, "body/metrics", func(value reflect.Value) {
					source, _ := structFieldByJSONFieldName(value.Index(0), "id")
					destination, _ := structFieldByJSONFieldName(value.Index(1), "id")
					destination.Set(source)
				})
			},
			wantPath: "body/metrics/*/id",
			wantWhy:  "duplicate",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := fixtureNamed(t, testCase.fixture)
			document := recordSchemaDocuments[fixture.ref]
			body := deepCopyBody(t, fixture.body)
			testCase.mutate(body)
			err := validateFixtureDeclared(document, fixture.ref, fixture.subjects, body.Interface())
			if err == nil {
				t.Fatalf("the declared schema accepted %s", testCase.name)
			}
			if !strings.HasPrefix(err.Error(), testCase.wantPath+":") {
				t.Errorf("error does not lead with the field path %q in the frozen grammar\n  got: %v", testCase.wantPath, err)
			}
			if !strings.Contains(err.Error(), testCase.wantWhy) {
				t.Errorf("error does not say why\n  got:  %v\n  want it to mention %q", err, testCase.wantWhy)
			}
		})
	}
}

// The subject set is the one envelope constraint a document declares. These are the
// three ways it can be wrong, and the fourth — `ports` — is deliberately absent,
// because where the candidate-port set comes from cannot be a schema rule.
func TestGenericCoreValidatorEnforcesTheDeclaredSubjectShape(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		fixture  string
		subjects []Subject
		want     string
	}{
		{
			name:     "no subjects where the shape requires one",
			fixture:  "review/accept-with-no-findings",
			subjects: nil,
			want:     "requires at least 1 subject",
		},
		{
			name:    "a role the shape does not allow",
			fixture: "review/accept-with-no-findings",
			subjects: []Subject{{
				ID: "candidate", Role: SubjectRoleCandidate, Input: "input-candidate",
				Type: "repository/v1", Digest: fixtureDigest('a'),
			}},
			want: `does not allow the "candidate" role`,
		},
		{
			name:    "two primaries where the shape allows one",
			fixture: "review/accept-with-no-findings",
			subjects: append(fixtureSubjects(SubjectRolePrimary, "primary"), Subject{
				ID: "second", Role: SubjectRolePrimary, Input: "input-second",
				Type: "repository/v1", Digest: fixtureDigest('b'),
			}),
			want: `allows at most 1 subject(s) with the "primary" role`,
		},
		{
			name:    "a subject of the wrong pinned type",
			fixture: "repository-change/patch",
			subjects: []Subject{{
				ID: "base", Role: SubjectRoleBase, Input: "input-base",
				Type: "opaque/v1", Digest: fixtureDigest('a'),
			}},
			want: `requires every subject to be "repository/v1"`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := fixtureNamed(t, testCase.fixture)
			document := recordSchemaDocuments[fixture.ref]
			err := document.validateDecodedRecord(testCase.subjects, fixture.body)
			if err == nil {
				t.Fatalf("the declared schema accepted %s", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %v, want it to mention %q", err, testCase.want)
			}
			if !strings.HasPrefix(err.Error(), "subjects") {
				t.Errorf("a subject-shape error must name the subjects field: %v", err)
			}
		})
	}
}

// measurements/v1 constrains its subjects not at all, and the declaration says so
// faithfully rather than tidily. A record with no subjects and no evidence is
// valid; one with no subjects and any evidence is not, and that anchor rule is the
// only thing tying this type's subject set to its body.
func TestMeasurementsSubjectToleranceIsDeclaredFaithfully(t *testing.T) {
	document := recordSchemaDocuments[measurementsType]
	withoutSubjects := fixtureNamed(t, "measurements/not-applicable")
	if err := document.validateDecodedRecord(nil, withoutSubjects.body); err != nil {
		t.Errorf("a measurements record with no subjects and no evidence must be valid: %v", err)
	}
	if err := withoutSubjects.validate(nil, withoutSubjects.body); err != nil {
		t.Errorf("the Go validator disagrees, so the declaration is not faithful: %v", err)
	}
	withEvidence := fixtureNamed(t, "measurements/measured")
	if err := document.validateDecodedRecord(nil, withEvidence.body); err == nil {
		t.Error("a measurements record with evidence and no subjects must be rejected")
	}
}

// Declared int and duration bounds are INCLUSIVE, and the one live duration bound
// sits exactly on its boundary: validation/v1 declares "minimum": "0s" and the Go
// validator accepts a zero duration (validation.go rejects only `duration < 0`).
// An exclusive reading would make the declared schema stricter than the validator
// it claims to describe — the over-declaration that retroactively corrupts stored
// records — so the boundary value is tested rather than assumed, in both
// descriptions and for both kinds.
func TestDeclaredIntAndDurationBoundsAreInclusiveAtTheBoundary(t *testing.T) {
	duration := fixtureNamed(t, "validation/failed")
	document := recordSchemaDocuments[duration.ref]
	if declared := document.resolved["body/checks/*/attempts/*/duration"]; string(declared.Minimum) != `"0s"` {
		t.Fatalf("the duration minimum is %s, so this test no longer sits on the boundary", declared.Minimum)
	}
	body := deepCopyBody(t, duration.body)
	setLocated(body, "body/checks/*/attempts/*/duration", func(value reflect.Value) { value.SetString("0s") })
	if err := validateFixtureDeclared(document, duration.ref, duration.subjects, body.Interface()); err != nil {
		t.Errorf("a duration exactly at the declared minimum must be accepted; the bound is inclusive: %v", err)
	}
	if err := duration.validate(duration.subjects, body.Interface()); err != nil {
		t.Errorf("the Go validator accepts a zero duration, so an exclusive declared bound would over-declare: %v", err)
	}

	rank := fixtureNamed(t, "diagnosis/identified")
	rankDocument := recordSchemaDocuments[rank.ref]
	if declared := rankDocument.resolved["body/hypotheses/*/rank"]; string(declared.Minimum) != "1" {
		t.Fatalf("the rank minimum is %s, so this test no longer sits on the boundary", declared.Minimum)
	}
	rankBody := deepCopyBody(t, rank.body)
	setLocated(rankBody, "body/hypotheses/*/rank", func(value reflect.Value) { value.SetInt(1) })
	if err := rankDocument.validateDecodedRecord(rank.subjects, rankBody.Interface()); err != nil {
		t.Errorf("an integer exactly at the declared minimum must be accepted; the bound is inclusive: %v", err)
	}
}

// The descriptor helper the coordinated bump will use has to agree with the
// document's own descriptor, or the bump would pin bytes nobody reviewed.
func TestMustCanonicalSchemaDescriptorMatchesTheDocument(t *testing.T) {
	for ref, document := range recordSchemaDocuments {
		descriptor, err := document.Descriptor()
		if err != nil {
			t.Fatalf("Descriptor(%q): %v", ref, err)
		}
		if mustCanonicalSchemaDescriptor(document) != descriptor {
			t.Errorf("%q: mustCanonicalSchemaDescriptor disagrees with Descriptor", ref)
		}
		if !strings.HasPrefix(descriptor, canonicalJSONAlgorithm) {
			t.Errorf("%q descriptor is not the framed serialization", ref)
		}
	}
}

// SchemaDocumentFor is the read surface, and it must not hand out a reference into
// the loaded table: a caller mutating what it was given would silently change the
// contract for every later validation in the process.
func TestSchemaDocumentForHandsOutACopy(t *testing.T) {
	document, found := SchemaDocumentFor(snapshot.TypeRef("review/v1"))
	if !found {
		t.Fatal("SchemaDocumentFor(review/v1) found nothing")
	}
	document.Fields["body/summary"] = FieldDeclaration{Kind: KindObject, Presence: PresenceForbidden}
	delete(document.SubjectShape.Roles, SubjectRolePrimary)
	again, _ := SchemaDocumentFor(snapshot.TypeRef("review/v1"))
	if again.Fields["body/summary"].Kind != KindMarkdown {
		t.Error("mutating the returned document changed the loaded schema")
	}
	if _, found := again.SubjectShape.Roles[SubjectRolePrimary]; !found {
		t.Error("mutating the returned subject shape changed the loaded schema")
	}
}

func fixtureNamed(t *testing.T, name string) recordFixture {
	t.Helper()
	for _, fixture := range recordFixtures() {
		if fixture.name == name {
			return fixture
		}
	}
	t.Fatalf("no fixture named %q", name)
	return recordFixture{}
}

func setLocated(body reflect.Value, path string, apply func(reflect.Value)) {
	values := locateDeclaredValues(body, path)
	if len(values) == 0 {
		panic("no value at " + path)
	}
	apply(values[0])
}
