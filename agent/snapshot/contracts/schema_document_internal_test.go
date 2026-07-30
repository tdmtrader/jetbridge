package contracts

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// These tests live inside the package because a schema document's whole job is to
// describe the Go types and validators next to it. Both sides have to be
// enumerated mechanically: a hand-written list of expected field paths would be a
// second copy of the thing under test and would go stale in the same commit that
// made it wrong.

// schemaDocumentFileNames lists the embedded documents straight off the embedded
// filesystem, so a document added without a loader entry — or a loader entry
// without a document — is a failing test rather than a silent omission.
func schemaDocumentFileNames(t *testing.T) []string {
	t.Helper()
	entries, err := schemaDocumentSources.ReadDir(schemaDocumentDirectory)
	if err != nil {
		t.Fatalf("read embedded schema documents: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("embedded schema directory contains a subdirectory %q", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no schema documents are embedded")
	}
	return names
}

func readSchemaDocumentFile(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := schemaDocumentSources.ReadFile(schemaDocumentDirectory + "/" + name)
	if err != nil {
		t.Fatalf("read embedded schema document %q: %v", name, err)
	}
	return contents
}

func TestEveryEmbeddedDocumentLoadsForExactlyOneRecordType(t *testing.T) {
	names := schemaDocumentFileNames(t)
	if len(recordSchemaDocumentRevisions) != len(names) {
		t.Fatalf("loaded %d schema document revisions from %d embedded files", len(recordSchemaDocumentRevisions), len(names))
	}
	for ref, document := range recordSchemaDocuments {
		if !IsRecordType(ref) {
			t.Errorf("schema document %q names no record type", ref)
		}
		if document.Contract != ref {
			t.Errorf("schema document filed under %q declares contract %q", ref, document.Contract)
		}
		if document.Envelope != recordEnvelopeName {
			t.Errorf("%q declares envelope %q, want %q", ref, document.Envelope, recordEnvelopeName)
		}
	}
	for ref := range recordSchemaHistories {
		if _, found := recordSchemaDocuments[ref]; !found {
			t.Errorf("record type %q has no schema document", ref)
		}
	}
	for key, document := range recordSchemaDocumentRevisions {
		if key.ref != document.Contract || key.revision != document.Revision {
			t.Errorf("revision index %q/%d does not match %q/%d", key.ref, key.revision, document.Contract, document.Revision)
		}
	}
}

// The filename carries the revision, so a revision is a new file and a superseded
// file is never edited. If the two ever disagree the history becomes unreadable,
// which is why the redundancy is checked rather than trusted.
func TestSchemaDocumentFileNameAgreesWithItsHeader(t *testing.T) {
	for ref, document := range recordSchemaDocuments {
		want := fmt.Sprintf("%s.rev%d.json", strings.Replace(ref.String(), "/", ".", 1), document.Revision)
		if document.fileName != want {
			t.Errorf("%q revision %d is in file %q, want %q", ref, document.Revision, document.fileName, want)
		}
	}
}

// Revision 1 is the pre-dialect one-line stamp and stays immutable and unparsed
// forever, so a document is at least revision 2. It is either ADOPTED — its
// revision is the type's current revision — or STAGED at exactly one past
// current, which is the state between this task and the coordinated bump.
func TestSchemaDocumentRevisionLinksToTheDigestHistory(t *testing.T) {
	for ref, document := range recordSchemaDocuments {
		current, found := CurrentSchemaRevisionFor(ref)
		if !found {
			t.Fatalf("%q has no schema revision history", ref)
		}
		if document.Revision < 2 {
			t.Errorf("%q document revision is %d; revision 1 is the immutable one-line stamp", ref, document.Revision)
		}
		if document.Supersedes != document.Revision-1 {
			t.Errorf("%q revision %d supersedes %d, want %d", ref, document.Revision, document.Supersedes, document.Revision-1)
		}
		if document.Revision != current && document.Revision != current+1 {
			t.Errorf("%q document revision %d is neither the current revision %d nor the next one", ref, document.Revision, current)
		}
	}
}

