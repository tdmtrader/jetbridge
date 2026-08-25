package db_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline run shared query scanners", func() {
	It("scans every pipeline scoped object of a live run payload", func() {
		// This fails if a shared query's column list and its scan function
		// disagree after the run presentation columns are dropped, and if
		// TaskCacheIdentity stops resolving the base template lazily.
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "scan-template"}, atc.Config{
			Template:      true,
			// The job must consume the resource: Concourse refuses a config
			// whose resource no job references ("resource 'x' is not used").
			Jobs: atc.JobConfigs{{
				Name:         "deploy",
				PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "scan-resource"}}},
			}},
			Resources:     atc.ResourceConfigs{{Name: "scan-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "source"}}},
			ResourceTypes: atc.ResourceTypes{{Name: "scan-type", Type: "some-base-resource-type", Source: atc.Source{"some": "source"}}},
			Prototypes:    atc.Prototypes{{Name: "scan-prototype", Type: "some-base-resource-type", Source: atc.Source{"some": "source"}}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())

		payload, found, err := defaultTeam.Pipeline(atc.PipelineRef{
			Name:         "scan-template",
			InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		// pipelinesQuery deliberately keeps run identity inline: the pipeline
		// list payload needs run_number and run_template_ref on every row.
		runNumber, hasRun := payload.RunNumber()
		Expect(hasRun).To(BeTrue())
		Expect(runNumber).To(Equal(creation.Run.Number()))
		Expect(payload.BasePipelineID()).To(Equal(template.ID()))

		// resourcesQuery / resourceTypesQuery / prototypesQuery / jobsQuery must
		// still scan cleanly now that the run presentation columns are gone.
		resources, err := payload.Resources()
		Expect(err).NotTo(HaveOccurred())
		Expect(resources).To(HaveLen(1))

		resourceTypes, err := payload.ResourceTypes()
		Expect(err).NotTo(HaveOccurred())
		Expect(resourceTypes).To(HaveLen(1))

		prototypes, err := payload.Prototypes()
		Expect(err).NotTo(HaveOccurred())
		Expect(prototypes).To(HaveLen(1))

		jobs, err := payload.Jobs()
		Expect(err).NotTo(HaveOccurred())
		Expect(jobs).To(HaveLen(1))

		// The base template is still resolvable for a run job -- now through the
		// pipeline rather than inline on jobsQuery.
		identity, err := jobs[0].TaskCacheIdentity()
		Expect(err).NotTo(HaveOccurred())
		Expect(identity).To(Equal(atc.TaskCacheIdentity{
			TeamID:             defaultTeam.ID(),
			TemplatePipelineID: template.ID(),
			RunJobName:         "deploy",
		}))
	})
})
