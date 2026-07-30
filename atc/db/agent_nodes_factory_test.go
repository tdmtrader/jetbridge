package db_test

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func dbNodeManifest(name, prompt string, includeOutput, includeParameter, parameterDefault bool) workflow.Manifest {
	outputs := ""
	if includeOutput {
		outputs = "outputs:\n  - {name: result, type: review/v1}\n"
	}
	parameters := ""
	if includeParameter {
		parameters = "parameters:\n  - name: MODE\n"
		if parameterDefault {
			parameters += "    default: standard\n"
		}
	}
	return workflow.Manifest{workflow.NodeFileName: fmt.Sprintf(`schema_version: 1
name: %s
description: durable node
%s%sstep:
  agent: review
  prompt: %s
`, name, outputs, parameters, prompt)}
}

func dbNodeConsumerWorkflowManifest(name string, instances ...string) workflow.Manifest {
	plan := ""
	for _, instance := range instances {
		plan += fmt.Sprintf(`  - node: %s
    uses: consumer-node@1
    input_mapping: {repository: repository}
    output_mapping: {result: review}
    params: {MODE: strict}
`, instance)
	}
	return workflow.Manifest{workflow.WorkflowFileName: fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs:
  - {name: repository, type: repository/v1}
outputs:
  - {name: review, type: review/v1, from: review}
plan:
%s`, name, plan)}
}

func dbConsumerNodeManifest() workflow.Manifest {
	return workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: consumer-node
inputs:
  - {name: repository, type: repository/v1}
outputs:
  - {name: result, type: review/v1}
parameters:
  - {name: MODE, default: standard}
step:
  agent: review
  prompt: review
`}
}

