package workflowtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
	"github.com/concourse/concourse/agent/workflowrun"
)

func functionManifest(name string, signatureVersion int, inputs []string, outputType, prompt string) workflow.Manifest {
	inputYAML := ""
	inputNames := ""
	inputTypes := ""
	for _, input := range inputs {
		inputYAML += fmt.Sprintf("  - name: %s\n    type: repository/v1\n", input)
		if inputNames != "" {
			inputNames += ", "
		}
		inputNames += input
		inputTypes += fmt.Sprintf("      %s:\n        type: repository/v1\n", input)
	}
	return workflow.Manifest{"workflow.yml": fmt.Sprintf(`schema_version: 3
name: %s
signature_version: %d
inputs:
%soutputs:
  - name: result
    type: %s
    from: result
plan:
  - agent: work
    function_id: work
    prompt: %s
    inputs: [%s]
    outputs: [result]
    input_types:
%s    output_types:
      result: %s
`, name, signatureVersion, inputYAML, outputType, prompt, inputNames, inputTypes, outputType)}
}

func defYAML(name, promptBody string) []byte {
	return []byte(`schema_version: 3
name: ` + name + `
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: ` + promptBody + `
`)
}

func TestMemoryStoreImportAssignsMonotonicVersions(t *testing.T) {
	s := workflowtest.NewMemoryStore()

	v1, err := s.Import("wf", defYAML("wf", "Do the work."), "alice")
	if err != nil {
		t.Fatalf("import v1: %v", err)
	}
	if v1.Version != 1 || v1.Name != "wf" || v1.CreatedBy != "alice" {
		t.Errorf("v1 = %+v", v1)
	}
	if v1.ContentHash != (workflow.Manifest{"workflow.yml": string(defYAML("wf", "Do the work."))}).Hash() {
		t.Errorf("hash mismatch: %s", v1.ContentHash)
	}

	v2, err := s.Import("wf", defYAML("wf", "Do the work carefully."), "bob")
	if err != nil {
		t.Fatalf("import v2: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("v2.Version = %d, want 2", v2.Version)
	}
}

func TestMemoryStoreImportIsIdempotentOnHash(t *testing.T) {
	s := workflowtest.NewMemoryStore()
	raw := defYAML("wf", "Do the work.")

	v1, _ := s.Import("wf", raw, "alice")
	again, err := s.Import("wf", raw, "bob")
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if again.Version != v1.Version || again.CreatedBy != "alice" {
		t.Errorf("re-import must return the existing version untouched, got %+v", again)
	}
	page, _ := s.Versions(context.Background(), "wf", workflow.VersionPageRequest{Limit: workflow.MaxVersionPageSize})
	if len(page.Definitions) != 1 {
		t.Errorf("expected 1 stored version, got %d", len(page.Definitions))
	}
}

func TestMemoryStoreImportManifestWithOutcomeIsAtomic(t *testing.T) {
	store := workflowtest.NewMemoryStore()
	manifest := functionManifest("atomic-outcome", 1, nil, "review/v1", "review")

	first, err := store.ImportManifestWithOutcome("atomic-outcome", manifest, "alice")
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if !first.Inserted || first.Definition == nil || first.Definition.CreatedBy != "alice" {
		t.Fatalf("first outcome = %+v, want inserted definition created by alice", first)
	}
	repeated, err := store.ImportManifestWithOutcome("atomic-outcome", manifest, "mallory")
	if err != nil {
		t.Fatalf("repeated import: %v", err)
	}
	if repeated.Inserted || repeated.Definition == nil ||
		repeated.Definition.ID != first.Definition.ID ||
		repeated.Definition.CreatedBy != "alice" {
		t.Fatalf("repeated outcome = %+v, want untouched idempotent hit", repeated)
	}
	repeated.Definition.CreatedBy = "mutated"
	stored, found, err := store.Get("atomic-outcome", first.Definition.Version)
	if err != nil || !found || stored.CreatedBy != "alice" {
		t.Fatalf("returned outcome mutated store authority: stored=%+v found=%v err=%v", stored, found, err)
	}

	concurrent := workflowtest.NewMemoryStore()
	const callers = 16
	start := make(chan struct{})
	outcomes := make(chan workflow.ImportOutcome, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			outcome, err := concurrent.ImportManifestWithOutcome(
				"atomic-outcome", manifest, fmt.Sprintf("caller-%d", index),
			)
			outcomes <- outcome
			errs <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(outcomes)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent import: %v", err)
		}
	}
	inserted := 0
	definitionID := 0
	createdBy := ""
	for outcome := range outcomes {
		if outcome.Definition == nil {
			t.Fatal("concurrent import returned nil definition")
		}
		if definitionID == 0 {
			definitionID = outcome.Definition.ID
			createdBy = outcome.Definition.CreatedBy
		}
		if outcome.Definition.ID != definitionID || outcome.Definition.Version != 1 ||
			outcome.Definition.CreatedBy != createdBy {
			t.Fatalf("concurrent outcome drifted: %+v, want id=%d version=1 creator=%q", outcome, definitionID, createdBy)
		}
		if outcome.Inserted {
			inserted++
		}
	}
	if inserted != 1 {
		t.Fatalf("inserted outcomes = %d, want exactly 1", inserted)
	}
}

