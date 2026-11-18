package db_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceConfig", func() {
	var resourceConfig db.ResourceConfig
	var otherResource db.Resource

	Context("when non-unique", func() {
		BeforeEach(func() {
			// Adding a resourceTypeConfig to create a scope. The reason behind is that,
			// when table "resource_config_scopes" is empty (just created), currentResourceConfigScopesIdSeq()
			// will return 1; after insert the first tuple, currentResourceConfigScopesIdSeq()
			// will also return 1. To work around the problem, adding a fake scope.
			resourceTypeConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
				defaultWorkerResourceType.Type,
				atc.Source{"some": "fake-source-type"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())
			_, err = resourceTypeConfig.FindOrCreateScope(defaultTeam.ID(), nil)
			Expect(err).ToNot(HaveOccurred())

			resourceConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
				defaultWorkerResourceType.Type,
				atc.Source{"some": "source"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			// Add a resource with the same config.
			otherPipeline, created, err := defaultTeam.SavePipeline(
				atc.PipelineRef{Name: "other-pipeline-with-resources"},
				atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name:   "other-resource",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "source"},
						},
					},
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name: "other-resource",
									},
								},
							},
						},
					},
				},
				0,
				false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			var found bool
			otherResource, found, err = otherPipeline.Resource("other-resource")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		Describe("FindOrCreateScope", func() {
			Context("given no resource", func() {
				It("finds or creates a global scope", func() {
					createdScope, err := resourceConfig.FindOrCreateScope(defaultTeam.ID(), nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(createdScope.ResourceID()).To(BeNil())
					Expect(createdScope.ResourceConfig().ID()).To(Equal(resourceConfig.ID()))
					seqAfterCreate, err := currentResourceConfigScopesIdSeq()
					Expect(err).ToNot(HaveOccurred())

					foundScope, err := resourceConfig.FindOrCreateScope(defaultTeam.ID(), nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(foundScope.ID()).To(Equal(createdScope.ID()))
					seqAfterFind, err := currentResourceConfigScopesIdSeq()
					Expect(err).ToNot(HaveOccurred())
					Expect(seqAfterCreate).To(Equal(seqAfterFind))
				})
			})

			Context("given a resource", func() {
				Context("sharing one version history", func() {
					It("finds or creates a global scope", func() {
						// create a scope with default resource
						seqBeforeCreate, err := currentResourceConfigScopesIdSeq()
						Expect(err).ToNot(HaveOccurred())
						createdScope, err := resourceConfig.FindOrCreateScope(defaultResource.TeamID(), intptr(defaultResource.ID()))
						Expect(err).ToNot(HaveOccurred())
						Expect(createdScope.ResourceID()).To(BeNil())
						Expect(createdScope.ResourceConfig().ID()).To(Equal(resourceConfig.ID()))
						seqAfterCreate, err := currentResourceConfigScopesIdSeq()
						Expect(err).ToNot(HaveOccurred())
						Expect(seqAfterCreate).To(Equal(seqBeforeCreate + 1))

						// should find the scope of default resource
						foundScope, err := resourceConfig.FindOrCreateScope(defaultResource.TeamID(), intptr(defaultResource.ID()))
						Expect(err).ToNot(HaveOccurred())
						Expect(foundScope.ID()).To(Equal(createdScope.ID()))
						seqAfterFind, err := currentResourceConfigScopesIdSeq()
						Expect(err).ToNot(HaveOccurred())
						Expect(seqAfterCreate).To(Equal(seqAfterFind))

						// should find the same scope even with a different resource id
						foundScope, err = resourceConfig.FindOrCreateScope(otherResource.TeamID(), intptr(otherResource.ID()))
						Expect(err).ToNot(HaveOccurred())
						Expect(foundScope.ID()).To(Equal(createdScope.ID()))
						seqAfterFind, err = currentResourceConfigScopesIdSeq()
						Expect(err).ToNot(HaveOccurred())
						Expect(seqAfterCreate).To(Equal(seqAfterFind))
					})
				})

				Context("of another team with the same config", func() {
					var anotherTeamsResource db.Resource

					BeforeEach(func() {
						scenario := dbtest.Setup(builder.WithPipeline(atc.Config{
							Resources: atc.ResourceConfigs{{
								Name:   "other-resource",
								Type:   "some-base-resource-type",
								Source: atc.Source{"some": "source"},
							}},
						}))
						anotherTeamsResource = scenario.Resource("other-resource")
						Expect(anotherTeamsResource.TeamID()).ToNot(Equal(defaultTeam.ID()))
					})

					It("finds or creates that team's own scope", func() {
						mine, err := resourceConfig.FindOrCreateScope(defaultResource.TeamID(), intptr(defaultResource.ID()))
						Expect(err).ToNot(HaveOccurred())

						theirs, err := resourceConfig.FindOrCreateScope(anotherTeamsResource.TeamID(), intptr(anotherTeamsResource.ID()))
						Expect(err).ToNot(HaveOccurred())
						Expect(theirs.ID()).ToNot(Equal(mine.ID()))
						Expect(theirs.ResourceID()).To(BeNil())

						again, err := resourceConfig.FindOrCreateScope(anotherTeamsResource.TeamID(), nil)
						Expect(err).ToNot(HaveOccurred())
						Expect(again.ID()).To(Equal(theirs.ID()))
					})

					It("refuses a resource of a team other than the one given", func() {
						_, err := resourceConfig.FindOrCreateScope(defaultTeam.ID(), intptr(anotherTeamsResource.ID()))
						Expect(err).To(MatchError(ContainSubstring("belongs to team")))
					})
				})
			})
		})
	})

	Context("when using a unique base resource type", func() {
		BeforeEach(func() {
			var err error
			resourceConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
				uniqueWorkerResourceType.Type,
				atc.Source{"some": "source"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		Describe("FindOrCreateScope", func() {
			Context("given no resource", func() {
				It("finds or creates a global scope", func() {
					createdScope, err := resourceConfig.FindOrCreateScope(defaultTeam.ID(), nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(createdScope.ResourceID()).To(BeNil())
					Expect(createdScope.ResourceConfig().ID()).To(Equal(resourceConfig.ID()))
					seqAfterCreate, err := currentResourceConfigScopesIdSeq()
					Expect(err).ToNot(HaveOccurred())

					foundScope, err := resourceConfig.FindOrCreateScope(defaultTeam.ID(), nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(foundScope.ID()).To(Equal(createdScope.ID()))
					seqAfterFind, err := currentResourceConfigScopesIdSeq()
					Expect(err).ToNot(HaveOccurred())
					Expect(seqAfterCreate).To(Equal(seqAfterFind))
				})
			})

			Context("given a resource", func() {
				It("finds or creates a unique scope", func() {
					createdScope, err := resourceConfig.FindOrCreateScope(defaultResource.TeamID(), intptr(defaultResource.ID()))
					Expect(err).ToNot(HaveOccurred())
					Expect(createdScope.ResourceID()).ToNot(BeNil())
					Expect(*createdScope.ResourceID()).To(Equal(defaultResource.ID()))
					Expect(createdScope.ResourceConfig().ID()).To(Equal(resourceConfig.ID()))
					seqAfterCreate, err := currentResourceConfigScopesIdSeq()
					Expect(err).ToNot(HaveOccurred())

					foundScope, err := resourceConfig.FindOrCreateScope(defaultResource.TeamID(), intptr(defaultResource.ID()))
					Expect(err).ToNot(HaveOccurred())
					Expect(foundScope.ID()).To(Equal(createdScope.ID()))
					seqAfterFind, err := currentResourceConfigScopesIdSeq()
					Expect(err).ToNot(HaveOccurred())
					Expect(seqAfterCreate).To(Equal(seqAfterFind))
				})
			})
		})
	})
})

func currentResourceConfigScopesIdSeq() (int, error) {
	var seq int
	row := psql.Select("last_value").From("resource_config_scopes_id_seq").RunWith(dbConn).QueryRow()
	err := row.Scan(&seq)
	return seq, err
}