var _ = Describe("AgentNodesFactory", func() {
	var (
		factory db.AgentNodesFactory
		ctx     context.Context
	)

	BeforeEach(func() {
		factory = db.NewAgentNodesFactory(dbConn)
		ctx = context.Background()
	})

	It("persists monotonic immutable versions and returns compiled exact, latest, list, and bounded history reads", func() {
		firstManifest := dbNodeManifest("node-history", "first", true, true, true)
		first, err := factory.ImportManifest("node-history", firstManifest, "alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Version).To(Equal(1))
		Expect(first.ContentHash).To(Equal(firstManifest.Hash()))

		replayed, err := factory.ImportManifest("node-history", firstManifest, "mallory")
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed.ID).To(Equal(first.ID))
		Expect(replayed.CreatedBy).To(Equal("alice"))

		second, err := factory.ImportManifest(
			"node-history",
			dbNodeManifest("node-history", "second", true, true, true),
			"bob",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Version).To(Equal(2))

		exact, found, err := factory.Get("node-history", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(exact.Compiled.Name).To(Equal("node-history"))
		Expect(exact.SourceManifest).NotTo(BeEmpty())

		latest, found, err := factory.Latest("node-history")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(latest.Version).To(Equal(2))
		Expect(latest.Compiled.Function.Plan).To(HaveLen(1))

		listed, err := factory.List()
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].Version).To(Equal(2))
		Expect(listed[0].Compiled.Name).To(Equal("node-history"))
		Expect(listed[0].SourceManifest).NotTo(BeEmpty())

		page, err := factory.Versions(ctx, "node-history", workflow.VersionPageRequest{Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Found).To(BeTrue())
		Expect(page.Definitions).To(HaveLen(1))
		Expect(page.Definitions[0].Version).To(Equal(2))
		Expect(page.Definitions[0].Compiled.Name).To(Equal("node-history"))
		Expect(page.NextCursor).To(Equal(2))

		older, err := factory.Versions(ctx, "node-history", workflow.VersionPageRequest{
			Cursor: page.NextCursor,
			Limit:  1,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(older.Definitions).To(HaveLen(1))
		Expect(older.Definitions[0].Version).To(Equal(1))
	})

	It("records immutable release predecessors, rejects false compatibility, and preserves direct lifecycle reads", func() {
		first, err := factory.ImportManifest(
			"node-release",
			dbNodeManifest("node-release", "first", true, true, true),
			"alice",
		)
		Expect(err).NotTo(HaveOccurred())
		release, err := factory.Release("node-release", first.Version, workflow.ReleaseCompatible, "alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(release.ReleasedBy).To(Equal("alice"))
		Expect(release.PredecessorVersion).To(BeZero())

		second, err := factory.ImportManifest(
			"node-release",
			dbNodeManifest("node-release", "second", false, false, false),
			"bob",
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Release("node-release", second.Version, workflow.ReleaseCompatible, "bob")
		Expect(err).To(MatchError(workflow.ErrInvalidCompatibility))

		breaking, err := factory.Release("node-release", second.Version, workflow.ReleaseBreaking, "bob")
		Expect(err).NotTo(HaveOccurred())
		Expect(breaking.PredecessorVersion).To(Equal(first.Version))
		Expect(breaking.Compatibility).To(Equal(workflow.ReleaseBreaking))

		previous, found, err := factory.Released("node-release", first.Version)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(previous.Release.ReleasedAt).To(BeNumerically(">", 0))

		Expect(factory.Deprecate("node-release", second.Version, true, "carol")).To(Succeed())
		deprecated, found, err := factory.Get("node-release", second.Version)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(deprecated.DeprecatedBy).To(Equal("carol"))
		Expect(deprecated.DeprecatedAt).To(BeNumerically(">", 0))
		Expect(deprecated.Compiled.Name).To(Equal("node-release"))

		Expect(factory.Deprecate("node-release", second.Version, false, "dave")).To(Succeed())
		restored, found, err := factory.Get("node-release", second.Version)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(restored.DeprecatedAt).To(BeZero())
		Expect(restored.DeprecatedBy).To(BeEmpty())
	})

	It("discovers durable workflow node consumers with a stable bounded cursor", func() {
		node, err := factory.ImportManifest(
			"consumer-node",
			dbConsumerNodeManifest(),
			"alice",
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Release(node.Name, node.Version, workflow.ReleaseCompatible, "alice")
		Expect(err).NotTo(HaveOccurred())

		workflows := db.NewAgentWorkflowsFactoryWithNodeResolver(
			dbConn,
			factory,
			workflowrun.WorkflowTargetRenderer{
				RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		)
		live, err := workflows.ImportManifest("consumer-live", dbNodeConsumerWorkflowManifest("consumer-live", "live-review"), "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = workflows.Promote(live.Name, live.Version, "alice")
		Expect(err).NotTo(HaveOccurred())
		historical, err := workflows.ImportManifest("consumer-history", dbNodeConsumerWorkflowManifest("consumer-history", "history-a", "history-b"), "bob")
		Expect(err).NotTo(HaveOccurred())

		first, err := factory.Consumers(ctx, node.Name, node.Version, workflow.NodeConsumerRequest{Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Consumers).To(HaveLen(1))
		Expect(first.NextCursor.WorkflowDefinitionID).To(BeNumerically(">", 0))
		Expect(first.Consumers[0].Binding.NodeDefinitionID).To(Equal(node.ID))
		Expect(first.Consumers[0].Binding.NodeContentHash).To(Equal(node.ContentHash))
		Expect(first.Consumers[0].Binding.InputMapping).To(Equal(map[string]string{"repository": "repository"}))
		Expect(first.Consumers[0].Binding.OutputMapping).To(Equal(map[string]string{"result": "review"}))
		Expect(first.Consumers[0].Binding.Parameters).To(Equal(map[string]string{"MODE": "strict"}))

		second, err := factory.Consumers(ctx, node.Name, node.Version, workflow.NodeConsumerRequest{Limit: 1, Cursor: first.NextCursor})
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Consumers).To(HaveLen(1))
		Expect(second.Consumers[0].WorkflowDefinitionID).To(Equal(first.Consumers[0].WorkflowDefinitionID))
		Expect(second.Consumers[0].Binding.InstanceName).NotTo(Equal(first.Consumers[0].Binding.InstanceName))
		third, err := factory.Consumers(ctx, node.Name, node.Version, workflow.NodeConsumerRequest{Limit: 1, Cursor: second.NextCursor})
		Expect(err).NotTo(HaveOccurred())
		Expect(third.Consumers).To(HaveLen(1))
		Expect(third.Consumers[0].WorkflowDefinitionID).To(Equal(live.ID))

		promoted, err := factory.Consumers(ctx, node.Name, node.Version, workflow.NodeConsumerRequest{Limit: 10, PromotedOnly: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(promoted.Consumers).To(HaveLen(1))
		Expect(promoted.Consumers[0].WorkflowDefinitionID).To(Equal(live.ID))
		Expect(promoted.Consumers[0].Live).To(BeTrue())

		bindings, err := factory.Bindings(historical.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bindings).To(ConsistOf(workflow.ResolvedNodeBinding{
			InstanceName: "history-a", NodeDefinitionID: node.ID, NodeName: node.Name,
			NodeVersion: node.Version, NodeContentHash: node.ContentHash,
			InputMapping:  map[string]string{"repository": "repository"},
			OutputMapping: map[string]string{"result": "review"}, Parameters: map[string]string{"MODE": "strict"},
		}, workflow.ResolvedNodeBinding{
			InstanceName: "history-b", NodeDefinitionID: node.ID, NodeName: node.Name,
			NodeVersion: node.Version, NodeContentHash: node.ContentHash,
			InputMapping:  map[string]string{"repository": "repository"},
			OutputMapping: map[string]string{"result": "review"}, Parameters: map[string]string{"MODE": "strict"},
		}))

		_, err = factory.Consumers(ctx, node.Name, node.Version, workflow.NodeConsumerRequest{Limit: 0})
		Expect(err).To(MatchError(workflow.ErrInvalidNodeConsumerPage))
		_, err = factory.Consumers(ctx, node.Name, node.Version, workflow.NodeConsumerRequest{
			Limit: 1, Cursor: workflow.NodeConsumerCursor{InstanceName: "not-a-cursor"},
		})
		Expect(err).To(MatchError(workflow.ErrInvalidNodeConsumerPage))
	})

	It("resolves reusable nodes while importing and promoting with a one-connection pool", func() {
		Expect(dbConn.Stats().MaxOpenConnections).To(Equal(1))

		node, err := factory.ImportManifest(
			"consumer-node",
			dbConsumerNodeManifest(),
			"alice",
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Release(node.Name, node.Version, workflow.ReleaseCompatible, "alice")
		Expect(err).NotTo(HaveOccurred())

		workflows := db.NewAgentWorkflowsFactoryWithNodeResolver(
			dbConn,
			factory,
			workflowrun.WorkflowTargetRenderer{
				RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		)
		first, err := workflows.ImportManifest(
			"transactional-consumer",
			dbNodeConsumerWorkflowManifest("transactional-consumer", "review-first"),
			"alice",
		)
		Expect(err).NotTo(HaveOccurred())
		second, err := workflows.ImportManifest(
			"transactional-consumer",
			dbNodeConsumerWorkflowManifest("transactional-consumer", "review-second"),
			"bob",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Version).To(Equal(first.Version + 1))

		_, err = workflows.Promote(second.Name, second.Version, "bob")
		Expect(err).NotTo(HaveOccurred())
	})

	It("serializes concurrent imports and releases per node name", func() {
		manifest := dbNodeManifest("node-concurrent", "same", true, true, true)
		const workers = 8
		definitions := make(chan *workflow.NodeDefinition, workers)
		errors := make(chan error, workers)
		var group sync.WaitGroup
		for index := 0; index < workers; index++ {
			group.Add(1)
			go func() {
				defer GinkgoRecover()
				defer group.Done()
				definition, err := factory.ImportManifest("node-concurrent", manifest, "author")
				definitions <- definition
				errors <- err
			}()
		}
		group.Wait()
		close(definitions)
		close(errors)
		for err := range errors {
			Expect(err).NotTo(HaveOccurred())
		}
		var id int
		for definition := range definitions {
			Expect(definition).NotTo(BeNil())
			if id == 0 {
				id = definition.ID
			}
			Expect(definition.ID).To(Equal(id))
			Expect(definition.Version).To(Equal(1))
		}

		releases := make(chan workflow.NodeRelease, workers)
		releaseErrors := make(chan error, workers)
		for index := 0; index < workers; index++ {
			group.Add(1)
			go func() {
				defer GinkgoRecover()
				defer group.Done()
				release, err := factory.Release("node-concurrent", 1, workflow.ReleaseCompatible, "releaser")
				releases <- release
				releaseErrors <- err
			}()
		}
		group.Wait()
		close(releases)
		close(releaseErrors)
		for err := range releaseErrors {
			Expect(err).NotTo(HaveOccurred())
		}
		var releasedAt int64
		for release := range releases {
			if releasedAt == 0 {
				releasedAt = release.ReleasedAt
			}
			Expect(release.ReleasedAt).To(Equal(releasedAt))
		}
	})

	It("rejects persisted runtime metadata and content identity corruption on every read surface", func() {
		schemaDefinition, err := factory.ImportManifest(
			"node-corrupt-schema",
			dbNodeManifest("node-corrupt-schema", "source", true, true, true),
			"alice",
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			UPDATE agent_workflow_definitions SET schema_version = 4 WHERE id = $1
		`, schemaDefinition.ID)
		Expect(err).To(HaveOccurred())

		for _, test := range []struct {
			name   string
			mutate string
		}{
			{
				name:   "signature",
				mutate: `signature_version = 2`,
			},
			{
				name: "name",
				mutate: `source_manifest = jsonb_build_object(
					'node.yaml',
					E'schema_version: 1\nname: different\nstep:\n  agent: review\n  prompt: source\n'
				)`,
			},
			{
				name:   "hash",
				mutate: `content_hash = '` + strings.Repeat("f", 64) + `'`,
			},
		} {
			name := "node-corrupt-" + test.name
			definition, err := factory.ImportManifest(name, dbNodeManifest(name, "source", true, true, true), "alice")
			Expect(err).NotTo(HaveOccurred())
			_, err = dbConn.Exec(`
				UPDATE agent_workflow_definitions SET `+test.mutate+`
				WHERE id = $1
			`, definition.ID)
			Expect(err).NotTo(HaveOccurred())

			_, _, err = factory.Get(name, 1)
			Expect(err).To(MatchError(ContainSubstring("stored metadata")))
			_, _, err = factory.Latest(name)
			Expect(err).To(MatchError(ContainSubstring("stored metadata")))
			_, err = factory.List()
			Expect(err).To(MatchError(ContainSubstring("stored metadata")))
			_, err = factory.Versions(ctx, name, workflow.VersionPageRequest{Limit: 10})
			Expect(err).To(MatchError(ContainSubstring("stored metadata")))
		}
	})

	It("keeps node versions separate from workflow catalog reads", func() {
		node, err := factory.ImportManifest(
			"code-review",
			dbNodeManifest("code-review", "inspect", true, true, true),
			"alice",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(node.Version).To(Equal(1))

		workflows := db.NewAgentWorkflowsFactory(dbConn)
		workflowDefinition, err := workflows.ImportManifest("code-review", dbFunctionManifest(
			"code-review", 1, nil, "review/v1", "workflow",
		), "bob")
		Expect(err).NotTo(HaveOccurred())
		Expect(workflowDefinition.Version).To(Equal(1))

		definitions, err := workflows.List()
		Expect(err).NotTo(HaveOccurred())
		Expect(definitions).To(ConsistOf(HaveField("ID", workflowDefinition.ID)))
		got, found, err := workflows.Get("code-review", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.ID).To(Equal(workflowDefinition.ID))
		_, found, err = workflows.Live("code-review")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		page, err := workflows.Versions(ctx, "code-review", workflow.VersionPageRequest{Limit: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Definitions).To(ConsistOf(HaveField("ID", workflowDefinition.ID)))
	})
})
