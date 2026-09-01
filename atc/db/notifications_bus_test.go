package db_test

import (
	"database/sql"
	"errors"

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

		Context("when there are multiple listeners on different channels", func() {
			var a, b *db.NotifySignal

			BeforeEach(func() {
				var err error
				a, err = bus.ListenSignal("some-channel")
				Expect(err).NotTo(HaveOccurred())

				b, err = bus.ListenSignal("some-other-channel")
				Expect(err).NotTo(HaveOccurred())
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

})
