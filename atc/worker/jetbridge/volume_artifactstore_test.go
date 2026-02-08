package jetbridge_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ArtifactStoreVolume", func() {
	var (
		ctx          context.Context
		fakeDBVolume *dbfakes.FakeCreatedVolume
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBVolume = new(dbfakes.FakeCreatedVolume)
		fakeDBVolume.HandleReturns("asv-handle")
		fakeDBVolume.WorkerNameReturns("k8s-worker-1")
	})

	Describe("NewArtifactStoreVolume", func() {
		It("returns a volume with the correct key, handle, and source", func() {
			asv := jetbridge.NewArtifactStoreVolume(
				"caches/42.tar", "asv-handle", "k8s-worker-1", fakeDBVolume,
			)

			Expect(asv.Key()).To(Equal("caches/42.tar"))
			Expect(asv.Handle()).To(Equal("asv-handle"))
			Expect(asv.Source()).To(Equal("k8s-worker-1"))
			Expect(asv.DBVolume()).To(BeIdenticalTo(fakeDBVolume))
		})
	})

	Describe("StreamOut", func() {
		It("returns an error directing callers to use init containers", func() {
			asv := jetbridge.NewArtifactStoreVolume(
				"caches/1.tar", "handle", "worker", nil,
			)
			_, err := asv.StreamOut(ctx, ".", nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("init containers"))
		})
	})

	Describe("StreamIn", func() {
		It("returns an error directing callers to use artifact-helper", func() {
			asv := jetbridge.NewArtifactStoreVolume(
				"caches/1.tar", "handle", "worker", nil,
			)
			err := asv.StreamIn(ctx, ".", nil, 0, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("artifact-helper"))
		})
	})

	Describe("InitializeResourceCache", func() {
		Context("with dbVolume", func() {
			It("delegates to dbVolume", func() {
				expected := &db.UsedWorkerResourceCache{ID: 99}
				fakeDBVolume.InitializeResourceCacheReturns(expected, nil)

				asv := jetbridge.NewArtifactStoreVolume(
					"caches/1.tar", "handle", "worker", fakeDBVolume,
				)
				result, err := asv.InitializeResourceCache(ctx, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(Equal(expected))
			})
		})

		Context("without dbVolume", func() {
			It("returns nil, nil", func() {
				asv := jetbridge.NewArtifactStoreVolume(
					"caches/1.tar", "handle", "worker", nil,
				)
				result, err := asv.InitializeResourceCache(ctx, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(BeNil())
			})
		})
	})

	Describe("InitializeTaskCache", func() {
		Context("with dbVolume", func() {
			It("delegates to dbVolume", func() {
				fakeDBVolume.InitializeTaskCacheReturns(nil)

				asv := jetbridge.NewArtifactStoreVolume(
					"caches/1.tar", "handle", "worker", fakeDBVolume,
				)
				err := asv.InitializeTaskCache(ctx, 7, "build-step", "/cache", false)
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeDBVolume.InitializeTaskCacheCallCount()).To(Equal(1))
			})
		})

		Context("without dbVolume", func() {
			It("returns nil", func() {
				asv := jetbridge.NewArtifactStoreVolume(
					"caches/1.tar", "handle", "worker", nil,
				)
				err := asv.InitializeTaskCache(ctx, 0, "", "", false)
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})
})
