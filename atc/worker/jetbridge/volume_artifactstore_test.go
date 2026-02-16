package jetbridge_test

import (
	"context"
	"fmt"
	"io"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = fmt.Sprintf // ensure fmt is used

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
		Context("without executor", func() {
			It("returns an error indicating no executor is configured", func() {
				asv := jetbridge.NewArtifactStoreVolume(
					"caches/1.tar", "handle", "worker", nil,
				)
				_, err := asv.StreamOut(ctx, ".", nil)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no executor"))
			})
		})

		Context("with executor", func() {
			var (
				fakeExecutor *fakeExecExecutor
				asv          *jetbridge.ArtifactStoreVolume
			)

			BeforeEach(func() {
				fakeExecutor = &fakeExecExecutor{}
				fakeExecutor.execStdout = []byte("tar-output-from-pvc")

				asv = jetbridge.NewArtifactStoreVolume(
					"artifacts/vol-123.tar", "vol-123", "k8s-worker-1", fakeDBVolume,
				)
				asv.SetExecutor(fakeExecutor, "build-pod-abc", "concourse")
			})

			It("execs a tar command in the artifact-helper sidecar", func() {
				readCloser, err := asv.StreamOut(ctx, ".", nil)
				Expect(err).ToNot(HaveOccurred())
				defer readCloser.Close()

				_, _ = io.ReadAll(readCloser)

				Expect(fakeExecutor.execCalls).To(HaveLen(1))
				call := fakeExecutor.execCalls[0]
				Expect(call.podName).To(Equal("build-pod-abc"))
				Expect(call.namespace).To(Equal("concourse"))
				Expect(call.containerName).To(Equal("artifact-helper"))
			})

			It("returns tar stream data from the executor", func() {
				readCloser, err := asv.StreamOut(ctx, ".", nil)
				Expect(err).ToNot(HaveOccurred())
				defer readCloser.Close()

				data, err := io.ReadAll(readCloser)
				Expect(err).ToNot(HaveOccurred())
				Expect(data).To(Equal([]byte("tar-output-from-pvc")))
			})

			It("extracts from the PVC tar and re-tars the root path", func() {
				readCloser, err := asv.StreamOut(ctx, ".", nil)
				Expect(err).ToNot(HaveOccurred())
				defer readCloser.Close()
				_, _ = io.ReadAll(readCloser)

				call := fakeExecutor.execCalls[0]
				// The command should extract the PVC tar to a tmpdir, then tar from there
				Expect(call.command[0]).To(Equal("sh"))
				Expect(call.command[1]).To(Equal("-c"))
				Expect(call.command[2]).To(ContainSubstring("/artifacts/artifacts/vol-123.tar"))
				Expect(call.command[2]).To(ContainSubstring("tar xf"))
				Expect(call.command[2]).To(ContainSubstring("tar cf"))
			})

			It("handles a specific file path", func() {
				readCloser, err := asv.StreamOut(ctx, "pipeline.yml", nil)
				Expect(err).ToNot(HaveOccurred())
				defer readCloser.Close()
				_, _ = io.ReadAll(readCloser)

				call := fakeExecutor.execCalls[0]
				Expect(call.command[2]).To(ContainSubstring("pipeline.yml"))
			})

			It("handles a nested file path", func() {
				readCloser, err := asv.StreamOut(ctx, "ci/pipeline.yml", nil)
				Expect(err).ToNot(HaveOccurred())
				defer readCloser.Close()
				_, _ = io.ReadAll(readCloser)

				call := fakeExecutor.execCalls[0]
				Expect(call.command[2]).To(ContainSubstring("ci/pipeline.yml"))
			})

			Context("when the exec returns an error", func() {
				BeforeEach(func() {
					fakeExecutor.execErr = fmt.Errorf("pod not found")
				})

				It("propagates the error through the pipe reader", func() {
					readCloser, err := asv.StreamOut(ctx, ".", nil)
					Expect(err).ToNot(HaveOccurred())
					defer readCloser.Close()

					_, err = io.ReadAll(readCloser)
					Expect(err).To(MatchError(ContainSubstring("pod not found")))
				})
			})
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