// This is the test that has to fail when someone adds a body field and forgets
// the declaration, or declares a field the Go type does not have. Drift the
// parity gate cannot see is worse than drift it can.
func TestSchemaDocumentsDeclareEveryGoBodyFieldAndNoOthers(t *testing.T) {
	for ref, document := range recordSchemaDocuments {
		prototype, found := recordPrototypes[ref]
		if !found {
			t.Fatalf("%q has no record prototype", ref)
		}
		leaves, err := recordLeafFields(reflect.TypeOf(prototype))
		if err != nil {
			t.Fatalf("recordLeafFields(%q): %v", ref, err)
		}
		wantBody := make(map[string]struct{})
		for _, leaf := range leaves {
			if strings.HasPrefix(leaf.path, "body") {
				wantBody[leaf.path] = struct{}{}
			}
		}
		gotBody := make(map[string]struct{})
		for _, path := range document.impliedLeafPaths() {
			gotBody[path] = struct{}{}
		}
		for path := range wantBody {
			if _, found := gotBody[path]; !found {
				t.Errorf("%q declares no schema field for Go field %q", ref, path)
			}
		}
		for path := range gotBody {
			if _, found := wantBody[path]; !found {
				t.Errorf("%q declares schema field %q, which the Go type does not have", ref, path)
			}
		}
	}
}

// absent_image is the one place the Go representation shows through, so a wrong
// value silently changes what presence means and what the parity gate compares.
func TestSchemaDocumentAbsentImageMatchesGoPointerness(t *testing.T) {
	for ref, document := range recordSchemaDocuments {
		leaves, err := recordLeafFields(reflect.TypeOf(recordPrototypes[ref]))
		if err != nil {
			t.Fatalf("recordLeafFields(%q): %v", ref, err)
		}
		byPath := make(map[string]recordLeaf, len(leaves))
		for _, leaf := range leaves {
			byPath[leaf.path] = leaf
		}
		for path, declaration := range document.resolved {
			switch declaration.Kind {
			case KindInt, KindFloat, KindBool:
			default:
				continue
			}
			leaf, found := byPath[path]
			if !found {
				t.Errorf("%q declares %q, which is not a Go leaf", ref, path)
				continue
			}
			want := AbsentImageZero
			if leaf.pointer {
				want = AbsentImageNull
			}
			if declaration.AbsentImage != want {
				t.Errorf("%q field %q declares absent_image %q; the Go field is %s, so it must be %q",
					ref, path, declaration.AbsentImage, goRepresentation(leaf.pointer), want)
			}
		}
	}
}

func goRepresentation(pointer bool) string {
	if pointer {
		return "a pointer"
	}
	return "a value"
}

// The dialect version has to be in the FROZEN BYTES, not merely in a Go field:
// the composite-kind expansions are dialect-fixed subtrees that appear nowhere in
// a document, so without the marker inside the digest input the descriptor names a
// subtree a later reader cannot reconstruct. Checking the raw file rather than the
// parsed struct is the point — a marker the canonicalizer never sees would buy
// nothing.
func TestEverySchemaDocumentDeclaresTheDialectVersionInItsOwnBytes(t *testing.T) {
	for _, name := range schemaDocumentFileNames(t) {
		source := readSchemaDocumentFile(t, name)
		payload, err := canonicalJSONPayload(source)
		if err != nil {
			t.Fatalf("canonicalJSONPayload(%s): %v", name, err)
		}
		want := `"dialect":"` + recordSchemaDialectName + `"`
		if !strings.Contains(string(payload), want) {
			t.Errorf("%s canonical payload does not contain %s; the dialect version must be inside the descriptor bytes", name, want)
		}
	}
	for ref, document := range recordSchemaDocuments {
		if document.Dialect != recordSchemaDialectName {
			t.Errorf("%q declares dialect %q, want %q", ref, document.Dialect, recordSchemaDialectName)
		}
	}
}

