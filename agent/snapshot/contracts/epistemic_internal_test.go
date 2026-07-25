package contracts

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// These tests live inside the package because completeness is a property of the
// declaration table against the Go types it describes. Both sides have to be
// enumerated mechanically — a hand-written list of expected field paths would
// simply be a second copy of the thing under test, and would go stale in
// exactly the same commit.

func TestEveryRecordTypeDeclaresEpistemicStatusForEveryRevision(t *testing.T) {
	want := make(map[epistemicKey]struct{})
	for ref, history := range recordSchemaHistories {
		for revision := 1; revision <= history.current.revision; revision++ {
			want[epistemicKey{ref: ref, revision: revision}] = struct{}{}
		}
	}
	for key := range want {
		if _, found := epistemicFieldStatuses[key]; !found {
			t.Errorf("%q revision %d has no epistemic declaration; every (type, revision) needs one",
				key.ref, key.revision)
		}
	}
	for key := range epistemicFieldStatuses {
		if _, found := want[key]; !found {
			t.Errorf("epistemic declaration for %q revision %d names no declared schema revision",
				key.ref, key.revision)
		}
	}
}

func TestEveryRecordTypeHasAnEpistemicPrototype(t *testing.T) {
	for ref := range recordSchemaHistories {
		if _, found := recordEpistemicPrototypes[ref]; !found {
			t.Errorf("%q has no record prototype, so its fields cannot be enumerated", ref)
		}
	}
	for ref := range recordEpistemicPrototypes {
		if _, found := recordSchemaHistories[ref]; !found {
			t.Errorf("record prototype %q is not a record type", ref)
		}
	}
}

// This is the test that has to fail loudly when someone adds a body field and
// forgets the declaration: it compares the declared paths against the fields
// the Go record type actually has, in both directions.
func TestEpistemicDeclarationsCoverEveryFieldExactlyOnce(t *testing.T) {
	for ref, prototype := range recordEpistemicPrototypes {
		actual, err := recordLeafFieldPaths(reflect.TypeOf(prototype))
		if err != nil {
			t.Fatalf("recordLeafFieldPaths(%q): %v", ref, err)
		}
		if len(actual) == 0 {
			t.Fatalf("recordLeafFieldPaths(%q) enumerated no fields", ref)
		}
		fields := make(map[string]struct{}, len(actual))
		for _, path := range actual {
			if _, found := fields[path]; found {
				t.Fatalf("recordLeafFieldPaths(%q) produced %q twice", ref, path)
			}
			fields[path] = struct{}{}
		}
		for revision := 1; revision <= recordSchemaHistories[ref].current.revision; revision++ {
			key := epistemicKey{ref: ref, revision: revision}
			declared := epistemicFieldStatuses[key]
			var missing, unknown []string
			for path := range fields {
				if _, found := declared[path]; !found {
					missing = append(missing, path)
				}
			}
			for path := range declared {
				if _, found := fields[path]; !found {
					unknown = append(unknown, path)
				}
			}
			sort.Strings(missing)
			sort.Strings(unknown)
			if len(missing) > 0 {
				t.Errorf("%q revision %d has no epistemic status for %d field(s):\n\t%s",
					ref, revision, len(missing), strings.Join(missing, "\n\t"))
			}
			if len(unknown) > 0 {
				t.Errorf("%q revision %d declares epistemic status for %d field(s) that do not exist:\n\t%s",
					ref, revision, len(unknown), strings.Join(unknown, "\n\t"))
			}
		}
	}
}

func TestEveryDeclaredPathAndStatusIsWellFormed(t *testing.T) {
	for key, fields := range epistemicFieldStatuses {
		for path, status := range fields {
			if err := ValidateFieldDeclarationPath(path); err != nil {
				t.Errorf("%q revision %d declares %q: %v", key.ref, key.revision, path, err)
			}
			if err := status.Validate(); err != nil {
				t.Errorf("%q revision %d declares %q as %q: %v", key.ref, key.revision, path, status, err)
			}
		}
	}
}

// "Exactly one entry" has a second failure mode the coverage test above cannot
// see: two parts of one declaration claiming the same path. Without a guard the
// later literal silently wins, so a field would have a status nobody chose.
func TestMergingEpistemicFieldsRefusesADuplicatePath(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("mergeEpistemicFields() accepted the same field path twice")
		}
		message, ok := recovered.(string)
		if !ok || !strings.Contains(message, "declared twice") {
			t.Fatalf("recovered %v, want a message naming the duplicate declaration", recovered)
		}
	}()
	mergeEpistemicFields(
		map[string]EpistemicStatus{"body/summary": EpistemicAsserted},
		map[string]EpistemicStatus{"body/summary": EpistemicDerived},
	)
}

type enumeratedNested struct {
	ID    string   `json:"id"`
	Names []string `json:"names"`
	Score *float64 `json:"score,omitempty"`
}

type enumeratedProbe struct {
	Name     string             `json:"name"`
	Ref      snapshot.TypeRef   `json:"ref"`
	Nested   enumeratedNested   `json:"nested"`
	Repeated []enumeratedNested `json:"repeated"`
	Skipped  string             `json:"-"`
	Hidden   string
}

func TestRecordLeafFieldPathsEnumeratesValueBearingFields(t *testing.T) {
	paths, err := recordLeafFieldPaths(reflect.TypeOf(enumeratedProbe{}))
	if err != nil {
		t.Fatalf("recordLeafFieldPaths(): %v", err)
	}
	want := []string{
		"Hidden",
		"name",
		"nested/id",
		"nested/names",
		"nested/score",
		"ref",
		"repeated/*/id",
		"repeated/*/names",
		"repeated/*/score",
	}
	sort.Strings(paths)
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("recordLeafFieldPaths() = %v, want %v", paths, want)
	}
}

func TestRecordLeafFieldPathsRejectsShapesTheGrammarCannotAddress(t *testing.T) {
	type unaddressable struct {
		Bag map[string]string `json:"bag"`
	}
	if _, err := recordLeafFieldPaths(reflect.TypeOf(unaddressable{})); err == nil {
		t.Fatal("recordLeafFieldPaths() accepted a map, which the frozen field-path grammar cannot address")
	}
}
