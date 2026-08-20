package present_test

import (
	"context"
	"encoding/json"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline presenter matrix", func() {
	It("presents stored templates, numbered payloads, and ordinary instances distinctly", func() {
		team, err := teamFactory.CreateTeam(atc.Team{Name: "team"})
		Expect(err).NotTo(HaveOccurred())

		template, _, err := team.SavePipeline(atc.PipelineRef{Name: "template"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{},
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())

		creation, err := db.NewPipelineRunFactory(dbConn, nil).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		Expect(template.Reload()).To(BeTrue())

		payload, found, err := team.Pipeline(atc.PipelineRef{
			Name:         "template",
			InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		ordinary, _, err := team.SavePipeline(atc.PipelineRef{
			Name:         "ordinary",
			InstanceVars: atc.InstanceVars{"run": float64(9)},
		}, atc.Config{Jobs: atc.JobConfigs{{Name: "entry"}}}, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())

		assertJSONFields(present.Pipeline(template, present.PipelineOptions{
			AuthorizedForParams: true,
			CanCreateRun:        false,
		}), map[string]any{
			"template":        true,
			"last_run_number": float64(1),
			"can_create_run":  false,
			"params_schema":   []any{},
		}, []string{"run_number", "run_template_ref"})

		assertJSONFields(present.Pipeline(payload, present.PipelineOptions{}), map[string]any{
			"template":   false,
			"run_number": float64(1),
			"run_template_ref": map[string]any{
				"team_name":     "team",
				"pipeline_name": "template",
			},
		}, []string{"params_schema", "last_run_number", "can_create_run"})

		assertJSONFields(present.Pipeline(ordinary, present.PipelineOptions{}), map[string]any{}, []string{
			"template", "run_number", "run_template_ref", "params_schema", "last_run_number", "can_create_run",
		})
	})

	It("omits template parameters from public responses", func() {
		team, err := teamFactory.CreateTeam(atc.Team{Name: "team"})
		Expect(err).NotTo(HaveOccurred())
		template, _, err := team.SavePipeline(atc.PipelineRef{Name: "template"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{},
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())

		assertJSONFields(present.Pipeline(template, present.PipelineOptions{
			AuthorizedForParams: false,
			CanCreateRun:        false,
		}), map[string]any{
			"template":        true,
			"last_run_number": float64(0),
			"can_create_run":  false,
		}, []string{"params_schema"})
	})

	It("uses the current base name after a rename without rewriting the payload identity", func() {
		team, err := teamFactory.CreateTeam(atc.Team{Name: "team"})
		Expect(err).NotTo(HaveOccurred())
		template, _, err := team.SavePipeline(atc.PipelineRef{Name: "before-rename"}, atc.Config{
			Template: true,
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())
		creation, err := db.NewPipelineRunFactory(dbConn, nil).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		childID, found := creation.Run.InstancePipelineID()
		Expect(found).To(BeTrue())
		_, err = dbConn.Exec("UPDATE pipelines SET name = $1 WHERE id = $2", "after-rename", template.ID())
		Expect(err).NotTo(HaveOccurred())

		payload, found, err := db.NewPipelineRef(childID, "", nil, dbConn, nil).Pipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		assertJSONFields(present.Pipeline(payload, present.PipelineOptions{}), map[string]any{
			"name":       "before-rename",
			"template":   false,
			"run_number": float64(1),
			"run_template_ref": map[string]any{
				"team_name":     "team",
				"pipeline_name": "after-rename",
			},
		}, nil)
	})
})

func assertJSONFields(pipeline atc.Pipeline, expected map[string]any, absent []string) {
	encoded, err := json.Marshal(pipeline)
	Expect(err).NotTo(HaveOccurred())

	var actual map[string]any
	Expect(json.Unmarshal(encoded, &actual)).To(Succeed())
	for field, value := range expected {
		Expect(actual).To(HaveKeyWithValue(field, value))
	}
	for _, field := range absent {
		Expect(actual).NotTo(HaveKey(field))
	}
}