func TestNodeAwareMemoryStoreRecompilesContentClonesWithExactResolver(t *testing.T) {
	nodes := workflowtest.NewMemoryNodeStore()
	node, err := nodes.ImportManifest("review", workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: review
inputs: []
outputs: []
step: {agent: review, prompt: review}
`}, "alice")
	if err != nil {
		t.Fatalf("import node: %v", err)
	}
	if _, err := nodes.Release(node.Name, node.Version, workflow.ReleaseCompatible, "alice"); err != nil {
		t.Fatalf("release node: %v", err)
	}
	store := workflowtest.NewMemoryStoreWithNodeResolver(
		nodes,
		workflowrun.WorkflowTargetRenderer{
			RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	)
	manifest := workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: node-consumer
signature_version: 1
inputs: []
outputs: []
plan:
  - node: review-change
    uses: review@1
    input_mapping: {}
    output_mapping: {}
`}

	outcome, err := store.ImportManifestWithOutcome("node-consumer", manifest, "alice")
	if err != nil {
		t.Fatalf("node-aware import: %v", err)
	}
	if !outcome.Inserted || outcome.Definition == nil || len(outcome.Definition.Compiled.Function.Plan) != 1 {
		t.Fatalf("node-aware import outcome = %+v", outcome)
	}
	got, found, err := store.Get("node-consumer", outcome.Definition.Version)
	if err != nil || !found || got.SourceManifest[workflow.WorkflowFileName] != manifest[workflow.WorkflowFileName] {
		t.Fatalf("Get node consumer: found=%v err=%v definition=%+v", found, err, got)
	}
	latest, found, err := store.Latest("node-consumer")
	if err != nil || !found || latest.ContentHash != manifest.Hash() {
		t.Fatalf("Latest node consumer: found=%v err=%v definition=%+v", found, err, latest)
	}
	if _, err := store.Promote("node-consumer", outcome.Definition.Version, "alice"); err != nil {
		t.Fatalf("Promote node consumer: %v", err)
	}
	live, found, err := store.Live("node-consumer")
	if err != nil || !found || live.Version != outcome.Definition.Version ||
		live.SourceManifest[workflow.WorkflowFileName] != manifest[workflow.WorkflowFileName] {
		t.Fatalf("Live node consumer: found=%v err=%v definition=%+v", found, err, live)
	}
}

