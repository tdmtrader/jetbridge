package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type countingDBPromotionValidator struct {
	calls    int
	delegate workflow.PromotionValidator
}

type promotionFailureRegistry struct{ err error }

func (registry promotionFailureRegistry) SaveAndActivateForPromotion(
	ctx context.Context,
	tx db.Tx,
	teamID int,
	definition workflow.Definition,
	_ workflow.RenderedResourceSourcePipeline,
) (db.WorkflowResourceSourcePipeline, error) {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_lifecycle (name, annotation, updated_by, updated_at)
		VALUES ($1, 'must roll back', 'test', now())
	`, definition.Name)
	if err != nil {
		return db.WorkflowResourceSourcePipeline{}, err
	}
	return db.WorkflowResourceSourcePipeline{}, registry.err
}

func (registry promotionFailureRegistry) DrainActiveForPromotion(
	_ context.Context,
	_ db.Tx,
	_ int,
	_ string,
) (bool, error) {
	return false, registry.err
}

func (validator *countingDBPromotionValidator) ValidatePromotion(definition workflow.Definition) error {
	validator.calls++
	return validator.delegate.ValidatePromotion(definition)
}

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

func dbResourceSourceManifest(name, prompt string) workflow.Manifest {
	return workflow.Manifest{workflow.WorkflowFileName: fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs: []
outputs: []
resources:
  - name: repository
    type: git
    source: {uri: https://example.invalid/repository.git}
resource_sources:
  - name: repository-source
    resource: repository
    type: repository/v1
plan:
  - agent: inspect
    function_id: inspect
    prompt: %s
    inputs: [repository-source]
    input_types:
      repository-source: {type: repository/v1}
`, name, prompt)}
}

