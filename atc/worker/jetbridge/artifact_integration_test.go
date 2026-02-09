package jetbridge_test

import (
	"bytes"
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Integration tests for artifact passing through the JetBridge runtime.
// These exercise the full CreateVolumeForArtifact → LookupVolume → step
// passing workflow using the fake K8s clientset and DB fakes.
var _ = Describe("Artifact Integration", func() {
	var (
		fakeDBWorker   *dbfakes.FakeWorker
		fakeClientset  *fake.Clientset
		fakeExecutor   *fakeExecExecutor
		fakeVolumeRepo *dbfakes.FakeVolumeRepository
		worker         *jetbridge.Worker
		ctx            context.Context
		cfg            jetbridge.Config
		delegate       runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		fakeExecutor = &fakeExecExecutor{}
		fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
		delegate = &noopDelegate{}

		cfg = jetbridge.NewConfig("ci-namespace", "")
		cfg.ArtifactStoreClaim = "artifact-store-pvc"

		worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		worker.SetExecutor(fakeExecutor)
		worker.SetVolumeRepo(fakeVolumeRepo)
	})

	// setupArtifactVolumeFakes configures the volume repo fakes to simulate
	// a successful CreateVolumeForArtifact call and returns the fakes for
	// further configuration.
	setupArtifactVolumeFakes := func(handle string, artifactID int) (*dbfakes.FakeCreatingVolume, *dbfakes.FakeCreatedVolume, *dbfakes.FakeWorkerArtifact) {
		fakeCreatingVolume := new(dbfakes.FakeCreatingVolume)
		fakeCreatingVolume.HandleReturns(handle)

		fakeCreatedVolume := new(dbfakes.FakeCreatedVolume)
		fakeCreatedVolume.HandleReturns(handle)
		fakeCreatedVolume.WorkerNameReturns("k8s-worker-1")

		fakeArtifact := new(dbfakes.FakeWorkerArtifact)
		fakeArtifact.IDReturns(artifactID)

		fakeVolumeRepo.CreateVolumeReturns(fakeCreatingVolume, nil)
		fakeCreatingVolume.CreatedReturns(fakeCreatedVolume, nil)
		fakeCreatedVolume.InitializeArtifactReturns(fakeArtifact, nil)

		return fakeCreatingVolume, fakeCreatedVolume, fakeArtifact
	}

	simulatePodRunning := func(podName string) {
		pod, err := fakeClientset.CoreV1().Pods("ci-namespace").Get(ctx, podName, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodRunning
		_, err = fakeClientset.CoreV1().Pods("ci-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	Describe("multi-step pipeline with artifact passing", func() {
		It("creates an artifact in step 1 and passes it as input to step 2", func() {
			By("step 1: creating an artifact volume (simulating fly execute upload)")
			_, fakeCreatedVolume, _ := setupArtifactVolumeFakes("artifact-vol-1", 10)

			vol, artifact, err := worker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(vol).ToNot(BeNil())
			Expect(artifact).ToNot(BeNil())
			Expect(artifact.ID()).To(Equal(10))

			By("verifying the volume is an ArtifactStoreVolume with correct key")
			asVol, ok := vol.(*jetbridge.ArtifactStoreVolume)
			Expect(ok).To(BeTrue(), "expected ArtifactStoreVolume, got %T", vol)
			Expect(asVol.Key()).To(Equal("artifacts/artifact-vol-1.tar"))
			Expect(asVol.Handle()).To(Equal("artifact-vol-1"))

			By("step 2: looking up the artifact volume for the next step")
			fakeVolumeRepo.FindVolumeReturns(fakeCreatedVolume, true, nil)

			lookedUpVol, found, err := worker.LookupVolume(ctx, "artifact-vol-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			lookedUpASV, ok := lookedUpVol.(*jetbridge.ArtifactStoreVolume)
			Expect(ok).To(BeTrue(), "LookupVolume should return ArtifactStoreVolume when artifact store is configured")
			Expect(lookedUpASV.Key()).To(Equal("artifacts/artifact-vol-1.tar"))

			By("step 3: creating a task container that receives the artifact as input")
			setupFakeDBContainer(fakeDBWorker, "task-consume-artifact")

			fakeExecutor.execStdout = []byte("artifact data received\n")
			container, mounts, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("task-consume-artifact"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					TeamName: "main",
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{
						ImageURL: "docker:///ubuntu:22.04",
					},
					Inputs: []runtime.Input{
						{
							Artifact:        lookedUpVol,
							DestinationPath: "/tmp/build/workdir/my-input",
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(container).ToNot(BeNil())

			By("verifying the input mount is present")
			var inputMountFound bool
			for _, m := range mounts {
				if m.MountPath == "/tmp/build/workdir/my-input" {
					inputMountFound = true
				}
			}
			Expect(inputMountFound).To(BeTrue(), "task should have an input mount for the artifact")

			By("running the task that consumes the artifact")
			stdout := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "cat /tmp/build/workdir/my-input/data.txt"},
				Dir:  "/tmp/build/workdir",
			}, runtime.ProcessIO{
				Stdout: stdout,
			})
			Expect(err).ToNot(HaveOccurred())

			simulatePodRunning("task-consume-artifact")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("verifying the main command was exec'd (plus artifact-helper tar calls)")
			Expect(fakeExecutor.execCalls).ToNot(BeEmpty())
			// The first exec call is the main task command
			Expect(fakeExecutor.execCalls[0].command).To(Equal([]string{
				"/bin/sh", "-c", "cat /tmp/build/workdir/my-input/data.txt",
			}))
			Expect(fakeExecutor.execCalls[0].containerName).To(Equal("main"))

			// Remaining calls are artifact-helper sidecar tar commands
			for _, call := range fakeExecutor.execCalls[1:] {
				Expect(call.containerName).To(Equal("artifact-helper"))
			}
		})

		It("passes artifacts through get → task → put pipeline steps", func() {
			By("step 1: get step produces an artifact (resource version)")
			_, getCreatedVolume, _ := setupArtifactVolumeFakes("get-output-vol", 20)

			getVol, getArtifact, err := worker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(getArtifact.ID()).To(Equal(20))

			By("step 2: task step receives get output as input and produces its own output")
			fakeVolumeRepo.FindVolumeReturns(getCreatedVolume, true, nil)

			lookedUpGetVol, found, err := worker.LookupVolume(ctx, "get-output-vol")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			setupFakeDBContainer(fakeDBWorker, "task-build-step")
			container, mounts, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("task-build-step"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					TeamName: "main",
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{
						ImageURL: "docker:///golang:1.25",
					},
					Inputs: []runtime.Input{
						{
							Artifact:        lookedUpGetVol,
							DestinationPath: "/tmp/build/workdir/repo",
						},
					},
					Outputs: runtime.OutputPaths{
						"binary": "/tmp/build/workdir/binary",
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			By("verifying both input and output mounts exist")
			mountPaths := make([]string, len(mounts))
			for i, m := range mounts {
				mountPaths[i] = m.MountPath
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/workdir",
				"/tmp/build/workdir/repo",
				"/tmp/build/workdir/binary",
			))

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "go build -o /tmp/build/workdir/binary/app ./..."},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			simulatePodRunning("task-build-step")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("step 3: put step receives task output as input")
			fakeExecutor.execCalls = nil
			putStdout := `{"version":{"ref":"v1.0.0"}}`
			fakeExecutor.execStdout = []byte(putStdout)

			setupFakeDBContainer(fakeDBWorker, "put-upload-step")
			putContainer, putMounts, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("put-upload-step"),
				db.ContainerMetadata{Type: db.ContainerTypePut},
				runtime.ContainerSpec{
					TeamID:   1,
					TeamName: "main",
					ImageSpec: runtime.ImageSpec{
						ResourceType: "s3",
					},
					Type: db.ContainerTypePut,
					Inputs: []runtime.Input{
						{DestinationPath: "/tmp/build/put/binary"},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			By("verifying the put step has the input mount")
			putMountPaths := make([]string, len(putMounts))
			for i, m := range putMounts {
				putMountPaths[i] = m.MountPath
			}
			Expect(putMountPaths).To(ContainElement("/tmp/build/put/binary"))

			putStdoutBuf := new(bytes.Buffer)
			putProcess, err := putContainer.Run(ctx, runtime.ProcessSpec{
				ID:   "resource",
				Path: "/opt/resource/out",
				Args: []string{"/tmp/build/put"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{"source":{"bucket":"releases"}}`),
				Stdout: putStdoutBuf,
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			simulatePodRunning("put-upload-step")
			putResult, err := putProcess.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(putResult.ExitStatus).To(Equal(0))
			Expect(putStdoutBuf.String()).To(Equal(putStdout))

			By("verifying the complete get→task→put chain used artifact store volumes")
			_, isASV := getVol.(*jetbridge.ArtifactStoreVolume)
			Expect(isASV).To(BeTrue(), "get output should be ArtifactStoreVolume")
			_, isASV = lookedUpGetVol.(*jetbridge.ArtifactStoreVolume)
			Expect(isASV).To(BeTrue(), "looked up get output should be ArtifactStoreVolume")
		})
	})

	Describe("artifact persistence across pod restarts", func() {
		It("returns the same ArtifactStoreVolume key across multiple lookups", func() {
			By("creating an artifact volume")
			_, fakeCreatedVolume, _ := setupArtifactVolumeFakes("persistent-artifact", 30)

			vol, _, err := worker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).ToNot(HaveOccurred())
			originalKey := vol.(*jetbridge.ArtifactStoreVolume).Key()
			Expect(originalKey).To(Equal("artifacts/persistent-artifact.tar"))

			By("looking up the volume (simulating a new step after pod restart)")
			fakeVolumeRepo.FindVolumeReturns(fakeCreatedVolume, true, nil)

			vol2, found, err := worker.LookupVolume(ctx, "persistent-artifact")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			restartedKey := vol2.(*jetbridge.ArtifactStoreVolume).Key()
			Expect(restartedKey).To(Equal(originalKey),
				"artifact key should be deterministic and survive pod restarts")

			By("looking up the volume again (simulating another step)")
			vol3, found, err := worker.LookupVolume(ctx, "persistent-artifact")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(vol3.(*jetbridge.ArtifactStoreVolume).Key()).To(Equal(originalKey),
				"artifact key should remain stable across all lookups")
		})

		It("preserves DB volume association through lookup", func() {
			By("creating an artifact volume with DB state")
			_, fakeCreatedVolume, _ := setupArtifactVolumeFakes("db-tracked-artifact", 40)

			fakeUsedCache := &db.UsedWorkerResourceCache{ID: 77}
			fakeCreatedVolume.InitializeResourceCacheReturns(fakeUsedCache, nil)

			_, _, err := worker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).ToNot(HaveOccurred())

			By("looking up the volume and verifying DB operations still work")
			fakeVolumeRepo.FindVolumeReturns(fakeCreatedVolume, true, nil)

			vol, found, err := worker.LookupVolume(ctx, "db-tracked-artifact")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			By("initializing a resource cache on the looked-up volume")
			usedCache, err := vol.InitializeResourceCache(ctx, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(usedCache).ToNot(BeNil())
			Expect(usedCache.ID).To(Equal(77))
		})
	})

	Describe("artifact cleanup", func() {
		It("artifact volumes are created as VolumeTypeArtifact for Reaper identification", func() {
			By("creating multiple artifact volumes")
			setupArtifactVolumeFakes("artifact-cleanup-1", 50)
			_, _, err := worker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).ToNot(HaveOccurred())

			By("verifying the volume was created with VolumeTypeArtifact")
			Expect(fakeVolumeRepo.CreateVolumeCallCount()).To(Equal(1))
			teamID, workerName, volType := fakeVolumeRepo.CreateVolumeArgsForCall(0)
			Expect(teamID).To(Equal(1))
			Expect(workerName).To(Equal("k8s-worker-1"))
			Expect(volType).To(Equal(db.VolumeTypeArtifact),
				"artifact volumes must be VolumeTypeArtifact so the Reaper can identify and clean orphans")
		})

		It("orphaned artifacts return not-found when DB record is removed", func() {
			By("creating an artifact volume")
			setupArtifactVolumeFakes("orphan-artifact", 60)
			_, _, err := worker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).ToNot(HaveOccurred())

			By("simulating the Reaper removing the DB record (orphan cleanup)")
			fakeVolumeRepo.FindVolumeReturns(nil, false, nil)

			_, found, err := worker.LookupVolume(ctx, "orphan-artifact")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse(),
				"after Reaper removes the DB record, LookupVolume should return not-found")
		})

		It("artifact volumes from different teams are isolated", func() {
			By("creating artifact for team 1")
			setupArtifactVolumeFakes("team1-artifact", 70)
			_, artifact1, err := worker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(artifact1.ID()).To(Equal(70))

			By("verifying team ID was passed correctly")
			teamID, _, _ := fakeVolumeRepo.CreateVolumeArgsForCall(0)
			Expect(teamID).To(Equal(1))

			By("creating artifact for team 2")
			setupArtifactVolumeFakes("team2-artifact", 71)
			_, artifact2, err := worker.CreateVolumeForArtifact(ctx, 2)
			Expect(err).ToNot(HaveOccurred())
			Expect(artifact2.ID()).To(Equal(71))

			By("verifying team 2's ID was passed correctly")
			teamID, _, _ = fakeVolumeRepo.CreateVolumeArgsForCall(1)
			Expect(teamID).To(Equal(2))
		})
	})

	Describe("artifact store disabled (fallback to SPDY streaming)", func() {
		var noArtifactWorker *jetbridge.Worker

		BeforeEach(func() {
			noCfg := jetbridge.NewConfig("ci-namespace", "")
			// No ArtifactStoreClaim set
			noArtifactWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, noCfg)
			noArtifactWorker.SetExecutor(fakeExecutor)
			noArtifactWorker.SetVolumeRepo(fakeVolumeRepo)
		})

		It("returns a DeferredVolume instead of ArtifactStoreVolume", func() {
			setupArtifactVolumeFakes("deferred-artifact", 80)
			vol, _, err := noArtifactWorker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).ToNot(HaveOccurred())

			_, isASV := vol.(*jetbridge.ArtifactStoreVolume)
			Expect(isASV).To(BeFalse(),
				"without artifact store configured, should return DeferredVolume")
			Expect(vol.Handle()).To(Equal("deferred-artifact"))
		})
	})
})
