package db_test

import (
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ContainerRepository", func() {
	Describe("FindOrphanedContainers", func() {
		Describe("check containers", func() {
			var (
				resourceConfig db.ResourceConfig
			)

			expiries := db.ContainerOwnerExpiries{
				Min: 5 * time.Minute,
				Max: 1 * time.Hour,
			}

			BeforeEach(func() {
				var err error
				resourceConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
					"some-base-resource-type",
					atc.Source{"some": "source"},
					nil,
				)
				Expect(err).NotTo(HaveOccurred())

				_, err = defaultWorker.CreateContainer(
					db.NewResourceConfigCheckSessionContainerOwner(
						resourceConfig.ID(),
						resourceConfig.OriginBaseResourceType().ID,
						expiries,
					),
					fullMetadata,
				)
				Expect(err).NotTo(HaveOccurred())
			})

			JustBeforeEach(func() {
				err := resourceConfigCheckSessionLifecycle.CleanExpiredResourceConfigCheckSessions()
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when check container best if use by date did not expire", func() {
				BeforeEach(func() {
					var rccsID int
					err := psql.Select("id").From("resource_config_check_sessions").
						Where(sq.Eq{"resource_config_id": resourceConfig.ID()}).RunWith(dbConn).QueryRow().Scan(&rccsID)
					Expect(err).NotTo(HaveOccurred())

					_, err = psql.Update("resource_config_check_sessions").
						Set("expires_at", sq.Expr("NOW() + '1 hour'::INTERVAL")).
						Where(sq.Eq{"id": rccsID}).
						RunWith(dbConn).Exec()
					Expect(err).NotTo(HaveOccurred())
				})

				It("does not find the container for deletion", func() {
					creatingContainers, createdContainers, destroyingContainers, err := containerRepository.FindOrphanedContainers()
					Expect(err).NotTo(HaveOccurred())

					Expect(creatingContainers).To(BeEmpty())
					Expect(createdContainers).To(BeEmpty())
					Expect(destroyingContainers).To(BeEmpty())
				})
			})

			Context("when the same worker base resource type is saved", func() {
				BeforeEach(func() {
					var err error
					sameWorker := defaultWorkerPayload

					defaultWorker, err = workerFactory.SaveWorker(sameWorker, 0)
					Expect(err).NotTo(HaveOccurred())
				})

				It("does not find the container for deletion", func() {
					creatingContainers, createdContainers, destroyingContainers, err := containerRepository.FindOrphanedContainers()
					Expect(err).NotTo(HaveOccurred())

					Expect(creatingContainers).To(BeEmpty())
					Expect(createdContainers).To(BeEmpty())
					Expect(destroyingContainers).To(BeEmpty())
				})
			})
		})

	})

	Describe("DestroyFailedContainers", func() {
		var failedErr error
		var failedContainersLen int

		JustBeforeEach(func() {
			failedContainersLen, failedErr = containerRepository.DestroyFailedContainers()
		})

		Context("when there are failed containers", func() {
			BeforeEach(func() {
				result, err := psql.Insert("containers").
					SetMap(map[string]any{
						"state":       atc.ContainerStateFailed,
						"handle":      "123-456-abc-def",
						"worker_name": defaultWorker.Name(),
					}).RunWith(dbConn).Exec()
				Expect(err).ToNot(HaveOccurred())
				Expect(result.RowsAffected()).To(Equal(int64(1)))
			})

			It("does not return an error", func() {
				Expect(failedErr).ToNot(HaveOccurred())
			})
		})

		Context("when there are no failed containers", func() {
			It("returns an empty array", func() {
				Expect(failedContainersLen).To(Equal(0))
			})
			It("does not return an error", func() {
				Expect(failedErr).ToNot(HaveOccurred())
			})
		})

		Describe("errors", func() {
			Context("when the query cannot be executed", func() {
				BeforeEach(func() {
					err := dbConn.Close()
					Expect(err).ToNot(HaveOccurred())
				})
				AfterEach(func() {
					dbConn = postgresRunner.OpenConn()
				})
				It("returns an error", func() {
					Expect(failedErr).To(HaveOccurred())
				})
			})
		})
	})

	Describe("FindDestroyingContainers", func() {
		var failedErr error
		var destroyingContainers []string

		JustBeforeEach(func() {
			destroyingContainers, failedErr = containerRepository.FindDestroyingContainers(defaultWorker.Name())
		})
		ItClosesConnection := func() {
			It("closes the connection", func() {
				closed := make(chan bool)

				go func() {
					_, _ = containerRepository.FindDestroyingContainers(defaultWorker.Name())
					closed <- true
				}()

				Eventually(closed).Should(Receive())
			})
		}

		Context("when there are destroying containers", func() {
			BeforeEach(func() {
				result, err := psql.Insert("containers").SetMap(map[string]any{
					"state":       "destroying",
					"handle":      "123-456-abc-def",
					"worker_name": defaultWorker.Name(),
				}).RunWith(dbConn).Exec()

				Expect(err).ToNot(HaveOccurred())
				Expect(result.RowsAffected()).To(Equal(int64(1)))
			})

			It("does not return an error", func() {
				Expect(failedErr).ToNot(HaveOccurred())
			})

			ItClosesConnection()
		})

		Describe("errors", func() {
			Context("when the query cannot be executed", func() {
				BeforeEach(func() {
					err := dbConn.Close()
					Expect(err).ToNot(HaveOccurred())
				})

				AfterEach(func() {
					dbConn = postgresRunner.OpenConn()
				})

				It("returns an error", func() {
					Expect(failedErr).To(HaveOccurred())
				})

				ItClosesConnection()
			})

			Context("when there is an error iterating through the rows", func() {
				BeforeEach(func() {
					By("adding a row without expected values")
					result, err := psql.Insert("containers").SetMap(map[string]any{
						"state":  "destroying",
						"handle": "123-456-abc-def",
					}).RunWith(dbConn).Exec()

					Expect(err).ToNot(HaveOccurred())
					Expect(result.RowsAffected()).To(Equal(int64(1)))

				})

				It("returns empty list", func() {
					Expect(destroyingContainers).To(HaveLen(0))
				})

				ItClosesConnection()
			})
		})
	})

	Describe("RemoveDestroyingContainers", func() {
		var failedErr error
		var handles []string

		JustBeforeEach(func() {
			_, failedErr = containerRepository.RemoveDestroyingContainers(defaultWorker.Name(), handles)
		})

		Context("when there are containers to destroy", func() {

			Context("when container is in destroying state", func() {
				BeforeEach(func() {
					handles = []string{"some-handle1", "some-handle2"}
					result, err := psql.Insert("containers").SetMap(map[string]any{
						"state":       atc.ContainerStateDestroying,
						"handle":      "123-456-abc-def",
						"worker_name": defaultWorker.Name(),
					}).RunWith(dbConn).Exec()

					Expect(err).ToNot(HaveOccurred())
					Expect(result.RowsAffected()).To(Equal(int64(1)))
				})
				It("does not return an error", func() {
					Expect(failedErr).ToNot(HaveOccurred())
				})
			})

			Context("when handles are empty list", func() {
				BeforeEach(func() {
					handles = []string{}
					result, err := psql.Insert("containers").SetMap(map[string]any{
						"state":       atc.ContainerStateDestroying,
						"handle":      "123-456-abc-def",
						"worker_name": defaultWorker.Name(),
					}).RunWith(dbConn).Exec()

					Expect(err).ToNot(HaveOccurred())
					Expect(result.RowsAffected()).To(Equal(int64(1)))
				})

				It("does not return an error", func() {
					Expect(failedErr).ToNot(HaveOccurred())
				})
			})

			Context("when container is in create/creating state", func() {
				BeforeEach(func() {
					handles = []string{"some-handle1", "some-handle2"}
					result, err := psql.Insert("containers").SetMap(map[string]any{
						"state":       "creating",
						"handle":      "123-456-abc-def",
						"worker_name": defaultWorker.Name(),
					}).RunWith(dbConn).Exec()

					Expect(err).ToNot(HaveOccurred())
					Expect(result.RowsAffected()).To(Equal(int64(1)))
				})
				It("does not return an error", func() {
					Expect(failedErr).ToNot(HaveOccurred())
				})
			})
		})

		Context("when there are no containers to destroy", func() {
			BeforeEach(func() {
				handles = []string{"some-handle1", "some-handle2"}

				result, err := psql.Insert("containers").SetMap(
					map[string]any{
						"state":       "destroying",
						"handle":      "some-handle1",
						"worker_name": defaultWorker.Name(),
					},
				).RunWith(dbConn).Exec()
				Expect(err).ToNot(HaveOccurred())
				Expect(result.RowsAffected()).To(Equal(int64(1)))

				result, err = psql.Insert("containers").SetMap(
					map[string]any{
						"state":       "destroying",
						"handle":      "some-handle2",
						"worker_name": defaultWorker.Name(),
					},
				).RunWith(dbConn).Exec()
				Expect(err).ToNot(HaveOccurred())
				Expect(result.RowsAffected()).To(Equal(int64(1)))
			})

			It("does not return an error", func() {
				Expect(failedErr).ToNot(HaveOccurred())
			})
		})

		Describe("errors", func() {
			Context("when the query cannot be executed", func() {
				BeforeEach(func() {
					err := dbConn.Close()
					Expect(err).ToNot(HaveOccurred())
				})

				AfterEach(func() {
					dbConn = postgresRunner.OpenConn()
				})

				It("returns an error", func() {
					Expect(failedErr).To(HaveOccurred())
				})
			})
		})
	})

	Describe("UpdateContainersMissingSince", func() {
		var (
			today   time.Time
			err     error
			handles []string
		)

		BeforeEach(func() {
			result, err := psql.Insert("containers").SetMap(map[string]any{
				"state":       atc.ContainerStateDestroying,
				"handle":      "some-handle1",
				"worker_name": defaultWorker.Name(),
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))

			result, err = psql.Insert("containers").SetMap(map[string]any{
				"state":       atc.ContainerStateDestroying,
				"handle":      "some-handle2",
				"worker_name": defaultWorker.Name(),
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))

			today = time.Date(2018, 9, 24, 0, 0, 0, 0, time.UTC)

			result, err = psql.Insert("containers").SetMap(map[string]any{
				"state":         atc.ContainerStateCreated,
				"handle":        "some-handle3",
				"worker_name":   defaultWorker.Name(),
				"missing_since": today,
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))
		})

		JustBeforeEach(func() {
			err = containerRepository.UpdateContainersMissingSince(defaultWorker.Name(), handles)
		})

		Context("when the reported handles is a subset", func() {
			BeforeEach(func() {
				handles = []string{"some-handle1"}
			})

			Context("having the containers in the creating state in the db", func() {
				BeforeEach(func() {
					result, err := psql.Update("containers").
						Where(sq.Eq{"handle": "some-handle3"}).
						SetMap(map[string]any{
							"state":         atc.ContainerStateCreating,
							"missing_since": nil,
						}).RunWith(dbConn).Exec()
					Expect(err).NotTo(HaveOccurred())
					Expect(result.RowsAffected()).To(Equal(int64(1)))
				})

			})

			It("does not return an error", func() {
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("when the reported handles is the full set", func() {
			BeforeEach(func() {
				handles = []string{"some-handle1", "some-handle2"}
			})

			It("does not return an error", func() {
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("when the reported handles includes a container marked as missing", func() {
			BeforeEach(func() {
				handles = []string{"some-handle1", "some-handle2", "some-handle3"}
			})

			It("does not return an error", func() {
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})

	Describe("DestroyUnknownContainers", func() {
		var (
			err                   error
			workerReportedHandles []string
		)

		BeforeEach(func() {
			result, err := psql.Insert("containers").SetMap(map[string]any{
				"state":       atc.ContainerStateDestroying,
				"handle":      "some-handle1",
				"worker_name": defaultWorker.Name(),
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))

			result, err = psql.Insert("containers").SetMap(map[string]any{
				"state":       atc.ContainerStateCreated,
				"handle":      "some-handle2",
				"worker_name": defaultWorker.Name(),
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))
		})

		JustBeforeEach(func() {
			_, err = containerRepository.DestroyUnknownContainers(defaultWorker.Name(), workerReportedHandles)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when there are containers on the worker that are not in the db", func() {
			BeforeEach(func() {
				workerReportedHandles = []string{"some-handle3", "some-handle4"}
			})

			It("does not affect containers in any other state", func() {
				rows, err := psql.Select("handle").
					From("containers").
					Where(sq.Eq{"state": atc.ContainerStateCreated}).
					RunWith(dbConn).Query()

				Expect(err).ToNot(HaveOccurred())

				var handle string
				var rowsAffected int
				for rows.Next() {
					err = rows.Scan(&handle)
					Expect(err).ToNot(HaveOccurred())
					Expect(handle).To(Equal("some-handle2"))
					rowsAffected++
				}

				Expect(rowsAffected).To(Equal(1))
			})
		})

		Context("when there are no unknown containers on the worker", func() {
			BeforeEach(func() {
				workerReportedHandles = []string{"some-handle1", "some-handle2"}
			})

		})
	})

})