func TestDefinitionJSONOmitsLegacyCompatibilityFields(t *testing.T) {
	store := workflowtest.NewMemoryStore()
	definition, err := store.ImportManifest(
		"wire-shape",
		functionManifest("wire-shape", 1, nil, "review/v1", "review"),
		"alice",
	)
	if err != nil {
		t.Fatalf("import v3: %v", err)
	}

	payload, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("decode definition wire shape: %v", err)
	}
	if _, found := wire["config"]; found {
		t.Fatalf("definition wire shape retains config: %s", payload)
	}
	compiled, ok := wire["compiled"].(map[string]any)
	if !ok {
		t.Fatalf("compiled = %T, want object", wire["compiled"])
	}
	if _, found := compiled["legacy"]; found {
		t.Fatalf("compiled wire shape retains legacy: %s", payload)
	}
	if function, found := compiled["function"]; !found || function == nil {
		t.Fatalf("compiled function missing: %s", payload)
	}
}

func TestMemoryStoreImportRejectsNameMismatchAndInvalid(t *testing.T) {
	s := workflowtest.NewMemoryStore()

	_, err := s.Import("other-name", defYAML("wf", "Do the work."), "alice")
	var inv workflow.InvalidDefinitionError
	if !errors.As(err, &inv) || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("name mismatch must be InvalidDefinitionError, got %v", err)
	}

	_, err = s.Import("wf", []byte("schema_version: 3\nname: wf\nsignature_version: 1\ninputs: []\noutputs: []\nplan: []\n"), "alice")
	if !errors.As(err, &inv) {
		t.Errorf("validation failure must be InvalidDefinitionError, got %v", err)
	}
}

func TestMemoryStoreNonV3ImportRejectsBeforeLegacyValidation(t *testing.T) {
	store := workflowtest.NewMemoryStore()
	tests := []struct {
		name       string
		importName string
		runImport  func() (*workflow.Definition, error)
		version    int
	}{
		{
			name:       "raw v1 before route name mismatch",
			importName: "different-route",
			version:    1,
			runImport: func() (*workflow.Definition, error) {
				return store.Import("different-route", []byte(`schema_version: 1
name: legacy-route
steps: []
`), "alice")
			},
		},
		{
			name:       "manifest v2 before missing asset",
			importName: "legacy-assets",
			version:    2,
			runImport: func() (*workflow.Definition, error) {
				return store.ImportManifest("legacy-assets", workflow.Manifest{
					"workflow.yml": `schema_version: 2
name: legacy-assets
prompt_files:
  work: prompts/missing.md
steps:
  - agent: work
    prompt: work
    outputs: [workspace]
`,
				}, "alice")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition, err := test.runImport()
			if definition != nil {
				t.Fatalf("definition = %+v, want nil", definition)
			}
			var invalid workflow.InvalidDefinitionError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %T %v, want InvalidDefinitionError", err, err)
			}
			var unsupported workflow.UnsupportedSchemaVersionError
			if !errors.As(err, &unsupported) || unsupported.Got != test.version {
				t.Fatalf("error = %T %v, unsupported = %+v", err, err, unsupported)
			}
			want := fmt.Sprintf(
				"workflow: unsupported schema_version %d; only schema_version 3 is supported",
				test.version,
			)
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err, want)
			}
			page, pageErr := store.Versions(context.Background(), test.importName, workflow.VersionPageRequest{
				Limit: workflow.MaxVersionPageSize,
			})
			if pageErr != nil || page.Found || len(page.Definitions) != 0 {
				t.Fatalf("rejected import stored a version: page=%+v err=%v", page, pageErr)
			}
		})
	}

	_, err := store.Import("malformed-v3", []byte(`schema_version: 3
name: malformed-v3
signature_version: 1
inputs: []
outputs: []
plan: []
`), "alice")
	var invalid workflow.InvalidDefinitionError
	if !errors.As(err, &invalid) {
		t.Fatalf("malformed v3 error = %T %v, want InvalidDefinitionError", err, err)
	}
	var unsupported workflow.UnsupportedSchemaVersionError
	if errors.As(err, &unsupported) {
		t.Fatalf("malformed v3 error = %v, must not be unsupported", err)
	}
}

