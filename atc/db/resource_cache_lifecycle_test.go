package db_test

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceCacheLifecycle", func() {

	var resourceCacheLifecycle db.ResourceCacheLifecycle

	BeforeEach(func() {
		resourceCacheLifecycle = db.NewResourceCacheLifecycle(dbConn)
	})

	Describe("CleanUpInvalidCaches", func() {
		Context("the resource cache is used by a build", func() {

			Context("when its a one off build", func() {
				It("doesn't delete the resource cache", func() {
					_, _ = resourceCacheForOneOffBuild()

					err := resourceCacheLifecycle.CleanUpInvalidCaches(logger.Session("resource-cache-lifecycle"))
					Expect(err).ToNot(HaveOccurred())
					Expect(countResourceCaches()).ToNot(BeZero())
				})

				Context("when the cache is for a saved image resource version for a finished build", func() {
					setBuildStatus := func(a db.BuildStatus) {
						resourceCache, build := resourceCacheForOneOffBuild()

						err := build.SaveImageResourceVersion(resourceCache)
						Expect(err).ToNot(HaveOccurred())

						err = build.SetInterceptible(false)
						Expect(err).ToNot(HaveOccurred())

						err = build.Finish(a)
						Expect(err).ToNot(HaveOccurred())

						err = resourceCacheLifecycle.CleanUsesForFinishedBuilds(logger)
						Expect(err).ToNot(HaveOccurred())
					}

					Context("when the build has succeeded", func() {
						It("does not remove the image resource cache", func() {
							setBuildStatus(db.BuildStatusSucceeded)
							Expect(countResourceCaches()).ToNot(BeZero())

							err := resourceCacheLifecycle.CleanUpInvalidCaches(logger.Session("resource-cache-lifecycle"))
							Expect(err).ToNot(HaveOccurred())

							Expect(countResourceCaches()).ToNot(BeZero())
						})
					})

					Context("when build has not succeeded", func() {
						It("does not removes the image resource cache", func() {
							setBuildStatus(db.BuildStatusFailed)
							Expect(countResourceCaches()).ToNot(BeZero())

							err := resourceCacheLifecycle.CleanUpInvalidCaches(logger.Session("resource-cache-lifecycle"))
							Expect(err).ToNot(HaveOccurred())

							Expect(countResourceCaches()).ToNot(BeZero())
						})
					})
				})
			})

			Context("when its a build of a job in a pipeline", func() {
				Context("when the cache is for a saved image resource version for a finished build", func() {
					setBuildStatus := func(a db.BuildStatus) (db.ResourceCache, db.Build) {
						resourceCache, build := resourceCacheForJobBuild()
						Expect(build.JobID()).ToNot(BeZero())

						err := build.SaveImageResourceVersion(resourceCache)
						Expect(err).ToNot(HaveOccurred())

						err = build.SetInterceptible(false)
						Expect(err).ToNot(HaveOccurred())

						err = build.Finish(a)
						Expect(err).ToNot(HaveOccurred())

						err = resourceCacheLifecycle.CleanUsesForFinishedBuilds(logger)
						Expect(err).ToNot(HaveOccurred())
						return resourceCache, build
					}

					Context("when the build has succeeded", func() {
						It("does not remove the resource cache for the most recent build", func() {
							setBuildStatus(db.BuildStatusSucceeded)
							Expect(countResourceCaches()).To(Equal(1))

							err := resourceCacheLifecycle.CleanUpInvalidCaches(logger.Session("resource-cache-lifecycle"))
							Expect(err).ToNot(HaveOccurred())

							Expect(countResourceCaches()).To(Equal(1))
						})

						It("does not remove the resource caches from other jobs", func() {
							By("creating a second pipeline")
							secondPipeline, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "second-pipeline"}, atc.Config{
								Jobs: atc.JobConfigs{
									{
										Name: "some-job",
									},
								},
								Resources: atc.ResourceConfigs{
									{
										Name: "some-resource",
										Type: "some-base-resource-type",
										Source: atc.Source{
											"some": "source",
										},
									},
								},
								ResourceTypes: atc.ResourceTypes{
									{
										Name: "some-type",
										Type: "some-base-resource-type",
										Source: atc.Source{
											"some-type": "source",
										},
									},
								},
							}, db.ConfigVersion(0), false)
							Expect(err).NotTo(HaveOccurred())

							By("creating an image resource cache tied to the job in the second pipeline")
							job, _, err := secondPipeline.Job("some-job")
							Expect(err).ToNot(HaveOccurred())
							build, err := job.CreateBuild(defaultBuildCreatedBy)
							Expect(err).ToNot(HaveOccurred())
							resourceCache := createResourceCacheWithUser(db.ForBuild(build.ID()))

							err = build.SaveImageResourceVersion(resourceCache)
							Expect(err).ToNot(HaveOccurred())

							err = build.SetInterceptible(false)
							Expect(err).ToNot(HaveOccurred())

							By("creating an image resource cached in the default pipeline")
							setBuildStatus(db.BuildStatusSucceeded)

							Expect(countResourceCaches()).To(Equal(2))
						})
					})

					Context("when build has not succeeded", func() {
						It("does not remove the image resource cache", func() {
							setBuildStatus(db.BuildStatusFailed)
							Expect(countResourceCaches()).ToNot(BeZero())

							err := resourceCacheLifecycle.CleanUpInvalidCaches(logger.Session("resource-cache-lifecycle"))
							Expect(err).ToNot(HaveOccurred())

							Expect(countResourceCaches()).ToNot(BeZero())
						})
					})
				})
			})
		})

		Context("when the cache is for a custom resource type", func() {
			It("does not remove the cache if the type is still configured", func() {
				imageResourceCache, build := resourceCacheForJobBuild()

				err := build.SetInterceptible(false)
				Expect(err).ToNot(HaveOccurred())

				By("removing the resource cache use for the build id")
				err = resourceCacheLifecycle.CleanUsesForFinishedBuilds(logger)
				Expect(err).ToNot(HaveOccurred())

				_, err = resourceConfigFactory.FindOrCreateResourceConfig(
					"some-type",
					atc.Source{
						"some": "source",
					},
					imageResourceCache,
				)
				Expect(err).ToNot(HaveOccurred())

				Expect(countResourceCaches()).ToNot(BeZero())
				err = resourceCacheLifecycle.CleanUpInvalidCaches(logger.Session("resource-cache-lifecycle"))
				Expect(err).ToNot(HaveOccurred())

				Expect(countResourceCaches()).ToNot(BeZero())
			})
		})
	})

	Describe("CleanInvalidWorkerResourceCaches", func() {
		var resourceCache db.ResourceCache
		var build db.Build
		var scenario *dbtest.Scenario
		var usedBaseResourceTypeOnWorker0 *db.UsedWorkerBaseResourceType

		BeforeEach(func() {
			scenario = dbtest.Setup(
				builder.WithTeam("some-team"),
				builder.WithBaseWorker(), // worker0
				builder.WithBaseWorker(), // worker1
				builder.WithBaseWorker(), // worker2
			)

			var err error
			build, err = scenario.Team.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())

			resourceTypeCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()),
				dbtest.BaseResourceType,
				atc.Version{"some-type": "version"},
				atc.Source{
					"some-type": "source",
				},
				nil,
				nil,
			)

			resourceCache, err = resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()),
				"some-type",
				atc.Version{"some": "version"},
				atc.Source{
					"some": "source",
				},
				atc.Params{"some": "params"},
				resourceTypeCache,
			)
			Expect(err).ToNot(HaveOccurred())

			var found bool
			usedBaseResourceTypeOnWorker0, found, err = db.WorkerBaseResourceType{
				Name:       resourceCache.BaseResourceType().Name,
				WorkerName: scenario.Workers[0].Name(),
			}.Find(dbConn)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			tx, err := dbConn.Begin()
			Expect(err).ToNot(HaveOccurred())
			_, valid, findErr := db.WorkerResourceCache{
				WorkerName:    scenario.Workers[0].Name(),
				ResourceCache: resourceCache,
			}.FindOrCreate(tx, usedBaseResourceTypeOnWorker0.ID)
			tx.Commit()
			Expect(findErr).ToNot(HaveOccurred())
			Expect(valid).To(BeTrue())
		})

		Context("when no running build", func() {
			BeforeEach(func() {
				tx, err := dbConn.Begin()
				Expect(err).ToNot(HaveOccurred())
				_, valid, findErr := db.WorkerResourceCache{
					WorkerName:    scenario.Workers[1].Name(),
					ResourceCache: resourceCache,
				}.FindOrCreate(tx, usedBaseResourceTypeOnWorker0.ID)
				tx.Commit()
				Expect(findErr).ToNot(HaveOccurred())
				Expect(valid).To(BeTrue())

				tx, err = dbConn.Begin()
				Expect(err).ToNot(HaveOccurred())
				_, valid, findErr = db.WorkerResourceCache{
					WorkerName:    scenario.Workers[2].Name(),
					ResourceCache: resourceCache,
				}.FindOrCreate(tx, usedBaseResourceTypeOnWorker0.ID)
				tx.Commit()
				Expect(findErr).ToNot(HaveOccurred())
				Expect(valid).To(BeTrue())

				err = scenario.Workers[0].Delete()
				Expect(err).ToNot(HaveOccurred())
			})

			It("should have 2 invalid caches", func() {
				Expect(countInvalidWorkerResourceCaches()).To(Equal(2))
			})

			It("should cleanup invalid caches", func() {
				// there are 2 invalid caches, delete one and one should be left
				err := resourceCacheLifecycle.CleanInvalidWorkerResourceCaches(logger, 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(countInvalidWorkerResourceCaches()).To(Equal(1))

				// delete one again and zero should be left
				err = resourceCacheLifecycle.CleanInvalidWorkerResourceCaches(logger, 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(countInvalidWorkerResourceCaches()).To(Equal(0))

				// now no invalid cache left, cleanup should not return error
				err = resourceCacheLifecycle.CleanInvalidWorkerResourceCaches(logger, 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(countInvalidWorkerResourceCaches()).To(Equal(0))
			})
		})

		Context("when there are running builds", func() {
			BeforeEach(func() {
				tx, err := dbConn.Begin()
				Expect(err).ToNot(HaveOccurred())
				_, valid, findErr := db.WorkerResourceCache{
					WorkerName:    scenario.Workers[1].Name(),
					ResourceCache: resourceCache,
				}.FindOrCreate(tx, usedBaseResourceTypeOnWorker0.ID)
				tx.Commit()
				Expect(findErr).ToNot(HaveOccurred())
				Expect(valid).To(BeTrue())

				tx, err = dbConn.Begin()
				Expect(err).ToNot(HaveOccurred())
				_, valid, findErr = db.WorkerResourceCache{
					WorkerName:    scenario.Workers[2].Name(),
					ResourceCache: resourceCache,
				}.FindOrCreate(tx, usedBaseResourceTypeOnWorker0.ID)
				tx.Commit()
				Expect(findErr).ToNot(HaveOccurred())
				Expect(valid).To(BeTrue())

				_, err = build.Start(atc.Plan{})
				Expect(err).ToNot(HaveOccurred())

				err = scenario.Workers[0].Delete()
				Expect(err).ToNot(HaveOccurred())
			})

			It("should have 2 invalid caches", func() {
				Expect(countInvalidWorkerResourceCaches()).To(Equal(2))
			})

			It("should cleanup invalid caches", func() {
				// there are 2 invalid caches, but as they got invalid later than
				// the build started, thus they should not be deleted.
				err := resourceCacheLifecycle.CleanInvalidWorkerResourceCaches(logger, 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(countInvalidWorkerResourceCaches()).To(Equal(2))
			})
		})
	})
})

