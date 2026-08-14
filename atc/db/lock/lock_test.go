package lock_test

import (
	"database/sql"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Locks", func() {
	var (
		lockFactory lock.LockFactory

		dbLock lock.Lock

		dbConn db.DbConn

		team        db.Team
		teamFactory db.TeamFactory

		logger      *lagertest.TestLogger
		fakeLogFunc = func(logger lager.Logger, id lock.LockID) {}

		lockConns [lock.FactoryCount]*sql.DB
	)

	BeforeEach(func() {
		postgresRunner.CreateTestDBFromTemplate()
		DeferCleanup(func() {
			postgresRunner.DropTestDB()
		})

		logger = lagertest.NewTestLogger("test")

		for i := 0; i < lock.FactoryCount; i++ {
			lockConn := postgresRunner.OpenSingleton()
			lockConns[i] = lockConn
			DeferCleanup(func() {
				Expect(lockConn.Close()).To(Succeed())
			})
		}

		lockFactory = lock.NewLockFactory(lockConns, fakeLogFunc, fakeLogFunc)

		dbConn = postgresRunner.OpenConn()
		DeferCleanup(func() {
			Expect(dbConn.Close()).To(Succeed())
		})
		teamFactory = db.NewTeamFactory(dbConn, lockFactory)

		var err error
		team, err = teamFactory.CreateTeam(atc.Team{Name: "team-name"})
		Expect(err).NotTo(HaveOccurred())
	})

	JustBeforeEach(func() {
		// Register after all nested BeforeEach nodes so a held lock is released
		// before any pool that may own it is closed. Some specs deliberately
		// release or lose the lock first, so cleanup remains best-effort.
		DeferCleanup(func() {
			if dbLock != nil {
				_ = dbLock.Release()
			}
		})
	})

	Describe("locks in general", func() {
		It("Acquire can only obtain lock once", func() {
			var acquired bool
			var err error
			dbLock, acquired, err = lockFactory.Acquire(logger, lock.LockID{42})
			Expect(err).NotTo(HaveOccurred())
			Expect(acquired).To(BeTrue())

			_, acquired, err = lockFactory.Acquire(logger, lock.LockID{42})
			Expect(err).NotTo(HaveOccurred())
			Expect(acquired).To(BeFalse())
		})

		It("Acquire accepts list of ids", func() {
			var acquired bool
			var err error
			dbLock, acquired, err = lockFactory.Acquire(logger, lock.LockID{42, 56})
			Expect(err).NotTo(HaveOccurred())
			Expect(acquired).To(BeTrue())

			Consistently(func() error {
				connCount := 3

				var anyError error
				var wg sync.WaitGroup
				wg.Add(connCount)

				for i := 0; i < connCount; i++ {
					go func() {
						defer wg.Done()

						_, _, err := lockFactory.Acquire(logger, lock.LockID{42, 56})
						if err != nil {
							anyError = err
						}

					}()
				}

				wg.Wait()

				return anyError
			}, 1500*time.Millisecond, 100*time.Millisecond).ShouldNot(HaveOccurred())

			dbLock, acquired, err = lockFactory.Acquire(logger, lock.LockID{56, 42})
			Expect(err).NotTo(HaveOccurred())
			Expect(acquired).To(BeTrue())

			_, acquired, err = lockFactory.Acquire(logger, lock.LockID{56, 42})
			Expect(err).NotTo(HaveOccurred())
			Expect(acquired).To(BeFalse())
		})

		Context("when another connection is holding the lock", func() {
			var lockFactory2 lock.LockFactory
			var lockConns2 [lock.FactoryCount]*sql.DB

			BeforeEach(func() {
				for i := 0; i < lock.FactoryCount; i++ {
					lockConn := postgresRunner.OpenSingleton()
					lockConns2[i] = lockConn
					DeferCleanup(func() {
						Expect(lockConn.Close()).To(Succeed())
					})
				}
				lockFactory2 = lock.NewLockFactory(lockConns2, fakeLogFunc, fakeLogFunc)
			})

			It("does not acquire the lock", func() {
				var acquired bool
				var err error
				dbLock, acquired, err = lockFactory.Acquire(logger, lock.LockID{42})
				Expect(err).NotTo(HaveOccurred())
				Expect(acquired).To(BeTrue())

				_, acquired, err = lockFactory2.Acquire(logger, lock.LockID{42})
				Expect(err).NotTo(HaveOccurred())
				Expect(acquired).To(BeFalse())

				err = dbLock.Release()
				Expect(err).NotTo(HaveOccurred())
			})

			It("acquires the locks once it is released", func() {
				var acquired bool
				var err error
				dbLock, acquired, err = lockFactory.Acquire(logger, lock.LockID{42})
				Expect(err).NotTo(HaveOccurred())
				Expect(acquired).To(BeTrue())

				_, acquired, err = lockFactory2.Acquire(logger, lock.LockID{42})
				Expect(err).NotTo(HaveOccurred())
				Expect(acquired).To(BeFalse())

				err = dbLock.Release()
				Expect(err).NotTo(HaveOccurred())

				dbLock2, acquired, err := lockFactory2.Acquire(logger, lock.LockID{42})
				Expect(err).NotTo(HaveOccurred())
				Expect(acquired).To(BeTrue())

				err = dbLock2.Release()
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("allows exactly one concurrent caller to acquire a lock", func() {
			type acquisition struct {
				lock     lock.Lock
				acquired bool
				err      error
			}

			var secondConns [lock.FactoryCount]*sql.DB
			for i := range lock.FactoryCount {
				conn := postgresRunner.OpenSingleton()
				secondConns[i] = conn
				DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
			}
			secondFactory := lock.NewLockFactory(secondConns, fakeLogFunc, fakeLogFunc)

			start := make(chan struct{})
			results := make(chan acquisition, 2)

			for _, factory := range []lock.LockFactory{lockFactory, secondFactory} {
				go func(factory lock.LockFactory) {
					<-start
					acquiredLock, acquired, err := factory.Acquire(logger, lock.LockID{57})
					results <- acquisition{lock: acquiredLock, acquired: acquired, err: err}
				}(factory)
			}

			close(start)
			first := <-results
			second := <-results

			Expect(first.err).NotTo(HaveOccurred())
			Expect(second.err).NotTo(HaveOccurred())
			Expect([]bool{first.acquired, second.acquired}).To(ConsistOf(true, false))

			if first.acquired {
				Expect(first.lock.Release()).To(Succeed())
			} else {
				Expect(second.lock.Release()).To(Succeed())
			}
		})
	})

	Describe("taking out a lock on build tracking", func() {
		var build db.Build

		BeforeEach(func() {
			var err error
			build, err = team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
		})

		It("gets and keeps the lock and stops others from getting it", func() {
			lock, acquired, err := build.AcquireTrackingLock(logger, 1*time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(acquired).To(BeTrue())

			Consistently(func() bool {
				_, acquired, err = build.AcquireTrackingLock(logger, 1*time.Second)
				Expect(err).NotTo(HaveOccurred())

				return acquired
			}, 1500*time.Millisecond, 100*time.Millisecond).Should(BeFalse())

			err = lock.Release()
			Expect(err).NotTo(HaveOccurred())

			time.Sleep(time.Second)

			newLock, acquired, err := build.AcquireTrackingLock(logger, 1*time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(acquired).To(BeTrue())

			err = newLock.Release()
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
