package db_test

import (
	"errors"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/jackc/pgx/v5/pgconn"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("NotificationBus", func() {

	var (
		c            chan *pgconn.Notification
		fakeExecutor *dbfakes.FakeExecutor
		fakeListener *dbfakes.FakeListener

		bus db.NotificationsBus
	)

	BeforeEach(func() {
		c = make(chan *pgconn.Notification, 1)

		fakeExecutor = new(dbfakes.FakeExecutor)
		fakeListener = new(dbfakes.FakeListener)
		fakeListener.NotificationChannelReturns(c)

		bus = db.NewNotificationsBus(fakeListener, fakeExecutor)
	})

	Context("Notify", func() {
		var (
			err error
		)

		JustBeforeEach(func() {
			err = bus.Notify("some-channel")
		})

		It("notifies the channel", func() {
			Expect(fakeExecutor.ExecCallCount()).To(Equal(1))
			msg, _ := fakeExecutor.ExecArgsForCall(0)
			Expect(msg).To(Equal("NOTIFY some-channel"))
		})

		Context("when the executor errors", func() {
			BeforeEach(func() {
				fakeExecutor.ExecReturns(nil, errors.New("nope"))
			})

			It("errors", func() {
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when the executor succeeds", func() {
			BeforeEach(func() {
				fakeExecutor.ExecReturns(nil, nil)
			})

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
				Expect(fakeListener.ListenCallCount()).To(Equal(1))
				channel := fakeListener.ListenArgsForCall(0)
				Expect(channel).To(Equal("some-channel"))
			})

			Context("when listening errors", func() {
				BeforeEach(func() {
					fakeListener.ListenReturns(errors.New("nope"))
				})

				It("errors", func() {
					Expect(err).To(HaveOccurred())
				})
			})

			Context("when listening succeeds", func() {
				BeforeEach(func() {
					fakeListener.ListenReturns(nil)
				})

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
				Expect(fakeListener.ListenCallCount()).To(Equal(1))
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
				Expect(fakeListener.UnlistenCallCount()).To(Equal(1))
				channel := fakeListener.UnlistenArgsForCall(0)
				Expect(channel).To(Equal("some-channel"))
			})

			Context("when unlistening errors", func() {
				BeforeEach(func() {
					fakeListener.UnlistenReturns(errors.New("nope"))
				})

				It("errors", func() {
					Expect(err).To(HaveOccurred())
				})
			})

			Context("when unlistening succeeds", func() {
				BeforeEach(func() {
					fakeListener.UnlistenReturns(nil)
				})

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
				Expect(fakeListener.UnlistenCallCount()).To(Equal(0))
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
				fakeListener.ListenCalls(func(_ string) error {
					c <- &pgconn.Notification{Channel: "some-channel"}
					c <- &pgconn.Notification{Channel: "some-channel"}
					c <- &pgconn.Notification{Channel: "some-channel"}
					return nil
				})
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
				fakeListener.UnlistenCalls(func(_ string) error {
					c <- &pgconn.Notification{Channel: "some-channel"}
					c <- &pgconn.Notification{Channel: "some-channel"}
					c <- &pgconn.Notification{Channel: "some-channel"}
					return nil
				})

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
