package db_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resource Factory", func() {
	var resourceFactory db.ResourceFactory

	BeforeEach(func() {
		resourceFactory = db.NewResourceFactory(dbConn, lockFactory)
	})

	Describe("Public And Private Resources", func() {
		var publicPipeline db.Pipeline

		BeforeEach(func() {
			otherTeam, err := teamFactory.CreateTeam(atc.Team{Name: "other-team"})
			Expect(err).NotTo(HaveOccurred())

			publicPipeline, _, err = otherTeam.SavePipeline(atc.PipelineRef{Name: "public-pipeline"}, atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "public-pipeline-resource"},
				},
			}, db.ConfigVersion(0), false)
			Expect(err).ToNot(HaveOccurred())
			Expect(publicPipeline.Expose()).To(Succeed())

			_, _, err = otherTeam.SavePipeline(atc.PipelineRef{Name: "private-pipeline"}, atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "private-pipeline-resource"},
				},
			}, db.ConfigVersion(0), false)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("VisibleResources", func() {
			It("returns resources in the provided teams and resources in public pipelines", func() {
				visibleResources, err := resourceFactory.VisibleResources([]string{"default-team"})
				Expect(err).ToNot(HaveOccurred())

				Expect(len(visibleResources)).To(Equal(2))
				Expect(visibleResources[0].Name()).To(Equal("some-resource"))
				Expect(visibleResources[1].Name()).To(Equal("public-pipeline-resource"))
			})

			It("returns team name and groups for each resource", func() {
				visibleResources, err := resourceFactory.VisibleResources([]string{"default-team"})
				Expect(err).ToNot(HaveOccurred())

				Expect(visibleResources[0].TeamName()).To(Equal("default-team"))
				Expect(visibleResources[1].TeamName()).To(Equal("other-team"))
			})

		})

		Context("AllResources", func() {
			It("returns all private and public resources from all teams", func() {
				visibleResources, err := resourceFactory.AllResources()
				Expect(err).ToNot(HaveOccurred())

				Expect(len(visibleResources)).To(Equal(3))
				Expect(visibleResources[0].Name()).To(Equal("some-resource"))
				Expect(visibleResources[1].Name()).To(Equal("public-pipeline-resource"))
				Expect(visibleResources[2].Name()).To(Equal("private-pipeline-resource"))
			})

			It("returns team name and groups for each resource", func() {
				visibleResources, err := resourceFactory.AllResources()
				Expect(err).ToNot(HaveOccurred())

				Expect(visibleResources[0].TeamName()).To(Equal("default-team"))
				Expect(visibleResources[1].TeamName()).To(Equal("other-team"))
			})
		})
	})
})
var _ = Describe("Resource Factory run payload exclusion", func() {
	var resourceFactory db.ResourceFactory

	resourceNames := func(resources []db.Resource) []string {
		names := make([]string, len(resources))
		for i, resource := range resources {
			names[i] = resource.Name()
		}
		return names
	}

	BeforeEach(func() {
		resourceFactory = db.NewResourceFactory(dbConn, lockFactory)

		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "run-template"}, atc.Config{
			Template:  true,
			Params:    []atc.ParamSchema{{Name: "value", Type: atc.ParamTypeString, Required: true}},
			Resources: atc.ResourceConfigs{{Name: "input-((value))", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"}}},
			Jobs: atc.JobConfigs{
				{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "input-((value))"}}}},
			},
		}, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())

		_, err = db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(
			context.Background(), template, db.RunParams{Vars: atc.RunParams{"value": "one"}}, "creator")
		Expect(err).NotTo(HaveOccurred())
	})

	It("keeps run payload resources out of AllResources", func() {
		// This fails if GET /api/v1/resources scales with run count instead of pipeline count.
		resources, err := resourceFactory.AllResources()
		Expect(err).NotTo(HaveOccurred())
		Expect(resourceNames(resources)).To(ContainElement("input-((value))"))
		Expect(resourceNames(resources)).NotTo(ContainElement("input-one"))
	})

	It("keeps run payload resources out of VisibleResources", func() {
		// This fails if the exclusion is applied to only one of the two enumerations.
		resources, err := resourceFactory.VisibleResources([]string{"default-team"})
		Expect(err).NotTo(HaveOccurred())
		Expect(resourceNames(resources)).To(ContainElement("input-((value))"))
		Expect(resourceNames(resources)).NotTo(ContainElement("input-one"))
	})
})
