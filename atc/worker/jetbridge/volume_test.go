package jetbridge_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/fake"
)

func volumeTarArchive(name string, data []byte) []byte {
	GinkgoHelper()

	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	Expect(writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))})).To(Succeed())
	_, err := writer.Write(data)
	Expect(err).NotTo(HaveOccurred())
	Expect(writer.Close()).To(Succeed())
	return archive.Bytes()
}

var _ = Describe("Volume", func() {
	var (
		ctx           context.Context
		database      jetbridgeDB
		team          db.Team
		dbWorker      db.Worker
		dbVolume      db.CreatedVolume
		podRuntime    *podRuntime
		volume        *jetbridge.Volume
		key           podKey
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
		podName = "test-pod"
		namespace = "test-namespace"
		containerName = "main"
		mountPath = "/tmp/build/inputs"
		podRuntime = newPodRuntime(fake.NewSimpleClientset())
		key = podKey{namespace, podName, containerName}
		Expect(podRuntime.AddContainer(key)).To(Succeed())

		volume = jetbridge.NewVolume(
			dbVolume,
			podRuntime,
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
		It("extracts archive files into the mounted container path", func() {
			archive := volumeTarArchive("data.txt", []byte("artifact payload"))

			Expect(volume.StreamIn(ctx, ".", nil, 0, bytes.NewReader(archive))).To(Succeed())

			data, found := podRuntime.File(key, "/tmp/build/inputs/data.txt")
			Expect(found).To(BeTrue())
			Expect(data).To(Equal([]byte("artifact payload")))
		})

		It("extracts archive files beneath a requested subdirectory", func() {
			archive := volumeTarArchive("nested.txt", []byte("nested payload"))

			Expect(volume.StreamIn(ctx, "sub/dir", nil, 0, bytes.NewReader(archive))).To(Succeed())

			data, found := podRuntime.File(key, "/tmp/build/inputs/sub/dir/nested.txt")
			Expect(found).To(BeTrue())
			Expect(data).To(Equal([]byte("nested payload")))
		})

		It("returns a stable error when the target container has terminated", func() {
			Expect(podRuntime.Terminate(key, "Completed")).To(Succeed())

			err := volume.StreamIn(ctx, ".", nil, 0, bytes.NewReader(volumeTarArchive("data", []byte("payload"))))
			Expect(err).To(MatchError(ContainSubstring("terminated")))
		})

		It("returns a stable error when the target pod is missing", func() {
			missing := jetbridge.NewVolume(
				dbVolume,
				newPodRuntime(fake.NewSimpleClientset()),
				"missing-pod",
				namespace,
				containerName,
				mountPath,
			)

			err := missing.StreamIn(ctx, ".", nil, 0, bytes.NewReader(volumeTarArchive("data", []byte("payload"))))
			Expect(err).To(MatchError(ContainSubstring("pod not found")))
		})
	})

	Describe("StreamOut", func() {
		BeforeEach(func() {
			Expect(podRuntime.PutFile(key, "/tmp/build/inputs/data.txt", []byte("artifact payload"))).To(Succeed())
			Expect(podRuntime.PutFile(key, "/tmp/build/inputs/sub/dir/nested.txt", []byte("nested payload"))).To(Succeed())
			Expect(podRuntime.PutFile(key, "/tmp/build/inputs/pipeline.yml", []byte("pipeline: value"))).To(Succeed())
		})

		It("streams a tar archive of files from the mounted container path", func() {
			readCloser, err := volume.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			destinationRuntime := newPodRuntime(fake.NewSimpleClientset())
			destinationKey := podKey{namespace, "destination-pod", containerName}
			Expect(destinationRuntime.AddContainer(destinationKey)).To(Succeed())
			destination := jetbridge.NewVolume(nil, destinationRuntime, destinationKey.Pod, namespace, containerName, "/tmp/destination")
			Expect(destination.StreamIn(ctx, ".", nil, 0, readCloser)).To(Succeed())

			data, found := destinationRuntime.File(destinationKey, "/tmp/destination/data.txt")
			Expect(found).To(BeTrue())
			Expect(data).To(Equal([]byte("artifact payload")))
		})

		It("streams only the requested subdirectory", func() {
			readCloser, err := volume.StreamOut(ctx, "sub/dir", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			destinationRuntime := newPodRuntime(fake.NewSimpleClientset())
			destinationKey := podKey{namespace, "subdirectory-destination", containerName}
			Expect(destinationRuntime.AddContainer(destinationKey)).To(Succeed())
			destination := jetbridge.NewVolume(nil, destinationRuntime, destinationKey.Pod, namespace, containerName, "/tmp/destination")
			Expect(destination.StreamIn(ctx, ".", nil, 0, readCloser)).To(Succeed())

			data, found := destinationRuntime.File(destinationKey, "/tmp/destination/sub/dir/nested.txt")
			Expect(found).To(BeTrue())
			Expect(data).To(Equal([]byte("nested payload")))
			_, found = destinationRuntime.File(destinationKey, "/tmp/destination/data.txt")
			Expect(found).To(BeFalse())
		})

		It("streams a requested file", func() {
			readCloser, err := volume.StreamOut(ctx, "pipeline.yml", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			destinationRuntime := newPodRuntime(fake.NewSimpleClientset())
			destinationKey := podKey{namespace, "file-destination", containerName}
			Expect(destinationRuntime.AddContainer(destinationKey)).To(Succeed())
			destination := jetbridge.NewVolume(nil, destinationRuntime, destinationKey.Pod, namespace, containerName, "/tmp/destination")
			Expect(destination.StreamIn(ctx, ".", nil, 0, readCloser)).To(Succeed())

			data, found := destinationRuntime.File(destinationKey, "/tmp/destination/pipeline.yml")
			Expect(found).To(BeTrue())
			Expect(data).To(Equal([]byte("pipeline: value")))
		})

		It("propagates a terminated-container error through the pipe reader", func() {
			Expect(podRuntime.Terminate(key, "Completed")).To(Succeed())
			readCloser, err := volume.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			_, err = io.ReadAll(readCloser)
			Expect(err).To(MatchError(ContainSubstring("terminated")))
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

		It("Handle returns the stub handle", func() {
			Expect(stubVolume.Handle()).To(Equal("rc-42"))
		})
	})

	Describe("volume uniqueness", func() {
		It("two volumes with different handles are distinguishable", func() {
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
				podRuntime,
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
})

var _ = Describe("Volume-to-Volume Streaming (same worker)", func() {
	var (
		ctx        context.Context
		podRuntime *podRuntime
	)

	BeforeEach(func() {
		ctx = context.Background()
		podRuntime = newPodRuntime(fake.NewSimpleClientset())
	})

	It("streams data from source volume (pod A) to destination volume (pod B)", func() {
		sourceKey := podKey{"test-namespace", "source-pod", "main"}
		destinationKey := podKey{"test-namespace", "dest-pod", "main"}
		Expect(podRuntime.AddContainer(sourceKey)).To(Succeed())
		Expect(podRuntime.AddContainer(destinationKey)).To(Succeed())
		Expect(podRuntime.PutFile(
			sourceKey,
			"/tmp/build/workdir/output/artifact.txt",
			[]byte("tar-payload-from-source"),
		)).To(Succeed())

		sourceVol := jetbridge.NewVolume(
			nil, podRuntime,
			"source-pod", "test-namespace", "main",
			"/tmp/build/workdir/output",
		)

		destVol := jetbridge.NewVolume(
			nil, podRuntime,
			"dest-pod", "test-namespace", "main",
			"/tmp/build/workdir/input",
		)

		By("StreamOut from source volume produces tar data")
		tarStream, err := sourceVol.StreamOut(ctx, ".", nil)
		Expect(err).ToNot(HaveOccurred())

		By("StreamIn to destination volume consumes tar data")
		err = destVol.StreamIn(ctx, ".", nil, 0, tarStream)
		tarStream.Close()
		Expect(err).ToNot(HaveOccurred())

		By("verifying the source file reached the destination pod")
		data, found := podRuntime.File(destinationKey, "/tmp/build/workdir/input/artifact.txt")
		Expect(found).To(BeTrue())
		Expect(data).To(Equal([]byte("tar-payload-from-source")))
	})

	It("works with deferred volumes after pod name is set", func() {
		sourceKey := podKey{"test-namespace", "step-1-pod", "main"}
		destinationKey := podKey{"test-namespace", "step-2-pod", "main"}
		Expect(podRuntime.AddContainer(sourceKey)).To(Succeed())
		Expect(podRuntime.AddContainer(destinationKey)).To(Succeed())
		Expect(podRuntime.PutFile(
			sourceKey,
			"/tmp/build/workdir/output/deferred.txt",
			[]byte("deferred-tar-data"),
		)).To(Succeed())

		sourceVol := jetbridge.NewDeferredVolume(
			"src-handle", "k8s-worker",
			podRuntime, "test-namespace", "main",
			"/tmp/build/workdir/output",
		)
		sourceVol.SetPodName("step-1-pod")

		destVol := jetbridge.NewDeferredVolume(
			"dst-handle", "k8s-worker",
			podRuntime, "test-namespace", "main",
			"/tmp/build/workdir/input",
		)
		destVol.SetPodName("step-2-pod")

		tarStream, err := sourceVol.StreamOut(ctx, ".", nil)
		Expect(err).ToNot(HaveOccurred())

		err = destVol.StreamIn(ctx, ".", nil, 0, tarStream)
		tarStream.Close()
		Expect(err).ToNot(HaveOccurred())

		data, found := podRuntime.File(destinationKey, "/tmp/build/workdir/input/deferred.txt")
		Expect(found).To(BeTrue())
		Expect(data).To(Equal([]byte("deferred-tar-data")))
	})
})
