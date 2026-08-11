package gc_test

import (
	"context"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ContainerCollector", func() {
	var (
		containerRepository db.ContainerRepository
		collector           GcCollector

		team   db.Team
		worker db.Worker
		build  db.Build

		missingContainerGracePeriod time.Duration
		hijackContainerGracePeriod  time.Duration
	)

	// A container is orphaned once its build stops being interceptible
	// (container_repository.go:234-237). Everything below builds a real
	// container in that state and then varies only the column under test.
	createdContainer := func(planID string) db.CreatedContainer {
		creating, err := worker.CreateContainer(
			db.NewBuildStepContainerOwner(build.ID(), atc.PlanID(planID), team.ID()),
			db.ContainerMetadata{Type: "task", StepName: "some-task"},
		)
		Expect(err).NotTo(HaveOccurred())
		created, err := creating.Created()
		Expect(err).NotTo(HaveOccurred())
		return created
	}

	createdCheckContainer := func(resourceConfig db.ResourceConfig) db.CreatedContainer {
		owner := db.NewResourceConfigCheckSessionContainerOwner(
			resourceConfig.ID(),
			resourceConfig.OriginBaseResourceType().ID,
			db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: time.Hour},
		)
		creating, err := worker.CreateContainer(owner, db.ContainerMetadata{Type: db.ContainerTypeCheck})
		Expect(err).NotTo(HaveOccurred())
		created, err := creating.Created()
		Expect(err).NotTo(HaveOccurred())
		return created
	}

	orphan := func() {
		Expect(build.SetInterceptible(false)).To(Succeed())
	}

	// last_hijack is set by the hijack path (container.go:215); these specs
	// backdate the column directly rather than hijacking and then sleeping.
	setColumn := func(handle, column string, value any) {
		_, err := psql.Update("containers").
			Set(column, value).
			Where(sq.Eq{"handle": handle}).
			RunWith(dbConn).Exec()
		Expect(err).NotTo(HaveOccurred())
	}

	stateOf := func(handle string) string {
		var state string
		Expect(dbConn.QueryRow("SELECT state FROM containers WHERE handle = $1", handle).Scan(&state)).To(Succeed())
		return state
	}

	exists := func(handle string) bool {
		var n int
		Expect(dbConn.QueryRow("SELECT count(*) FROM containers WHERE handle = $1", handle).Scan(&n)).To(Succeed())
		return n > 0
	}

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		missingContainerGracePeriod = time.Minute
		hijackContainerGracePeriod = time.Minute

		containerRepository = db.NewContainerRepository(dbConn)
		collector = gc.NewContainerCollector(
			containerRepository,
			missingContainerGracePeriod,
			hijackContainerGracePeriod,
		)

		var err error
		team, err = teamFactory.CreateTeam(atc.Team{Name: "collector-team"})
		Expect(err).NotTo(HaveOccurred())

		build, err = team.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())

		worker, err = db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0)).
			SaveWorker(atc.Worker{
				Name: "some-worker",
				ResourceTypes: []atc.WorkerResourceType{
					{Type: "some-base-type", Image: "/some-image", Version: "some-version"},
				},
			}, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("Run", func() {
		It("succeeds with nothing to collect", func() {
			Expect(collector.Run(context.TODO())).To(Succeed())
		})

		Describe("orphaned containers", func() {
			It("marks a created orphan that was never hijacked as destroying", func() {
				container := createdContainer("never-hijacked")
				orphan()

				Expect(collector.Run(context.TODO())).To(Succeed())

				Expect(stateOf(container.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
			})

			It("marks a created orphan hijacked beyond the grace period as destroying", func() {
				container := createdContainer("stale-hijack")
				setColumn(container.Handle(), "last_hijack", sq.Expr("NOW() - '1 hour'::interval"))
				orphan()

				Expect(collector.Run(context.TODO())).To(Succeed())

				Expect(stateOf(container.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
			})

			It("leaves a created orphan hijacked within the grace period alone", func() {
				container := createdContainer("fresh-hijack")
				setColumn(container.Handle(), "last_hijack", sq.Expr("NOW()"))
				orphan()

				Expect(collector.Run(context.TODO())).To(Succeed())

				Expect(stateOf(container.Handle())).To(Equal(string(atc.ContainerStateCreated)),
					"a recently hijacked container is still in use")
			})

			It("leaves a container whose build is still interceptible alone", func() {
				container := createdContainer("live-build")

				Expect(collector.Run(context.TODO())).To(Succeed())

				Expect(stateOf(container.Handle())).To(Equal(string(atc.ContainerStateCreated)),
					"a container belonging to an interceptible build is not an orphan")
			})
		})

		Describe("missing containers", func() {
			It("removes a container missing for longer than the grace period", func() {
				container := createdContainer("long-missing")
				setColumn(container.Handle(), "missing_since", sq.Expr("NOW() - '1 hour'::interval"))

				Expect(collector.Run(context.TODO())).To(Succeed())

				Expect(exists(container.Handle())).To(BeFalse())
			})

			It("keeps a container missing for less than the grace period", func() {
				container := createdContainer("just-missing")
				setColumn(container.Handle(), "missing_since", sq.Expr("NOW()"))

				Expect(collector.Run(context.TODO())).To(Succeed())

				Expect(exists(container.Handle())).To(BeTrue())
			})
		})

		It("marks failed containers as destroying", func() {
			creating, err := worker.CreateContainer(
				db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("failed"), team.ID()),
				db.ContainerMetadata{Type: "task", StepName: "some-task"},
			)
			Expect(err).NotTo(HaveOccurred())
			failed, err := creating.Failed()
			Expect(err).NotTo(HaveOccurred())
			Expect(stateOf(failed.Handle())).To(Equal(string(atc.ContainerStateFailed)))

			Expect(collector.Run(context.TODO())).To(Succeed())

			Expect(stateOf(failed.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
		})

		It("marks check containers beyond the per-resource cap as destroying", func() {
			resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-base-type",
				atc.Source{"repository": "some-check-image"},
				nil,
			)
			Expect(err).NotTo(HaveOccurred())

			oldest := createdCheckContainer(resourceConfig)
			newest := createdCheckContainer(resourceConfig)

			Expect(collector.Run(context.TODO())).To(Succeed())

			Expect(stateOf(oldest.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
			Expect(stateOf(newest.Handle())).To(Equal(string(atc.ContainerStateCreated)))
		})

		// The specs below keep a narrowly scoped fake. Each proves that a failure
		// in one repository call does not stop the others from running -- error
		// isolation between independent steps of Run(). A real database cannot be
		// made to fail one of those calls and not the rest.
		Describe("error isolation", func() {
			It("forwards the exact per-resource cap and hijack grace period", func() {
				// Narrowly scoped wiring seam: the real outcome specs above prove
				// cleanup behavior, while this fake observes the two policy values
				// that are otherwise indistinguishable over a range of fixtures.
				observing := new(dbfakes.FakeContainerRepository)

				Expect(gc.NewContainerCollector(observing, missingContainerGracePeriod, hijackContainerGracePeriod).
					Run(context.TODO())).To(Succeed())

				Expect(observing.DestroyExcessCheckContainersCallCount()).To(Equal(1))
				maxPerResource, gracePeriod := observing.DestroyExcessCheckContainersArgsForCall(0)
				Expect(maxPerResource).To(Equal(1))
				Expect(gracePeriod).To(Equal(hijackContainerGracePeriod))
			})

			It("still looks for orphans when destroying failed containers errors", func() {
				failing := new(dbfakes.FakeContainerRepository)
				failing.DestroyFailedContainersReturns(0, errors.New("nope"))

				_ = gc.NewContainerCollector(failing, missingContainerGracePeriod, hijackContainerGracePeriod).
					Run(context.TODO())

				Expect(failing.FindOrphanedContainersCallCount()).To(Equal(1))
			})

			It("still cleans up other containers when DestroyExcessCheckContainers errors", func() {
				failing := new(dbfakes.FakeContainerRepository)
				failing.DestroyExcessCheckContainersReturns(0, errors.New("nope"))

				_ = gc.NewContainerCollector(failing, missingContainerGracePeriod, hijackContainerGracePeriod).
					Run(context.TODO())

				Expect(failing.FindOrphanedContainersCallCount()).To(Equal(1))
				Expect(failing.DestroyFailedContainersCallCount()).To(Equal(1))
			})

			It("returns the error when finding orphaned containers fails", func() {
				failing := new(dbfakes.FakeContainerRepository)
				failing.FindOrphanedContainersReturns(nil, nil, nil, errors.New("some error"))

				err := gc.NewContainerCollector(failing, missingContainerGracePeriod, hijackContainerGracePeriod).
					Run(context.TODO())

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("some error"))
			})
		})
	})
})
