package db_test

import (
	"strings"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func dbBrokerCatalog(profileID, model string) *broker.Catalog {
	catalog, err := broker.NewCatalog([]broker.Profile{{
		ID: profileID, Revision: 1,
		Selector:           broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools:              []broker.Tool{broker.ToolConsultAgent},
		WorkerImage:        "registry.example/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:            broker.AdapterSpec{Name: broker.AdapterCodex, Version: "1.2.3"},
		Provider:           broker.ProviderSpec{Name: "provider", Model: model},
		NativeEffort:       "high",
		InstructionsDigest: "sha256:" + strings.Repeat("b", 64),
		CredentialSlot:     "shared",
		Limits:             broker.Limits{Timeout: time.Minute, MaxInputBytes: 1024, MaxOutputBytes: 1024},
		Controls: broker.Controls{
			ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true,
			NativeOutputSchema: true, IgnoresUserConfig: true,
		},
	}})
	Expect(err).NotTo(HaveOccurred())
	return catalog
}

func dbBrokerWorkflowManifest(name, prompt string) workflow.Manifest {
	return workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: ` + name + `
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: consult
    function_id: consult
    prompt: ` + prompt + `
    broker_profiles:
      - tool: consult_agent
        tier: balanced
        effort: high
`}
}

func dbBrokerNodeManifest(name string) workflow.Manifest {
	return workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: ` + name + `
step:
  agent: consult
  function_id: consult
  prompt: consult
  broker_profiles:
    - tool: consult_agent
      tier: balanced
      effort: high
`}
}

var _ = Describe("durable compiled broker definitions", func() {
	var (
		catalogA = dbBrokerCatalog("catalog-a", "model-a")
		catalogB = dbBrokerCatalog("catalog-b", "model-b")
		renderer = workflowrun.WorkflowTargetRenderer{
			RuntimeImage: "registry.example/agent-runner@sha256:" + strings.Repeat("c", 64),
		}
	)

	It("keeps the original workflow resolution across catalog changes, reads, promotion, and idempotent reimport", func() {
		source := dbBrokerWorkflowManifest("durable-broker-workflow", "first")
		factoryA := db.NewAgentWorkflowsFactoryWithBrokerCatalog(dbConn, catalogA, renderer)
		imported, err := factoryA.ImportManifest("durable-broker-workflow", source, "alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(imported.Compiled.Function.BrokerProfiles[0].Profile.ID).To(Equal("catalog-a"))

		factoryB := db.NewAgentWorkflowsFactoryWithBrokerCatalog(dbConn, catalogB, renderer)
		read, found, err := factoryB.Get("durable-broker-workflow", imported.Version)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(read.Compiled.Function.BrokerProfiles[0].Profile.ID).To(Equal("catalog-a"))

		replayed, err := factoryB.ImportManifest("durable-broker-workflow", source, "mallory")
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed.ID).To(Equal(imported.ID))
		Expect(replayed.Compiled.Function.BrokerProfiles[0].Profile.ID).To(Equal("catalog-a"))

		_, err = factoryB.Promote("durable-broker-workflow", imported.Version, "operator")
		Expect(err).NotTo(HaveOccurred())
		live, found, err := factoryB.Live("durable-broker-workflow")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(live.Compiled.Function.BrokerProfiles[0].Profile.ID).To(Equal("catalog-a"))

		changed, err := factoryB.ImportManifest(
			"durable-broker-workflow",
			dbBrokerWorkflowManifest("durable-broker-workflow", "second"),
			"bob",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed.Version).To(Equal(imported.Version + 1))
		Expect(changed.Compiled.Function.BrokerProfiles[0].Profile.ID).To(Equal("catalog-b"))
	})

	It("keeps a released node's original resolution when a workflow imports it under a different catalog", func() {
		nodesA := db.NewAgentNodesFactoryWithBrokerCatalog(dbConn, catalogA)
		node, err := nodesA.ImportManifest("durable-broker-node", dbBrokerNodeManifest("durable-broker-node"), "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = nodesA.Release(node.Name, node.Version, workflow.ReleaseBreaking, "operator")
		Expect(err).NotTo(HaveOccurred())

		nodesB := db.NewAgentNodesFactoryWithBrokerCatalog(dbConn, catalogB)
		read, found, err := nodesB.Get(node.Name, node.Version)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(read.Compiled.Function.BrokerProfiles[0].Profile.ID).To(Equal("catalog-a"))

		workflowsB := db.NewAgentWorkflowsFactoryWithNodeResolverAndBrokerCatalog(
			dbConn, nodesB, catalogB, renderer,
		)
		consumer, err := workflowsB.ImportManifest("durable-node-consumer", workflow.Manifest{
			workflow.WorkflowFileName: `schema_version: 3
name: durable-node-consumer
signature_version: 1
inputs: []
outputs: []
plan:
  - node: consult-instance
    uses: durable-broker-node@1
`,
		}, "bob")
		Expect(err).NotTo(HaveOccurred())
		Expect(consumer.Compiled.Function.BrokerProfiles).To(HaveLen(1))
		Expect(consumer.Compiled.Function.BrokerProfiles[0].Profile.ID).To(Equal("catalog-a"))
	})

	It("fails closed for malformed compiled bytes and legacy broker rows while retaining ordinary legacy fallback", func() {
		factoryA := db.NewAgentWorkflowsFactoryWithBrokerCatalog(dbConn, catalogA, renderer)
		brokered, err := factoryA.ImportManifest(
			"malformed-durable-broker",
			dbBrokerWorkflowManifest("malformed-durable-broker", "review"),
			"alice",
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_workflow_definitions SET compiled_definition='{"schema_version":3}'::jsonb WHERE id=$1`, brokered.ID)
		Expect(err).NotTo(HaveOccurred())
		_, _, err = factoryA.Get(brokered.Name, brokered.Version)
		Expect(err).To(MatchError(ContainSubstring("compiled definition")))

		legacyBroker, err := factoryA.ImportManifest(
			"legacy-durable-broker",
			dbBrokerWorkflowManifest("legacy-durable-broker", "review"),
			"alice",
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_workflow_definitions SET compiled_definition=NULL WHERE id=$1`, legacyBroker.ID)
		Expect(err).NotTo(HaveOccurred())
		_, _, err = factoryA.Get(legacyBroker.Name, legacyBroker.Version)
		Expect(err).To(MatchError(ContainSubstring("broker catalog is required")))

		ordinarySource := dbFunctionManifest("legacy-ordinary-workflow", 1, nil, "review/v1", "ordinary")
		ordinaryFactory := db.NewAgentWorkflowsFactory(dbConn, renderer)
		ordinary, err := ordinaryFactory.ImportManifest("legacy-ordinary-workflow", ordinarySource, "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_workflow_definitions SET compiled_definition=NULL WHERE id=$1`, ordinary.ID)
		Expect(err).NotTo(HaveOccurred())
		read, found, err := ordinaryFactory.Get(ordinary.Name, ordinary.Version)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(read.Compiled.Name).To(Equal(ordinary.Name))
	})
})
