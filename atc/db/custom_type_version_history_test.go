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
		upstreamBuild db.Build

		resourceSource = atc.Source{"some": "source"}
		typeSource     = atc.Source{"some": "type-source"}
	)

	// The resource config of a resource on a custom type is keyed on the
	// resource cache of the type's image, so a new image version is a new
	// resource cache, a new config and a new global scope.
	configOnType := func(typeSource atc.Source, typeParams atc.Params, imageVersion string, source atc.Source) db.ResourceConfig {
		imageCache, err := resourceCacheFactory.FindOrCreateResourceCache(
			db.ForBuild(upstreamBuild.ID()),
			dbtest.BaseResourceType,
			atc.Version{"custom-type": imageVersion},
			typeSource,
			typeParams,
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		config, err := resourceConfigFactory.FindOrCreateResourceConfig("some-custom-type", source, imageCache)
		Expect(err).ToNot(HaveOccurred())

		return config
	}

	configOnTypeImage := func(imageVersion string, source atc.Source) db.ResourceConfig {
		return configOnType(typeSource, nil, imageVersion, source)
	}

	moveTo := func(resourceName string, config db.ResourceConfig) db.ResourceConfigScope {
		resource := scenario.Resource(resourceName)

		scope, err := config.FindOrCreateScope(intptr(resource.ID()))
		Expect(err).ToNot(HaveOccurred())
		Expect(resource.SetResourceConfigScope(scope)).To(Succeed())

		return scope
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

	BeforeEach(func() {
		scenario = dbtest.Setup(
			builder.WithPipeline(atc.Config{
				ResourceTypes: atc.ResourceTypes{
					{
						Name:   "some-custom-type",
						Type:   dbtest.BaseResourceType,
						Source: typeSource,
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
			}),
			builder.WithResourceVersions("some-resource",
				atc.Version{"ref": "v1"},
				atc.Version{"ref": "v2"},
				atc.Version{"ref": "v3"},
			),
			builder.WithJobBuild(&upstreamBuild, "upstream-job", dbtest.JobInputs{
				{
					Name:    "some-resource",
					Version: atc.Version{"ref": "v2"},
				},
			}, dbtest.JobOutputs{}),
		)
	})

	Context("when the resource is checked against the new image", func() {
		var (
			oldConfigID int
			oldScopeID  int
			newScope    db.ResourceConfigScope
		)

		BeforeEach(func() {
			oldConfigID = scenario.Resource("some-resource").ResourceConfigID()
			oldScopeID = scenario.Resource("some-resource").ResourceConfigScopeID()

			newScope = moveTo("some-resource", configOnTypeImage("bumped", resourceSource))
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

		It("carries the history on through a second image change", func() {
			Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v4"}})).To(Succeed())

			thirdScope := moveTo("some-resource", configOnTypeImage("bumped-again", resourceSource))

			Expect(versionsOf(thirdScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4"}))
			Expect(deprecated(newScope.ID())).To(BeTrue())
			Expect(deprecated(oldScopeID)).To(BeTrue())
		})

		It("carries the history back if a check still runs against the old image", func() {
			Expect(newScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v4"}})).To(Succeed())

			revivedScope := moveTo("some-resource", configOnTypeImage("version", resourceSource))
			Expect(revivedScope.ID()).ToNot(Equal(oldScopeID))
			Expect(revivedScope.ResourceConfig().ID()).To(Equal(oldConfigID))

			Expect(versionsOf(revivedScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4"}))
		})
	})

	Context("when another active resource still uses the old scope", func() {
		var oldScopeID int

		BeforeEach(func() {
			oldScopeID = scenario.Resource("some-resource").ResourceConfigScopeID()

			scenario.Run(builder.WithResourceVersions("same-resource"))
			Expect(scenario.Resource("same-resource").ResourceConfigScopeID()).To(Equal(oldScopeID))
		})

		It("leaves the old scope live until the last resource has moved", func() {
			newConfig := configOnTypeImage("bumped", resourceSource)

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
				newScope = moveTo("some-resource", configOnTypeImage("bumped", resourceSource))
				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3"}))

				// same-resource is still checked against the old image.
				oldScope, err := configOnTypeImage("version", resourceSource).FindOrCreateScope(intptr(scenario.Resource("same-resource").ID()))
				Expect(err).ToNot(HaveOccurred())
				Expect(oldScope.ID()).To(Equal(oldScopeID))
				Expect(oldScope.SaveVersions(db.SpanContext{}, []atc.Version{{"ref": "v3"}, {"ref": "v4"}})).To(Succeed())
				Expect(versionsOf(oldScopeID)).To(Equal([]string{"v1", "v2", "v3", "v4"}))
			})

			It("carries the newer version in above the ones already carried", func() {
				moveTo("same-resource", configOnTypeImage("bumped", resourceSource))

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

				moveTo("same-resource", configOnTypeImage("bumped", resourceSource))

				Expect(versionsOf(newScope.ID())).To(Equal([]string{"v1", "v2", "v3", "v4", "v5"}))

				latest, found, err := newScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(latest.Version()).To(Equal(db.Version{"ref": "v5"}))
			})
		})
	})

	Context("when the resource's source changes too", func() {
		It("starts a new history and leaves the old scope alone", func() {
			oldScopeID := scenario.Resource("some-resource").ResourceConfigScopeID()

			newScope := moveTo("some-resource", configOnTypeImage("bumped", atc.Source{"some": "other-source"}))

			Expect(versionsOf(newScope.ID())).To(BeEmpty())
			Expect(deprecated(oldScopeID)).To(BeFalse())
		})
	})

	Context("when the type's own source changes", func() {
		It("carries nothing into the scope another type image already holds", func() {
			// Another pipeline's type, on an image this pipeline does not
			// control, checks the same resource source into a global scope.
			otherConfig := configOnType(atc.Source{"some": "other-type-source"}, nil, "other", resourceSource)
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

	Context("when the type's params change", func() {
		It("starts a new history and leaves the old scope alone", func() {
			oldScopeID := scenario.Resource("some-resource").ResourceConfigScopeID()

			newScope := moveTo("some-resource", configOnType(typeSource, atc.Params{"some": "param"}, "version", resourceSource))
			Expect(newScope.ID()).ToNot(Equal(oldScopeID))

			Expect(versionsOf(newScope.ID())).To(BeEmpty())
			Expect(deprecated(oldScopeID)).To(BeFalse())
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

			moveTo("some-resource", configOnTypeImage("bumped", resourceSource))
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

			newScope := moveTo("some-resource", configOnTypeImage("bumped", resourceSource))

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

	Context("when a put saves its output against the new image", func() {
		It("carries the history below the put's version", func() {
			oldScopeID := scenario.Resource("some-resource").ResourceConfigScopeID()

			imageCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(upstreamBuild.ID()),
				dbtest.BaseResourceType,
				atc.Version{"custom-type": "bumped"},
				typeSource,
				nil,
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(upstreamBuild.SaveOutput("some-custom-type", imageCache, resourceSource, atc.Version{"ref": "put"}, nil, "some-output", "some-resource")).To(Succeed())

			resource := scenario.Resource("some-resource")
			Expect(resource.ResourceConfigScopeID()).ToNot(Equal(oldScopeID))
			Expect(versionsOf(resource.ResourceConfigScopeID())).To(Equal([]string{"v1", "v2", "v3", "put"}))
			Expect(deprecated(oldScopeID)).To(BeTrue())
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

			var err error
			imageCache, err = resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(upstreamBuild.ID()),
				dbtest.BaseResourceType,
				atc.Version{"custom-type": "bumped"},
				typeSource,
				nil,
				nil,
			)
			Expect(err).ToNot(HaveOccurred())
			newScope, err = configOnTypeImage("bumped", resourceSource).FindOrCreateScope(intptr(resource.ID()))
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

			newScope, err := configOnTypeImage("bumped", resourceSource).FindOrCreateScope(intptr(resource.ID()))
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
