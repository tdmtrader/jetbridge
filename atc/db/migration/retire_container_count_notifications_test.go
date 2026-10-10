package migration_test

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("retiring container-count notifications", func() {
	const before = 1789793157
	const after = 1789793158

	var db *sql.DB
	var listener *pgx.Conn

	BeforeEach(func() {
		db = postgresRunner.OpenDBAtVersion(before)
		var err error
		listener, err = pgx.Connect(context.Background(), postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		_, err = listener.Exec(context.Background(), `LISTEN worker_events_channel; LISTEN container_events_channel; LISTEN notification_barrier`)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		Expect(listener.Close(context.Background())).To(Succeed())
		Expect(db.Close()).To(Succeed())
	})

	expectNotification := func(channel string) {
		GinkgoHelper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		notification, err := listener.WaitForNotification(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(notification.Channel).To(Equal(channel))
	}

	exercise := func(containerNotifications bool) {
		GinkgoHelper()
		var functionExists, triggerExists bool
		Expect(db.QueryRow(`SELECT to_regprocedure('notify_container_event()') IS NOT NULL`).Scan(&functionExists)).To(Succeed())
		Expect(db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname = 'containers_notify_trigger')`).Scan(&triggerExists)).To(Succeed())
		Expect(functionExists).To(Equal(containerNotifications))
		Expect(triggerExists).To(Equal(containerNotifications))

		mustExec(db, `INSERT INTO workers(name, addr, state, platform, version) VALUES('notification-worker', '', 'running', 'linux', '2.5')`)
		expectNotification("worker_events_channel")
		mustExec(db, `UPDATE workers SET active_containers = 7 WHERE name = 'notification-worker'`)
		expectNotification("worker_events_channel")

		for _, query := range []string{
			`INSERT INTO containers(handle, worker_name) VALUES('notification-container', 'notification-worker')`,
			`DELETE FROM containers WHERE handle = 'notification-container'`,
		} {
			mustExec(db, query)
			// The barrier follows the committed mutation. If retirement failed,
			// its container notification arrives before this barrier.
			mustExec(db, `SELECT pg_notify('notification_barrier', '')`)
			if containerNotifications {
				expectNotification("container_events_channel")
			}
			expectNotification("notification_barrier")
		}

		mustExec(db, `DELETE FROM workers WHERE name = 'notification-worker'`)
		expectNotification("worker_events_channel")
	}

	It("removes container notifications and preserves worker notifications", func() {
		exercise(true)
		postgresRunner.MigrateToVersion(after)
		exercise(false)
	})
})
