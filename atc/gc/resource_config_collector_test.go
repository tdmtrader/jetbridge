package gc_test

import (
	"context"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/gc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceConfigCollector", func() {
	var collector GcCollector
	var gracePeriod = time.Hour

	BeforeEach(func() {
		collector = gc.NewResourceConfigCollector(resourceConfigFactory, gracePeriod)
	})

	Describe("Run", func() {
		Describe("configs", func() {
			countResourceConfigs := func() int {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer tx.Rollback()

				var result int
				err = psql.Select("count(*)").
					From("resource_configs").
					RunWith(tx).
					QueryRow().
					Scan(&result)
				Expect(err).NotTo(HaveOccurred())

				return result
			}

			Context("when the config is referenced in resource config check sessions", func() {
				ownerExpiries := db.ContainerOwnerExpiries{
					Min: 5 * time.Minute,
					Max: 10 * time.Minute,
				}

				BeforeEach(func() {
					resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
						"some-base-type",
						atc.Source{
							"some": "source",
						},
						nil,
					)
					Expect(err).NotTo(HaveOccurred())

					workerFactory := db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0))
					defaultWorkerPayload := atc.Worker{
						ResourceTypes: []atc.WorkerResourceType{
							{
								Type:    "some-base-type",
								Image:   "/path/to/image",
								Version: "some-brt-version",
							},
						},
						Name: "default-worker",
					}
					worker, err := workerFactory.SaveWorker(defaultWorkerPayload, 0)
					Expect(err).NotTo(HaveOccurred())

					_, err = worker.CreateContainer(db.NewResourceConfigCheckSessionContainerOwner(resourceConfig.ID(), resourceConfig.OriginBaseResourceType().ID, ownerExpiries), db.ContainerMetadata{})
					Expect(err).NotTo(HaveOccurred())
				})

				It("preserves the config", func() {
					Expect(countResourceConfigs()).ToNot(BeZero())
					Expect(collector.Run(context.TODO())).To(Succeed())
					Expect(countResourceConfigs()).ToNot(BeZero())
				})
			})

			Context("when config is referenced in resource caches", func() {
				BeforeEach(func() {
					_, err = resourceCacheFactory.FindOrCreateResourceCache(
						db.ForBuild(defaultBuild.ID()),
						"some-base-type",
						atc.Version{"some": "version"},
						atc.Source{
							"some": "source",
						},
						nil,
						nil,
					)
					Expect(err).NotTo(HaveOccurred())
				})

				It("preserve the config", func() {
					Expect(countResourceConfigs()).NotTo(BeZero())
					Expect(collector.Run(context.TODO())).To(Succeed())
					Expect(countResourceConfigs()).NotTo(BeZero())
				})
			})

			Context("when config is not referenced in resource caches", func() {
				BeforeEach(func() {
					_, err = resourceCacheFactory.FindOrCreateResourceCache(
						db.ForBuild(defaultBuild.ID()),
						"some-base-type",
						atc.Version{"some": "version"},
						atc.Source{
							"some": "source",
						},
						nil,
						nil,
					)
					Expect(err).NotTo(HaveOccurred())

					tx, err := dbConn.Begin()
					Expect(err).NotTo(HaveOccurred())
					defer tx.Rollback()
					_, err = psql.Delete("resource_cache_uses").RunWith(tx).Exec()
					Expect(err).NotTo(HaveOccurred())
					_, err = psql.Delete("resource_caches").RunWith(tx).Exec()
					Expect(err).NotTo(HaveOccurred())
					Expect(tx.Commit()).To(Succeed())
				})

				It("cleans up the config", func() {
					Expect(countResourceConfigs()).NotTo(BeZero())
					Expect(collector.Run(context.TODO())).To(Succeed())
					Expect(countResourceConfigs()).To(BeZero())
				})
			})

			Context("when config is referenced in resources", func() {
				BeforeEach(func() {
					dbtest.Setup(
						builder.WithPipeline(atc.Config{
							Resources: atc.ResourceConfigs{
								{
									Name:   "some-resource",
									Type:   "some-base-type",
									Source: atc.Source{"some": "source"},
								},
							},
						}),
						builder.WithResourceVersions("some-resource"),
					)
				})

				It("preserve the config", func() {
					Expect(countResourceConfigs()).NotTo(BeZero())
					Expect(collector.Run(context.TODO())).To(Succeed())
					Expect(countResourceConfigs()).NotTo(BeZero())
				})
			})

			Context("when config is not referenced in resources", func() {
				var resourceConfig db.ResourceConfig

				BeforeEach(func() {
					var err error
					resourceConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
						"some-base-type",
						atc.Source{"some": "source"},
						nil,
					)
					Expect(err).NotTo(HaveOccurred())
				})

				It("spares the config until the grace period elapses", func() {
					Expect(countResourceConfigs()).NotTo(BeZero())
					Expect(collector.Run(context.TODO())).To(Succeed())
					Expect(countResourceConfigs()).NotTo(BeZero())

					// tightly coupled but better than a flaky sleep test. :/
					_, err := psql.Update("resource_configs").
						Set(
							"last_referenced",
							sq.Expr(fmt.Sprintf("now() - '%d seconds'::interval", int(gracePeriod.Seconds()))),
						).
						Where(sq.Eq{"id": resourceConfig.ID()}).
						RunWith(dbConn).
						Exec()
					Expect(err).ToNot(HaveOccurred())

					Expect(collector.Run(context.TODO())).To(Succeed())
					Expect(countResourceConfigs()).To(BeZero())
				})
			})

			Context("when config is referenced in resource types", func() {
				BeforeEach(func() {
					dbtest.Setup(
						builder.WithPipeline(atc.Config{
							ResourceTypes: atc.ResourceTypes{
								{
									Name:   "some-resource-type",
									Type:   "some-base-type",
									Source: atc.Source{"some": "source-type"},
								},
							},
						}),
						builder.WithResourceTypeVersions("some-resource-type"),
					)
				})

				It("preserve the config", func() {
					Expect(countResourceConfigs()).NotTo(BeZero())
					Expect(collector.Run(context.TODO())).To(Succeed())
					Expect(countResourceConfigs()).NotTo(BeZero())
				})
			})

			Context("when config is not referenced in resource types", func() {
				var resourceConfig db.ResourceConfig

				BeforeEach(func() {
					var err error
					resourceConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
						"some-base-type",
						atc.Source{"some": "source-type"},
						nil,
					)
					Expect(err).NotTo(HaveOccurred())
					_, err = usedResourceType.Reload()
					Expect(err).NotTo(HaveOccurred())
				})

				It("spares the config until the grace period elapses", func() {
					Expect(countResourceConfigs()).NotTo(BeZero())
					Expect(collector.Run(context.TODO())).To(Succeed())
					Expect(countResourceConfigs()).NotTo(BeZero())

					// tightly coupled but better than a flaky sleep test. :/
					_, err := psql.Update("resource_configs").
						Set(
							"last_referenced",
							sq.Expr(fmt.Sprintf("now() - '%d seconds'::interval", int(gracePeriod.Seconds()))),
						).
						Where(sq.Eq{"id": resourceConfig.ID()}).
						RunWith(dbConn).
						Exec()
					Expect(err).ToNot(HaveOccurred())

					Expect(collector.Run(context.TODO())).To(Succeed())
					Expect(countResourceConfigs()).To(BeZero())
				})
			})
		})
	})
})

