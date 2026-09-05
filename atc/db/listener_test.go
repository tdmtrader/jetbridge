package db_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PgxListener", func() {

	var (
		err          error
		notifierConn db.DbConn
		listener     *db.PgxListener

		testPayload = "hello"
	)

	BeforeEach(func(ctx context.Context) {
		notifierConn = postgresRunner.OpenConn()

		pool, err := pgxpool.New(ctx, postgresRunner.DataSourceName())
		Expect(err).ToNot(HaveOccurred())

		listener = db.NewPgxListener(pool)
		Expect(listener).ToNot(BeNil())
	})

	AfterEach(func(ctx context.Context) {
		err = notifierConn.Close()
		Expect(err).ToNot(HaveOccurred())

		c := listener.NotificationChannel()
		err = listener.Close()
		Expect(err).ToNot(HaveOccurred())
		Eventually(ctx, c).WithTimeout(time.Second).Should(BeClosed())
	})

	Context("Listen()", func() {
		It("succeeds", func(ctx context.Context) {
			err = listener.Listen("test")
			Expect(err).ToNot(HaveOccurred())

			_, err = notifierConn.Exec("select pg_notify('test', $1)", testPayload)
			Expect(err).ToNot(HaveOccurred())

			var notification *pgconn.Notification
			c := listener.NotificationChannel()
			Eventually(ctx, c).WithTimeout(time.Second).Should(Receive(&notification))
			Expect(notification.Payload).To(Equal(testPayload))
		})

		It("listening on the same channel twice results in no error", func(ctx context.Context) {
			err = listener.Listen("test")
			Expect(err).ToNot(HaveOccurred())

			err = listener.Listen("test")
			Expect(err).ToNot(HaveOccurred())

			_, err = notifierConn.Exec("select pg_notify('test', $1)", testPayload)
			Expect(err).ToNot(HaveOccurred())

			var notification *pgconn.Notification
			c := listener.NotificationChannel()
			Eventually(ctx, c).WithTimeout(time.Second).Should(Receive(&notification))
			Expect(notification.Payload).To(Equal(testPayload))
		})

		It("listens on multiple channels", func(ctx context.Context) {
			payload1 := "hello1"
			payload2 := "hello2"

			err = listener.Listen("test1")
			Expect(err).ToNot(HaveOccurred())

			err = listener.Listen("test2")
			Expect(err).ToNot(HaveOccurred())

			_, err = notifierConn.Exec("select pg_notify('test1', $1)", payload1)
			Expect(err).ToNot(HaveOccurred())
			_, err = notifierConn.Exec("select pg_notify('test2', $1)", payload2)
			Expect(err).ToNot(HaveOccurred())

			var notification *pgconn.Notification
			c := listener.NotificationChannel()
			Eventually(ctx, c).WithTimeout(time.Second).Should(Receive(&notification))
			Expect(notification.Payload).To(Equal(payload1))
			Eventually(ctx, c).WithTimeout(time.Second).Should(Receive(&notification))
			Expect(notification.Payload).To(Equal(payload2))
		})
	})

	Context("when the connection is lost", func() {
		It("reports the disconnect and listens again on the new connection", func(ctx context.Context) {
			err = listener.Listen("test")
			Expect(err).ToNot(HaveOccurred())

			c := listener.NotificationChannel()

			By("dropping the listener's backend out from under it")
			_, err = notifierConn.Exec(`
				SELECT pg_terminate_backend(pid)
				FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND query = 'LISTEN test'`)
			Expect(err).ToNot(HaveOccurred())

			By("delivering the disconnect notice, since anything notified meanwhile is gone")
			var notification *pgconn.Notification
			Eventually(ctx, c).WithTimeout(30 * time.Second).Should(Receive(&notification))
			Expect(notification).To(BeNil())

			By("delivering notifications again on the reconnected connection")
			Eventually(ctx, func() bool {
				_, notifyErr := notifierConn.Exec("select pg_notify('test', $1)", testPayload)
				Expect(notifyErr).ToNot(HaveOccurred())

				select {
				case n := <-c:
					return n != nil && n.Payload == testPayload
				case <-time.After(200 * time.Millisecond):
					return false
				}
			}).WithTimeout(30 * time.Second).Should(BeTrue())
		})
	})

	Context("Unlisten()", func() {
		It("succeeds after listening on a channel", func(ctx context.Context) {
			err = listener.Listen("test")
			Expect(err).ToNot(HaveOccurred())

			err = listener.Unlisten("test")
			Expect(err).ToNot(HaveOccurred())

			_, err = notifierConn.Exec("select pg_notify('test', $1)", testPayload)
			Expect(err).ToNot(HaveOccurred())

			c := listener.NotificationChannel()
			Eventually(ctx, c).WithTimeout(time.Second).ShouldNot(Receive())
		})

		It("succeeds even if we weren't listening on the channel", func(ctx context.Context) {
			err = listener.Unlisten("test")
			Expect(err).ToNot(HaveOccurred())

			_, err = notifierConn.Exec("select pg_notify('test', $1)", testPayload)
			Expect(err).ToNot(HaveOccurred())

			c := listener.NotificationChannel()
			Eventually(ctx, c).WithTimeout(time.Second).ShouldNot(Receive())
		})
	})
})
