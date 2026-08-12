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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tedsuo/ifrit"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type unlistenCall struct {
	channel string
	signal  *db.NotifySignal
}

type recordingBus struct {
	db.NotificationsBus

	listened   []string
	signal     *db.NotifySignal
	unlistened []unlistenCall
}

func (bus *recordingBus) ListenSignal(channel string) (*db.NotifySignal, error) {
	signal, err := bus.NotificationsBus.ListenSignal(channel)
	bus.listened = append(bus.listened, channel)
	bus.signal = signal
	return signal, err
}

func (bus *recordingBus) UnlistenSignal(channel string, signal *db.NotifySignal) error {
	bus.unlistened = append(bus.unlistened, unlistenCall{channel, signal})
	return bus.NotificationsBus.UnlistenSignal(channel, signal)
}

var _ = Describe("Runner", func() {
	const componentName = "some_component"

	var (
		bus *recordingBus

		ran chan context.Context

		process ifrit.Process
	)

	BeforeEach(func() {
		postgresRunner.CreateTestDBFromTemplate()
		DeferCleanup(func() {
			postgresRunner.DropTestDB()
		})

		dbConn := postgresRunner.OpenConn()
		DeferCleanup(func() {
			Expect(dbConn.Close()).To(Succeed())
		})

		dbComponent, err := db.NewComponentFactory(dbConn).CreateOrUpdate(atc.Component{Name: componentName})
		Expect(err).NotTo(HaveOccurred())

		pool, err := pgxpool.New(context.Background(), postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(pool.Close)

		bus = &recordingBus{
			NotificationsBus: db.NewNotificationsBus(db.NewPgxListener(pool), dbConn),
		}
		DeferCleanup(func() {
			Expect(bus.Close()).To(Succeed())
		})

		ran = make(chan context.Context, 100)

		runner := &component.Runner{
			Logger:    lagertest.NewTestLogger("test"),
			Component: dbComponent,
			Bus:       bus,
			Schedulable: &component.Coordinator{
				Locker:    newLockFactory(newLockConns()),
				Component: dbComponent,
				Runnable: component.RunFunc(func(ctx context.Context) error {
					ran <- ctx
					return nil
				}),
			},
		}

		process = ifrit.Background(runner)
		DeferCleanup(func() {
			process.Signal(os.Interrupt)
			Eventually(process.Wait()).Should(Receive(BeNil()))
		})

		select {
		case <-process.Ready():
		case err := <-process.Wait():
			Fail(fmt.Sprintf("process exited early: %v", err))
		}
	})

	It("listens for component signals on start and fires an initial run", func() {
		Expect(bus.listened).To(Equal([]string{componentName}))

		Eventually(ran).Should(Receive())
	})

	It("runs immediately when the component is notified", func() {
		Eventually(ran).Should(Receive())

		Expect(bus.Notify(componentName)).To(Succeed())
		Eventually(ran).Should(Receive())

		Expect(bus.Notify(componentName)).To(Succeed())
		Eventually(ran).Should(Receive())
	})

	It("does not run when another component is notified", func() {
		Eventually(ran).Should(Receive())

		Expect(bus.Notify("some_other_component")).To(Succeed())

		Consistently(ran, 200*time.Millisecond).ShouldNot(Receive())
	})

	It("coalesces signals", func() {
		Eventually(ran).Should(Receive())

		for i := 0; i < 100; i++ {
			bus.signal.Signal()
		}

		Eventually(ran).Should(Receive())

		Consistently(func() int {
			return len(ran)
		}, 200*time.Millisecond).Should(BeNumerically("<", 9), "100 signals should coalesce to far fewer than 10 wake-ups")
	})

	It("unlistens on exit", func() {
		Eventually(ran).Should(Receive())

		process.Signal(os.Interrupt)
		Eventually(process.Wait()).Should(Receive(BeNil()))

		Expect(bus.unlistened).To(HaveLen(1))
		Expect(bus.unlistened[0].channel).To(Equal(componentName))
		Expect(bus.unlistened[0].signal).To(BeIdenticalTo(bus.signal))
	})
})
