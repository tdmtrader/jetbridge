package db_test

import (
	"database/sql"
	"errors"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/jackc/pgx/v5/pgconn"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type stubExecutor struct {
	statements []string
	err        error
}

func (e *stubExecutor) Exec(statement string, args ...any) (sql.Result, error) {
	e.statements = append(e.statements, statement)
	return nil, e.err
}

type stubListener struct {
	notifications chan *pgconn.Notification

	listened   []string
	unlistened []string

	listenErr   error
	unlistenErr error

	onListen   func()
	onUnlisten func()
}

func (l *stubListener) Close() error {
	close(l.notifications)
	return nil
}

func (l *stubListener) Listen(channel string) error {
	l.listened = append(l.listened, channel)
	if l.onListen != nil {
		l.onListen()
	}
	return l.listenErr
}

func (l *stubListener) Unlisten(channel string) error {
	l.unlistened = append(l.unlistened, channel)
	if l.onUnlisten != nil {
		l.onUnlisten()
	}
	return l.unlistenErr
}

func (l *stubListener) NotificationChannel() <-chan *pgconn.Notification {
	return l.notifications
}

var _ = Describe("NotificationBus", func() {

	var (
		c        chan *pgconn.Notification
		executor *stubExecutor
		listener *stubListener

		bus db.NotificationsBus
	)

	BeforeEach(func() {
		c = make(chan *pgconn.Notification, 1)

		executor = new(stubExecutor)
		listener = &stubListener{notifications: c}

		bus = db.NewNotificationsBus(listener, executor)
		DeferCleanup(bus.Close)
	})

	Context("Notify", func() {
		var (
			err error
		)

		JustBeforeEach(func() {
			err = bus.Notify("some-channel")
		})

		It("notifies the channel", func() {
			Expect(executor.statements).To(Equal([]string{"NOTIFY some-channel"}))
		})

		Context("when the executor errors", func() {
			BeforeEach(func() {
				executor.err = errors.New("nope")
			})

			It("errors", func() {
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when the executor succeeds", func() {
			It("succeeds", func() {
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Context("ListenSignal", func() {
		var (
			err error
		)

		JustBeforeEach(func() {
			_, err = bus.ListenSignal("some-channel")
		})

		Context("when not already listening on channel", func() {
			It("listens on the given channel", func() {
				Expect(listener.listened).To(Equal([]string{"some-channel"}))
			})

			Context("when listening errors", func() {
				BeforeEach(func() {
					listener.listenErr = errors.New("nope")
				})

				It("errors", func() {
					Expect(err).To(HaveOccurred())
				})
			})

			Context("when listening succeeds", func() {
				It("succeeds", func() {
					Expect(err).NotTo(HaveOccurred())
				})
			})
		})

		Context("when already listening on the channel", func() {
			BeforeEach(func() {
				_, err := bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())
			})

			It("only listens once", func() {
				Expect(listener.listened).To(HaveLen(1))
			})
		})
	})

	Context("UnlistenSignal", func() {
		var (
			err    error
			signal *db.NotifySignal
		)

		JustBeforeEach(func() {
			err = bus.UnlistenSignal("some-channel", signal)
		})

		Context("when there's only one listener", func() {
			BeforeEach(func() {
				signal, err = bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())
			})

			It("unlistens on the given channel", func() {
				Expect(listener.unlistened).To(Equal([]string{"some-channel"}))
			})

			Context("when unlistening errors", func() {
				BeforeEach(func() {
					listener.unlistenErr = errors.New("nope")
				})

				It("errors", func() {
					Expect(err).To(HaveOccurred())
				})
			})

			Context("when unlistening succeeds", func() {
				It("succeeds", func() {
					Expect(err).NotTo(HaveOccurred())
				})
			})
		})

		Context("when there's multiple listeners", func() {
			BeforeEach(func() {
				signal, err = bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())

				_, err = bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())
			})

			It("succeeds", func() {
				Expect(err).NotTo(HaveOccurred())
			})

			It("does not unlisten on the given channel", func() {
				Expect(listener.unlistened).To(BeEmpty())
			})
		})
	})

	Describe("Receiving Signals", func() {
		Context("when there are multiple listeners for the same channel", func() {
			var a, b *db.NotifySignal

			BeforeEach(func() {
				var err error
				a, err = bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())

				b, err = bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when it receives an upstream notification", func() {
				BeforeEach(func() {
					c <- &pgconn.Notification{Channel: "some-channel"}
				})

				It("delivers the signal to all listeners", func() {
					Eventually(a.C()).Should(Receive())
					Eventually(b.C()).Should(Receive())
				})
			})

			Context("when it receives an upstream disconnect notice", func() {
				BeforeEach(func() {
					c <- nil
				})

				It("delivers the signal to all listeners", func() {
					Eventually(a.C()).Should(Receive())
					Eventually(b.C()).Should(Receive())
				})
			})

			Context("when one of the listeners unlistens", func() {
				BeforeEach(func() {
					bus.UnlistenSignal("some-channel", a)
				})

				It("should still send signals to the other listeners", func() {
					c <- &pgconn.Notification{Channel: "some-channel"}
					Eventually(b.C()).Should(Receive())
				})
			})
		})

		Context("when there are multiple listeners on different channels", func() {
			var a, b *db.NotifySignal

			BeforeEach(func() {
				var err error
				a, err = bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())

				b, err = bus.ListenSignal("some-other-channel")
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when it receives an upstream notification", func() {
				BeforeEach(func() {
					c <- &pgconn.Notification{Channel: "some-channel"}
				})

				It("delivers the signal to only specific listeners", func() {
					Eventually(a.C()).Should(Receive())
					Consistently(b.C()).ShouldNot(Receive())
				})
			})

			Context("when it receives an upstream disconnect notice", func() {
				BeforeEach(func() {
					c <- nil
				})

				It("delivers the signal to all listeners", func() {
					Eventually(a.C()).Should(Receive())
					Eventually(b.C()).Should(Receive())
				})
			})
		})

		Context("when the signal coalesces", func() {
			var a *db.NotifySignal

			BeforeEach(func() {
				var err error
				a, err = bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when it receives many upstream notifications", func() {
				BeforeEach(func() {
					for i := 0; i < 100; i++ {
						c <- &pgconn.Notification{Channel: "some-channel"}
					}
					Eventually(c).Should(BeEmpty())
					// allow time for the last event to be processed
					time.Sleep(1 * time.Second)
				})

				It("only sends one signal to the Go channel", func() {
					Eventually(a.C()).Should(Receive())
					Consistently(a.C()).ShouldNot(Receive())
				})

				It("should send signals again after the channel is drained", func() {
					<-a.C()

					c <- &pgconn.Notification{Channel: "some-channel"}
					Eventually(a.C()).Should(Receive())
				})
			})
		})

		Context("when the notification channel fills up while listening", func() {
			BeforeEach(func() {
				listener.onListen = func() {
					c <- &pgconn.Notification{Channel: "some-channel"}
					c <- &pgconn.Notification{Channel: "some-channel"}
					c <- &pgconn.Notification{Channel: "some-channel"}
				}
			})

			It("should still be able to listen for signals", func() {
				_, err := bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())

				_, err = bus.ListenSignal("some-other-channel")
				Expect(err).NotTo(HaveOccurred())

				_, err = bus.ListenSignal("some-new-channel")
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("when the notification channel fills up while unlistening", func() {
			var a *db.NotifySignal

			BeforeEach(func() {
				listener.onUnlisten = func() {
					c <- &pgconn.Notification{Channel: "some-channel"}
					c <- &pgconn.Notification{Channel: "some-channel"}
					c <- &pgconn.Notification{Channel: "some-channel"}
				}

				var err error
				a, err = bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())
			})

			It("should still be able to unlisten for signals", func() {
				err := bus.UnlistenSignal("some-channel", a)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("backed by Postgres LISTEN/NOTIFY", func() {
		const channel = "notifications_bus_round_trip"
		const otherChannel = "notifications_bus_round_trip_other"

		var (
			realBus db.NotificationsBus
			signal  *db.NotifySignal
		)

		BeforeEach(func() {
			realBus = dbConn.Bus()

			var err error
			signal, err = realBus.ListenSignal(channel)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				Expect(realBus.UnlistenSignal(channel, signal)).To(Succeed())
			})
		})

		It("delivers a signal for a NOTIFY that round-trips through Postgres", func() {
			Expect(realBus.Notify(channel)).To(Succeed())

			Eventually(signal.C(), 10*time.Second).Should(Receive())
		})

		It("does not deliver a signal for a channel it is not listening on", func() {
			Expect(realBus.Notify(otherChannel)).To(Succeed())

			Consistently(signal.C(), time.Second).ShouldNot(Receive())
		})
	})
})