// §5.3: `optional` never means unconstrained, and the go_rules back-reference is
// how a reader is stopped from concluding that it does. An UNPINNED score is the
// mechanically detectable case — its bounds and target are optional only because a
// sibling value decides them — so a score site that leaves either axis open must
// name the Go rule that closes it.
//
// Every score site in the tree is currently pinned on both axes (diagnosis/v1's
// confidence is unit-interval and higher-is-better), so the rule guards the next
// open one rather than an existing violation. The nonzero-site check keeps the
// walk itself honest: a test that examined no score at all would pass silently.
func TestAnUnpinnedScoreNamesTheGoRuleThatConstrainsIt(t *testing.T) {
	scoreSites := 0
	for ref, document := range recordSchemaDocuments {
		for _, path := range sortedFieldPaths(document.Fields) {
			declaration := document.Fields[path]
			if declaration.Kind != KindScore {
				continue
			}
			scoreSites++
			if declaration.Scale != schemaOpenValue && declaration.Direction != schemaOpenValue {
				continue
			}
			if len(declaration.GoRules) == 0 {
				t.Errorf("%q field %q leaves scale or direction open, so its bounds and target are conditionally "+
					"constrained; it must reference the go_only_rules entry that constrains them", ref, path)
			}
		}
	}
	if scoreSites == 0 {
		t.Fatal("no score site was examined at all, so this test is not checking what it claims")
	}
}

func TestSchemaDocumentGoRuleReferencesResolve(t *testing.T) {
	for ref, document := range recordSchemaDocuments {
		ids := make(map[string]struct{}, len(document.GoOnlyRules))
		for _, rule := range document.GoOnlyRules {
			if _, found := ids[rule.ID]; found {
				t.Errorf("%q declares go_only_rules id %q twice", ref, rule.ID)
			}
			ids[rule.ID] = struct{}{}
			if strings.TrimSpace(rule.Rule) == "" {
				t.Errorf("%q go_only_rules %q has no rule text", ref, rule.ID)
			}
		}
		for path, declaration := range document.Fields {
			for _, id := range declaration.GoRules {
				if _, found := ids[id]; !found {
					t.Errorf("%q field %q references go rule %q, which the document does not declare", ref, path, id)
				}
			}
		}
	}
}

// A document may not author `forbidden`: it arises only as a mechanical
// consequence of a pinning elsewhere in the same declaration, and the loader
// emits it.
func TestForbiddenPresenceIsDerivedAndNeverAuthored(t *testing.T) {
	for ref, document := range recordSchemaDocuments {
		for path, declaration := range document.Fields {
			if declaration.Presence == PresenceForbidden {
				t.Errorf("%q authors presence forbidden at %q; forbidden is derived from a pinning, never written", ref, path)
			}
		}
		forbidden := 0
		for _, declaration := range document.resolved {
			if declaration.Presence == PresenceForbidden {
				forbidden++
			}
		}
		if ref == snapshot.TypeRef("diagnosis/v1") && forbidden != 3 {
			t.Errorf("diagnosis/v1 derives %d forbidden leaves, want 3 (a pinned unit-interval higher-is-better score forbids minimum, maximum and target)", forbidden)
		}
	}
}

