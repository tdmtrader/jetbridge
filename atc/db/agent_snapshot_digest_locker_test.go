package db_test

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentSnapshotDigestLocker", func() {
	var manager snapshot.DigestLockManager

	digest := func(hexDigit string) snapshot.Digest {
		return snapshot.Digest("sha256:" + strings.Repeat(hexDigit, 64))
	}

	BeforeEach(func() {
		// The suite defaults to one connection to expose accidental pool use;
		// advisory-lock behavior intentionally requires independent sessions.
		dbConn.SetMaxOpenConns(8)
		manager = db.NewAgentSnapshotDigestLocker(dbConn)
	})

	It("blocks the same digest on an independent connection until release", func() {
		value := digest("1")
		first, err := manager.AcquireMany(context.Background(), []snapshot.Digest{value})
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Covers(value)).To(BeTrue())

		blockedContext, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		partial, err := manager.AcquireMany(blockedContext, []snapshot.Digest{value})
		Expect(err).To(HaveOccurred())
		Expect(partial).NotTo(BeNil())
		Expect(partial.Close()).To(Succeed())

		Expect(first.Close()).To(Succeed())
		second, err := manager.AcquireMany(context.Background(), []snapshot.Digest{value})
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Close()).To(Succeed())
	})

	It("allows different digests to proceed and deduplicates unsorted coverage", func() {
		firstDigest, secondDigest := digest("2"), digest("3")
		first, err := manager.AcquireMany(context.Background(), []snapshot.Digest{secondDigest, firstDigest, secondDigest})
		Expect(err).NotTo(HaveOccurred())
		defer first.Close()
		Expect(first.Covers(firstDigest)).To(BeTrue())
		Expect(first.Covers(secondDigest)).To(BeTrue())

		thirdDigest := digest("4")
		second, err := manager.AcquireMany(context.Background(), []snapshot.Digest{thirdDigest})
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Close()).To(Succeed())
	})

	It("returns an acquired partial lease that releases earlier lexical locks", func() {
		freeDigest, blockedDigest := digest("5"), digest("6")
		blocker, err := manager.AcquireMany(context.Background(), []snapshot.Digest{blockedDigest})
		Expect(err).NotTo(HaveOccurred())
		defer blocker.Close()

		blockedContext, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		partial, err := manager.AcquireMany(blockedContext, []snapshot.Digest{blockedDigest, freeDigest})
		Expect(err).To(HaveOccurred())
		Expect(partial).NotTo(BeNil())
		Expect(partial.Covers(freeDigest)).To(BeTrue())
		Expect(partial.Covers(blockedDigest)).To(BeFalse())
		Expect(partial.Close()).To(Succeed())

		probe, err := manager.AcquireMany(context.Background(), []snapshot.Digest{freeDigest})
		Expect(err).NotTo(HaveOccurred())
		Expect(probe.Close()).To(Succeed())
	})

	It("closes concurrently and idempotently", func() {
		lease, err := manager.AcquireMany(context.Background(), []snapshot.Digest{digest("7"), digest("8")})
		Expect(err).NotTo(HaveOccurred())

		errors := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				errors <- lease.Close()
			}()
		}
		wg.Wait()
		close(errors)
		for err := range errors {
			Expect(err).NotTo(HaveOccurred())
		}
	})
})
