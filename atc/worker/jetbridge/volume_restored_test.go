package jetbridge_test

// RESTORED 2026-09-05 (rebase onto core for 0.3.2), from the deleted
// atc/worker/jetbridge/volume_test.go (merge-base aef2244a63, via the
// compile-adapted copy the port stage produced for this branch).
//
// Eighteen of that file's twenty-two Its -- rows JB-volume-000, -002, -003,
// -004, -005, -006, -007, -009, -010, -011, -013, -014, -015, -016, -017, -019,
// -020 and -021 of DISPOSITION-jetbridge.md -- were recorded DELETED on
// FILE-level evidence only, and the rebase re-verification did not sustain them
// (eleven REFUTED, seven GAP). Restoring is always acceptable; deleting on
// inference is not.
// The six root-path StreamIn/StreamOut cases now share two contract tests;
// their exact routing, metadata and byte assertions remain (see CONSOLIDATION.md).
//
// The four whose evidence held -- "Source returns the worker name from the db
// volume", StreamIn's "returns the error", StreamOut's "uses a subdirectory
// path when path is not root" and StubVolume's "Handle returns the stub handle"
// -- stay deleted.

import (
	"bytes"
	"context"
	"errors"
	"io"
	// PORT-ADAPT: "sync" import dropped along with the fakeExecExecutor block that
	// used to end this file (its mu sync.Mutex field was the only user); keeping the
	// import here is an `imported and not used` compile error.

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Volume", func() {
	var (
		ctx           context.Context
		database      jetbridgeDB
		team          db.Team
		dbWorker      db.Worker
		dbVolume      db.CreatedVolume
		fakeExecutor  *fakeExecExecutor
		volume        *jetbridge.Volume
		podName       string
		namespace     string
		containerName string
		mountPath     string
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		var err error
		team, err = database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
		Expect(err).ToNot(HaveOccurred())
		dbWorker, err = persistNamedWorker(database, "k8s-worker-1")
		Expect(err).ToNot(HaveOccurred())

		creatingVolume, err := database.VolumeRepository.CreateVolumeWithHandle(
			"vol-handle-123",
			team.ID(),
			dbWorker.Name(),
			db.VolumeTypeArtifact,
		)
		Expect(err).ToNot(HaveOccurred())
		createdVolume, err := creatingVolume.Created()
		Expect(err).ToNot(HaveOccurred())
		artifact, err := createdVolume.InitializeArtifact("volume-test-artifact", 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(artifact.ID()).To(BeNumerically(">", 0))

		var found bool
		dbVolume, found, err = database.VolumeRepository.FindVolume(createdVolume.Handle())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		artifactVolume, found, err := artifact.Volume(team.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(artifactVolume.Handle()).To(Equal(dbVolume.Handle()))
		fakeExecutor = &fakeExecExecutor{}

		podName = "test-pod"
		namespace = "test-namespace"
		containerName = "main"
		mountPath = "/tmp/build/inputs"

		volume = jetbridge.NewVolume(
			dbVolume,
			fakeExecutor,
			podName,
			namespace,
			containerName,
			mountPath,
		)
	})

	It("Handle returns the db volume handle", func() {

		Expect(volume.Handle()).To(Equal("vol-handle-123"))

	})

	Describe("DBVolume", func() {
		It("returns the underlying db volume", func() {
			Expect(volume.DBVolume()).To(BeIdenticalTo(dbVolume))
		})

		It("returns the persisted DB volume from a DaemonSetVolume", func() {
			daemonSetVolume := jetbridge.NewDaemonSetVolume(
				"key",
				"runtime-handle",
				dbWorker.Name(),
				dbVolume,
				"",
				jetbridge.Config{},
				nil,
			)

			Expect(daemonSetVolume.DBVolume()).To(BeIdenticalTo(dbVolume))
			Expect(daemonSetVolume.DBVolume().Handle()).To(Equal("vol-handle-123"))
			Expect(daemonSetVolume.DBVolume().WorkerName()).To(Equal(dbWorker.Name()))
			Expect(daemonSetVolume.DBVolume().TeamID()).To(Equal(team.ID()))
			Expect(daemonSetVolume.DBVolume().Type()).To(Equal(db.VolumeTypeArtifact))
		})
	})

	Describe("StreamIn", func() {
		It("streams exact input bytes to the intended tar destination with stream-in metadata", func() {
			inputData := []byte("some-tar-stream-data")
			err := volume.StreamIn(ctx, ".", nil, 0, bytes.NewReader(inputData))
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			call := fakeExecutor.execCalls[0]
			Expect(call.podName).To(Equal("test-pod"))
			Expect(call.namespace).To(Equal("test-namespace"))
			Expect(call.containerName).To(Equal("main"))
			Expect(call.command).To(Equal([]string{"tar", "xf", "-", "-C", "/tmp/build/inputs"}))
			Expect(call.attrs.Purpose).To(Equal("stream-in"))
			Expect(call.attrs.VolumeMountPath).To(Equal("/tmp/build/inputs"))
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

	})

	Describe("StreamOut", func() {
		BeforeEach(func() {
			fakeExecutor.execStdout = []byte("tar-output-bytes")
		})

		It("streams exact output bytes from the intended tar destination with stream-out metadata", func() {
			readCloser, err := volume.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			data, err := io.ReadAll(readCloser)
			Expect(err).ToNot(HaveOccurred())
			Expect(data).To(Equal([]byte("tar-output-bytes")))
			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			call := fakeExecutor.execCalls[0]
			Expect(call.podName).To(Equal("test-pod"))
			Expect(call.namespace).To(Equal("test-namespace"))
			Expect(call.containerName).To(Equal("main"))
			Expect(call.command).To(Equal([]string{"tar", "cf", "-", "-C", "/tmp/build/inputs", "."}))
			Expect(call.attrs.Purpose).To(Equal("stream-out"))
			Expect(call.attrs.VolumeMountPath).To(Equal("/tmp/build/inputs"))
		})

		It("handles a file path by tarring from the mount root", func() {
			readCloser, err := volume.StreamOut(ctx, "pipeline.yml", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			_, _ = io.ReadAll(readCloser)

			call := fakeExecutor.execCalls[0]
			Expect(call.command).To(Equal([]string{"tar", "cf", "-", "-C", "/tmp/build/inputs", "pipeline.yml"}))
		})

		It("when the exec returns an error propagates the error through the pipe reader", func() {

			fakeExecutor.execErr = errors.New("exec failed: pod terminated")

			readCloser, err := volume.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			_, err = io.ReadAll(readCloser)
			Expect(err).To(MatchError(ContainSubstring("exec failed")))

		})
	})

	Describe("StubVolume (nil executor)", func() {
		var stubVolume *jetbridge.Volume

		BeforeEach(func() {
			stubVolume = jetbridge.NewStubVolume("rc-42", "k8s-worker", "")
		})

		It("StreamOut returns an error instead of panicking", func() {
			_, err := stubVolume.StreamOut(ctx, ".", nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot stream out"))
			Expect(err.Error()).To(ContainSubstring("no executor"))
		})

		It("StreamIn returns an error instead of panicking", func() {
			reader := bytes.NewReader([]byte("data"))
			err := stubVolume.StreamIn(ctx, ".", nil, 0, reader)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot stream in"))
			Expect(err.Error()).To(ContainSubstring("no executor"))
		})

		It("HasExecutor returns false", func() {
			Expect(stubVolume.HasExecutor()).To(BeFalse())
		})
	})

	It("volume uniqueness two volumes with different handles are distinguishable", func() {

		creatingVolume2, err := database.VolumeRepository.CreateVolumeWithHandle(
			"vol-handle-456",
			team.ID(),
			dbWorker.Name(),
			db.VolumeTypeArtifact,
		)
		Expect(err).ToNot(HaveOccurred())
		createdVolume2, err := creatingVolume2.Created()
		Expect(err).ToNot(HaveOccurred())
		artifact2, err := createdVolume2.InitializeArtifact("volume-test-artifact-2", 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(artifact2.ID()).To(BeNumerically(">", 0))
		dbVolume2, found, err := database.VolumeRepository.FindVolume(createdVolume2.Handle())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		volume2 := jetbridge.NewVolume(
			dbVolume2,
			fakeExecutor,
			"other-pod",
			namespace,
			containerName,
			"/tmp/build/outputs",
		)

		Expect(volume.Handle()).ToNot(Equal(volume2.Handle()))
		Expect(dbVolume2.Handle()).To(Equal("vol-handle-456"))
		artifactVolume2, found, err := artifact2.Volume(team.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(artifactVolume2.Handle()).To(Equal(volume2.Handle()))

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
		sourceVol := jetbridge.NewVolume(
			nil, fakeExecutor,
			"source-pod", "test-namespace", "main",
			"/tmp/build/workdir/output",
		)

		destVol := jetbridge.NewVolume(
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
		sourceVol := jetbridge.NewDeferredVolume(
			"src-handle", "k8s-worker",
			fakeExecutor, "test-namespace", "main",
			"/tmp/build/workdir/output",
		)
		sourceVol.SetPodName("step-1-pod")

		destVol := jetbridge.NewDeferredVolume(
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

// PORT-ADAPT: the trailing test-double block that lived here in the merge-base
// version (`type fakeExecExecutor`, `type execCall`, `func (f *fakeExecExecutor)
// ExecInPod`) was deleted from this restored copy. It was rescued verbatim into
// jetbridge_suite_test.go when this file was removed, so restoring it here would
// produce `fakeExecExecutor redeclared in this block` / `execCall redeclared`.
// The suite-file copy is byte-identical, so no assertion above changed.
