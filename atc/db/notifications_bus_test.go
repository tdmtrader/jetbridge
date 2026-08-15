package db_test

import (
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func listenForNotification(bus db.NotificationsBus, channel string) (*db.NotifySignal, func()) {
	GinkgoHelper()

	signal, err := bus.ListenSignal(channel)
	Expect(err).NotTo(HaveOccurred())

	var once sync.Once
	stop := func() {
		once.Do(func() {
			Expect(bus.UnlistenSignal(channel, signal)).To(Succeed())
		})
	}
	DeferCleanup(stop)

	return signal, stop
}

var _ = Describe("NotificationBus", func() {
	const (
		targetChannel   = "notifications_bus_target"
		otherChannel    = "notifications_bus_other"
		barrierChannel  = "notifications_bus_barrier"
		deliveryTimeout = 10 * time.Second
	)

	var (
		receiverBus db.NotificationsBus
		peerBus     db.NotificationsBus
		peerConn    db.DbConn
	)

	BeforeEach(func() {
		receiverBus = dbConn.Bus()

		peerConn = postgresRunner.OpenConn()
		DeferCleanup(func() {
			Expect(peerConn.Close()).To(Succeed())
		})
		peerBus = peerConn.Bus()
	})

	It("delivers by channel across independent buses and supports unlisten and relisten", func() {
		receiverA, stopReceiverA := listenForNotification(receiverBus, targetChannel)
		receiverB, stopReceiverB := listenForNotification(receiverBus, targetChannel)
		peer, _ := listenForNotification(peerBus, targetChannel)
		other, _ := listenForNotification(peerBus, otherChannel)

		Expect(peerBus.Notify(targetChannel)).To(Succeed())
		Eventually(receiverA.C(), deliveryTimeout).Should(Receive())
		Eventually(receiverB.C(), deliveryTimeout).Should(Receive())
		Eventually(peer.C(), deliveryTimeout).Should(Receive())
		Consistently(other.C(), 150*time.Millisecond).ShouldNot(Receive())

		stopReceiverA()
		Expect(receiverBus.Notify(targetChannel)).To(Succeed())
		Eventually(receiverB.C(), deliveryTimeout).Should(Receive())
		Eventually(peer.C(), deliveryTimeout).Should(Receive())
		Consistently(receiverA.C(), 150*time.Millisecond).ShouldNot(Receive())

		stopReceiverB()
		replacement, _ := listenForNotification(receiverBus, targetChannel)
		Expect(peerBus.Notify(targetChannel)).To(Succeed())
		Eventually(replacement.C(), deliveryTimeout).Should(Receive())
		Eventually(peer.C(), deliveryTimeout).Should(Receive())
	})

	It("coalesces committed notifications and resumes delivery after the signal is drained", func() {
		target, _ := listenForNotification(receiverBus, targetChannel)
		barrier, _ := listenForNotification(receiverBus, barrierChannel)

		tx, err := peerConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = tx.Rollback() })

		for i := range 32 {
			_, err = tx.Exec("SELECT pg_notify($1, $2)", targetChannel, fmt.Sprintf("event-%02d", i))
			Expect(err).NotTo(HaveOccurred())
		}
		_, err = tx.Exec("SELECT pg_notify($1, $2)", barrierChannel, "all-target-events-published")
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		Eventually(barrier.C(), deliveryTimeout).Should(Receive())
		Eventually(target.C(), deliveryTimeout).Should(Receive())
		Consistently(target.C(), 150*time.Millisecond).ShouldNot(Receive())

		Expect(peerBus.Notify(targetChannel)).To(Succeed())
		Eventually(target.C(), deliveryTimeout).Should(Receive())
	})
})