var _ = Describe("AgentWorkflowsFactory", func() {
	var factory db.AgentWorkflowsFactory

	BeforeEach(func() {
		factory = db.NewAgentWorkflowsFactory(dbConn, workflowrun.WorkflowTargetRenderer{
			RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		})
	})

	defYAML := func(name, promptBody string) []byte {
		return []byte(`schema_version: 3
name: ` + name + `
description: test definition
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: ` + promptBody + `
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
		Expect(v1.Compiled.Function).NotTo(BeNil())
		Expect(v1.Compiled.Function.Plan).To(HaveLen(1))

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

		_, err = factory.Import("wf-bad", []byte("schema_version: 3\nname: wf-bad\nsignature_version: 1\ninputs: []\noutputs: []\nplan: []\n"), "alice")
		Expect(errors.As(err, &inv)).To(BeTrue())
	})

	It("rejects NonV3 Imports before legacy validation without storing rows", func() {
		_, err := factory.Import("wf-non-v3-raw", []byte(`schema_version: 1
name: different-name
steps: []
`), "alice")
		var invalid workflow.InvalidDefinitionError
		var unsupported workflow.UnsupportedSchemaVersionError
		Expect(errors.As(err, &invalid)).To(BeTrue())
		Expect(errors.As(err, &unsupported)).To(BeTrue())
		Expect(unsupported.Got).To(Equal(1))
		Expect(err).To(MatchError("workflow: unsupported schema_version 1; only schema_version 3 is supported"))

		_, err = factory.ImportManifest("wf-non-v3-manifest", workflow.Manifest{
			"workflow.yml": `schema_version: 2
name: wf-non-v3-manifest
prompt_files:
  work: prompts/missing.md
steps:
  - agent: work
    prompt: work
    outputs: [workspace]
`,
		}, "alice")
		unsupported = workflow.UnsupportedSchemaVersionError{}
		Expect(errors.As(err, &invalid)).To(BeTrue())
		Expect(errors.As(err, &unsupported)).To(BeTrue())
		Expect(unsupported.Got).To(Equal(2))
		Expect(err).To(MatchError("workflow: unsupported schema_version 2; only schema_version 3 is supported"))

		for _, name := range []string{"wf-non-v3-raw", "wf-non-v3-manifest"} {
			var count int
			Expect(dbConn.QueryRow(`SELECT COUNT(*) FROM agent_workflow_definitions WHERE name = $1`, name).Scan(&count)).To(Succeed())
			Expect(count).To(BeZero())
		}
	})

	It("preserves manifest validation and malformed v3 Import errors as ordinary invalid definitions", func() {
		for _, test := range []struct {
			name     string
			manifest workflow.Manifest
			want     string
		}{
			{name: "empty-manifest", manifest: workflow.Manifest{}, want: "workflow: manifest has no files"},
			{name: "missing-workflow", manifest: workflow.Manifest{"README.md": "source only"}, want: "workflow: manifest has no workflow.yaml (or legacy workflow.yml)"},
		} {
			_, err := factory.ImportManifest(test.name, test.manifest, "alice")
			var invalid workflow.InvalidDefinitionError
			Expect(errors.As(err, &invalid)).To(BeTrue())
			Expect(err).To(MatchError(test.want))
		}

		_, err := factory.Import("wf-malformed-v3", []byte(`schema_version: 3
name: wf-malformed-v3
signature_version: 1
inputs: []
outputs: []
plan: []
`), "alice")
		var invalid workflow.InvalidDefinitionError
		Expect(errors.As(err, &invalid)).To(BeTrue())
		var unsupported workflow.UnsupportedSchemaVersionError
		Expect(errors.As(err, &unsupported)).To(BeFalse())
		var count int
		Expect(dbConn.QueryRow(`SELECT COUNT(*) FROM agent_workflow_definitions WHERE name = 'wf-malformed-v3'`).Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
	})

	It("gets by version and reports found=false for unknowns", func() {
		_, err := factory.Import("wf-get", defYAML("wf-get", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())

		def, found, err := factory.Get("wf-get", 1)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(def.RawYAML).ToNot(BeEmpty())
		Expect(def.Compiled.Function).NotTo(BeNil())
		Expect(def.Compiled.Name).To(Equal("wf-get"))

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

		// The promotion audit is READ BACK, not merely written: who made a
		// version live and when is the one governance fact the platform
		// records about it, and it must reach every surface.
		Expect(v1.PromotedBy).To(Equal("alice"))
		Expect(v1.PromotedAt).To(BeNumerically(">", 0))
		Expect(live.PromotedBy).To(Equal("bob"))
		Expect(live.PromotedAt).To(BeNumerically(">", 0))

		// A version that was never promoted carries no audit at all.
		_, err = factory.Import("wf-promote", defYAML("wf-promote", "Three."), "alice")
		Expect(err).ToNot(HaveOccurred())
		v3, _, err := factory.Get("wf-promote", 3)
		Expect(err).ToNot(HaveOccurred())
		Expect(v3.PromotedBy).To(BeEmpty())
		Expect(v3.PromotedAt).To(BeZero())

		_, err = factory.Promote("wf-promote", 99, "alice")
		Expect(err).To(MatchError(workflow.ErrVersionNotFound))
		_, err = factory.Promote("wf-nonexistent", 1, "alice")
		Expect(err).To(MatchError(workflow.ErrVersionNotFound))
	})

	It("atomically owns source pipelines and preserves frozen declarations on exact promotion repeats", func() {
		registry := db.NewWorkflowResourceSourcePipelinesFactory(dbConn)
		sourceFactory := db.NewAgentWorkflowsFactoryWithResourceSources(
			dbConn,
			workflowrun.WorkflowTargetRenderer{RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			db.AgentWorkflowResourceSourcePromotion{
				TeamID: defaultTeam.ID(), Registry: registry,
				Renderer: db.DefaultWorkflowResourceSourcePipelineRenderer{},
			},
		)
		name := fmt.Sprintf("wf-source-promotion-%d", GinkgoRandomSeed())
		first, err := sourceFactory.ImportManifest(name, dbResourceSourceManifest(name, "first"), "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = sourceFactory.Promote(name, first.Version, "alice")
		Expect(err).NotTo(HaveOccurred())

		registered, found, err := registry.FindActive(context.Background(), defaultTeam.ID(), name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(registered.WorkflowDefinitionID).To(Equal(first.ID))
		Expect(registered.SourceDeclarations).To(Equal([]db.ResourceSourceDeclaration{{
			SourceName: "repository-source", ResourceName: "repository", SnapshotType: "repository/v1",
		}}))
		firstPipelineID := registered.PipelineID
		firstPipeline, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: "agent-workflow-source-" + name + "-v1-" + registered.ConfigHash[:12]})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		config, err := firstPipeline.Config()
		Expect(err).NotTo(HaveOccurred())
		_, _, err = defaultTeam.SavePipeline(
			atc.PipelineRef{Name: firstPipeline.Name()}, config, firstPipeline.ConfigVersion(), false,
		)
		Expect(errors.Is(err, db.ErrAgentWorkflowResourceSourceImmutable)).To(BeTrue())

		_, err = sourceFactory.Promote(name, first.Version, "alice")
		Expect(err).NotTo(HaveOccurred())
		repeated, found, err := registry.FindActive(context.Background(), defaultTeam.ID(), name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(repeated).To(Equal(registered))

		alteredDeclarations, err := json.Marshal([]db.ResourceSourceDeclaration{{
			SourceName: "repository-source", ResourceName: "substituted", SnapshotType: "repository/v1",
		}})
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_workflow_resource_source_pipelines SET source_declarations=$1 WHERE pipeline_id=$2`, alteredDeclarations, firstPipelineID)
		Expect(err).NotTo(HaveOccurred())
		_, err = sourceFactory.Promote(name, first.Version, "alice")
		var invalid workflow.InvalidPromotionError
		Expect(errors.As(err, &invalid)).To(BeTrue())
		Expect(err).To(MatchError(ContainSubstring("registered source revision cannot be reactivated")))
		originalDeclarations, err := json.Marshal(registered.SourceDeclarations)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_workflow_resource_source_pipelines SET source_declarations=$1 WHERE pipeline_id=$2`, originalDeclarations, firstPipelineID)
		Expect(err).NotTo(HaveOccurred())

		second, err := sourceFactory.ImportManifest(name, dbResourceSourceManifest(name, "second"), "bob")
		Expect(err).NotTo(HaveOccurred())
		_, err = sourceFactory.Promote(name, second.Version, "bob")
		Expect(err).NotTo(HaveOccurred())
		active, found, err := registry.FindActive(context.Background(), defaultTeam.ID(), name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(active.WorkflowDefinitionID).To(Equal(second.ID))
		Expect(active.PipelineID).NotTo(Equal(firstPipelineID))
		drained, found, err := registry.Find(context.Background(), defaultTeam.ID(), firstPipelineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(drained.State).To(Equal(db.AgentWorkflowResourceSourcePipelineDraining))
	})

	It("unpauses active source pipelines and physically archives drained pipelines only after a pause pass", func() {
		registry := db.NewWorkflowResourceSourcePipelinesFactory(dbConn)
		sourceFactory := db.NewAgentWorkflowsFactoryWithResourceSources(
			dbConn,
			workflowrun.WorkflowTargetRenderer{RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			db.AgentWorkflowResourceSourcePromotion{
				TeamID: defaultTeam.ID(), Registry: registry,
				Renderer: db.DefaultWorkflowResourceSourcePipelineRenderer{},
			},
		)
		name := fmt.Sprintf("wf-source-lifecycle-%d", GinkgoRandomSeed())
		first, err := sourceFactory.ImportManifest(name, dbResourceSourceManifest(name, "first"), "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = sourceFactory.Promote(name, first.Version, "alice")
		Expect(err).NotTo(HaveOccurred())
		active, found, err := registry.FindActive(context.Background(), defaultTeam.ID(), name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		firstPipeline, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: "agent-workflow-source-" + name + "-v1-" + active.ConfigHash[:12]})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(firstPipeline.Paused()).To(BeTrue())

		lifecycle, err := workflowrun.NewSourcePipelineLifecycle(defaultTeam.ID(), registry)
		Expect(err).NotTo(HaveOccurred())
		Expect(lifecycle.Reconcile(context.Background())).To(Succeed())
		Expect(firstPipeline.Reload()).To(BeTrue())
		Expect(firstPipeline.Paused()).To(BeFalse())

		second, err := sourceFactory.ImportManifest(name, dbResourceSourceManifest(name, "second"), "bob")
		Expect(err).NotTo(HaveOccurred())
		_, err = sourceFactory.Promote(name, second.Version, "bob")
		Expect(err).NotTo(HaveOccurred())
		Expect(lifecycle.Reconcile(context.Background())).To(Succeed())
		Expect(firstPipeline.Reload()).To(BeTrue())
		Expect(firstPipeline.Paused()).To(BeTrue())
		Expect(firstPipeline.Archived()).To(BeFalse())

		Expect(lifecycle.Reconcile(context.Background())).To(Succeed())
		Expect(firstPipeline.Reload()).To(BeTrue())
		Expect(firstPipeline.Archived()).To(BeTrue())
		archived, found, err := registry.Find(context.Background(), defaultTeam.ID(), active.PipelineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(archived.State).To(Equal(db.AgentWorkflowResourceSourcePipelineArchived))
	})

	It("composes source-pipeline promotion with exact released-node imports without changing the legacy constructor", func() {
		nodes := db.NewAgentNodesFactory(dbConn)
		node, err := nodes.ImportManifest("source-review", workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: source-review
inputs: []
outputs: []
step: {agent: review, prompt: review}`}, "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = nodes.Release(node.Name, node.Version, workflow.ReleaseCompatible, "alice")
		Expect(err).NotTo(HaveOccurred())

		name := fmt.Sprintf("wf-source-node-%d", GinkgoRandomSeed())
		manifest := workflow.Manifest{workflow.WorkflowFileName: fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs: []
outputs: []
plan:
  - node: review-change
    uses: source-review@1
    input_mapping: {}
    output_mapping: {}
`, name)}
		registry := db.NewWorkflowResourceSourcePipelinesFactory(dbConn)
		legacy := db.NewAgentWorkflowsFactoryWithResourceSources(
			dbConn,
			workflowrun.WorkflowTargetRenderer{RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			db.AgentWorkflowResourceSourcePromotion{TeamID: defaultTeam.ID(), Registry: registry, Renderer: db.DefaultWorkflowResourceSourcePipelineRenderer{}},
		)
		_, err = legacy.ImportManifest(name, manifest, "alice")
		Expect(err).To(HaveOccurred())

		factory := db.NewAgentWorkflowsFactoryWithResourceSourcesAndNodeResolver(
			dbConn, nodes,
			workflowrun.WorkflowTargetRenderer{RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			db.AgentWorkflowResourceSourcePromotion{TeamID: defaultTeam.ID(), Registry: registry, Renderer: db.DefaultWorkflowResourceSourcePipelineRenderer{}},
		)
		definition, err := factory.ImportManifest(name, manifest, "alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(definition.Compiled.Function.Plan).To(HaveLen(1))
	})

	It("does not make a workflow live when source-pipeline activation fails", func() {
		activationFailure := errors.New("source registry unavailable")
		factory := db.NewAgentWorkflowsFactoryWithResourceSources(
			dbConn,
			workflowrun.WorkflowTargetRenderer{RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			db.AgentWorkflowResourceSourcePromotion{
				TeamID: defaultTeam.ID(), Registry: promotionFailureRegistry{err: activationFailure},
				Renderer: db.DefaultWorkflowResourceSourcePipelineRenderer{},
			},
		)
		name := fmt.Sprintf("wf-source-activation-failure-%d", GinkgoRandomSeed())
		definition, err := factory.ImportManifest(name, dbResourceSourceManifest(name, "failed activation"), "alice")
		Expect(err).NotTo(HaveOccurred())

		_, err = factory.Promote(name, definition.Version, "alice")
		var invalid workflow.InvalidPromotionError
		Expect(errors.As(err, &invalid)).To(BeTrue())
		Expect(errors.Is(err, activationFailure)).To(BeTrue())
		live, found, err := factory.Live(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(live).To(Equal(workflow.Definition{}))
		var lifecycleRows int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_workflow_lifecycle WHERE name=$1`, name).Scan(&lifecycleRows)).To(Succeed())
		Expect(lifecycleRows).To(BeZero())
	})

	It("rejects Promote of an Unsupported historical schema before decoding or validator mutation", func() {
		validator := &countingDBPromotionValidator{
			delegate: workflowrun.WorkflowTargetRenderer{
				RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		}
		admissionFactory := db.NewAgentWorkflowsFactory(dbConn, validator)
		live, err := admissionFactory.Import("wf-non-v3-promote", defYAML("wf-non-v3-promote", "live"), "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = admissionFactory.Promote("wf-non-v3-promote", live.Version, "alice")
		Expect(err).NotTo(HaveOccurred())
		baselineCalls := validator.calls
		Expect(baselineCalls).To(BeNumerically(">", 0))

		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, source_manifest, description, created_by,
				 schema_version, signature_version)
			VALUES ($1, $2, 'historical-v2', 'not: [valid YAML', '[]'::jsonb, 'legacy', 'history', 2, 0)`,
			"wf-non-v3-promote", live.Version+1,
		)
		Expect(err).NotTo(HaveOccurred())

		_, err = admissionFactory.Promote("wf-non-v3-promote", live.Version+1, "bob")
		var invalid workflow.InvalidPromotionError
		var unsupported workflow.UnsupportedSchemaVersionError
		Expect(errors.As(err, &invalid)).To(BeTrue())
		Expect(errors.As(err, &unsupported)).To(BeTrue())
		Expect(unsupported.Got).To(Equal(2))
		Expect(err).To(MatchError("workflow: version is not runnable: workflow: unsupported schema_version 2; only schema_version 3 is supported"))
		Expect(validator.calls).To(Equal(baselineCalls))

		var liveV3, liveV2 bool
		Expect(dbConn.QueryRow(`
			SELECT live FROM agent_workflow_definitions WHERE name = $1 AND version = $2`,
			"wf-non-v3-promote", live.Version,
		).Scan(&liveV3)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT live FROM agent_workflow_definitions WHERE name = $1 AND version = $2`,
			"wf-non-v3-promote", live.Version+1,
		).Scan(&liveV2)).To(Succeed())
		Expect(liveV3).To(BeTrue())
		Expect(liveV2).To(BeFalse())
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
				"workflow.yml": `schema_version: 3
name: ` + name + `
description: manifest test
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt_file: prompts/work.md
    system_prompt_file: system/base.md
    context_files: [context/conventions.md]
    skills: [tdd]
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
			Expect(v1.SchemaVersion).To(Equal(3))
			Expect(v1.SignatureVersion).To(Equal(1))
			Expect(v1.ContentHash).To(Equal(src.Hash()))

			again, err := factory.ImportManifest("wf-manifest", src, "bob")
			Expect(err).ToNot(HaveOccurred())
			Expect(again.Version).To(Equal(1))

			got, found, err := factory.Get("wf-manifest", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.RawYAML).To(Equal(src["workflow.yml"]))
			Expect(got.SourceManifest["skills/tdd/refs/red.md"]).To(Equal("red-green"))
			Expect(got.Compiled.Function).NotTo(BeNil())
			Expect(got.Compiled.Function.SkillFiles).To(HaveKey("skills/tdd/SKILL.md"))
			agent, ok := got.Compiled.Function.Plan[0].Config.(*atc.AgentStep)
			Expect(ok).To(BeTrue())
			Expect(agent.Prompt).To(Equal("Do the work."))
			Expect(agent.SystemPrompt).To(Equal("base system prompt"))
			Expect(agent.Context).To(ContainSubstring("conventions"))
		})

		It("imports and reads back a manifest keyed workflow.yaml", func() {
			src := manifest("wf-manifest-yaml")
			src[workflow.WorkflowFileName] = src["workflow.yml"]
			delete(src, "workflow.yml")

			v1, err := factory.ImportManifest("wf-manifest-yaml", src, "alice")
			Expect(err).ToNot(HaveOccurred())
			Expect(v1.ContentHash).To(Equal(src.Hash()))

			got, found, err := factory.Get("wf-manifest-yaml", v1.Version)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.RawYAML).To(Equal(src[workflow.WorkflowFileName]))
			Expect(got.SourceManifest).To(HaveKey(workflow.WorkflowFileName))
			Expect(got.SourceManifest).NotTo(HaveKey("workflow.yml"))
			Expect(got.Compiled.Function).NotTo(BeNil())
		})

		It("returns schema-1 and schema-2 history as exact opaque persisted rows", func() {
			const (
				name        = "wf-legacy-opaque"
				rawV1       = "schema_version: [malformed-v1"
				rawV2       = "not: [valid YAML"
				contentHash = "opaque-v2-content-hash"
				description = "historical opaque definition"
				createdBy   = "history-import"
			)

			var v1ID int
			Expect(dbConn.QueryRow(`
					INSERT INTO agent_workflow_definitions
						(name, version, content_hash, definition, source_manifest, description, created_by,
						 schema_version, signature_version)
					VALUES ($1, 1, 'opaque-v1-content-hash', $2, '[]'::jsonb, 'older history', 'legacy-import', 1, 0)
					RETURNING id
				`, name, rawV1).Scan(&v1ID)).To(Succeed())

			var (
				v2ID        int
				v2CreatedAt int64
			)
			Expect(dbConn.QueryRow(`
					INSERT INTO agent_workflow_definitions
						(name, version, content_hash, definition, source_manifest, live, description, created_by,
						 schema_version, signature_version)
					VALUES ($1, 2, $2, $3, '[]'::jsonb, false, $4, $5, 2, 0)
					RETURNING id, EXTRACT(EPOCH FROM created_at)::bigint
				`, name, contentHash, rawV2, description, createdBy).Scan(&v2ID, &v2CreatedAt)).To(Succeed())

			older, found, err := factory.Get(name, 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(older.ID).To(Equal(v1ID))
			Expect(older.SchemaVersion).To(Equal(1))
			Expect(older.RawYAML).To(Equal(rawV1))
			Expect(older.SourceManifest).To(BeNil())
			Expect(older.Compiled).To(Equal(workflow.CompiledDefinition{}))
			wirePayload, err := json.Marshal(older)
			Expect(err).NotTo(HaveOccurred())
			var wire map[string]any
			Expect(json.Unmarshal(wirePayload, &wire)).To(Succeed())
			Expect(wire).NotTo(HaveKey("config"))

			assertOpaqueV2 := func(got *workflow.Definition, found bool, err error) {
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(got.ID).To(Equal(v2ID))
				Expect(got.Name).To(Equal(name))
				Expect(got.Version).To(Equal(2))
				Expect(got.ContentHash).To(Equal(contentHash))
				Expect(got.Live).To(BeFalse())
				Expect(got.Description).To(Equal(description))
				Expect(got.CreatedBy).To(Equal(createdBy))
				Expect(got.CreatedAt).To(Equal(v2CreatedAt))
				Expect(got.SchemaVersion).To(Equal(2))
				Expect(got.SignatureVersion).To(BeZero())
				Expect(got.RawYAML).To(Equal(rawV2))
				Expect(got.SourceManifest).To(BeNil())
				Expect(got.Compiled).To(Equal(workflow.CompiledDefinition{}))
			}

			got, found, err := factory.Get(name, 2)
			assertOpaqueV2(got, found, err)
			latest, found, err := factory.Latest(name)
			assertOpaqueV2(latest, found, err)
		})
	})

	Describe("schema and signature metadata", func() {
		It("derives and scans metadata for legacy and function definitions", func() {
			first, err := factory.Import("wf-meta-first", defYAML("wf-meta-first", "first"), "alice")
			Expect(err).NotTo(HaveOccurred())
			Expect(first.SchemaVersion).To(Equal(3))
			Expect(first.SignatureVersion).To(Equal(1))
			Expect(first.Compiled.Function).NotTo(BeNil())

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
			_, found, err = factory.Latest("wf-metadata-only")
			Expect(found).To(BeFalse())
			Expect(err).To(HaveOccurred())
		})
	})
})

var _ = Describe("AgentWorkflowsFactory lifecycle", func() {
	var factory db.AgentWorkflowsFactory

	const lcYAML = `schema_version: 3
name: lc-wf
description: lifecycle test
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: do it
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

		page, err := factory.Versions(context.Background(), "lc-wf", workflow.VersionPageRequest{Limit: workflow.MaxVersionPageSize})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Found).To(BeTrue())
		Expect(page.Definitions).To(HaveLen(1))
		Expect(page.Definitions[0].Hidden).To(BeTrue())
	})

	It("returns ErrVersionNotFound for an unknown workflow", func() {
		Expect(factory.Annotate("ghost", "x", "alice")).To(MatchError(workflow.ErrVersionNotFound))
		Expect(factory.SetHidden("ghost", true, "alice")).To(MatchError(workflow.ErrVersionNotFound))
	})
})