func resourceCacheForOneOffBuild() (db.ResourceCache, db.Build) {
	build, err := defaultTeam.CreateOneOffBuild()
	Expect(err).ToNot(HaveOccurred())
	return createResourceCacheWithUser(db.ForBuild(build.ID())), build
}

func resourceCacheForJobBuild() (db.ResourceCache, db.Build) {
	build, err := defaultJob.CreateBuild(defaultBuildCreatedBy)
	Expect(err).ToNot(HaveOccurred())
	return createResourceCacheWithUser(db.ForBuild(build.ID())), build
}

func countResourceCaches() int {
	var result int
	err := psql.Select("count(*)").
		From("resource_caches").
		RunWith(dbConn).
		QueryRow().
		Scan(&result)
	Expect(err).ToNot(HaveOccurred())
	return result
}

func createResourceCacheWithUser(resourceCacheUser db.ResourceCacheUser) db.ResourceCache {
	usedResourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(
		resourceCacheUser,
		"some-base-resource-type",
		atc.Version{"some": "version"},
		atc.Source{
			"some": "source",
		},
		atc.Params{"some": fmt.Sprintf("param-%d", time.Now().UnixNano())},
		nil,
	)
	Expect(err).ToNot(HaveOccurred())

	return usedResourceCache
}

func countInvalidWorkerResourceCaches() int {
	var result int
	err := psql.Select("count(*)").
		From("worker_resource_caches").
		Where(sq.Expr("worker_base_resource_type_id is null")).
		RunWith(dbConn).
		QueryRow().
		Scan(&result)
	Expect(err).ToNot(HaveOccurred())
	return result
}
