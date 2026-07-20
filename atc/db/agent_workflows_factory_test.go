package db_test

import (
	"errors"
	"sync"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentWorkflowsFactory", func() {
	var factory db.AgentWorkflowsFactory

	BeforeEach(func() {
		factory = db.NewAgentWorkflowsFactory(dbConn)
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
		Expect(factory.Promote("wf-lv-a", 1, "alice")).To(Succeed())
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

	It("returns all versions ascending", func() {
		_, err := factory.Import("wf-vers", defYAML("wf-vers", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-vers", defYAML("wf-vers", "Two."), "alice")
		Expect(err).ToNot(HaveOccurred())

		versions, err := factory.Versions("wf-vers")
		Expect(err).ToNot(HaveOccurred())
		Expect(versions).To(HaveLen(2))
		Expect(versions[0].Version).To(Equal(1))
		Expect(versions[1].Version).To(Equal(2))

		Expect(factory.Versions("wf-nonexistent")).To(BeEmpty())
	})

	It("promotes atomically, swapping the live flag", func() {
		_, err := factory.Import("wf-promote", defYAML("wf-promote", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-promote", defYAML("wf-promote", "Two."), "alice")
		Expect(err).ToNot(HaveOccurred())

		Expect(factory.Promote("wf-promote", 1, "alice")).To(Succeed())
		live, found, err := factory.Live("wf-promote")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(live.Version).To(Equal(1))

		Expect(factory.Promote("wf-promote", 2, "bob")).To(Succeed())
		live, _, err = factory.Live("wf-promote")
		Expect(err).ToNot(HaveOccurred())
		Expect(live.Version).To(Equal(2))

		v1, _, err := factory.Get("wf-promote", 1)
		Expect(err).ToNot(HaveOccurred())
		Expect(v1.Live).To(BeFalse())

		Expect(factory.Promote("wf-promote", 99, "alice")).To(MatchError(workflow.ErrVersionNotFound))
		Expect(factory.Promote("wf-nonexistent", 1, "alice")).To(MatchError(workflow.ErrVersionNotFound))
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
					Expect(factory.Promote("wf-promote-race", 1, "alice")).To(Succeed())
				}
			}
		}()

		wg.Add(1)
		go func() {
			defer GinkgoRecover()
			defer close(done)
			defer wg.Done()

			for i := 0; i < 100; i++ {
				Expect(factory.Promote("wf-promote-race", 2, "bob")).To(Succeed())
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
					(name, version, content_hash, definition, description, created_by)
				VALUES ('wf-legacy', 1, 'legacyhash', $1, 'legacy', 'alice')`, string(raw))
			Expect(err).ToNot(HaveOccurred())

			got, found, err := factory.Get("wf-legacy", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.Config.Name).To(Equal("wf-legacy"))
			Expect(got.SourceManifest).To(BeEmpty())
		})
	})
})

var _ = Describe("AgentWorkflowsFactory lifecycle", func() {
	var factory db.AgentWorkflowsFactory

	const lcYAML = `schema_version: 1
name: lc-wf
description: lifecycle test
prompts:
  work: "do it"
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`

	BeforeEach(func() {
		factory = db.NewAgentWorkflowsFactory(dbConn)
		_, err := factory.Import("lc-wf", []byte(lcYAML), "importer")
		Expect(err).NotTo(HaveOccurred())
	})

	It("persists annotation and hidden and surfaces them in List", func() {
		Expect(factory.Annotate("lc-wf", "hotfix workhorse", "alice")).To(Succeed())
		Expect(factory.SetHidden("lc-wf", true, "alice")).To(Succeed())

		defs, err := factory.List()
		Expect(err).NotTo(HaveOccurred())
		var found bool
		for _, d := range defs {
			if d.Name == "lc-wf" {
				found = true
				Expect(d.Annotation).To(Equal("hotfix workhorse"))
				Expect(d.Hidden).To(BeTrue())
			}
		}
		Expect(found).To(BeTrue())
	})

	It("surfaces lifecycle on Get/Versions read paths", func() {
		Expect(factory.SetHidden("lc-wf", true, "alice")).To(Succeed())

		def, found, err := factory.Get("lc-wf", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(def.Hidden).To(BeTrue())

		vs, err := factory.Versions("lc-wf")
		Expect(err).NotTo(HaveOccurred())
		Expect(vs).To(HaveLen(1))
		Expect(vs[0].Hidden).To(BeTrue())
	})

	It("returns ErrVersionNotFound for an unknown workflow", func() {
		Expect(factory.Annotate("ghost", "x", "alice")).To(MatchError(workflow.ErrVersionNotFound))
		Expect(factory.SetHidden("ghost", true, "alice")).To(MatchError(workflow.ErrVersionNotFound))
	})
})