// The canonical serialization of every document is pinned, so a reformat that
// changes canonical output — and only that — fails loudly. A pure whitespace
// reformat must not.
// These are the digests the coordinated descriptor bump will move into
// recordSchemaHistories. They are pinned here first, deliberately: a document edit
// that changes canonical output has to be a visible, intentional two-line change
// rather than a silent re-digest.
func TestSchemaDocumentCanonicalSerializationIsStable(t *testing.T) {
	golden := map[string]string{
		"diagnosis.v1.rev2.json":             "sha256:47301b1acc54725bd94804e6a20ca284b85568e4336d5592c64a89bcd2f58c47",
		"measurements.v1.rev2.json":          "sha256:d2e0b89126ce534c957a8e93166391f517e126aec5aa961f66a4c6c178bc57a0",
		"repository-change.v1.rev2.json":     "sha256:afdb59e4eb682a09f86fb92165c57d3df215487be5a55e316944eba8bdc1f013",
		"review.v1.rev2.json":                "sha256:8b460c4d9ea3a6ca6c7d1b8fb1e8dce448df8a2745f3d81a52992cec8e760220",
		"validation.v1.rev2.json":            "sha256:68811d591b6f1f9cac7f2c27f36d96282717298c2420e3e16f521e5cd7351821",
		"validation.v1.rev3.json":            "sha256:4d03fc7af7796a7add4d72d08a286c64ecfa7e9486fb06f136600aacc3f1b0e7",
		"pull-request.v1.rev2.json":          "sha256:0d2ecf6dc021678fb2fffa8f82d9cd9355165521f0a05f2faeed6024d661b737",
		"pull-request-response.v1.rev2.json": "sha256:9ecb1a7bb3269ae9d98a38d54c94011e1cb1e4975b4f23476a53b239fe66bc0e",
		"publish-impact.v1.rev2.json":        "sha256:21952c646a7c81107f3eaad385a9467a9548e3d03492a87942989bedff00f045",
		"pull-request.v1.rev3.json":          "sha256:3cbba0207c612715c8050c69357ac727fc6b9b2f68b3083736d846b7d6c668f4",
		"pull-request-response.v1.rev3.json": "sha256:d60fcaa7aa054b5eaac43e96ef5d6386a69221da9d5346de3b675313229c8538",
		"pull-request-response.v1.rev4.json": "sha256:fe1c95e54437bf948ce79117f8735dd37e549716a07faa55ca249c2e2b51217e",
		"publish-impact.v1.rev3.json":        "sha256:58b98f51d3372bb41e197ccac545d6a0d47ee1d2d551fdd79d3389049d1809b2",
	}
	if len(golden) != len(schemaDocumentFileNames(t)) {
		t.Fatalf("%d pinned digests for %d embedded documents", len(golden), len(schemaDocumentFileNames(t)))
	}
	digests := make(map[string]string, len(golden))
	for _, document := range recordSchemaDocumentRevisions {
		want, pinned := golden[document.fileName]
		if !pinned {
			t.Errorf("%s has no pinned canonical digest; every document's descriptor bytes must be pinned", document.fileName)
			continue
		}
		descriptor, err := document.Descriptor()
		if err != nil {
			t.Fatalf("Descriptor(%s): %v", document.fileName, err)
		}
		if !strings.HasPrefix(descriptor, canonicalJSONAlgorithm+"\n") {
			t.Errorf("%s descriptor is not framed; only the framed serialization is a descriptor", document.fileName)
		}
		got := schemaDescriptorDigest(descriptor).String()
		if got != want {
			t.Errorf("%s canonical descriptor digest = %s, want %s", document.fileName, got, want)
		}
		if owner, found := digests[got]; found {
			t.Errorf("%s and %s compute the same descriptor digest", owner, document.fileName)
		}
		digests[got] = document.fileName
		// A revision-1 stamp begins with '{' and a framed serialization with 's',
		// so the two forms are trivially distinguishable and coexist in one
		// history.
		//
		// This check was the NEGATIVE form until the coordinated bump landed: while
		// the documents were staged, a descriptor appearing in the history meant a
		// type had been bumped on its own. It is now the positive form, and it is
		// the assertion that the histories derive their current descriptor from
		// these exact bytes — a document edited after the bump would compute a
		// descriptor that is in no revision, and both halves of this test would
		// fail rather than silently re-digesting the contract.
		installed := false
		for _, accepted := range mustAcceptedDescriptors(t, document.Contract) {
			if accepted == descriptor {
				installed = true
			}
		}
		if !installed {
			t.Errorf("%s descriptor does not appear in %q's digest history; the current revision must derive from this document",
				document.fileName, document.Contract)
		}
	}
}

