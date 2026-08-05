package db_test

import (
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunTranscriptFactory", func() {
	var factory db.AgentRunTranscriptFactory

	BeforeEach(func() {
		factory = db.NewAgentRunTranscriptFactory(dbConn)
	})

	// createWorkflowRun materializes a durable workflow run (and the
	// definition it points at) so transcripts can carry the v3 execution
	// identity the ListByWorkflowRun join asserts.
	createWorkflowRun := func(workflowName, idempotencyKey string, buildID int) snapshot.WorkflowRunID {
		GinkgoHelper()
		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, workflowName, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())

		var runID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7,
			        'manual', '', 'alice', 'running', $8)
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, workflowName,
			strings.Repeat("a", 64), idempotencyKey, strings.Repeat("b", 64), buildID).Scan(&runID)).To(Succeed())

		return snapshot.WorkflowRunID(runID)
	}

	It("upserts on (build_id, plan_id): a re-ingest overwrites, never duplicates", func() {
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		runID := createWorkflowRun("upsert-workflow", "transcript-upsert-key", build.ID())

		nd := `{"type":"system","subtype":"init"}` + "\n" + `{"type":"result","total_cost_usd":0.4}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: build.ID(), PlanID: "aa11", WorkflowRunID: &runID,
			FunctionID: "implement", StepName: "implement",
			NDJSON: nd, ByteLen: len(nd), Truncated: false,
		})).To(Succeed())

		rows, err := factory.ListByWorkflowRun("upsert-workflow", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].NDJSON).To(Equal(nd))
		Expect(rows[0].ByteLen).To(Equal(len(nd)))
		Expect(rows[0].Truncated).To(BeFalse())
		Expect(rows[0].FunctionID).To(Equal("implement"))

		// second write of the same key overwrites, does not duplicate
		nd2 := nd + `{"type":"extra"}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: build.ID(), PlanID: "aa11", WorkflowRunID: &runID,
			FunctionID: "implement", StepName: "implement",
			NDJSON: nd2, ByteLen: len(nd2), Truncated: true,
		})).To(Succeed())

		rows, err = factory.ListByWorkflowRun("upsert-workflow", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].NDJSON).To(Equal(nd2))
		Expect(rows[0].Truncated).To(BeTrue())
	})

	It("returns nothing for a run that captured no transcript", func() {
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		runID := createWorkflowRun("empty-workflow", "transcript-empty-key", build.ID())

		rows, err := factory.ListByWorkflowRun("empty-workflow", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})

	It("lists transcripts by durable workflow run, surviving its build's deletion", func() {
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		runID := createWorkflowRun("code-review", "transcript-run-key", build.ID())

		nd := `{"type":"result","step":"review"}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: build.ID(), PlanID: "p1", WorkflowRunID: &runID, FunctionID: "review",
			StepName: "review-diff", NDJSON: nd, ByteLen: len(nd),
		})).To(Succeed())

		rows, err := factory.ListByWorkflowRun("code-review", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].WorkflowRunID).ToNot(BeNil())
		Expect(*rows[0].WorkflowRunID).To(Equal(runID))
		Expect(rows[0].FunctionID).To(Equal("review"))
		Expect(rows[0].StepName).To(Equal("review-diff"))
		Expect(rows[0].NDJSON).To(Equal(nd))

		By("returning nothing for a run id under the wrong workflow name")
		wrong, err := factory.ListByWorkflowRun("some-other-workflow", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(wrong).To(BeEmpty())

		By("keeping the transcript after its builds row is deleted")
		Expect(build.Delete()).To(BeTrue())
		rows, err = factory.ListByWorkflowRun("code-review", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].NDJSON).To(Equal(nd))
	})

	It("orders a run's transcripts oldest-first, with no step-name heuristic", func() {
		firstBuild, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		secondBuild, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		runID := createWorkflowRun("multi-step", "transcript-multi-key", firstBuild.ID())

		// 'implement' is written FIRST and 'spec' second: the retired harvest-era
		// heuristic would have hoisted 'implement' to the front regardless of
		// time; v3 orders purely by creation.
		first := `{"type":"result","step":"implement"}` + "\n"
		second := `{"type":"result","step":"spec"}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: firstBuild.ID(), PlanID: "implement-plan", WorkflowRunID: &runID, FunctionID: "implement",
			StepName: "implement", NDJSON: first, ByteLen: len(first),
		})).To(Succeed())
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: secondBuild.ID(), PlanID: "spec-plan", WorkflowRunID: &runID, FunctionID: "spec",
			StepName: "spec", NDJSON: second, ByteLen: len(second),
		})).To(Succeed())

		rows, err := factory.ListByWorkflowRun("multi-step", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0].PlanID).To(Equal("implement-plan"))
		Expect(rows[1].PlanID).To(Equal("spec-plan"))
	})

})
