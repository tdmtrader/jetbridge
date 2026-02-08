package k8sruntime_test

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Volume", func() {
	var (
		ctx           context.Context
		fakeDBVolume  *dbfakes.FakeCreatedVolume
		fakeExecutor  *fakeExecExecutor
		volume        *k8sruntime.Volume
		podName       string
		namespace     string
		containerName string
		mountPath     string
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBVolume = new(dbfakes.FakeCreatedVolume)
		fakeDBVolume.HandleReturns("vol-handle-123")
		fakeDBVolume.WorkerNameReturns("k8s-worker-1")
		fakeExecutor = &fakeExecExecutor{}

		podName = "test-pod"
		namespace = "test-namespace"
		containerName = "main"
		mountPath = "/tmp/build/inputs"

		volume = k8sruntime.NewVolume(
			fakeDBVolume,
			fakeExecutor,
			podName,
			namespace,
			containerName,
			mountPath,
		)
	})

	Describe("Handle", func() {
		It("returns the db volume handle", func() {
			Expect(volume.Handle()).To(Equal("vol-handle-123"))
		})
	})

	Describe("Source", func() {
		It("returns the worker name from the db volume", func() {
			Expect(volume.Source()).To(Equal("k8s-worker-1"))
		})
	})

	Describe("DBVolume", func() {
		It("returns the underlying db volume", func() {
			Expect(volume.DBVolume()).To(BeIdenticalTo(fakeDBVolume))
		})
	})

	Describe("StreamIn", func() {
		It("execs tar extract in the correct Pod container at the specified path", func() {
			reader := bytes.NewReader([]byte("tar-data"))

			err := volume.StreamIn(ctx, ".", nil, 0, reader)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			call := fakeExecutor.execCalls[0]
			Expect(call.podName).To(Equal("test-pod"))
			Expect(call.namespace).To(Equal("test-namespace"))
			Expect(call.containerName).To(Equal("main"))
			Expect(call.command).To(Equal([]string{"tar", "xf", "-", "-C", "/tmp/build/inputs"}))
		})

		It("pipes the reader data to stdin of the exec", func() {
			inputData := []byte("some-tar-stream-data")
			reader := bytes.NewReader(inputData)

			err := volume.StreamIn(ctx, ".", nil, 0, reader)
			Expect(err).ToNot(HaveOccurred())

			call := fakeExecutor.execCalls[0]
			Expect(call.stdin).ToNot(BeNil())
			stdinData, err := io.ReadAll(call.stdin)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdinData).To(Equal(inputData))
		})

		It("uses a subdirectory path when path is not root", func() {
			reader := bytes.NewReader([]byte("tar-data"))

			err := volume.StreamIn(ctx, "sub/dir", nil, 0, reader)
			Expect(err).ToNot(HaveOccurred())

			call := fakeExecutor.execCalls[0]
			Expect(call.command).To(Equal([]string{"tar", "xf", "-", "-C", "/tmp/build/inputs/sub/dir"}))
		})

		Context("when the exec returns an error", func() {
			BeforeEach(func() {
				fakeExecutor.execErr = errors.New("exec failed: container not running")
			})

			It("returns the error", func() {
				reader := bytes.NewReader([]byte("data"))
				err := volume.StreamIn(ctx, ".", nil, 0, reader)
				Expect(err).To(MatchError(ContainSubstring("exec failed")))
			})
		})
	})

	Describe("StreamOut", func() {
		BeforeEach(func() {
			fakeExecutor.execStdout = []byte("tar-output-bytes")
		})

		It("execs tar create in the correct Pod container at the specified path", func() {
			readCloser, err := volume.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			call := fakeExecutor.execCalls[0]
			Expect(call.podName).To(Equal("test-pod"))
			Expect(call.namespace).To(Equal("test-namespace"))
			Expect(call.containerName).To(Equal("main"))
			Expect(call.command).To(Equal([]string{"tar", "cf", "-", "-C", "/tmp/build/inputs", "."}))
		})

		It("returns the stdout as a ReadCloser", func() {
			readCloser, err := volume.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			data, err := io.ReadAll(readCloser)
			Expect(err).ToNot(HaveOccurred())
			Expect(data).To(Equal([]byte("tar-output-bytes")))
		})

		It("uses a subdirectory path when path is not root", func() {
			readCloser, err := volume.StreamOut(ctx, "sub/dir", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			call := fakeExecutor.execCalls[0]
			Expect(call.command).To(Equal([]string{"tar", "cf", "-", "-C", "/tmp/build/inputs/sub/dir", "."}))
		})

		Context("when the exec returns an error", func() {
			BeforeEach(func() {
				fakeExecutor.execErr = errors.New("exec failed: pod terminated")
			})

			It("returns the error", func() {
				_, err := volume.StreamOut(ctx, ".", nil)
				Expect(err).To(MatchError(ContainSubstring("exec failed")))
			})
		})
	})

	Describe("volume uniqueness", func() {
		It("two volumes with different handles are distinguishable", func() {
			fakeDBVolume2 := new(dbfakes.FakeCreatedVolume)
			fakeDBVolume2.HandleReturns("vol-handle-456")
			fakeDBVolume2.WorkerNameReturns("k8s-worker-1")

			volume2 := k8sruntime.NewVolume(
				fakeDBVolume2,
				fakeExecutor,
				"other-pod",
				namespace,
				containerName,
				"/tmp/build/outputs",
			)

			Expect(volume.Handle()).ToNot(Equal(volume2.Handle()))
		})
	})
})