func mustAcceptedDescriptors(t *testing.T, ref snapshot.TypeRef) []string {
	t.Helper()
	history, found := recordSchemaHistories[ref]
	if !found {
		t.Fatalf("%q has no schema history", ref)
	}
	descriptors := []string{history.current.descriptor}
	for _, superseded := range history.superseded {
		descriptors = append(descriptors, superseded.descriptor)
	}
	return descriptors
}

// The synthetic document below is the base for the malformed-input table. It is
// deliberately not one of the six: a mutation table over a real document would
// break the completeness checks for unrelated reasons and hide what each case is
// actually about.
const wellFormedSchemaDocument = `{
  "dialect": "record-schema-dialect/1",
  "contract": "example/v1",
  "envelope": "record/v1",
  "revision": 2,
  "supersedes": 1,
  "description": "A minimal illustration.",
  "subject_shape": {
    "minimum": 1,
    "maximum": 1,
    "roles": { "primary": { "minimum": 1, "maximum": 1 } },
    "subject_type": null,
    "uniform_subject_type": false,
    "ports": "any-exposed-input"
  },
  "fields": {
    "body": { "kind": "object", "presence": "required" },
    "body/note": { "kind": "markdown", "presence": "required" },
    "body/rank": { "kind": "int", "presence": "required", "absent_image": "zero", "minimum": 1 },
    "body/findings": { "kind": "entity-set", "presence": "optional", "id_field": "id" },
    "body/findings/*/id": { "kind": "identifier", "presence": "required" },
    "body/findings/*/severity": {
      "kind": "enum",
      "presence": "required",
      "values": [{ "value": "low" }, { "value": "high" }],
      "go_rules": ["a-go-rule"]
    }
  },
  "go_only_rules": [{ "id": "a-go-rule", "rule": "Something the schema cannot say." }]
}`

const wellFormedSchemaDocumentFileName = "example.v1.rev2.json"

func TestParseSchemaDocumentAcceptsTheWellFormedBase(t *testing.T) {
	document, err := parseSchemaDocument(wellFormedSchemaDocumentFileName, []byte(wellFormedSchemaDocument))
	if err != nil {
		t.Fatalf("parseSchemaDocument(): %v", err)
	}
	if document.Contract.String() != "example/v1" {
		t.Errorf("contract = %q, want example/v1", document.Contract)
	}
	if got := len(document.Fields); got != 6 {
		t.Errorf("parsed %d field declarations, want 6", got)
	}
}

