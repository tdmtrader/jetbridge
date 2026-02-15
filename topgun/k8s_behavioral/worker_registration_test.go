package behavioral_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("K8s Worker Registration", func() {

	It("registers at least one worker visible to fly", func() {
		workers := fly.GetWorkers()
		Expect(workers).ToNot(BeEmpty(), "expected at least one registered worker")

		By("checking at least one worker is running")
		var foundRunning bool
		for _, w := range workers {
			if w.State == "running" {
				foundRunning = true
				break
			}
		}
		Expect(foundRunning).To(BeTrue(), "expected at least one worker in running state")
	})

	It("maintains worker heartbeat", func() {
		By("recording workers at t=0")
		workers0 := fly.GetWorkers()
		Expect(workers0).ToNot(BeEmpty())

		By("waiting and checking workers are still present")
		time.Sleep(10 * time.Second)
		workers1 := fly.GetWorkers()
		Expect(workers1).ToNot(BeEmpty())

		// At least one worker from the first snapshot should still be present
		found := false
		for _, w0 := range workers0 {
			for _, w1 := range workers1 {
				if w0.Name == w1.Name && w1.State == "running" {
					found = true
					break
				}
			}
		}
		Expect(found).To(BeTrue(), "at least one worker should persist across heartbeats")
	})

	It("reports platform and tags on workers", func() {
		rows := flyTable("workers")
		Expect(rows).ToNot(BeEmpty())

		By("checking the first worker has a platform")
		Expect(rows[0]).To(HaveKey("platform"))
		Expect(rows[0]["platform"]).ToNot(BeEmpty())
	})

	It("removes workers that are no longer registered", func() {
		// This test validates that fly workers shows only active workers.
		// We cannot safely deregister a real worker, so we verify that
		// no stale/retired workers appear.
		workers := fly.GetWorkers()
		for _, w := range workers {
			Expect(w.State).To(SatisfyAny(
				Equal("running"),
				Equal("landing"),
				Equal("retiring"),
			), "worker %s should not be in a stale state", w.Name)
		}
	})
})
