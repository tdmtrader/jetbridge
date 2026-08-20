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

var _ = Describe("Pipeline run presenter", func() {
	It("presents authorized empty payload fields and the actual enterable child reference", func() {
		team, err := teamFactory.CreateTeam(atc.Team{Name: "team"})
		Expect(err).NotTo(HaveOccurred())
		template, _, err := team.SavePipeline(atc.PipelineRef{Name: "template"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{},
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())

		factory := db.NewPipelineRunFactory(dbConn, nil)
		creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())

		run, found, err := factory.GetRun(template, creation.Run.Number())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		childID, found := run.InstancePipelineID()
		Expect(found).To(BeTrue())
		child, found, err := db.NewPipelineRef(childID, "", nil, dbConn, nil).Pipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		assertRunJSONFields(present.PipelineRun(emptyConfigHashRun{run}, child, present.PipelineRunOptions{
			AuthorizedForParams: true,
			CanEnterPayload:     true,
		}), map[string]any{
			"params":      map[string]any{},
			"config_hash": "",
			"reclaimed":   false,
			"instance_ref": map[string]any{
				"team_name":     "team",
				"pipeline_name": "template",
				"instance_vars": map[string]any{"run": float64(1)},
			},
		}, nil)

		assertRunJSONFields(present.PipelineRun(run, child, present.PipelineRunOptions{
			AuthorizedForParams: false,
			CanEnterPayload:     false,
		}), map[string]any{"reclaimed": false}, []string{"params", "config_hash", "instance_ref"})

		assertRunJSONFields(present.PipelineRun(run, nil, present.PipelineRunOptions{
			AuthorizedForParams: true,
			CanEnterPayload:     true,
		}), map[string]any{"reclaimed": true}, []string{"instance_ref"})
	})
})

// Pipeline-run configuration is immutable once stored. This wrapper preserves
// every stored model value except the legal empty wire boundary for config hash.
type emptyConfigHashRun struct{ db.PipelineRun }

func (emptyConfigHashRun) ConfigHash() string { return "" }

func assertRunJSONFields(run atc.PipelineRun, expected map[string]any, absent []string) {
	encoded, err := json.Marshal(run)
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