func TestParseSchemaDocumentRejectsMalformedDocuments(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		fileName string
		mutate   func(string) string
		want     string
	}{
		{
			name: "a missing top-level key",
			mutate: func(document string) string {
				return remove(t, document, `  "description": "A minimal illustration.",`)
			},
			want: "description",
		},
		{
			// The composite-kind expansions — anchor, score, blob — are dialect-fixed
			// subtrees that appear nowhere in the document's own bytes. Without a
			// dialect version inside those bytes, nothing frozen records which
			// expansion applied, so a document that omits it cannot be read back with
			// certainty and must be refused.
			name: "no dialect version at all",
			mutate: func(document string) string {
				return remove(t, document, `  "dialect": "record-schema-dialect/1",`)
			},
			want: "dialect",
		},
		{
			name: "a dialect version this loader does not implement",
			mutate: func(document string) string {
				return replace(t, document, `"record-schema-dialect/1"`, `"record-schema-dialect/2"`)
			},
			want: "record-schema-dialect/2",
		},
		{
			name:   "an unknown top-level key",
			mutate: func(document string) string { return replace(t, document, `"envelope":`, `"extra": 1, "envelope":`) },
			want:   "extra",
		},
		{
			name:   "revision 1, which is the immutable one-line stamp",
			mutate: func(document string) string { return replace(t, document, `"revision": 2`, `"revision": 1`) },
			want:   "revision",
		},
		{
			name:   "a supersedes that is not revision minus one",
			mutate: func(document string) string { return replace(t, document, `"supersedes": 1`, `"supersedes": 0`) },
			want:   "supersedes",
		},
		{
			name:   "another envelope",
			mutate: func(document string) string { return replace(t, document, `"record/v1"`, `"record/v2"`) },
			want:   "envelope",
		},
		{
			name:     "a file name that disagrees with the header",
			fileName: "example.v1.rev3.json",
			mutate:   func(document string) string { return document },
			want:     "file name",
		},
		{
			name: "an authored forbidden presence",
			mutate: func(document string) string {
				return replace(t, document, `"body/note": { "kind": "markdown", "presence": "required" }`, `"body/note": { "kind": "markdown", "presence": "forbidden" }`)
			},
			want: "forbidden",
		},
		{
			name:   "an unknown kind",
			mutate: func(document string) string { return replace(t, document, `"kind": "markdown"`, `"kind": "prose"`) },
			want:   "kind",
		},
		{
			name: "an enum with no values",
			mutate: func(document string) string {
				return remove(t, document, `      "values": [{ "value": "low" }, { "value": "high" }],`)
			},
			want: "values",
		},
		{
			name:   "a duplicate enum value",
			mutate: func(document string) string { return replace(t, document, `{ "value": "high" }`, `{ "value": "low" }`) },
			want:   "duplicate",
		},
		{
			name: "an int with no absent_image",
			mutate: func(document string) string {
				return replace(t, document, `"presence": "required", "absent_image": "zero", "minimum": 1`, `"presence": "required", "minimum": 1`)
			},
			want: "absent_image",
		},
		{
			name: "declarable bounds on a float, which are always a lie",
			mutate: func(document string) string {
				return replace(t, document, `"kind": "int", "presence": "required", "absent_image": "zero", "minimum": 1`, `"kind": "float", "presence": "required", "absent_image": "zero", "minimum": 1`)
			},
			want: "minimum",
		},
		{
			name: "an entity-set with no id_field",
			mutate: func(document string) string {
				return replace(t, document, `"kind": "entity-set", "presence": "optional", "id_field": "id"`, `"kind": "entity-set", "presence": "optional"`)
			},
			want: "id_field",
		},
		{
			name:   "an entity-set whose id_field is not a declared identifier leaf",
			mutate: func(document string) string { return replace(t, document, `"id_field": "id"`, `"id_field": "key"`) },
			want:   "key",
		},
		{
			name: "an array with no element kind",
			mutate: func(document string) string {
				return replace(t, document, `"kind": "entity-set", "presence": "optional", "id_field": "id"`, `"kind": "array", "presence": "optional"`)
			},
			want: "element",
		},
		{
			name:   "a declaration path with no declared parent",
			mutate: func(document string) string { return replace(t, document, `"body/note":`, `"body/deep/note":`) },
			want:   "body/deep",
		},
		{
			name:   "a wildcard child of something that is not an element set",
			mutate: func(document string) string { return replace(t, document, `"body/note":`, `"body/note/*/x":`) },
			want:   "body/note",
		},
		{
			name:   "a path outside the frozen grammar",
			mutate: func(document string) string { return replace(t, document, `"body/note":`, `"body/a note":`) },
			want:   "field path",
		},
		{
			name:   "a declaration outside the body subtree",
			mutate: func(document string) string { return replace(t, document, `"body/note":`, `"subjects/*/id":`) },
			want:   "body",
		},
		{
			name: "a go_rules reference the document does not declare",
			mutate: func(document string) string {
				return replace(t, document, `"go_rules": ["a-go-rule"]`, `"go_rules": ["another-go-rule"]`)
			},
			want: "another-go-rule",
		},
		{
			name: "a duplicate go_only_rules id",
			mutate: func(document string) string {
				return replace(t, document, `[{ "id": "a-go-rule", "rule": "Something the schema cannot say." }]`, `[{ "id": "a-go-rule", "rule": "One." }, { "id": "a-go-rule", "rule": "Two." }]`)
			},
			want: "duplicate",
		},
		{
			name: "an epistemic attribute",
			mutate: func(document string) string {
				return replace(t, document, `"body/note": { "kind": "markdown", "presence": "required" }`, `"body/note": { "kind": "markdown", "presence": "required", "epistemic": "asserted" }`)
			},
			want: "epistemic",
		},
		{
			name: "no body declaration at all",
			mutate: func(document string) string {
				return remove(t, document, `    "body": { "kind": "object", "presence": "required" },`)
			},
			want: "body",
		},
		{
			name:   "a subject_shape with a missing key",
			mutate: func(document string) string { return remove(t, document, `    "uniform_subject_type": false,`) },
			want:   "uniform_subject_type",
		},
		{
			name: "a subject role outside the closed vocabulary",
			mutate: func(document string) string {
				return replace(t, document, `"primary": { "minimum": 1, "maximum": 1 }`, `"author": { "minimum": 1, "maximum": 1 }`)
			},
			want: "author",
		},
		{
			name: "a subject maximum below its minimum",
			mutate: func(document string) string {
				return replace(t, document, `"minimum": 1,
    "maximum": 1,`, `"minimum": 2,
    "maximum": 1,`)
			},
			want: "maximum",
		},
		{
			name: "an unknown ports rule",
			mutate: func(document string) string {
				return replace(t, document, `"any-exposed-input"`, `"whatever-is-exposed"`)
			},
			want: "ports",
		},
		{
			name:   "a blank description",
			mutate: func(document string) string { return replace(t, document, `"A minimal illustration."`, `"   "`) },
			want:   "description",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fileName := testCase.fileName
			if fileName == "" {
				fileName = wellFormedSchemaDocumentFileName
			}
			document, err := parseSchemaDocument(fileName, []byte(testCase.mutate(wellFormedSchemaDocument)))
			if err == nil {
				t.Fatalf("parseSchemaDocument() accepted %s: %+v", testCase.name, document)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("parseSchemaDocument() error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func replace(t *testing.T, document, old, new string) string {
	t.Helper()
	if !strings.Contains(document, old) {
		t.Fatalf("the base document does not contain %q", old)
	}
	return strings.Replace(document, old, new, 1)
}

func remove(t *testing.T, document, line string) string {
	t.Helper()
	return replace(t, document, line+"\n", "")
}

// TestBodyFieldPathsAreMatchedBySegmentAndNotByPrefix pins the classification
// that splits a record's leaves into envelope and body.
//
// The envelope and the body share ONE path namespace, and the frozen grammar
// separates segments with "/". A string-prefix test therefore puts a future
// envelope field named `bodyX` on the body side, where the completeness check
// demands a declaration for it and panics the package at initialisation. That is
// a defect nobody can see today, because no such field exists yet — which is
// exactly why the rule is pinned here rather than left to the day it is written.
func TestBodyFieldPathsAreMatchedBySegmentAndNotByPrefix(t *testing.T) {
	for path, want := range map[string]bool{
		"body":               true,
		"body/summary":       true,
		"body/findings/*/id": true,
		"bodyX":              false,
		"bodyX/summary":      false,
		"body_version":       false,
		"bodies":             false,
		"record_version":     false,
		"type":               false,
		"subjects/*/id":      false,
		"embodiment":         false,
	} {
		if got := isBodyFieldPath(path); got != want {
			t.Errorf("isBodyFieldPath(%q) = %t, want %t", path, got, want)
		}
	}
}
