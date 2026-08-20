package db_test

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PipelineRunFactory", func() {
	var factory db.PipelineRunFactory

	BeforeEach(func() {
		factory = db.NewPipelineRunFactory(dbConn, lockFactory)
	})

	It("creates a complete run graph in the caller transaction", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "template"}, atc.Config{
			Template:  true,
			Params:    []atc.ParamSchema{{Name: "value", Type: atc.ParamTypeString, Required: true}},
			Resources: atc.ResourceConfigs{{Name: "input", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"}}},
			Jobs:      atc.JobConfigs{{Name: "entry-((value))"}, {Name: "downstream", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "input", Passed: []string{"entry-((value))"}, Trigger: true}}}}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		creation, err := factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{Vars: atc.RunParams{"value": "one"}}, "creator", db.RunCreationOpts{
			BeforeCommit: func(callbackTx db.Tx, got db.RunCreation) error {
				var headers, payloads, builds int
				Expect(callbackTx.QueryRow("SELECT count(*) FROM pipeline_runs WHERE id = $1", got.Run.ID()).Scan(&headers)).To(Succeed())
				Expect(callbackTx.QueryRow("SELECT count(*) FROM pipelines WHERE pipeline_run_id = $1", got.Run.ID()).Scan(&payloads)).To(Succeed())
				Expect(callbackTx.QueryRow("SELECT count(*) FROM builds WHERE pipeline_run_id = $1", got.Run.ID()).Scan(&builds)).To(Succeed())
				Expect(headers).To(Equal(1))
				Expect(payloads).To(Equal(1))
				Expect(builds).To(Equal(1))
				return nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(creation.Run.Number()).To(Equal(1))
		childID, found := creation.Run.InstancePipelineID()
		Expect(found).To(BeTrue())
		Expect(childID).To(BeNumerically(">", 0))
		Expect(creation.Config.Jobs[0].Name).To(Equal("entry-one"))
		Expect(creation.EntryJobs).To(Equal([]string{"entry-one"}))
		Expect(creation.EntryBuilds).To(HaveLen(1))
		Expect(creation.EntryBuilds[0].RunJobName()).To(Equal("entry-one"))
		Expect(creation.EntryBuilds[0].RunJobKey()).To(Equal("entry-((value))"))
		expectedHash := fmt.Sprintf("%x", sha256.Sum256(append([]byte("run-instance-config/v1\x00"), creation.CanonicalJSON...)))
		Expect(creation.ConfigHash).To(Equal(expectedHash))

		Expect(tx.Commit()).To(Succeed())
	})

	It("does not allocate a number when validation fails", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "invalid-run"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "required", Type: atc.ParamTypeString, Required: true}},
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		_, err = factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{}, "creator", db.RunCreationOpts{})
		Expect(err).To(MatchError(ContainSubstring("required")))
		var number, runs int
		Expect(tx.QueryRow("SELECT last_run_number FROM pipelines WHERE id = $1", template.ID()).Scan(&number)).To(Succeed())
		Expect(tx.QueryRow("SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1", template.ID()).Scan(&runs)).To(Succeed())
		Expect(number).To(Equal(0))
		Expect(runs).To(Equal(0))
	})

	It("rolls back all run rows when BeforeCommit rejects the creation", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "rollback-run"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{}, "creator", db.RunCreationOpts{BeforeCommit: func(db.Tx, db.RunCreation) error { return fmt.Errorf("stop") }})
		Expect(err).To(MatchError("stop"))
		Expect(tx.Rollback()).To(Succeed())
		var runs, payloads int
		Expect(dbConn.QueryRow("SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1", template.ID()).Scan(&runs)).To(Succeed())
		Expect(dbConn.QueryRow("SELECT count(*) FROM pipelines WHERE name = 'rollback-run' AND pipeline_run_id IS NOT NULL").Scan(&payloads)).To(Succeed())
		Expect(runs).To(Equal(0))
		Expect(payloads).To(Equal(0))
	})
})