func TestMemoryStoreImportManifestValidationPrecedesSchemaInspection(t *testing.T) {
	store := workflowtest.NewMemoryStore()
	for name, manifest := range map[string]workflow.Manifest{
		"empty":            {},
		"missing-workflow": {"README.md": "source only"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.ImportManifest(name, manifest, "alice")
			var invalid workflow.InvalidDefinitionError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %T %v, want InvalidDefinitionError", err, err)
			}
			want := "workflow: manifest has no workflow.yaml (or legacy workflow.yml)"
			if name == "empty" {
				want = "workflow: manifest has no files"
			}
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err, want)
			}
		})
	}
}

func TestMemoryStoreImportManifestAcceptsWorkflowYAMLKey(t *testing.T) {
	store := workflowtest.NewMemoryStore()
	source := functionManifest("yaml-key", 1, nil, "review/v1", "review")["workflow.yml"]
	m := workflow.Manifest{workflow.WorkflowFileName: source}

	imported, err := store.ImportManifest("yaml-key", m, "alice")
	if err != nil {
		t.Fatalf("ImportManifest: %v", err)
	}
	if imported.RawYAML != source {
		t.Fatalf("RawYAML = %q, want %q", imported.RawYAML, source)
	}

	got, found, err := store.Get("yaml-key", imported.Version)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if got.SourceManifest[workflow.WorkflowFileName] != source {
		t.Fatalf("SourceManifest round-trip lost the %s key: %+v", workflow.WorkflowFileName, got.SourceManifest)
	}
}

