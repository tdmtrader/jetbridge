package db_test

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func dbFunctionManifest(name string, signatureVersion int, inputs []string, outputType, prompt string) workflow.Manifest {
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

var _ = Describe("AgentWorkflowsFactory", func() {
	var factory db.AgentWorkflowsFactory

	BeforeEach(func() {
		factory = db.NewAgentWorkflowsFactory(dbConn, workflowrun.WorkflowTargetRenderer{
			RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		})
	})

	defYAML := func(name, promptBody string) []byte {
		return []byte(`schema_version: 1
name: ` + name + `
description: test definition
prompts:
  work: |
    ` + promptBody + `
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`)
	}

	It("imports with monotonic versions and scans all fields", func() {
		v1, err := factory.Import("wf-import", defYAML("wf-import", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())
		Expect(v1.Version).To(Equal(1))
		Expect(v1.ID).To(BeNumerically(">", 0))
		Expect(v1.Name).To(Equal("wf-import"))
		Expect(v1.ContentHash).To(Equal(workflow.Manifest{"workflow.yml": string(defYAML("wf-import", "One."))}.Hash()))
		Expect(v1.Description).To(Equal("test definition"))
		Expect(v1.CreatedBy).To(Equal("alice"))
		Expect(v1.CreatedAt).To(BeNumerically(">", 0))
		Expect(v1.Live).To(BeFalse())
		Expect(v1.RawYAML).To(Equal(string(defYAML("wf-import", "One."))))
		Expect(v1.Config.Steps).To(HaveLen(1))

		v2, err := factory.Import("wf-import", defYAML("wf-import", "Two."), "bob")
		Expect(err).ToNot(HaveOccurred())
		Expect(v2.Version).To(Equal(2))
	})

	It("is idempotent on content hash", func() {
		raw := defYAML("wf-idem", "Same bytes.")
		v1, err := factory.Import("wf-idem", raw, "alice")
		Expect(err).ToNot(HaveOccurred())

		again, err := factory.Import("wf-idem", raw, "bob")
		Expect(err).ToNot(HaveOccurred())
		Expect(again.Version).To(Equal(v1.Version))
		Expect(again.CreatedBy).To(Equal("alice")) // existing row untouched
	})

	It("rejects name mismatch and invalid definitions as InvalidDefinitionError", func() {
		_, err := factory.Import("wrong-name", defYAML("wf-mismatch", "One."), "alice")
		var inv workflow.InvalidDefinitionError
		Expect(errors.As(err, &inv)).To(BeTrue())

		_, err = factory.Import("wf-bad", []byte("schema_version: 1\nname: wf-bad\nsteps: []\n"), "alice")
		Expect(errors.As(err, &inv)).To(BeTrue())
	})

	It("gets by version and reports found=false for unknowns", func() {
		_, err := factory.Import("wf-get", defYAML("wf-get", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())

		def, found, err := factory.Get("wf-get", 1)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(def.RawYAML).ToNot(BeEmpty())
		Expect(def.Config.Name).To(Equal("wf-get"))

		_, found, err = factory.Get("wf-get", 42)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())

		_, found, err = factory.Live("wf-get")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("lists the latest version per name", func() {
		_, err := factory.Import("wf-list-a", defYAML("wf-list-a", "A one."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-list-a", defYAML("wf-list-a", "A two."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-list-b", defYAML("wf-list-b", "B one."), "alice")
		Expect(err).ToNot(HaveOccurred())

		list, err := factory.List()
		Expect(err).ToNot(HaveOccurred())

		byName := map[string]workflow.Definition{}
		for _, d := range list {
			byName[d.Name] = d
			Expect(d.RawYAML).To(BeEmpty()) // metadata-only listing
		}
		Expect(byName["wf-list-a"].Version).To(Equal(2))
		Expect(byName["wf-list-b"].Version).To(Equal(1))
	})

	It("returns all live versions in one lookup, and the latest version per name", func() {
		_, err := factory.Import("wf-lv-a", defYAML("wf-lv-a", "A one."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Promote("wf-lv-a", 1, "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Import("wf-lv-a", defYAML("wf-lv-a", "A two."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-lv-b", defYAML("wf-lv-b", "B one."), "alice")
		Expect(err).ToNot(HaveOccurred())

		live, err := factory.LiveVersions()
		Expect(err).ToNot(HaveOccurred())
		Expect(live).To(HaveKeyWithValue("wf-lv-a", 1)) // live stays at 1 after v2 import
		Expect(live).ToNot(HaveKey("wf-lv-b"))          // never promoted

		latest, found, err := factory.Latest("wf-lv-a")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(latest.Version).To(Equal(2))

		_, found, err = factory.Latest("wf-nonexistent")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("returns hard-bounded version pages ascending within a newest-first cursor", func() {
		_, err := factory.Import("wf-vers", defYAML("wf-vers", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-vers", defYAML("wf-vers", "Two."), "alice")
		Expect(err).ToNot(HaveOccurred())

		page, err := factory.Versions(context.Background(), "wf-vers", workflow.VersionPageRequest{Limit: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(page.Found).To(BeTrue())
		Expect(page.Definitions).To(HaveLen(1))
		Expect(page.Definitions[0].Version).To(Equal(2))
		Expect(page.NextCursor).To(Equal(2))

		older, err := factory.Versions(context.Background(), "wf-vers", workflow.VersionPageRequest{
			Cursor: page.NextCursor,
			Limit:  1,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(older.Found).To(BeTrue())
		Expect(older.Definitions).To(HaveLen(1))
		Expect(older.Definitions[0].Version).To(Equal(1))
		Expect(older.NextCursor).To(BeZero())

		missing, err := factory.Versions(context.Background(), "wf-nonexistent", workflow.VersionPageRequest{Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(missing.Found).To(BeFalse())
		Expect(missing.Definitions).To(BeEmpty())

		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = factory.Versions(canceled, "wf-vers", workflow.VersionPageRequest{Limit: 1})
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	It("promotes atomically, swapping the live flag", func() {
		_, err := factory.Import("wf-promote", defYAML("wf-promote", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-promote", defYAML("wf-promote", "Two."), "alice")
		Expect(err).ToNot(HaveOccurred())

		_, err = factory.Promote("wf-promote", 1, "alice")
		Expect(err).NotTo(HaveOccurred())
		live, found, err := factory.Live("wf-promote")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(live.Version).To(Equal(1))

		_, err = factory.Promote("wf-promote", 2, "bob")
		Expect(err).NotTo(HaveOccurred())
		live, _, err = factory.Live("wf-promote")
		Expect(err).ToNot(HaveOccurred())
		Expect(live.Version).To(Equal(2))

		v1, _, err := factory.Get("wf-promote", 1)
		Expect(err).ToNot(HaveOccurred())
		Expect(v1.Live).To(BeFalse())

		_, err = factory.Promote("wf-promote", 99, "alice")
		Expect(err).To(MatchError(workflow.ErrVersionNotFound))
		_, err = factory.Promote("wf-nonexistent", 1, "alice")
		Expect(err).To(MatchError(workflow.ErrVersionNotFound))
	})

	It("rejects an imported but unrunnable schema-v3 target without disturbing the live version", func() {
		valid := workflow.Manifest{"workflow.yml": `schema_version: 3
name: wf-promotion-preflight
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: do the work
`}
		unrunnable := workflow.Manifest{"workflow.yml": `schema_version: 3
name: wf-promotion-preflight
signature_version: 1
inputs: []
outputs: []
plan:
  - task: work
    function_id: work
    file: repository/ci/task.yml
`}
		first, err := factory.ImportManifest("wf-promotion-preflight", valid, "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Promote("wf-promotion-preflight", first.Version, "alice")
		Expect(err).NotTo(HaveOccurred())
		second, err := factory.ImportManifest("wf-promotion-preflight", unrunnable, "bob")
		Expect(err).NotTo(HaveOccurred())

		_, err = factory.Promote("wf-promotion-preflight", second.Version, "bob")
		var invalid workflow.InvalidPromotionError
		Expect(errors.As(err, &invalid)).To(BeTrue())
		Expect(err).To(MatchError(ContainSubstring("file-backed")))

		live, found, liveErr := factory.Live("wf-promotion-preflight")
		Expect(liveErr).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(live.Version).To(Equal(first.Version))
		rejected, found, getErr := factory.Get("wf-promotion-preflight", second.Version)
		Expect(getErr).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(rejected.Live).To(BeFalse())
	})

	It("fails closed for schema-v3 promotion without an authoritative validator", func() {
		unvalidated := db.NewAgentWorkflowsFactory(dbConn)
		definition, err := unvalidated.ImportManifest(
			"wf-promotion-validator",
			dbFunctionManifest("wf-promotion-validator", 1, []string{"before"}, "review/v1", "one"),
			"alice",
		)
		Expect(err).NotTo(HaveOccurred())

		_, err = unvalidated.Promote("wf-promotion-validator", definition.Version, "alice")
		var invalid workflow.InvalidPromotionError
		Expect(errors.As(err, &invalid)).To(BeTrue())
		Expect(errors.Is(err, workflow.ErrPromotionValidatorRequired)).To(BeTrue())
		_, found, liveErr := unvalidated.Live("wf-promotion-validator")
		Expect(liveErr).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("consistently promotes under concurrent promotion of the same name", func() {
		_, err := factory.Import("wf-promote-race", defYAML("wf-promote-race", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-promote-race", defYAML("wf-promote-race", "Two."), "alice")
		Expect(err).ToNot(HaveOccurred())

		// enable concurrent use of database. this is set to 1 by default to
		// ensure methods don't require more than one in a single connection,
		// which can cause deadlocking as the pool is limited.
		dbConn.SetMaxOpenConns(2)

		done := make(chan struct{})

		wg := new(sync.WaitGroup)
		wg.Add(1)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()

			for {
				select {
				case <-done:
					return
				default:
					_, err := factory.Promote("wf-promote-race", 1, "alice")
					Expect(err).NotTo(HaveOccurred())
				}
			}
		}()

		wg.Add(1)
		go func() {
			defer GinkgoRecover()
			defer close(done)
			defer wg.Done()

			for i := 0; i < 100; i++ {
				_, err := factory.Promote("wf-promote-race", 2, "bob")
				Expect(err).NotTo(HaveOccurred())
			}
		}()

		wg.Wait()

		live, found, err := factory.Live("wf-promote-race")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(live.Version).To(BeElementOf(1, 2))
	})

	Describe("manifest imports", func() {
		manifest := func(name string) workflow.Manifest {
			return workflow.Manifest{
				"workflow.yml": `schema_version: 2
name: ` + name + `
description: manifest test
skills: [tdd]
context: [context/conventions.md]
system_prompt_file: system/base.md
prompt_files:
  work: prompts/work.md
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`,
				"prompts/work.md":        "Do the work.",
				"system/base.md":         "base system prompt",
				"context/conventions.md": "conventions",
				"skills/tdd/SKILL.md":    "# tdd",
				"skills/tdd/refs/red.md": "red-green",
			}
		}

		It("imports, compiles on read, and is idempotent on the manifest hash", func() {
			src := manifest("wf-manifest")
			v1, err := factory.ImportManifest("wf-manifest", src, "alice")
			Expect(err).ToNot(HaveOccurred())
			Expect(v1.Version).To(Equal(1))
			Expect(v1.SchemaVersion).To(Equal(2))
			Expect(v1.SignatureVersion).To(Equal(0))
			Expect(v1.ContentHash).To(Equal(src.Hash()))

			again, err := factory.ImportManifest("wf-manifest", src, "bob")
			Expect(err).ToNot(HaveOccurred())
			Expect(again.Version).To(Equal(1))

			got, found, err := factory.Get("wf-manifest", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.RawYAML).To(Equal(src["workflow.yml"]))
			Expect(got.SourceManifest["skills/tdd/refs/red.md"]).To(Equal("red-green"))
			Expect(got.Config.SystemPrompt).To(Equal("base system prompt"))
			Expect(got.Config.SkillFiles).To(HaveKey("skills/tdd/SKILL.md"))
			Expect(got.Config.ContextFiles["context/conventions.md"]).To(Equal("conventions"))
		})

		It("reads legacy rows (no source_manifest) via the Parse path", func() {
			raw := defYAML("wf-legacy", "Legacy.")
			// Simulate a pre-slice row: definition only, NULL manifest.
			_, err := dbConn.Exec(`
				INSERT INTO agent_workflow_definitions
					(name, version, content_hash, definition, description, created_by,
					 schema_version, signature_version)
				VALUES ('wf-legacy', 1, 'legacyhash', $1, 'legacy', 'alice', 1, 0)`, string(raw))
			Expect(err).ToNot(HaveOccurred())

			got, found, err := factory.Get("wf-legacy", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.Config.Name).To(Equal("wf-legacy"))
			Expect(got.SourceManifest).To(BeEmpty())
		})
	})

	Describe("schema and signature metadata", func() {
		It("derives and scans metadata for legacy and function definitions", func() {
			legacy, err := factory.Import("wf-meta-legacy", defYAML("wf-meta-legacy", "legacy"), "alice")
			Expect(err).NotTo(HaveOccurred())
			Expect(legacy.SchemaVersion).To(Equal(1))
			Expect(legacy.SignatureVersion).To(Equal(0))
			Expect(legacy.Compiled.Legacy).NotTo(BeNil())

			function, err := factory.ImportManifest("wf-meta-function", dbFunctionManifest("wf-meta-function", 7, []string{"before"}, "review/v1", "review"), "bob")
			Expect(err).NotTo(HaveOccurred())
			Expect(function.SchemaVersion).To(Equal(3))
			Expect(function.SignatureVersion).To(Equal(7))
			Expect(function.Compiled.Function).NotTo(BeNil())

			var schema, signature int
			Expect(dbConn.QueryRow(`
				SELECT schema_version, signature_version
				FROM agent_workflow_definitions WHERE id = $1`, function.ID).Scan(&schema, &signature)).To(Succeed())
			Expect(schema).To(Equal(3))
			Expect(signature).To(Equal(7))

			listed, err := factory.List()
			Expect(err).NotTo(HaveOccurred())
			byName := map[string]workflow.Definition{}
			for _, definition := range listed {
				byName[definition.Name] = definition
			}
			Expect(byName["wf-meta-function"].SchemaVersion).To(Equal(3))
			Expect(byName["wf-meta-function"].SignatureVersion).To(Equal(7))
			Expect(byName["wf-meta-function"].Compiled.Function).To(BeNil())
		})

		It("enforces exact ordered compatibility for a reused positive signature", func() {
			_, err := factory.ImportManifest("wf-contract", dbFunctionManifest("wf-contract", 1, []string{"before", "after"}, "review/v1", "one"), "alice")
			Expect(err).NotTo(HaveOccurred())
			compatible, err := factory.ImportManifest("wf-contract", dbFunctionManifest("wf-contract", 1, []string{"before", "after"}, "review/v1", "two"), "bob")
			Expect(err).NotTo(HaveOccurred())
			Expect(compatible.Version).To(Equal(2))

			_, err = factory.ImportManifest("wf-contract", dbFunctionManifest("wf-contract", 1, []string{"after", "before"}, "review/v1", "bad"), "mallory")
			var invalid workflow.InvalidDefinitionError
			Expect(errors.As(err, &invalid)).To(BeTrue())
			page, pageErr := factory.Versions(context.Background(), "wf-contract", workflow.VersionPageRequest{Limit: workflow.MaxVersionPageSize})
			Expect(pageErr).NotTo(HaveOccurred())
			Expect(page.Definitions).To(HaveLen(2))

			different, err := factory.ImportManifest("wf-contract", dbFunctionManifest("wf-contract", 2, []string{"after", "before"}, "review/v2", "new"), "carol")
			Expect(err).NotTo(HaveOccurred())
			Expect(different.Version).To(Equal(3))
		})

		It("fails closed when durable metadata drifts from compiled source", func() {
			source := dbFunctionManifest("wf-drift", 1, []string{"before"}, "review/v1", "one")
			definition, err := factory.ImportManifest("wf-drift", source, "alice")
			Expect(err).NotTo(HaveOccurred())
			_, err = dbConn.Exec(`UPDATE agent_workflow_definitions SET signature_version = 2 WHERE id = $1`, definition.ID)
			Expect(err).NotTo(HaveOccurred())

			_, found, err := factory.Get("wf-drift", 1)
			Expect(found).To(BeFalse())
			Expect(err).To(MatchError(ContainSubstring("metadata")))

			_, err = factory.ImportManifest("wf-drift", source, "bob")
			Expect(err).To(MatchError(ContainSubstring("metadata")))
		})

		It("returns the atomic previous and target signature metadata on promotion", func() {
			_, err := factory.ImportManifest("wf-promotion-meta", dbFunctionManifest("wf-promotion-meta", 1, []string{"before"}, "review/v1", "one"), "alice")
			Expect(err).NotTo(HaveOccurred())
			_, err = factory.ImportManifest("wf-promotion-meta", dbFunctionManifest("wf-promotion-meta", 2, []string{"after"}, "review/v2", "two"), "alice")
			Expect(err).NotTo(HaveOccurred())

			first, err := factory.Promote("wf-promotion-meta", 1, "alice")
			Expect(err).NotTo(HaveOccurred())
			Expect(first.PreviousLive).To(BeNil())
			Expect(first.SignatureChanged).To(BeFalse())
			changed, err := factory.Promote("wf-promotion-meta", 2, "bob")
			Expect(err).NotTo(HaveOccurred())
			Expect(changed.PreviousLive).NotTo(BeNil())
			Expect(changed.PreviousLive.SignatureVersion).To(Equal(1))
			Expect(changed.Target.SignatureVersion).To(Equal(2))
			Expect(changed.SignatureChanged).To(BeTrue())
		})

		It("serializes incompatible first imports so only one public signature commits", func() {
			dbConn.SetMaxOpenConns(2)
			start := make(chan struct{})
			errorsSeen := make(chan error, 2)
			manifests := []workflow.Manifest{
				dbFunctionManifest("wf-contract-race", 1, []string{"before", "after"}, "review/v1", "one"),
				dbFunctionManifest("wf-contract-race", 1, []string{"after", "before"}, "review/v1", "two"),
			}
			var wait sync.WaitGroup
			for index := range manifests {
				wait.Add(1)
				go func(source workflow.Manifest) {
					defer GinkgoRecover()
					defer wait.Done()
					<-start
					_, err := factory.ImportManifest("wf-contract-race", source, "alice")
					errorsSeen <- err
				}(manifests[index])
			}
			close(start)
			wait.Wait()
			close(errorsSeen)

			succeeded, rejected := 0, 0
			for err := range errorsSeen {
				if err == nil {
					succeeded++
					continue
				}
				var invalid workflow.InvalidDefinitionError
				Expect(errors.As(err, &invalid)).To(BeTrue())
				rejected++
			}
			Expect(succeeded).To(Equal(1))
			Expect(rejected).To(Equal(1))
			page, err := factory.Versions(context.Background(), "wf-contract-race", workflow.VersionPageRequest{Limit: workflow.MaxVersionPageSize})
			Expect(err).NotTo(HaveOccurred())
			Expect(page.Definitions).To(HaveLen(1))
		})

		It("keeps List and Versions metadata-only even when stored source is corrupt", func() {
			definition, err := factory.ImportManifest("wf-metadata-only", dbFunctionManifest("wf-metadata-only", 1, []string{"before"}, "review/v1", "one"), "alice")
			Expect(err).NotTo(HaveOccurred())
			_, err = dbConn.Exec(`
				UPDATE agent_workflow_definitions
				SET source_manifest = jsonb_build_object('workflow.yml', 'not: [yaml')
				WHERE id = $1`, definition.ID)
			Expect(err).NotTo(HaveOccurred())

			listed, err := factory.List()
			Expect(err).NotTo(HaveOccurred())
			Expect(listed).To(ContainElement(And(
				HaveField("Name", "wf-metadata-only"),
				HaveField("SchemaVersion", 3),
				HaveField("SignatureVersion", 1),
			)))
			page, err := factory.Versions(context.Background(), "wf-metadata-only", workflow.VersionPageRequest{Limit: workflow.MaxVersionPageSize})
			Expect(err).NotTo(HaveOccurred())
			Expect(page.Definitions).To(HaveLen(1))
			_, found, err := factory.Get("wf-metadata-only", 1)
			Expect(found).To(BeFalse())
			Expect(err).To(HaveOccurred())
		})
	})
})
