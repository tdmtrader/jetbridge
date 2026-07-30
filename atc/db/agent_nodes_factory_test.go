package db_test

import (
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentNodesFactory", func() {
	It("keeps node versions separate from workflow catalog reads", func() {
		nodes := db.NewAgentNodesFactory(dbConn)
		node, err := nodes.ImportManifest("code-review", workflow.Manifest{
			"node.yaml": `schema_version: 1
name: code-review
step:
  agent: review
  prompt: inspect
`,
		}, "alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(node.Version).To(Equal(1))
		Expect(node.Compiled.SchemaVersion).To(Equal(1))
		Expect(node.Compiled.Function.SignatureVersion).To(Equal(1))

		workflows := db.NewAgentWorkflowsFactory(dbConn)
		definitions, err := workflows.List()
		Expect(err).NotTo(HaveOccurred())
		Expect(definitions).To(BeEmpty())
		_, found, err := workflows.Get("code-review", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