// These mixed-row cases distinguish preservation from a collector that never deletes.
var _ = Describe("ResourceConfigCollector reference discrimination", func() {
	newConfig := func(name string) db.ResourceConfig {
		config, err := resourceConfigFactory.FindOrCreateResourceConfig("some-base-type", atc.Source{"name": name}, nil)
		Expect(err).NotTo(HaveOccurred())
		return config
	}
	ageConfigs := func() {
		_, err := dbConn.Exec("UPDATE resource_configs SET last_referenced = now() - interval '2 hours'")
		Expect(err).NotTo(HaveOccurred())
	}
	expectPresent := func(config db.ResourceConfig, present bool) {
		_, found, err := resourceConfigFactory.FindResourceConfigByID(config.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(Equal(present), "config %d", config.ID())
	}

	It("collects an eligible orphan beside configs held by a cache, resource and resource type", func() {
		cache, err := resourceCacheFactory.FindOrCreateResourceCache(db.ForBuild(defaultBuild.ID()),
			"some-base-type", atc.Version{"ref": "v1"}, atc.Source{"name": "cache"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		cached := cache.ResourceConfig()
		resource := newConfig("resource")
		scope, err := resource.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(usedResource.SetResourceConfigScope(scope)).To(Succeed())
		resourceType := newConfig("resource-type")
		typeScope, err := resourceType.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(usedResourceType.SetResourceConfigScope(typeScope)).To(Succeed())
		orphan := newConfig("orphan")
		ageConfigs()

		Expect(gc.NewResourceConfigCollector(resourceConfigFactory, time.Hour).Run(context.Background())).To(Succeed())
		expectPresent(orphan, false)
		for _, config := range []db.ResourceConfig{cached, resource, resourceType} {
			expectPresent(config, true)
		}
	})

	It("reports success when a check-session FK blocks the whole delete, including an eligible orphan", func() {
		pinned := newConfig("check-session")
		orphan := newConfig("orphan")
		workerFactory := db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0))
		worker, err := workerFactory.SaveWorker(atc.Worker{
			Name:          "check-session-worker",
			ResourceTypes: []atc.WorkerResourceType{{Type: "some-base-type", Image: "/image", Version: "v1"}},
		}, 0)
		Expect(err).NotTo(HaveOccurred())
		_, err = worker.CreateContainer(db.NewResourceConfigCheckSessionContainerOwner(
			pinned.ID(), pinned.OriginBaseResourceType().ID,
			db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: 10 * time.Minute}), db.ContainerMetadata{})
		Expect(err).NotTo(HaveOccurred())
		ageConfigs()

		// Characterize the current query: check sessions are absent from its
		// reference union. ON DELETE RESTRICT aborts the entire statement and
		// the collector swallows that FK error, so even the orphan survives.
		Expect(gc.NewResourceConfigCollector(resourceConfigFactory, time.Hour).Run(context.Background())).To(Succeed())
		expectPresent(pinned, true)
		expectPresent(orphan, true)
	})
})