var _ = Describe("Cache Initialization Methods", func() {
	var (
		ctx          context.Context
		fakeDBVolume *dbfakes.FakeCreatedVolume
		fakeExecutor *fakeExecExecutor
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBVolume = new(dbfakes.FakeCreatedVolume)
		fakeDBVolume.HandleReturns("cache-init-handle")
		fakeExecutor = &fakeExecExecutor{}
	})

	Describe("InitializeResourceCache", func() {
		Context("when dbVolume is non-nil", func() {
			var cacheVolume *k8sruntime.Volume

			BeforeEach(func() {
				cacheVolume = k8sruntime.NewCacheVolume(
					fakeDBVolume, "cache-init-handle", "k8s-worker-1",
					fakeExecutor, "test-namespace", "main",
				)
				expectedResult := &db.UsedWorkerResourceCache{ID: 42}
				fakeDBVolume.InitializeResourceCacheReturns(expectedResult, nil)
			})

			It("delegates to dbVolume.InitializeResourceCache and returns the result", func() {
				result, err := cacheVolume.InitializeResourceCache(ctx, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
				Expect(result.ID).To(Equal(42))

				Expect(fakeDBVolume.InitializeResourceCacheCallCount()).To(Equal(1))
			})
		})

		Context("when dbVolume is nil (stub volume)", func() {
			var stubVolume *k8sruntime.Volume

			BeforeEach(func() {
				stubVolume = k8sruntime.NewStubVolume("stub-handle", "k8s-worker-1", "/tmp/mount")
			})

			It("returns nil, nil for backward compatibility", func() {
				result, err := stubVolume.InitializeResourceCache(ctx, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(BeNil())
			})
		})
	})

	Describe("InitializeStreamedResourceCache", func() {
		Context("when dbVolume is non-nil", func() {
			var cacheVolume *k8sruntime.Volume

			BeforeEach(func() {
				cacheVolume = k8sruntime.NewCacheVolume(
					fakeDBVolume, "cache-init-handle", "k8s-worker-1",
					fakeExecutor, "test-namespace", "main",
				)
				expectedResult := &db.UsedWorkerResourceCache{ID: 99}
				fakeDBVolume.InitializeStreamedResourceCacheReturns(expectedResult, nil)
			})

			It("delegates to dbVolume.InitializeStreamedResourceCache and returns the result", func() {
				result, err := cacheVolume.InitializeStreamedResourceCache(ctx, nil, 55)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
				Expect(result.ID).To(Equal(99))

				Expect(fakeDBVolume.InitializeStreamedResourceCacheCallCount()).To(Equal(1))
				_, passedSourceID := fakeDBVolume.InitializeStreamedResourceCacheArgsForCall(0)
				Expect(passedSourceID).To(Equal(55))
			})
		})

		Context("when dbVolume is nil (stub volume)", func() {
			var stubVolume *k8sruntime.Volume

			BeforeEach(func() {
				stubVolume = k8sruntime.NewStubVolume("stub-handle", "k8s-worker-1", "/tmp/mount")
			})

			It("returns nil, nil for backward compatibility", func() {
				result, err := stubVolume.InitializeStreamedResourceCache(ctx, nil, 0)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(BeNil())
			})
		})
	})

	Describe("InitializeTaskCache", func() {
		Context("when dbVolume is non-nil", func() {
			var cacheVolume *k8sruntime.Volume

			BeforeEach(func() {
				cacheVolume = k8sruntime.NewCacheVolume(
					fakeDBVolume, "cache-init-handle", "k8s-worker-1",
					fakeExecutor, "test-namespace", "main",
				)
				fakeDBVolume.InitializeTaskCacheReturns(nil)
			})

			It("delegates to dbVolume.InitializeTaskCache", func() {
				err := cacheVolume.InitializeTaskCache(ctx, 7, "build-step", "/cache/path", false)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeDBVolume.InitializeTaskCacheCallCount()).To(Equal(1))
				passedJobID, passedStepName, passedPath := fakeDBVolume.InitializeTaskCacheArgsForCall(0)
				Expect(passedJobID).To(Equal(7))
				Expect(passedStepName).To(Equal("build-step"))
				Expect(passedPath).To(Equal("/cache/path"))
			})
		})

		Context("when dbVolume is nil (stub volume)", func() {
			var stubVolume *k8sruntime.Volume

			BeforeEach(func() {
				stubVolume = k8sruntime.NewStubVolume("stub-handle", "k8s-worker-1", "/tmp/mount")
			})

			It("returns nil for backward compatibility", func() {
				err := stubVolume.InitializeTaskCache(ctx, 0, "", "", false)
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})
})

var _ = Describe("NewCacheVolume", func() {
	var (
		ctx          context.Context
		fakeExecutor *fakeExecExecutor
		fakeDBVolume *dbfakes.FakeCreatedVolume
		cacheVolume  *k8sruntime.Volume
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeExecutor = &fakeExecExecutor{}
		fakeDBVolume = new(dbfakes.FakeCreatedVolume)
		fakeDBVolume.HandleReturns("cache-vol-handle")
		fakeDBVolume.WorkerNameReturns("k8s-worker-1")

		cacheVolume = k8sruntime.NewCacheVolume(
			fakeDBVolume,
			"cache-vol-handle",
			"k8s-worker-1",
			fakeExecutor,
			"test-namespace",
			"main",
		)
	})

	It("has a mountPath at CacheBasePath/<handle>", func() {
		Expect(cacheVolume.MountPath()).To(Equal(k8sruntime.CacheBasePath + "/cache-vol-handle"))
	})

	It("returns the handle", func() {
		Expect(cacheVolume.Handle()).To(Equal("cache-vol-handle"))
	})

	It("returns the dbVolume", func() {
		Expect(cacheVolume.DBVolume()).To(BeIdenticalTo(fakeDBVolume))
	})

	It("has an executor for StreamIn/StreamOut", func() {
		Expect(cacheVolume.HasExecutor()).To(BeTrue())
	})

	It("supports StreamIn targeting the PVC subdirectory", func() {
		cacheVolume.SetPodName("test-pod")

		reader := bytes.NewReader([]byte("cache-data"))
		err := cacheVolume.StreamIn(ctx, ".", nil, 0, reader)
		Expect(err).ToNot(HaveOccurred())

		Expect(fakeExecutor.execCalls).To(HaveLen(1))
		call := fakeExecutor.execCalls[0]
		Expect(call.command).To(Equal([]string{"tar", "xf", "-", "-C", k8sruntime.CacheBasePath + "/cache-vol-handle"}))
	})

	It("supports StreamOut targeting the PVC subdirectory", func() {
		cacheVolume.SetPodName("test-pod")
		fakeExecutor.execStdout = []byte("cache-tar-data")

		readCloser, err := cacheVolume.StreamOut(ctx, ".", nil)
		Expect(err).ToNot(HaveOccurred())
		defer readCloser.Close()

		Expect(fakeExecutor.execCalls).To(HaveLen(1))
		call := fakeExecutor.execCalls[0]
		Expect(call.command).To(Equal([]string{"tar", "cf", "-", "-C", k8sruntime.CacheBasePath + "/cache-vol-handle", "."}))
	})

	Context("when dbVolume is nil", func() {
		BeforeEach(func() {
			cacheVolume = k8sruntime.NewCacheVolume(
				nil,
				"stub-cache-handle",
				"k8s-worker-1",
				fakeExecutor,
				"test-namespace",
				"main",
			)
		})

		It("uses the explicit handle", func() {
			Expect(cacheVolume.Handle()).To(Equal("stub-cache-handle"))
		})

		It("returns nil dbVolume", func() {
			Expect(cacheVolume.DBVolume()).To(BeNil())
		})
	})
})

var _ = Describe("Volume-to-Volume Streaming (same worker)", func() {
	var (
		ctx          context.Context
		fakeExecutor *fakeExecExecutor
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeExecutor = &fakeExecExecutor{}
	})

	It("streams data from source volume (pod A) to destination volume (pod B)", func() {
		sourceVol := k8sruntime.NewVolume(
			nil, fakeExecutor,
			"source-pod", "test-namespace", "main",
			"/tmp/build/workdir/output",
		)

		destVol := k8sruntime.NewVolume(
			nil, fakeExecutor,
			"dest-pod", "test-namespace", "main",
			"/tmp/build/workdir/input",
		)

		By("StreamOut from source volume produces tar data")
		fakeExecutor.execStdout = []byte("tar-payload-from-source")
		tarStream, err := sourceVol.StreamOut(ctx, ".", nil)
		Expect(err).ToNot(HaveOccurred())

		By("StreamIn to destination volume consumes tar data")
		err = destVol.StreamIn(ctx, ".", nil, 0, tarStream)
		tarStream.Close()
		Expect(err).ToNot(HaveOccurred())

		By("verifying the exec calls target different pods")
		Expect(fakeExecutor.execCalls).To(HaveLen(2))

		streamOutCall := fakeExecutor.execCalls[0]
		Expect(streamOutCall.podName).To(Equal("source-pod"))
		Expect(streamOutCall.command).To(Equal([]string{"tar", "cf", "-", "-C", "/tmp/build/workdir/output", "."}))

		streamInCall := fakeExecutor.execCalls[1]
		Expect(streamInCall.podName).To(Equal("dest-pod"))
		Expect(streamInCall.command).To(Equal([]string{"tar", "xf", "-", "-C", "/tmp/build/workdir/input"}))

		By("the tar data piped from source to destination")
		stdinData, err := io.ReadAll(streamInCall.stdin)
		Expect(err).ToNot(HaveOccurred())
		Expect(stdinData).To(Equal([]byte("tar-payload-from-source")))
	})

	It("works with deferred volumes after pod name is set", func() {
		sourceVol := k8sruntime.NewDeferredVolume(
			"src-handle", "k8s-worker",
			fakeExecutor, "test-namespace", "main",
			"/tmp/build/workdir/output",
		)
		sourceVol.SetPodName("step-1-pod")

		destVol := k8sruntime.NewDeferredVolume(
			"dst-handle", "k8s-worker",
			fakeExecutor, "test-namespace", "main",
			"/tmp/build/workdir/input",
		)
		destVol.SetPodName("step-2-pod")

		fakeExecutor.execStdout = []byte("deferred-tar-data")
		tarStream, err := sourceVol.StreamOut(ctx, ".", nil)
		Expect(err).ToNot(HaveOccurred())

		err = destVol.StreamIn(ctx, ".", nil, 0, tarStream)
		tarStream.Close()
		Expect(err).ToNot(HaveOccurred())

		Expect(fakeExecutor.execCalls).To(HaveLen(2))
		Expect(fakeExecutor.execCalls[0].podName).To(Equal("step-1-pod"))
		Expect(fakeExecutor.execCalls[1].podName).To(Equal("step-2-pod"))
	})
})

// fakeExecExecutor is a test double for k8sruntime.PodExecutor.
type fakeExecExecutor struct {
	execCalls  []execCall
	execErr    error
	execStdout []byte
}

type execCall struct {
	podName       string
	namespace     string
	containerName string
	command       []string
	stdin         io.Reader
	tty           bool
}

func (f *fakeExecExecutor) ExecInPod(
	ctx context.Context,
	namespace, podName, containerName string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	tty bool,
) error {
	f.execCalls = append(f.execCalls, execCall{
		podName:       podName,
		namespace:     namespace,
		containerName: containerName,
		command:       command,
		stdin:         stdin,
		tty:           tty,
	})
	if f.execErr != nil {
		return f.execErr
	}
	if stdout != nil && f.execStdout != nil {
		_, _ = stdout.Write(f.execStdout)
	}
	return nil
}
