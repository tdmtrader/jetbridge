package db_test

import (
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceConfigFactory", func() {
	var build db.Build

	BeforeEach(func() {
		job, found, err := defaultPipeline.Job("some-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		build, err = job.CreateBuild(defaultBuildCreatedBy)
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when a config is created", func() {
		var resourceConfig db.ResourceConfig

		BeforeEach(func() {
			var err error
			resourceConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
				"some-base-resource-type",
				atc.Source{"some": "unique-source"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("has a recent 'last referenced' value", func() {
			Expect(resourceConfig.LastReferenced()).To(BeTemporally("~", time.Now(), time.Minute))
		})

		Context("and created again", func() {
			var sameConfig db.ResourceConfig

			BeforeEach(func() {
				var err error
				sameConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
					"some-base-resource-type",
					atc.Source{"some": "unique-source"},
					nil,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns the same config, but with the same 'last referenced' time because we only update last_referenced once a minute", func() {
				Expect(sameConfig.ID()).To(Equal(resourceConfig.ID()))
				Expect(sameConfig.LastReferenced()).To(Equal(resourceConfig.LastReferenced()))
			})
		})

		Context("when cleaning up with no grace period", func() {
			It("removes the config immediately", func() {
				Expect(resourceConfigFactory.CleanUnreferencedConfigs(0)).To(Succeed())

				recreated, err := resourceConfigFactory.FindOrCreateResourceConfig(
					"some-base-resource-type",
					atc.Source{"some": "unique-source"},
					nil,
				)
				Expect(err).ToNot(HaveOccurred())
				Expect(recreated.ID()).ToNot(Equal(resourceConfig.ID()))
			})
		})

		Context("when cleaning up with a grace period", func() {
			It("spares the config", func() {
				Expect(resourceConfigFactory.CleanUnreferencedConfigs(time.Hour)).To(Succeed())

				recreated, err := resourceConfigFactory.FindOrCreateResourceConfig(
					"some-base-resource-type",
					atc.Source{"some": "unique-source"},
					nil,
				)
				Expect(err).ToNot(HaveOccurred())
				Expect(recreated.ID()).To(Equal(resourceConfig.ID()))
			})
		})
	})

	Context("when a resource type is declared with a direct image:", func() {
		// An image: type's configs are created under the resource cache of the
		// image it is pinned to (db.PinnedImage), so a base type or another
		// image: type of the same name, in any team, shares nothing with it.
		pinnedImageCache := func(imageRef string) db.ResourceCache {
			resourceType, source, version := db.PinnedImage(imageRef)
			cache, err := resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()), resourceType, version, source, nil, nil,
			)
			Expect(err).ToNot(HaveOccurred())
			return cache
		}

		It("is a different resource config from the base type of the same name", func() {
			imageType, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-base-resource-type",
				atc.Source{"some": "unique-source"},
				pinnedImageCache("registry.example/one@sha256:aaa"),
			)
			Expect(err).ToNot(HaveOccurred())

			baseType, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-base-resource-type",
				atc.Source{"some": "unique-source"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(imageType.ID()).ToNot(Equal(baseType.ID()))
			Expect(imageType.CreatedByBaseResourceType()).To(BeNil())
			Expect(imageType.OriginBaseResourceType().Name).To(Equal("registry-image"))
			Expect(baseType.OriginBaseResourceType().Name).To(Equal("some-base-resource-type"))
		})

		It("is a different resource config for each image the same type name is pinned to", func() {
			teamA, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"shared-name",
				atc.Source{"some": "unique-source"},
				pinnedImageCache("registry.example/team-a@sha256:aaa"),
			)
			Expect(err).ToNot(HaveOccurred())

			teamB, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"shared-name",
				atc.Source{"some": "unique-source"},
				pinnedImageCache("registry.example/team-b@sha256:bbb"),
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(teamA.ID()).ToNot(Equal(teamB.ID()))
		})

		It("is the same resource config for the same type name and image", func() {
			first, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"shared-name",
				atc.Source{"some": "unique-source"},
				pinnedImageCache("registry.example/same@sha256:ccc"),
			)
			Expect(err).ToNot(HaveOccurred())

			again, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"shared-name",
				atc.Source{"some": "unique-source"},
				pinnedImageCache("registry.example/same@sha256:ccc"),
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(again.ID()).To(Equal(first.ID()))
		})

		It("registers no base resource type of its own", func() {
			_, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"shared-name",
				atc.Source{"some": "unique-source"},
				pinnedImageCache("registry.example/none@sha256:ddd"),
			)
			Expect(err).ToNot(HaveOccurred())

			var count int
			err = dbConn.QueryRow(`SELECT count(*) FROM base_resource_types WHERE name = 'shared-name' OR name LIKE '%sha256%'`).Scan(&count)
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(BeZero())
		})
	})

	Context("when a resource's source changes and its old scope is deprecated", func() {
		var (
			oldConfigID int
			oldScopeID  int
			resource    db.Resource
		)

		BeforeEach(func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name:   "some-resource",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "source"},
						},
					},
				}),
				builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}, atc.Version{"ref": "v2"}),
			)

			resource = scenario.Resource("some-resource")
			oldConfigID = resource.ResourceConfigID()
			oldScopeID = resource.ResourceConfigScopeID()

			newConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-base-resource-type",
				atc.Source{"some": "changed-source"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())
			newScope, err := newConfig.FindOrCreateScope(intptr(resource.ID()))
			Expect(err).ToNot(HaveOccurred())
			Expect(resource.SetResourceConfigScope(newScope)).To(Succeed())

			_, err = dbConn.Exec(`UPDATE resource_configs SET last_referenced = now() - interval '2 hours' WHERE id = $1`, oldConfigID)
			Expect(err).ToNot(HaveOccurred())
		})

		It("keeps the old config, so the deprecated scope and its versions survive", func() {
			Expect(resourceConfigFactory.CleanUnreferencedConfigs(0)).To(Succeed())

			deprecated, err := resource.DeprecatedScopes()
			Expect(err).ToNot(HaveOccurred())
			Expect(deprecated).To(HaveLen(1))
			Expect(deprecated[0].ID).To(Equal(oldScopeID))

			var versions int
			Expect(dbConn.QueryRow(`SELECT COUNT(*) FROM resource_config_versions WHERE resource_config_scope_id = $1`, oldScopeID).Scan(&versions)).To(Succeed())
			Expect(versions).To(Equal(2))
		})

		It("removes the old config once the deprecated scope is gone", func() {
			_, err := dbConn.Exec(`DELETE FROM resource_config_scopes WHERE id = $1`, oldScopeID)
			Expect(err).ToNot(HaveOccurred())

			Expect(resourceConfigFactory.CleanUnreferencedConfigs(0)).To(Succeed())

			_, found, err := resourceConfigFactory.FindResourceConfigByID(oldConfigID)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})
	})

	Context("when the resource config is concurrently created", func() {
		BeforeEach(func() {
			Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
			Expect(build.SetInterceptible(false)).To(Succeed())
		})

		It("consistently is able to be created", func() {
			// enable concurrent use of database. this is set to 1 by default to
			// ensure methods don't require more than one in a single connection,
			// which can cause deadlocking as the pool is limited.
			dbConn.SetMaxOpenConns(2)

			done := make(chan struct{})

			wg := new(sync.WaitGroup)
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()

				for {
					select {
					case <-done:
						return
					default:
						_, err := resourceConfigFactory.FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"some": "unique-source"}, nil)
						Expect(err).ToNot(HaveOccurred())
					}
				}
			}()

			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer close(done)
				defer wg.Done()

				for i := 0; i < 100; i++ {
					_, err := resourceConfigFactory.FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"some": "unique-source"}, nil)
					Expect(err).ToNot(HaveOccurred())
				}
			}()

			wg.Wait()
		})
	})
	Context("when the resource config is concurrently deleted and created", func() {
		BeforeEach(func() {
			Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
			Expect(build.SetInterceptible(false)).To(Succeed())
		})

		It("consistently is able to be used", func() {
			// enable concurrent use of database. this is set to 1 by default to
			// ensure methods don't require more than one in a single connection,
			// which can cause deadlocking as the pool is limited.
			dbConn.SetMaxOpenConns(2)

			done := make(chan struct{})

			wg := new(sync.WaitGroup)
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()

				for {
					select {
					case <-done:
						return
					default:
						Expect(resourceConfigFactory.CleanUnreferencedConfigs(0)).To(Succeed())
					}
				}
			}()

			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer close(done)
				defer wg.Done()

				for i := 0; i < 100; i++ {
					_, err := resourceConfigFactory.FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"some": "unique-source"}, nil)
					Expect(err).ToNot(HaveOccurred())
				}
			}()

			wg.Wait()
		})
	})

	Describe("FindResourceConfigByID", func() {
		var (
			resourceConfigID      int
			resourceConfig        db.ResourceConfig
			createdResourceConfig db.ResourceConfig
			found                 bool
			err                   error
		)

		JustBeforeEach(func() {
			resourceConfig, found, err = resourceConfigFactory.FindResourceConfigByID(resourceConfigID)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when the resource config does exist", func() {
			Context("when the resource config uses a base resource type", func() {
				BeforeEach(func() {
					setupTx, err := dbConn.Begin()
					Expect(err).ToNot(HaveOccurred())

					brt := db.BaseResourceType{
						Name: "base-resource-type-name",
					}

					_, err = brt.FindOrCreate(setupTx, false)
					Expect(err).NotTo(HaveOccurred())
					Expect(setupTx.Commit()).To(Succeed())

					createdResourceConfig, err = resourceConfigFactory.FindOrCreateResourceConfig("base-resource-type-name", atc.Source{}, nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(createdResourceConfig).ToNot(BeNil())

					resourceConfigID = createdResourceConfig.ID()
				})

				It("should find the resource config using the resource's config id", func() {
					Expect(found).To(BeTrue())
					Expect(resourceConfig).ToNot(BeNil())
					Expect(resourceConfig.ID()).To(Equal(resourceConfigID))
					Expect(resourceConfig.CreatedByBaseResourceType()).To(Equal(createdResourceConfig.CreatedByBaseResourceType()))
				})
			})

			Context("when the resource config uses a custom resource type", func() {
				BeforeEach(func() {
					imageResourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(db.ForBuild(build.ID()), "some-base-resource-type", atc.Version{}, atc.Source{}, nil, nil)
					Expect(err).ToNot(HaveOccurred())

					createdResourceConfig, err = resourceConfigFactory.FindOrCreateResourceConfig("some-type", atc.Source{}, imageResourceCache)
					Expect(err).ToNot(HaveOccurred())
					Expect(createdResourceConfig).ToNot(BeNil())

					resourceConfigID = createdResourceConfig.ID()
				})

				It("should find the resource config using the resource's config id", func() {
					Expect(found).To(BeTrue())
					Expect(resourceConfig).ToNot(BeNil())
					Expect(resourceConfig.ID()).To(Equal(resourceConfigID))
					Expect(resourceConfig.CreatedByResourceCache().ID()).To(Equal(createdResourceConfig.CreatedByResourceCache().ID()))
					Expect(resourceConfig.CreatedByResourceCache().ResourceConfig().ID()).To(Equal(createdResourceConfig.CreatedByResourceCache().ResourceConfig().ID()))
				})
			})
		})

		Context("when the resource config id does not exist", func() {
			BeforeEach(func() {
				resourceConfigID = 123
			})

			It("should not find the resource config", func() {
				Expect(found).To(BeFalse())
			})
		})
	})
})
