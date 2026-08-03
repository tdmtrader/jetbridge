package db_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceCaptureTemplateAssociation", func() {
	operationKey := strings.Repeat("a", 64)
	validName := "agent-resource-capture-" + operationKey[:24] + "-" + strings.Repeat("a", 12)

	type ownership struct {
		templateName string
		registerOwned,
		durableWorkflow,
		archived bool
		jobName string
	}

	// buildFor recreates the chain CreateRunForServerTemplate produces, so each
	// spec can knock out exactly one link and prove the association fails
	// closed on it.
	buildFor := func(own ownership) db.Build {
		GinkgoHelper()
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		var templateID, instanceID, pipelineRunID, jobID, buildID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering, template, archived)
			VALUES ($1, $2, 1, true, $3) RETURNING id
		`, own.templateName, defaultTeam.ID(), own.archived).Scan(&templateID)).To(Succeed())
		if own.registerOwned {
			_, err := dbConn.Exec(`INSERT INTO agent_workflow_run_templates (pipeline_id) VALUES ($1)`, templateID)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering, template, instance_vars)
			VALUES ($1, $2, 1, true, '{"run":1}') RETURNING id
		`, own.templateName, defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number, status)
			VALUES ($1, $2, 1, 'running') RETURNING id
		`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
		if own.durableWorkflow {
			var definitionID int
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_workflow_definitions
					(name, version, content_hash, definition, created_by, schema_version, signature_version)
				VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
				RETURNING id
			`, "capture-lookalike-"+suffix, "hash-"+suffix).Scan(&definitionID)).To(Succeed())
			_, err := dbConn.Exec(`
				INSERT INTO agent_workflow_runs
					(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
					 schema_version, signature_version, definition_content_hash, idempotency_key,
					 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
					 created_by, status, pipeline_run_id, template_pipeline_id, instance_pipeline_id,
					 concrete_config, concrete_config_hash)
				VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7, 'manual', '', 'alice', 'running',
				        $8, $9, $10, '{}', $11)
			`, defaultTeam.ID(), defaultTeam.Name(), definitionID, "capture-lookalike-"+suffix,
				strings.Repeat("a", 64), "run-"+suffix, strings.Repeat("b", 64),
				pipelineRunID, templateID, instanceID, strings.Repeat("c", 64))
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(dbConn.QueryRow(`
			INSERT INTO jobs (pipeline_id, name, config)
			VALUES ($1, $2, '{}') RETURNING id
		`, instanceID, own.jobName).Scan(&jobID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, pipeline_id, job_id)
			VALUES ($1, 'started', $2, $3, $4) RETURNING id
		`, "capture-"+suffix, defaultTeam.ID(), instanceID, jobID).Scan(&buildID)).To(Succeed())

		build, found, err := buildFactory.Build(buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		return build
	}

	sanctioned := ownership{templateName: validName, registerOwned: true, jobName: "capture"}

	It("authenticates the server-owned capture template that owns the build's run", func() {
		association, found, err := buildFor(sanctioned).ResourceCaptureTemplateAssociation()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(association.TemplateName).To(Equal(validName))
		Expect(association.TemplatePipelineID).To(BeNumerically(">", 0))
		Expect(association.Validate()).To(Succeed())
	})

	DescribeTable("refuses to authenticate a build that is not a server-owned capture",
		func(mutate func(*ownership)) {
			own := sanctioned
			mutate(&own)
			association, found, err := buildFor(own).ResourceCaptureTemplateAssociation()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(association).To(Equal(db.ResourceCaptureBuildAssociation{}))
		},
		// This is the forgery that matters: a user can name their own pipeline
		// anything, but they cannot get it registered as a server-owned
		// workflow-run template.
		Entry("template is not registered as server-owned", func(o *ownership) { o.registerOwned = false }),
		Entry("template name is not the reserved capture shape", func(o *ownership) {
			o.templateName = "my-own-capture-" + strings.Repeat("a", 12)
		}),
		Entry("template name has an uppercase operation key", func(o *ownership) {
			o.templateName = "agent-resource-capture-" + strings.ToUpper(operationKey[:24]) + "-" + strings.Repeat("a", 12)
		}),
		Entry("template name is missing its config-hash suffix", func(o *ownership) {
			o.templateName = "agent-resource-capture-" + operationKey[:24]
		}),
		Entry("template is also a durable workflow template", func(o *ownership) { o.durableWorkflow = true }),
		Entry("template is archived", func(o *ownership) { o.archived = true }),
		Entry("build is not the capture entry job", func(o *ownership) { o.jobName = "not-capture" }),
	)

	It("does not authenticate an ordinary build with no pipeline run at all", func() {
		build, err := defaultJob.CreateBuild("alice")
		Expect(err).NotTo(HaveOccurred())
		_, found, err := build.ResourceCaptureTemplateAssociation()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