func TestMemoryStoreGetLiveAndPromote(t *testing.T) {
	s := workflowtest.NewMemoryStore(workflowrun.WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	s.Import("wf", defYAML("wf", "One."), "alice")
	s.Import("wf", defYAML("wf", "Two."), "alice")

	if _, found, _ := s.Live("wf"); found {
		t.Error("nothing should be live before Promote")
	}

	if _, err := s.Promote("wf", 1, "alice"); err != nil {
		t.Fatalf("promote v1: %v", err)
	}
	live, found, _ := s.Live("wf")
	if !found || live.Version != 1 {
		t.Fatalf("live = %+v, found=%v", live, found)
	}
	if live.RawYAML != string(defYAML("wf", "One.")) {
		t.Error("Live must populate RawYAML")
	}

	// Promotion atomically swaps: v2 live, v1 not.
	if _, err := s.Promote("wf", 2, "bob"); err != nil {
		t.Fatalf("promote v2: %v", err)
	}
	live, _, _ = s.Live("wf")
	if live.Version != 2 {
		t.Errorf("live.Version = %d, want 2", live.Version)
	}
	v1, _, _ := s.Get("wf", 1)
	if v1.Live {
		t.Error("v1 must no longer be live")
	}

	if _, err := s.Promote("wf", 99, "alice"); !errors.Is(err, workflow.ErrVersionNotFound) {
		t.Errorf("unknown version must be ErrVersionNotFound, got %v", err)
	}

	if _, found, _ := s.Get("wf", 99); found {
		t.Error("Get unknown version must report found=false")
	}
}

func TestMemoryStoreListReturnsLatestPerName(t *testing.T) {
	s := workflowtest.NewMemoryStore()
	s.Import("aa", defYAML("aa", "A one."), "alice")
	s.Import("aa", defYAML("aa", "A two."), "alice")
	s.Import("bb", defYAML("bb", "B one."), "alice")

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	if list[0].Name != "aa" || list[0].Version != 2 {
		t.Errorf("list[0] = %+v", list[0])
	}
	if list[1].Name != "bb" || list[1].Version != 1 {
		t.Errorf("list[1] = %+v", list[1])
	}
}

func TestMemoryStoreLiveVersions(t *testing.T) {
	s := workflowtest.NewMemoryStore(workflowrun.WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	// wf-a: v1 promoted, then v2 imported (live stays at 1).
	if _, err := s.Import("wf-a", defYAML("wf-a", "One."), "alice"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := s.Promote("wf-a", 1, "alice"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := s.Import("wf-a", defYAML("wf-a", "Two."), "alice"); err != nil {
		t.Fatalf("import: %v", err)
	}
	// wf-b: never promoted.
	if _, err := s.Import("wf-b", defYAML("wf-b", "B."), "bob"); err != nil {
		t.Fatalf("import: %v", err)
	}

	live, err := s.LiveVersions()
	if err != nil {
		t.Fatalf("live versions: %v", err)
	}
	if got, want := live["wf-a"], 1; got != want {
		t.Errorf("wf-a live = %d, want %d", got, want)
	}
	if _, ok := live["wf-b"]; ok {
		t.Errorf("wf-b should have no live version, got %d", live["wf-b"])
	}
}

func TestMemoryStoreLatest(t *testing.T) {
	s := workflowtest.NewMemoryStore()

	if _, err := s.Import("wf", defYAML("wf", "One."), "alice"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := s.Import("wf", defYAML("wf", "Two."), "alice"); err != nil {
		t.Fatalf("import: %v", err)
	}

	def, found, err := s.Latest("wf")
	if err != nil || !found {
		t.Fatalf("latest: found=%v err=%v", found, err)
	}
	if def.Version != 2 {
		t.Errorf("latest version = %d, want 2", def.Version)
	}

	_, found, err = s.Latest("nope")
	if err != nil {
		t.Fatalf("latest unknown: %v", err)
	}
	if found {
		t.Error("unknown workflow reported found")
	}
}

func TestMemoryStoreImportManifest(t *testing.T) {
	store := workflowtest.NewMemoryStore()

	m := workflow.Manifest{
		"workflow.yml": `schema_version: 3
name: dev
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt_file: prompts/implement.md
    skills: [tdd]
`,
		"prompts/implement.md":   "Do the work.",
		"skills/tdd/SKILL.md":    "# tdd",
		"skills/tdd/refs/red.md": "red-green",
	}
	def, err := store.ImportManifest("dev", m, "alice")
	if err != nil {
		t.Fatalf("import manifest: %v", err)
	}
	if def.ContentHash != m.Hash() {
		t.Fatalf("hash must be the canonical-manifest hash: %s vs %s", def.ContentHash, m.Hash())
	}
	if def.RawYAML != m["workflow.yml"] {
		t.Fatal("RawYAML must be the manifest's workflow.yml")
	}
	if def.Compiled.Function == nil || def.Compiled.Function.SkillFiles["skills/tdd/SKILL.md"] == "" {
		t.Fatal("stored function must be compiled (skill trees resolved)")
	}

	again, err := store.ImportManifest("dev", m, "bob")
	if err != nil || again.Version != def.Version {
		t.Fatalf("expected idempotent hit, got v%d err %v", again.Version, err)
	}

	got, found, err := store.Get("dev", def.Version)
	if err != nil || !found {
		t.Fatalf("get: %v %v", found, err)
	}
	if got.SourceManifest["prompts/implement.md"] == "" {
		t.Fatal("Get must return the source manifest")
	}

	// Import(raw) is the single-file degenerate case: same hash scheme.
	raw := defYAML("raw-v3", "single file")
	viaRaw, err := store.Import("raw-v3", raw, "alice")
	if err != nil {
		t.Fatal(err)
	}
	wantHash := workflow.Manifest{"workflow.yml": string(raw)}.Hash()
	if viaRaw.ContentHash != wantHash {
		t.Fatalf("raw import must wrap into a single-file manifest: %s vs %s", viaRaw.ContentHash, wantHash)
	}

	// Metadata listings stay lean.
	list, _ := store.List()
	for _, d := range list {
		if d.RawYAML != "" || len(d.SourceManifest) != 0 {
			t.Fatal("List must not carry RawYAML/SourceManifest")
		}
	}
}

func TestMemoryStorePersistsDerivedSchemaAndSignatureMetadata(t *testing.T) {
	store := workflowtest.NewMemoryStore()

	v1, err := store.Import("first-meta", defYAML("first-meta", "first"), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if v1.SchemaVersion != 3 || v1.SignatureVersion != 1 || v1.Compiled.Function == nil {
		t.Fatalf("first metadata = %+v", v1)
	}
	v2, err := store.ImportManifest(
		"second-meta",
		functionManifest("second-meta", 2, []string{"source"}, "review/v1", "second"),
		"alice",
	)
	if err != nil {
		t.Fatal(err)
	}
	if v2.SchemaVersion != 3 || v2.SignatureVersion != 2 || v2.Compiled.Function == nil {
		t.Fatalf("second metadata = %+v", v2)
	}

	v3, err := store.ImportManifest("function-meta", functionManifest("function-meta", 7, []string{"before"}, "review/v1", "review"), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if v3.SchemaVersion != 3 || v3.SignatureVersion != 7 || v3.Compiled.Function == nil {
		t.Fatalf("v3 metadata = %+v", v3)
	}

	got, found, err := store.Get("function-meta", 1)
	if err != nil || !found || got.SchemaVersion != 3 || got.SignatureVersion != 7 {
		t.Fatalf("Get metadata: found=%v err=%v def=%+v", found, err, got)
	}
	list, err := store.List()
	if err != nil || len(list) != 3 {
		t.Fatalf("List: %v %+v", err, list)
	}
	for _, def := range list {
		if def.SchemaVersion == 0 {
			t.Fatalf("List omitted schema metadata: %+v", def)
		}
		if def.RawYAML != "" || def.SourceManifest != nil || def.Compiled.Function != nil {
			t.Fatalf("List must remain metadata-only: %+v", def)
		}
	}
}

func TestMemoryStoreEnforcesOrderedPublicSignatureCompatibility(t *testing.T) {
	store := workflowtest.NewMemoryStore()
	first := functionManifest("compatible", 1, []string{"before", "after"}, "review/v1", "first prompt")
	if _, err := store.ImportManifest("compatible", first, "alice"); err != nil {
		t.Fatal(err)
	}

	implementationOnly := functionManifest("compatible", 1, []string{"before", "after"}, "review/v1", "changed prompt")
	if def, err := store.ImportManifest("compatible", implementationOnly, "bob"); err != nil || def.Version != 2 {
		t.Fatalf("compatible implementation change: def=%+v err=%v", def, err)
	}

	incompatible := functionManifest("compatible", 1, []string{"after", "before"}, "review/v1", "reordered")
	if _, err := store.ImportManifest("compatible", incompatible, "mallory"); err == nil {
		t.Fatal("reordered public inputs must be rejected")
	} else {
		var invalid workflow.InvalidDefinitionError
		if !errors.As(err, &invalid) {
			t.Fatalf("compatibility error must be invalid-definition, got %T: %v", err, err)
		}
	}

	page, err := store.Versions(context.Background(), "compatible", workflow.VersionPageRequest{Limit: workflow.MaxVersionPageSize})
	if err != nil || len(page.Definitions) != 2 {
		t.Fatalf("rejection allocated a version: %v %+v", err, page.Definitions)
	}

	newSignature := functionManifest("compatible", 2, []string{"after", "before"}, "review/v2", "new contract")
	if def, err := store.ImportManifest("compatible", newSignature, "carol"); err != nil || def.Version != 3 {
		t.Fatalf("new signature version must accept contract change: def=%+v err=%v", def, err)
	}
}

func TestMemoryStoreReturnedDefinitionsCannotMutateCompatibilityAuthority(t *testing.T) {
	store := workflowtest.NewMemoryStore()
	manifest := functionManifest("immutable", 1, []string{"before"}, "review/v1", "first")
	returned, err := store.ImportManifest("immutable", manifest, "alice")
	if err != nil {
		t.Fatal(err)
	}
	returned.Compiled.Function.Inputs[0].Name = "mutated"
	returned.SourceManifest["workflow.yml"] = "corrupt"

	compatible := functionManifest("immutable", 1, []string{"before"}, "review/v1", "second")
	if _, err := store.ImportManifest("immutable", compatible, "bob"); err != nil {
		t.Fatalf("caller mutation changed compatibility authority: %v", err)
	}
}

func TestMemoryStorePromotionReturnsAtomicSignatureComparison(t *testing.T) {
	store := workflowtest.NewMemoryStore(workflowrun.WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if _, err := store.ImportManifest("promote-meta", functionManifest("promote-meta", 1, []string{"before"}, "review/v1", "one"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportManifest("promote-meta", functionManifest("promote-meta", 1, []string{"before"}, "review/v1", "two"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportManifest("promote-meta", functionManifest("promote-meta", 2, []string{"after"}, "review/v2", "three"), "alice"); err != nil {
		t.Fatal(err)
	}

	first, err := store.Promote("promote-meta", 1, "alice")
	if err != nil || first.PreviousLive != nil || first.SignatureChanged || first.Target.SignatureVersion != 1 {
		t.Fatalf("first promotion = %+v err=%v", first, err)
	}
	same, err := store.Promote("promote-meta", 2, "bob")
	if err != nil || same.PreviousLive == nil || same.SignatureChanged {
		t.Fatalf("same-signature promotion = %+v err=%v", same, err)
	}
	changed, err := store.Promote("promote-meta", 3, "carol")
	if err != nil || changed.PreviousLive == nil || !changed.SignatureChanged || changed.PreviousLive.SignatureVersion != 1 || changed.Target.SignatureVersion != 2 {
		t.Fatalf("changed-signature promotion = %+v err=%v", changed, err)
	}
}

func TestMemoryStorePromotionRejectsImportedButUnrunnableV3AndPreservesLive(t *testing.T) {
	renderer := workflowrun.WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	store := workflowtest.NewMemoryStore(renderer)
	valid := workflow.Manifest{"workflow.yml": `schema_version: 3
name: promotion-preflight
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: do the work
`}
	unrunnable := workflow.Manifest{"workflow.yml": `schema_version: 3
name: promotion-preflight
signature_version: 1
inputs: []
outputs: []
plan:
  - task: work
    function_id: work
    file: repository/ci/task.yml
`}

	first, err := store.ImportManifest("promotion-preflight", valid, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Promote("promotion-preflight", first.Version, "alice"); err != nil {
		t.Fatalf("promote valid version: %v", err)
	}
	second, err := store.ImportManifest("promotion-preflight", unrunnable, "bob")
	if err != nil {
		t.Fatalf("the unrunnable definition must remain importable for iteration: %v", err)
	}

	_, err = store.Promote("promotion-preflight", second.Version, "bob")
	var invalid workflow.InvalidPromotionError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "file-backed") {
		t.Fatalf("promotion error = %v, want InvalidPromotionError for file-backed task", err)
	}
	live, found, liveErr := store.Live("promotion-preflight")
	if liveErr != nil || !found || live.Version != first.Version {
		t.Fatalf("live after rejected promotion = %+v, found=%v err=%v", live, found, liveErr)
	}
}

func TestMemoryStoreSchemaV3PromotionFailsClosedWithoutAuthoritativeValidator(t *testing.T) {
	store := workflowtest.NewMemoryStore()
	definition, err := store.ImportManifest("promotion-validator", functionManifest(
		"promotion-validator", 1, []string{"before"}, "review/v1", "one",
	), "alice")
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.Promote("promotion-validator", definition.Version, "alice")
	var invalid workflow.InvalidPromotionError
	if !errors.As(err, &invalid) || !errors.Is(err, workflow.ErrPromotionValidatorRequired) {
		t.Fatalf("promotion error = %v, want required authoritative validator", err)
	}
	if _, found, liveErr := store.Live("promotion-validator"); liveErr != nil || found {
		t.Fatalf("rejected version became live: found=%v err=%v", found, liveErr)
	}
}

func TestPublicSignatureIgnoresDescriptionsAndOutputMappingsButNotOrderedPortIdentity(t *testing.T) {
	base, err := workflow.CompileDefinition(functionManifest("signature", 1, []string{"before", "after"}, "review/v1", "one"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := base.PublicSignature()
	if err != nil {
		t.Fatal(err)
	}

	base.Function.Inputs[0].Description = "new prose"
	base.Function.Outputs[0].Description = "other prose"
	base.Function.Outputs[0].From = "implementation-only-name"
	got, err := base.PublicSignature()
	if err != nil || !want.Equal(got) {
		t.Fatalf("description/from changed signature: equal=%v err=%v", want.Equal(got), err)
	}

	base.Function.Inputs[0].Optional = true
	changed, err := base.PublicSignature()
	if err != nil || want.Equal(changed) {
		t.Fatalf("optionality did not change signature: equal=%v err=%v", want.Equal(changed), err)
	}
}

// TestMemoryStoreAnnotateAndHide covers the name-level lifecycle metadata
// (S-6): it decorates every read path (Get/Live/Latest/List/Versions) without
// allocating a new version, and refuses a workflow that has no versions.
func TestMemoryStoreAnnotateAndHide(t *testing.T) {
	m := workflowtest.NewMemoryStore()
	if _, err := m.Import("wf", defYAML("wf", "Do the work."), "importer"); err != nil {
		t.Fatal(err)
	}

	if err := m.Annotate("wf", "prefer for hotfixes", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetHidden("wf", true, "alice"); err != nil {
		t.Fatal(err)
	}

	defs, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Annotation != "prefer for hotfixes" || !defs[0].Hidden {
		t.Fatalf("list did not surface lifecycle: %+v", defs)
	}

	got, found, err := m.Get("wf", 1)
	if err != nil || !found || got.Annotation != "prefer for hotfixes" || !got.Hidden {
		t.Fatalf("Get did not surface lifecycle: def=%+v found=%v err=%v", got, found, err)
	}
	latest, found, err := m.Latest("wf")
	if err != nil || !found || !latest.Hidden {
		t.Fatalf("Latest did not surface lifecycle: def=%+v found=%v err=%v", latest, found, err)
	}
	page, err := m.Versions(context.Background(), "wf", workflow.VersionPageRequest{Limit: workflow.MaxVersionPageSize})
	if err != nil || len(page.Definitions) != 1 || !page.Definitions[0].Hidden ||
		page.Definitions[0].Annotation != "prefer for hotfixes" {
		t.Fatalf("Versions did not surface lifecycle: %+v err=%v", page.Definitions, err)
	}

	// Lifecycle writes are name-scoped: they never allocate a version.
	if page.Definitions[0].Version != 1 {
		t.Fatalf("lifecycle write allocated a version: %+v", page.Definitions)
	}

	if err := m.SetHidden("wf", false, "alice"); err != nil {
		t.Fatal(err)
	}
	if defs, err = m.List(); err != nil || defs[0].Hidden {
		t.Fatalf("un-hide did not stick: %+v err=%v", defs, err)
	}

	if err := m.Annotate("nope", "x", "alice"); !errors.Is(err, workflow.ErrVersionNotFound) {
		t.Errorf("Annotate on unknown workflow = %v, want ErrVersionNotFound", err)
	}
	if err := m.SetHidden("nope", true, "alice"); !errors.Is(err, workflow.ErrVersionNotFound) {
		t.Errorf("SetHidden on unknown workflow = %v, want ErrVersionNotFound", err)
	}
}
