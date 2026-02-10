package storage_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
	"github.com/concourse/ci-agent/storage"
)

var _ = Describe("Storage", func() {
	Describe("NewStore", func() {
		It("returns noop store when DATABASE_URL is empty", func() {
			store, err := storage.NewStore("")
			Expect(err).NotTo(HaveOccurred())
			Expect(store).NotTo(BeNil())

			// SaveReview should succeed (no-op).
			err = store.SaveReview(context.Background(), &schema.ReviewOutput{
				SchemaVersion: "1.0.0",
				Summary:       "test",
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns noop store for no-op operations", func() {
			store, _ := storage.NewStore("")
			review, err := store.GetReview(context.Background(), "repo", "abc123")
			Expect(err).NotTo(HaveOccurred())
			Expect(review).To(BeNil())
		})
	})

	Describe("NoopStore", func() {
		It("gracefully handles all operations", func() {
			store := &storage.NoopStore{}
			Expect(store.SaveReview(context.Background(), &schema.ReviewOutput{})).To(Succeed())

			r, err := store.GetReview(context.Background(), "repo", "commit")
			Expect(err).NotTo(HaveOccurred())
			Expect(r).To(BeNil())

			rs, err := store.ListReviews(context.Background(), "repo", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs).To(BeEmpty())
		})
	})
})
