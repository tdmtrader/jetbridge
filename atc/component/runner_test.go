package component_test

import (
	"context"
	"fmt"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/ifrit"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Runner", func() {
	const (
		componentName = "some_component"
		componentWait = 10 * time.Second
	)

	var (
		dbConn db.DbConn
		bus    db.NotificationsBus

		runStarted  chan context.Context
		releaseRun  chan struct{}
		runFinished chan struct{}
		finishRun   func()

		process       ifrit.Process
		processExited bool
	)

	BeforeEach(func() {
		postgresRunner.CreateTestDBFromTemplate()
		DeferCleanup(func() {
			postgresRunner.DropTestDB()
		})

		dbConn = postgresRunner.OpenConn()
		DeferCleanup(func() {
			Expect(dbConn.Close()).To(Succeed())
		})

		dbComponent, err := db.NewComponentFactory(dbConn).CreateOrUpdate(atc.Component{Name: componentName})
		Expect(err).NotTo(HaveOccurred())

		bus = dbConn.Bus()
		runStarted = make(chan context.Context, 8)
		releaseRun = make(chan struct{}, 8)
		runFinished = make(chan struct{}, 8)
		finishRun = func() {
			GinkgoHelper()
			releaseRun <- struct{}{}
			Eventually(runFinished, componentWait).Should(Receive())
		}

		runner := &component.Runner{
			Logger:    lagertest.NewTestLogger("test"),
			Component: dbComponent,
			Bus:       bus,
			Schedulable: &component.Coordinator{
				Locker:    newLockFactory(newLockConns()),
				Component: dbComponent,
				Runnable: component.RunFunc(func(ctx context.Context) error {
					runStarted <- ctx
					select {
					case <-releaseRun:
					case <-ctx.Done():
					}
					runFinished <- struct{}{}
					return nil
				}),
			},
		}

		process = ifrit.Background(runner)
		DeferCleanup(func() {
			if processExited {
				return
			}
			process.Signal(os.Interrupt)
			Eventually(process.Wait(), componentWait).Should(Receive(BeNil()))
		})

		select {
		case <-process.Ready():
		case err := <-process.Wait():
			Fail(fmt.Sprintf("process exited early: %v", err))
		}
	})

	It("fires an initial run through the real component lifecycle", func() {
		var ctx context.Context
		Eventually(runStarted, componentWait).Should(Receive(&ctx))
		Expect(ctx.Err()).NotTo(HaveOccurred())
		finishRun()
	})

	It("runs immediately when the component is notified", func() {
		Eventually(runStarted, componentWait).Should(Receive())
		finishRun()

		Expect(bus.Notify(componentName)).To(Succeed())
		Eventually(runStarted, componentWait).Should(Receive())
		finishRun()
	})

	It("does not run when another component is notified", func() {
		Eventually(runStarted, componentWait).Should(Receive())
		finishRun()

		Expect(bus.Notify("some_other_component")).To(Succeed())

		Consistently(runStarted, 200*time.Millisecond).ShouldNot(Receive())
	})

	It("coalesces notifications while a run is in progress", func() {
		Eventually(runStarted, componentWait).Should(Receive())
		finishRun()

		Expect(bus.Notify(componentName)).To(Succeed())
		Eventually(runStarted, componentWait).Should(Receive())

		const barrierChannel = "some_component_barrier"
		barrier, err := bus.ListenSignal(barrierChannel)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(bus.UnlistenSignal(barrierChannel, barrier)).To(Succeed())
		})

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = tx.Rollback() })
		for i := range 32 {
			_, err = tx.Exec("SELECT pg_notify($1, $2)", componentName, fmt.Sprintf("run-%02d", i))
			Expect(err).NotTo(HaveOccurred())
		}
		_, err = tx.Exec("SELECT pg_notify($1, $2)", barrierChannel, "all-runs-published")
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		Eventually(barrier.C(), componentWait).Should(Receive())

		finishRun()
		Eventually(runStarted, componentWait).Should(Receive())
		finishRun()

		Consistently(runStarted, 200*time.Millisecond).ShouldNot(Receive())
	})

	It("cancels an active run and exits cleanly", func() {
		var runCtx context.Context
		Eventually(runStarted, componentWait).Should(Receive(&runCtx))

		process.Signal(os.Interrupt)
		Eventually(runCtx.Done(), componentWait).Should(BeClosed())
		Eventually(runFinished, componentWait).Should(Receive())
		Eventually(process.Wait(), componentWait).Should(Receive(BeNil()))
		processExited = true

		replacement, err := bus.ListenSignal(componentName)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(bus.UnlistenSignal(componentName, replacement)).To(Succeed())
		})
		Expect(bus.Notify(componentName)).To(Succeed())
		Eventually(replacement.C(), componentWait).Should(Receive())
		Consistently(runStarted, 200*time.Millisecond).ShouldNot(Receive())
	})
})
