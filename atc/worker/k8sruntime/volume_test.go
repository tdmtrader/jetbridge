package k8sruntime_test

import (
	"bytes"
	"context"
	"errors"
	"io"

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
