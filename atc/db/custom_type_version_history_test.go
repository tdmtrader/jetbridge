package db_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	gocache "github.com/patrickmn/go-cache"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("A custom resource type's image changing", func() {
	var (
		scenario      *dbtest.Scenario
		cacheUser     db.Build
		upstreamBuild db.Build

		resourceSource = atc.Source{"some": "source"}
	)

	// The type below is a registry-image type, as production keys it: once
	// lidar resolves it, it is planned as its image's repository@digest
	// (ResourceTypes' Deserialize, ResolvedImage, ImageForType), and every
	// resource config of the type is created under the resource cache of that
	// exact reference (db.PinnedImage): registry-image, {repository},
	// {digest}, no params. So a new digest in the same repository is a new
	// resource cache, a new config and a new global scope, whatever moved it:
	// a pushed image, an edit of the type's tag or params, or an older digest
	// selected.
	const repository = "registry.example/some-custom-type"

	imageAt := func(digest string) string {
		return repository + "@sha256:" + digest
	}

	pinnedImage := func(imageRef string) db.ResourceCache {
		resourceType, source, version := db.PinnedImage(imageRef)
		cache, err := resourceCacheFactory.FindOrCreateResourceCache(
			db.ForBuild(cacheUser.ID()), resourceType, version, source, nil, nil,
		)
		Expect(err).ToNot(HaveOccurred())
		return cache
	}

	configOnImage := func(imageRef string, source atc.Source) db.ResourceConfig {
		config, err := resourceConfigFactory.FindOrCreateResourceConfig("some-custom-type", source, pinnedImage(imageRef))
		Expect(err).ToNot(HaveOccurred())
		return config
	}

	configOnDigest := func(digest string, source atc.Source) db.ResourceConfig {
		return configOnImage(imageAt(digest), source)
	}

	attach := func(resource db.Resource, config db.ResourceConfig) db.ResourceConfigScope {
		scope, err := config.FindOrCreateScope(intptr(resource.ID()))
		Expect(err).ToNot(HaveOccurred())
		Expect(resource.SetResourceConfigScope(scope)).To(Succeed())

		return scope
	}

	moveTo := func(resourceName string, config db.ResourceConfig) db.ResourceConfigScope {
		return attach(scenario.Resource(resourceName), config)
	}

	versionsOf := func(scopeID int) []string {
		rows, err := dbConn.Query(`
			SELECT version->>'ref' FROM resource_config_versions
			WHERE resource_config_scope_id = $1
			ORDER BY check_order
		`, scopeID)
		Expect(err).ToNot(HaveOccurred())
		defer rows.Close()

		refs := []string{}
		for rows.Next() {
			var ref string
			Expect(rows.Scan(&ref)).To(Succeed())
			refs = append(refs, ref)
		}

		return refs
	}

	deprecated := func(scopeID int) bool {
		var isDeprecated bool
		Expect(dbConn.QueryRow(`
			SELECT deprecated_at IS NOT NULL FROM resource_config_scopes WHERE id = $1
		`, scopeID).Scan(&isDeprecated)).To(Succeed())

		return isDeprecated
	}

	pipelineConfig := atc.Config{
		ResourceTypes: atc.ResourceTypes{
			{
				Name:   "some-custom-type",
				Type:   "registry-image",
				Source: atc.Source{"repository": repository},
			},
		},
		Resources: atc.ResourceConfigs{
			{
				Name:   "some-resource",
				Type:   "some-custom-type",
				Source: resourceSource,
			},
			{
				Name:   "same-resource",
				Type:   "some-custom-type",
				Source: resourceSource,
			},
		},
		Jobs: atc.JobConfigs{
			{
				Name: "upstream-job",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "some-resource"}},
				},
			},
			{
				Name: "downstream-job",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "some-resource", Passed: []string{"upstream-job"}}},
				},
			},
		},
	}

	BeforeEach(func() {
		scenario = dbtest.Setup(
			builder.WithPipeline(pipelineConfig),
			builder.WithBaseWorker(),
		)

		var err error
		cacheUser, err = scenario.Team.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())

		// A check against the type's first digest, d0, found v1..v3.
		scope := moveTo("some-resource", configOnDigest("d0", resourceSource))
		Expect(scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v1"}, {"ref": "v2"}, {"ref": "v3"}})).To(Succeed())

		scenario.Run(
			builder.WithJobBuild(&upstreamBuild, "upstream-job", dbtest.JobInputs{
				{
					Name:    "some-resource",
					Version: atc.Version{"ref": "v2"},
				},
			}, dbtest.JobOutputs{}),
		)
	})

	Context("when the resource is checked against the type's new digest", func() {
		var (
			oldConfigID int
			oldScopeID  int
			newScope    db.ResourceConfigScope
		)

		BeforeEach(func() {
			oldConfigID = scenario.Resource("some-resource").ResourceConfigID()
			oldScopeID = scenario.Resource("some-resource").ResourceConfigScopeID()

			newScope = moveTo("some-resource", configOnDigest("d1", resourceSource))
			Expect(newScope.ID()).ToNot(Equal(oldScopeID))
			Expect(newScope.ResourceConfig().ID()).ToNot(Equal(oldConfigID))
		})

		It("carries the old versions into the new scope, in order", func() {
			Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).To(Equal(newScope.ID()))
			Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))

			latest, found, err := newScope.LatestVersion()
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(latest.Version()).To(Equal(db.Version{"ref": "v3"}))
		})

		It("ranks the carried versions below the ones the new image's check finds", func() {
			Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v3"}, {"ref": "v4"}})).To(Succeed())

			Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4"}))
		})

		It("keeps a passed constraint on an earlier build satisfiable", func() {
			versionsDB := db.NewVersionsDB(dbConn, 100, gocache.New(10*time.Second, 10*time.Second))
			resourceID := scenario.Resource("some-resource").ID()

			outputs, err := versionsDB.SuccessfulBuildOutputs(context.TODO(), upstreamBuild.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(outputs).To(HaveLen(1))
			Expect(outputs[0].ResourceID).To(Equal(resourceID))

			exists, err := versionsDB.VersionExists(context.TODO(), resourceID, outputs[0].Version)
			Expect(err).ToNot(HaveOccurred())
			Expect(exists).To(BeTrue())
		})

		It("deprecates the old scope, listed for the resource", func() {
			Expect(deprecated(oldScopeID)).To(BeTrue())

			deprecatedScopes, err := scenario.Resource("some-resource").DeprecatedScopes()
			Expect(err).ToNot(HaveOccurred())
			Expect(deprecatedScopes).To(HaveLen(1))
			Expect(deprecatedScopes[0].ID).To(Equal(oldScopeID))
		})

		It("survives the old config outliving its grace period", func() {
			_, err := dbConn.Exec(`UPDATE resource_configs SET last_referenced = now() - interval '2 hours' WHERE id = $1`, oldConfigID)
			Expect(err).ToNot(HaveOccurred())

			Expect(resourceConfigFactory.CleanUnreferencedConfigs(0)).To(Succeed())

			Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
			Expect(versionsOf(oldScopeID)).To(Equal([]string{"v1", "v2", "v3"}))

			By("and the old scope being collected")
			_, err = dbConn.Exec(`DELETE FROM resource_config_scopes WHERE id = $1`, oldScopeID)
			Expect(err).ToNot(HaveOccurred())
			Expect(resourceConfigFactory.CleanUnreferencedConfigs(0)).To(Succeed())

			_, found, err := resourceConfigFactory.FindResourceConfigByID(oldConfigID)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())

			Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).To(Equal(newScope.ID()))
			Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
		})

		It("carries the history on through a second digest change", func() {
			Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v4"}})).To(Succeed())

			thirdScope := moveTo("some-resource", configOnDigest("d2", resourceSource))

			Expect(versionsOf(thirdScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4"}))
			Expect(deprecated(newScope.ID())).To(BeTrue())
			Expect(deprecated(oldScopeID)).To(BeTrue())
		})

		It("carries the history back if a check still runs against the old digest", func() {
			Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v4"}})).To(Succeed())

			revivedScope := moveTo("some-resource", configOnDigest("d0", resourceSource))
			Expect(revivedScope.ID()).ToNot(Equal(oldScopeID))
			Expect(revivedScope.ResourceConfig().ID()).To(Equal(oldConfigID))

			Expect(versionsOf(revivedScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4"}))
		})
	})

	Context("when another active resource still uses the old scope", func() {
		var oldScopeID int

		BeforeEach(func() {
			oldScopeID = scenario.Resource("some-resource").ResourceConfigScopeID()

			Expect(moveTo("same-resource", configOnDigest("d0", resourceSource)).ID()).To(Equal(oldScopeID))
		})

		It("leaves the old scope live until the last resource has moved", func() {
			newConfig := configOnDigest("d1", resourceSource)

			newScope := moveTo("some-resource", newConfig)
			Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
			Expect(deprecated(oldScopeID)).To(BeFalse())
			Expect(scenario.Resource("same-resource").ResourceConfigScopeID()).To(Equal(oldScopeID))

			Expect(moveTo("same-resource", newConfig).ID()).To(Equal(newScope.ID()))
			Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
			Expect(deprecated(oldScopeID)).To(BeTrue())

			deprecatedScopes, err := scenario.Resource("same-resource").DeprecatedScopes()
			Expect(err).ToNot(HaveOccurred())
			Expect(deprecatedScopes).To(HaveLen(1))
		})

		Context("when the old scope finds a version after the first resource has moved", func() {
			var newScope db.ResourceConfigScope

			BeforeEach(func() {
				newScope = moveTo("some-resource", configOnDigest("d1", resourceSource))
				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))

				// same-resource is still checked against the old digest.
				oldScope, err := configOnDigest("d0", resourceSource).FindOrCreateScope(intptr(scenario.Resource("same-resource").ID()))
				Expect(err).ToNot(HaveOccurred())
				Expect(oldScope.ID()).To(Equal(oldScopeID))
				Expect(oldScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v3"}, {"ref": "v4"}})).To(Succeed())
				Expect(versionsOf(oldScopeID)).To(Equal([]string{"v1", "v2", "v3", "v4"}))
			})

			It("carries the newer version in above the ones already carried", func() {
				moveTo("same-resource", configOnDigest("d1", resourceSource))

				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4"}))

				latest, found, err := newScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(latest.Version()).To(Equal(db.Version{"ref": "v4"}))

				By("and a following check keeps that order")
				Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v3"}, {"ref": "v4"}})).To(Succeed())
				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4"}))
			})

			It("carries it in below a version the new image's check found meanwhile", func() {
				Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v3"}, {"ref": "v5"}})).To(Succeed())

				moveTo("same-resource", configOnDigest("d1", resourceSource))

				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4", "v5"}))

				latest, found, err := newScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(latest.Version()).To(Equal(db.Version{"ref": "v5"}))
			})
		})
	})

	// Another team's pipeline declares the same type on the same repository,
	// so its resources with the same source share global scopes with this
	// pipeline's: a scope is keyed on the config alone, whoever's it is.
	otherTeamsPipeline := func() *dbtest.Scenario {
		config := pipelineConfig
		config.ResourceTypes = append(atc.ResourceTypes{
			{
				// A type on the custom type, with the resource's source:
				// its own checks share the resource's global scope.
				Name:   "nested-type",
				Type:   "some-custom-type",
				Source: resourceSource,
			},
		}, pipelineConfig.ResourceTypes...)

		return dbtest.Setup(builder.WithPipeline(config))
	}

	sameTeamsOtherPipeline := func() db.Pipeline {
		pipeline, _, err := scenario.Team.SavePipeline(atc.PipelineRef{Name: "other-pipeline"}, pipelineConfig, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())
		return pipeline
	}

	resourceOf := func(pipeline db.Pipeline, name string) db.Resource {
		resource, found, err := pipeline.Resource(name)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		return resource
	}

	Context("when another team's resource uses the history the resource moves to", func() {
		var (
			oldScopeID  int
			otherTeam   *dbtest.Scenario
			otherScope  db.ResourceConfigScope
			otherTeams  db.Resource
			carriedRefs = []string{"v1", "v2", "v3"}
		)

		BeforeEach(func() {
			oldScopeID = scenario.Resource("some-resource").ResourceConfigScopeID()

			otherTeam = otherTeamsPipeline()
			otherTeams = otherTeam.Resource("some-resource")
		})

		expectRefused := func(destination db.ResourceConfigScope, destinationRefs []string) {
			GinkgoHelper()

			Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).To(Equal(destination.ID()))
			Expect(versionsOf(destination.ID())).To(Equal(destinationRefs))
			Expect(versionsOf(oldScopeID)).To(Equal(carriedRefs))
			Expect(deprecated(oldScopeID)).To(BeFalse())

			deprecatedScopes, err := scenario.Resource("some-resource").DeprecatedScopes()
			Expect(err).ToNot(HaveOccurred())
			Expect(deprecatedScopes).To(BeEmpty())
		}

		Context("when the type's digest moves to the one the other team's resource is on (someone pushed to the repository)", func() {
			BeforeEach(func() {
				otherScope = attach(otherTeams, configOnDigest("d1", resourceSource))
				Expect(otherScope.ResourceID()).To(BeNil())
				Expect(otherScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "o1"}, {"ref": "o2"}})).To(Succeed())
			})

			It("refuses the carry on a check, and leaves both histories as they were", func() {
				Expect(moveTo("some-resource", configOnDigest("d1", resourceSource)).ID()).To(Equal(otherScope.ID()))

				expectRefused(otherScope, []string{"o1", "o2"})
				Expect(otherTeam.Resource("some-resource").ResourceConfigScopeID()).To(Equal(otherScope.ID()))
			})

			It("refuses the carry on a put, which saves only its own version", func() {
				Expect(upstreamBuild.SaveOutput("some-custom-type", pinnedImage(imageAt("d1")), resourceSource, atc.Version{"ref": "put"}, nil, "some-output", "some-resource")).To(Succeed())

				expectRefused(otherScope, []string{"o1", "o2", "put"})
			})

			It("refuses the carry through the pipeline's own move of the resource", func() {
				scope, err := configOnDigest("d1", resourceSource).FindOrCreateScope(nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(scenario.Pipeline.SetResourceConfigScopeForResource("some-resource", scope)).To(Succeed())

				expectRefused(otherScope, []string{"o1", "o2"})
			})

			It("refuses it too when the other team's resource is no longer active", func() {
				_, err := dbConn.Exec(`UPDATE resources SET active = false WHERE id = $1`, otherTeams.ID())
				Expect(err).ToNot(HaveOccurred())

				moveTo("some-resource", configOnDigest("d1", resourceSource))

				expectRefused(otherScope, []string{"o1", "o2"})
			})
		})

		Context("when an older digest the other team's resource is on is selected", func() {
			var newScope db.ResourceConfigScope

			BeforeEach(func() {
				// Both teams' resources started on d0. This one moved on to
				// d1, alone there, so its history carried, and found v4.
				Expect(attach(otherTeams, configOnDigest("d0", resourceSource)).ID()).To(Equal(oldScopeID))

				newScope = moveTo("some-resource", configOnDigest("d1", resourceSource))
				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
				Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v4"}})).To(Succeed())
				Expect(deprecated(oldScopeID)).To(BeFalse())
			})

			It("refuses the carry back, and leaves the other team's history without the newer version", func() {
				Expect(moveTo("some-resource", configOnDigest("d0", resourceSource)).ID()).To(Equal(oldScopeID))

				Expect(versionsOf(oldScopeID)).To(Equal([]string{"v1", "v2", "v3"}))
				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4"}))
				Expect(deprecated(newScope.ID())).To(BeFalse())
			})
		})

		Context("when only another team's resource type uses the history", func() {
			BeforeEach(func() {
				otherScope = attach(otherTeams, configOnDigest("d1", resourceSource))
				Expect(otherScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "o1"}})).To(Succeed())

				nestedType := otherTeam.ResourceType("nested-type")
				Expect(nestedType.SetResourceConfigScope(otherScope)).To(Succeed())

				// Detach the other team's resource, so the type alone uses it.
				_, err := dbConn.Exec(`UPDATE resources SET resource_config_id = NULL, resource_config_scope_id = NULL WHERE id = $1`, otherTeams.ID())
				Expect(err).ToNot(HaveOccurred())
			})

			It("refuses the carry", func() {
				Expect(moveTo("some-resource", configOnDigest("d1", resourceSource)).ID()).To(Equal(otherScope.ID()))

				expectRefused(otherScope, []string{"o1"})
			})
		})

		Context("when the other team's resource attaches as the resource moves", func() {
			It("waits for the attachment, and then refuses the carry", func() {
				destination, err := configOnDigest("d1", resourceSource).FindOrCreateScope(nil)
				Expect(err).ToNot(HaveOccurred())

				// The other team's move, part way through on its own
				// connection: its resource points at the destination, and it
				// has not committed.
				otherConn := postgresRunner.OpenConn()
				defer otherConn.Close()
				otherMove, err := otherConn.Begin()
				Expect(err).ToNot(HaveOccurred())
				defer otherMove.Rollback()
				_, err = otherMove.Exec(`
					UPDATE resources SET resource_config_id = $2, resource_config_scope_id = $3 WHERE id = $1
				`, otherTeams.ID(), destination.ResourceConfig().ID(), destination.ID())
				Expect(err).ToNot(HaveOccurred())

				moved := make(chan error, 1)
				go func() {
					defer GinkgoRecover()
					moved <- scenario.Resource("some-resource").SetResourceConfigScope(destination)
				}()

				Consistently(moved, time.Second).ShouldNot(Receive())
				Expect(otherMove.Commit()).To(Succeed())
				Eventually(moved, 10*time.Second).Should(Receive(BeNil()))

				expectRefused(destination, []string{})
			})
		})

		Context("when another team's resource type attaches as the resource's history is carried", func() {
			It("waits for the carry to commit", func() {
				destination, err := configOnDigest("d1", resourceSource).FindOrCreateScope(nil)
				Expect(err).ToNot(HaveOccurred())

				// A carry or copy into the destination holds it FOR UPDATE
				// until it commits.
				holderConn := postgresRunner.OpenConn()
				defer holderConn.Close()
				holder, err := holderConn.Begin()
				Expect(err).ToNot(HaveOccurred())
				defer holder.Rollback()
				_, err = holder.Exec(`SELECT id FROM resource_config_scopes WHERE id = $1 FOR UPDATE`, destination.ID())
				Expect(err).ToNot(HaveOccurred())

				attached := make(chan error, 1)
				go func() {
					defer GinkgoRecover()
					attached <- otherTeam.ResourceType("nested-type").SetResourceConfigScope(destination)
				}()

				Consistently(attached, time.Second).ShouldNot(Receive())
				Expect(holder.Commit()).To(Succeed())
				Eventually(attached, 10*time.Second).Should(Receive(BeNil()))
			})
		})
	})

	Context("when only the same team's resources use the history the resource moves to", func() {
		It("carries the history, whichever of the team's pipelines they are in", func() {
			oldScopeID := scenario.Resource("some-resource").ResourceConfigScopeID()

			otherScope := attach(resourceOf(sameTeamsOtherPipeline(), "some-resource"), configOnDigest("d1", resourceSource))
			Expect(otherScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "o1"}})).To(Succeed())

			Expect(moveTo("some-resource", configOnDigest("d1", resourceSource)).ID()).To(Equal(otherScope.ID()))

			Expect(versionsOf(otherScope.ID())).To(Equal([]string{"v1", "v2", "v3", "o1"}))
			Expect(deprecated(oldScopeID)).To(BeTrue())
		})
	})

	Context("when the resource's source changes too", func() {
		It("starts a new history and leaves the old scope alone", func() {
			oldScopeID := scenario.Resource("some-resource").ResourceConfigScopeID()

			newScope := moveTo("some-resource", configOnDigest("d1", atc.Source{"some": "other-source"}))

			Expect(versionsOf(newScope.ID())).To(BeEmpty())
			Expect(deprecated(oldScopeID)).To(BeFalse())
		})
	})

	Context("when the type's image moves to another repository", func() {
		It("carries nothing into the scope that repository's image already holds", func() {
			// Another pipeline's type, on an image in a repository this
			// pipeline does not control, checks the same resource source into
			// a global scope.
			otherConfig := configOnImage("registry.example/another-type@sha256:a0", resourceSource)
			otherScope, err := otherConfig.FindOrCreateScope(nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(otherScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "b1"}, {"ref": "b2"}})).To(Succeed())

			oldScopeID := scenario.Resource("some-resource").ResourceConfigScopeID()
			Expect(versionsOf(oldScopeID)).To(Equal([]string{"v1", "v2", "v3"}))

			Expect(moveTo("some-resource", otherConfig).ID()).To(Equal(otherScope.ID()))

			Expect(versionsOf(otherScope.ID())).To(Equal([]string{"b1", "b2"}))
			Expect(deprecated(oldScopeID)).To(BeFalse())
		})
	})

	Context("when the type is pinned by tag alone and the tag changes", func() {
		It("starts a new history and leaves the old scope alone", func() {
			// A direct image: reference without a digest is keyed on its
			// repository and tag, so another tag is another image.
			oldScope := moveTo("some-resource", configOnImage(repository+":1.0", resourceSource))
			Expect(oldScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "t1"}})).To(Succeed())

			newScope := moveTo("some-resource", configOnImage(repository+":2.0", resourceSource))
			Expect(newScope.ID()).ToNot(Equal(oldScope.ID()))

			Expect(versionsOf(newScope.ID())).To(BeEmpty())
			Expect(deprecated(oldScope.ID())).To(BeFalse())
		})
	})

	Context("when the type's image is fetched by a check and get", func() {
		// A type ResolvedImage cannot pin (one whose repository holds a
		// ((var)), or that is not a registry-image type) is planned as a
		// check and get of its image instead, and keyed on that get's
		// resource cache: the type's base type, source and params, and the
		// image version.
		typeSource := atc.Source{"some": "type-source"}

		configOnFetchedImage := func(typeSource atc.Source, typeParams atc.Params, imageVersion string) db.ResourceConfig {
			imageCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(cacheUser.ID()),
				dbtest.BaseResourceType,
				atc.Version{"custom-type": imageVersion},
				typeSource,
				typeParams,
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			config, err := resourceConfigFactory.FindOrCreateResourceConfig("some-custom-type", resourceSource, imageCache)
			Expect(err).ToNot(HaveOccurred())

			return config
		}

		var fetchedScope db.ResourceConfigScope

		BeforeEach(func() {
			fetchedScope = moveTo("some-resource", configOnFetchedImage(typeSource, nil, "1"))
			Expect(versionsOf(fetchedScope.ID())).To(BeEmpty())
			Expect(fetchedScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "f1"}, {"ref": "f2"}})).To(Succeed())
		})

		It("carries the history to a new image version", func() {
			newScope := moveTo("some-resource", configOnFetchedImage(typeSource, nil, "2"))

			Expect(versionsOf(newScope.ID())).To(Equal([]string{"f1", "f2"}))
			Expect(deprecated(fetchedScope.ID())).To(BeTrue())
		})

		It("starts a new history when the type's own source changes", func() {
			newScope := moveTo("some-resource", configOnFetchedImage(atc.Source{"some": "other-type-source"}, nil, "2"))

			Expect(versionsOf(newScope.ID())).To(BeEmpty())
			Expect(deprecated(fetchedScope.ID())).To(BeFalse())
		})

		It("starts a new history when the type's params change", func() {
			newScope := moveTo("some-resource", configOnFetchedImage(typeSource, atc.Params{"some": "param"}, "1"))
			Expect(newScope.ID()).ToNot(Equal(fetchedScope.ID()))

			Expect(versionsOf(newScope.ID())).To(BeEmpty())
			Expect(deprecated(fetchedScope.ID())).To(BeFalse())
		})
	})

	Context("when the resource has a pinned and a disabled version", func() {
		BeforeEach(func() {
			resource := scenario.Resource("some-resource")

			v1, found, err := resource.FindVersion(atc.Version{"ref": "v1"})
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resource.DisableVersion(v1.ID())).To(Succeed())

			v2, found, err := resource.FindVersion(atc.Version{"ref": "v2"})
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			pinned, err := resource.PinVersion(v2.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(pinned).To(BeTrue())

			moveTo("some-resource", configOnDigest("d1", resourceSource))
		})

		It("still resolves the pinned version and still holds the disabled one back", func() {
			resource := scenario.Resource("some-resource")

			Expect(resource.CurrentPinnedVersion()).To(Equal(atc.Version{"ref": "v2"}))
			_, found, err := resource.FindVersion(resource.CurrentPinnedVersion())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			versions, _, found, err := resource.Versions(db.Page{Limit: 10}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			enabled := map[string]bool{}
			for _, v := range versions {
				enabled[v.Version["ref"]] = v.Enabled
			}
			Expect(enabled).To(Equal(map[string]bool{"v1": false, "v2": true, "v3": true}))
		})
	})

	Context("when a version: every job has used a version", func() {
		It("offers the version after it once the history is carried", func() {
			versionsDB := db.NewVersionsDB(dbConn, 100, gocache.New(10*time.Second, 10*time.Second))
			jobID := scenario.Job("upstream-job").ID()
			resourceID := scenario.Resource("some-resource").ID()

			before, _, found, err := versionsDB.NextEveryVersion(context.TODO(), jobID, resourceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			newScope := moveTo("some-resource", configOnDigest("d1", resourceSource))

			after, hasNext, found, err := versionsDB.NextEveryVersion(context.TODO(), jobID, resourceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(hasNext).To(BeFalse())
			Expect(after).To(Equal(before))

			var ref string
			Expect(dbConn.QueryRow(`
				SELECT version->>'ref' FROM resource_config_versions
				WHERE resource_config_scope_id = $1 AND $2 IN (version_md5, version_sha256)
			`, newScope.ID(), string(after)).Scan(&ref)).To(Succeed())
			Expect(ref).To(Equal("v3"))
		})
	})

	Context("when a put saves its output against the new digest", func() {
		It("carries the history below the put's version", func() {
			oldScopeID := scenario.Resource("some-resource").ResourceConfigScopeID()

			Expect(upstreamBuild.SaveOutput("some-custom-type", pinnedImage(imageAt("d1")), resourceSource, atc.Version{"ref": "put"}, nil, "some-output", "some-resource")).To(Succeed())

			resource := scenario.Resource("some-resource")
			Expect(resource.ResourceConfigScopeID()).ToNot(Equal(oldScopeID))
			Expect(versionsOf(resource.ResourceConfigScopeID())).To(Equal([]string{"v1", "v2", "v3", "put"}))
			Expect(deprecated(oldScopeID)).To(BeTrue())
		})
	})

	Describe("copying a deprecated scope's versions with copy-resource-versions", func() {
		var (
			oldScopeID  int
			sharedScope db.ResourceConfigScope
		)

		baseConfig := func(baseType string, source atc.Source) db.ResourceConfig {
			config, err := resourceConfigFactory.FindOrCreateResourceConfig(baseType, source, nil)
			Expect(err).ToNot(HaveOccurred())
			return config
		}

		BeforeEach(func() {
			oldScopeID = scenario.Resource("some-resource").ResourceConfigScopeID()

			// A resource on the base type, with the same source, would share
			// this global scope.
			var err error
			sharedScope, err = baseConfig(dbtest.BaseResourceType, resourceSource).FindOrCreateScope(nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(sharedScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "b1"}, {"ref": "b2"}, {"ref": "b3"}})).To(Succeed())
		})

		It("refuses a custom type's carried-from scope into a scope other resources share", func() {
			oldScope, err := configOnDigest("d0", resourceSource).FindOrCreateScope(intptr(scenario.Resource("some-resource").ID()))
			Expect(err).ToNot(HaveOccurred())
			Expect(oldScope.ID()).To(Equal(oldScopeID))
			Expect(oldScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "b3"}, {"ref": "EVIL"}})).To(Succeed())

			moveTo("some-resource", configOnDigest("d1", resourceSource))
			Expect(deprecated(oldScopeID)).To(BeTrue())

			Expect(moveTo("some-resource", baseConfig(dbtest.BaseResourceType, resourceSource)).ID()).To(Equal(sharedScope.ID()))

			resource := scenario.Resource("some-resource")
			listed, err := resource.DeprecatedScopes()
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).ToNot(BeEmpty())
			Expect(listed[0].ID).To(Equal(oldScopeID))

			_, err = resource.CopyVersionsFromScope(oldScopeID)
			Expect(err).To(MatchError(db.ErrCopyIntoSharedScope))
			Expect(versionsOf(sharedScope.ID())).To(Equal([]string{"b1", "b2", "b3"}))
		})

		It("refuses a unique history's scope into a scope other resources share", func() {
			resource := scenario.Resource("some-resource")

			uniqueScope := moveTo("some-resource", baseConfig("some-unique-base-resource-type", atc.Source{"some": "fork"}))
			Expect(uniqueScope.ResourceID()).ToNot(BeNil())
			Expect(uniqueScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "b3"}, {"ref": "EVIL"}})).To(Succeed())
			moveTo("some-resource", baseConfig("some-unique-base-resource-type", atc.Source{"some": "other-fork"}))
			Expect(deprecated(uniqueScope.ID())).To(BeTrue())

			Expect(moveTo("some-resource", baseConfig(dbtest.BaseResourceType, resourceSource)).ID()).To(Equal(sharedScope.ID()))

			resource = scenario.Resource("some-resource")
			_, err := resource.CopyVersionsFromScope(uniqueScope.ID())
			Expect(err).To(MatchError(db.ErrCopyIntoSharedScope))
			Expect(versionsOf(sharedScope.ID())).To(Equal([]string{"b1", "b2", "b3"}))
		})

		It("copies another digest's history into the shared scope", func() {
			newScope := moveTo("some-resource", configOnDigest("d1", resourceSource))
			_, err := dbConn.Exec(`DELETE FROM resource_config_versions WHERE resource_config_scope_id = $1`, newScope.ID())
			Expect(err).ToNot(HaveOccurred())

			copied, err := scenario.Resource("some-resource").CopyVersionsFromScope(oldScopeID)
			Expect(err).ToNot(HaveOccurred())
			Expect(copied).To(Equal(3))
			Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
		})

		Context("when the resource's current history is the same type image's", func() {
			var newScope db.ResourceConfigScope

			BeforeEach(func() {
				newScope = moveTo("some-resource", configOnDigest("d1", resourceSource))
				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
				Expect(deprecated(oldScopeID)).To(BeTrue())

				// The deprecated history holds a version the current one lacks.
				_, err := dbConn.Exec(`
					INSERT INTO resource_config_versions (resource_config_scope_id, version, version_sha256, check_order)
					SELECT $1, '{"ref":"EVIL"}', encode(digest('{"ref":"EVIL"}', 'sha256'), 'hex'), max(check_order) + 1
					FROM resource_config_versions WHERE resource_config_scope_id = $1
				`, oldScopeID)
				Expect(err).ToNot(HaveOccurred())
				Expect(versionsOf(oldScopeID)).To(Equal([]string{"v1", "v2", "v3", "EVIL"}))
			})

			It("refuses copying into it once another team's resource uses it", func() {
				Expect(attach(otherTeamsPipeline().Resource("some-resource"), configOnDigest("d1", resourceSource)).ID()).To(Equal(newScope.ID()))

				_, err := scenario.Resource("some-resource").CopyVersionsFromScope(oldScopeID)
				Expect(err).To(MatchError(db.ErrCopyIntoAnotherTeamsHistory))
				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
			})

			It("refuses copying into it once another team's resource type uses it", func() {
				otherTeam := otherTeamsPipeline()
				Expect(otherTeam.ResourceType("nested-type").SetResourceConfigScope(newScope)).To(Succeed())

				_, err := scenario.Resource("some-resource").CopyVersionsFromScope(oldScopeID)
				Expect(err).To(MatchError(db.ErrCopyIntoAnotherTeamsHistory))
				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
			})

			It("copies into it while only the same team's resources use it", func() {
				Expect(attach(resourceOf(sameTeamsOtherPipeline(), "some-resource"), configOnDigest("d1", resourceSource)).ID()).To(Equal(newScope.ID()))

				copied, err := scenario.Resource("some-resource").CopyVersionsFromScope(oldScopeID)
				Expect(err).ToNot(HaveOccurred())
				Expect(copied).To(Equal(1))
				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3", "EVIL"}))
			})
		})

		It("copies a history of the same resource config into the shared scope", func() {
			newScope := moveTo("some-resource", configOnDigest("d1", resourceSource))
			Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v4"}})).To(Succeed())

			revivedScope := moveTo("some-resource", configOnDigest("d0", resourceSource))
			var oldConfigID int
			Expect(dbConn.QueryRow(`SELECT resource_config_id FROM resource_config_scopes WHERE id = $1`, oldScopeID).Scan(&oldConfigID)).To(Succeed())
			Expect(revivedScope.ID()).ToNot(Equal(oldScopeID))
			Expect(revivedScope.ResourceConfig().ID()).To(Equal(oldConfigID))
			_, err := dbConn.Exec(`DELETE FROM resource_config_versions WHERE resource_config_scope_id = $1`, revivedScope.ID())
			Expect(err).ToNot(HaveOccurred())

			copied, err := scenario.Resource("some-resource").CopyVersionsFromScope(oldScopeID)
			Expect(err).ToNot(HaveOccurred())
			Expect(copied).To(Equal(3))
			Expect(versionsOf(revivedScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))
		})
	})

	Context("while a move of the resource waits for a scope", func() {
		var (
			resource   db.Resource
			newScope   db.ResourceConfigScope
			imageCache db.ResourceCache
			holder     db.Tx
			moved      chan error
		)

		// Another transaction holds the new scope, so the move stops after
		// locking the resource row.
		BeforeEach(func() {
			resource = scenario.Resource("some-resource")

			imageCache = pinnedImage(imageAt("d1"))

			var err error
			newScope, err = configOnDigest("d1", resourceSource).FindOrCreateScope(intptr(resource.ID()))
			Expect(err).ToNot(HaveOccurred())

			holderConn := postgresRunner.OpenConn()
			DeferCleanup(holderConn.Close)
			holder, err = holderConn.Begin()
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = holder.Rollback() })
			_, err = holder.Exec(`SELECT id FROM resource_config_scopes WHERE id = $1 FOR UPDATE`, newScope.ID())
			Expect(err).ToNot(HaveOccurred())

			moved = make(chan error, 1)
		})

		// The scheduler's SaveNextInputMapping, for one, inserts rows whose
		// foreign key takes the resource row FOR KEY SHARE while it holds a
		// job that the move then asks to schedule.
		expectKeyShareNotBlocked := func() {
			Consistently(moved, 500*time.Millisecond).ShouldNot(Receive())

			otherConn := postgresRunner.OpenConn()
			defer otherConn.Close()
			inserted := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				_, err := otherConn.Exec(`
					INSERT INTO resource_disabled_versions (resource_id, version_digest) VALUES ($1, 'some-digest')
				`, resource.ID())
				inserted <- err
			}()
			Eventually(inserted, 5*time.Second).Should(Receive(BeNil()))

			Expect(holder.Commit()).To(Succeed())
			Eventually(moved, 10*time.Second).Should(Receive(BeNil()))
		}

		It("does not block a foreign key on the resource during a check's move", func() {
			go func() {
				defer GinkgoRecover()
				moved <- resource.SetResourceConfigScope(newScope)
			}()

			expectKeyShareNotBlocked()
		})

		It("does not block a foreign key on the resource during a put's move", func() {
			go func() {
				defer GinkgoRecover()
				moved <- upstreamBuild.SaveOutput("some-custom-type", imageCache, resourceSource, atc.Version{"ref": "put"}, nil, "some-output", "some-resource")
			}()

			expectKeyShareNotBlocked()
		})
	})

	Context("when a check of the new scope is saving versions as the resource moves", func() {
		It("waits for the check, and carries the history below what it saved", func() {
			resource := scenario.Resource("some-resource")

			newScope, err := configOnDigest("d1", resourceSource).FindOrCreateScope(intptr(resource.ID()))
			Expect(err).ToNot(HaveOccurred())
			Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "n1"}})).To(Succeed())

			// A check's SaveVersions, part way through on its own connection:
			// its new version is inserted and ranked, and it has not
			// committed.
			checkConn := postgresRunner.OpenConn()
			defer checkConn.Close()
			check, err := checkConn.Begin()
			Expect(err).ToNot(HaveOccurred())
			defer check.Rollback()
			_, err = check.Exec(`
				INSERT INTO resource_config_versions (resource_config_scope_id, version, version_sha256, check_order)
				SELECT $1, '{"ref":"n2"}', encode(digest('{"ref":"n2"}', 'sha256'), 'hex'), max(check_order) + 1
				FROM resource_config_versions WHERE resource_config_scope_id = $1
			`, newScope.ID())
			Expect(err).ToNot(HaveOccurred())

			moved := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				moved <- resource.SetResourceConfigScope(newScope)
			}()

			Consistently(moved, time.Second).ShouldNot(Receive())
			Expect(check.Commit()).To(Succeed())
			Eventually(moved, 10*time.Second).Should(Receive(BeNil()))

			Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3", "n1", "n2"}))

			var distinct, total int
			Expect(dbConn.QueryRow(`
				SELECT count(DISTINCT check_order), count(*) FROM resource_config_versions
				WHERE resource_config_scope_id = $1
			`, newScope.ID()).Scan(&distinct, &total)).To(Succeed())
			Expect(distinct).To(Equal(total))
		})
	})
})
