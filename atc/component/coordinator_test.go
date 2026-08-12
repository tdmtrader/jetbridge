package component_test

import (
	"context"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Coordinator", func() {
	const componentName = "some-name"

	var (
		logger *lagertest.TestLogger
		ctx    context.Context

		dbConn db.DbConn

		otherLockFactory lock.LockFactory

		runCtxs           []context.Context
		lockHeldDuringRun bool

		coordinator *component.Coordinator
	)

	BeforeEach(func() {
		postgresRunner.CreateTestDBFromTemplate()
		DeferCleanup(func() {
			postgresRunner.DropTestDB()
		})

		logger = lagertest.NewTestLogger("test")
		ctx = lagerctx.NewContext(context.Background(), logger)

		dbConn = postgresRunner.OpenConn()
		DeferCleanup(func() {
			Expect(dbConn.Close()).To(Succeed())
		})

		otherLockFactory = newLockFactory(newLockConns())

		dbComponent, err := db.NewComponentFactory(dbConn).CreateOrUpdate(atc.Component{Name: componentName})
		Expect(err).NotTo(HaveOccurred())

		runCtxs = nil
		lockHeldDuringRun = false

		coordinator = &component.Coordinator{
			Locker:    newLockFactory(newLockConns()),
			Component: dbComponent,
			Runnable: component.RunFunc(func(runCtx context.Context) error {
				runCtxs = append(runCtxs, runCtx)

				_, acquired, err := otherLockFactory.Acquire(logger, lock.NewTaskLockID(componentName))
				Expect(err).NotTo(HaveOccurred())
				lockHeldDuringRun = !acquired

				return nil
			}),
		}
	})

	Describe("RunImmediately", func() {
		It("runs the component while holding its lock, and releases it after", func() {
			coordinator.RunImmediately(ctx)

			Expect(runCtxs).To(HaveLen(1))
			Expect(runCtxs[0]).To(Equal(ctx))
			Expect(lockHeldDuringRun).To(BeTrue(), "lock was released too early")

			releasedLock, acquired, err := otherLockFactory.Acquire(logger, lock.NewTaskLockID(componentName))
			Expect(err).NotTo(HaveOccurred())
			Expect(acquired).To(BeTrue(), "lock was not released")
			Expect(releasedLock.Release()).To(Succeed())
		})

		Context("when another connection holds the lock", func() {
			BeforeEach(func() {
				heldLock, acquired, err := otherLockFactory.Acquire(logger, lock.NewTaskLockID(componentName))
				Expect(err).NotTo(HaveOccurred())
				Expect(acquired).To(BeTrue())

				DeferCleanup(func() {
					Expect(heldLock.Release()).To(Succeed())
				})
			})

			It("does not run the component", func() {
				coordinator.RunImmediately(ctx)

				Expect(runCtxs).To(BeEmpty())
			})
		})

		Context("when acquiring the lock errors", func() {
			BeforeEach(func() {
				conns := newLockConns()
				for _, conn := range conns {
					Expect(conn.Close()).To(Succeed())
				}

				coordinator.Locker = newLockFactory(conns)
			})

			It("does not run the component", func() {
				coordinator.RunImmediately(ctx)

				Expect(runCtxs).To(BeEmpty())
			})
		})

		Context("when reloading the component errors", func() {
			BeforeEach(func() {
				_, err := dbConn.Exec("DROP TABLE components CASCADE")
				Expect(err).NotTo(HaveOccurred())
			})

			It("does not run the component, but releases the lock", func() {
				coordinator.RunImmediately(ctx)

				Expect(runCtxs).To(BeEmpty())

				releasedLock, acquired, err := otherLockFactory.Acquire(logger, lock.NewTaskLockID(componentName))
				Expect(err).NotTo(HaveOccurred())
				Expect(acquired).To(BeTrue(), "lock was not released")
				Expect(releasedLock.Release()).To(Succeed())
			})
		})

		Context("when the component disappeared", func() {
			BeforeEach(func() {
				_, err := dbConn.Exec("DELETE FROM components WHERE name = $1", componentName)
				Expect(err).NotTo(HaveOccurred())
			})

			It("does not run the component", func() {
				coordinator.RunImmediately(ctx)

				Expect(runCtxs).To(BeEmpty())
			})
		})
	})
})
