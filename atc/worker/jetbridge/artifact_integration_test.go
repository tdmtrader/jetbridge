package jetbridge_test

import (
	"bytes"
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
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
// passing workflow using the fake K8s clientset and real PostgreSQL state.
var _ = Describe("Artifact Integration", func() {
	var (
		database      jetbridgeDB
		team          db.Team
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		fakeExecutor  *fakeExecExecutor
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		var err error
		team, err = database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
		Expect(err).ToNot(HaveOccurred())
		dbWorker, err = persistNamedWorker(database, "k8s-worker-1")
		Expect(err).ToNot(HaveOccurred())
		fakeClientset = fake.NewSimpleClientset()
		fakeExecutor = &fakeExecExecutor{}
		delegate = &noopDelegate{}

		cfg = jetbridge.NewConfig("ci-namespace", "")

		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		worker.SetExecutor(fakeExecutor)
		worker.SetVolumeRepo(database.VolumeRepository)
	})

	createArtifactVolume := func(teamID int) (*jetbridge.DaemonSetVolume, db.WorkerArtifact) {
		vol, artifact, err := worker.CreateVolumeForArtifact(ctx, teamID)
		Expect(err).ToNot(HaveOccurred())
		Expect(artifact.ID()).To(BeNumerically(">", 0))
		Expect(artifact.Name()).To(BeEmpty())
		Expect(artifact.BuildID()).To(BeZero())

		daemonSetVolume, ok := vol.(*jetbridge.DaemonSetVolume)
		Expect(ok).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)
		return daemonSetVolume, artifact
	}

	expectContainerPersisted := func(handle string) {
		var state, workerName string
		err := database.Conn.QueryRow(
			"SELECT state::text, worker_name FROM containers WHERE handle = $1",
			handle,
		).Scan(&state, &workerName)
		Expect(err).ToNot(HaveOccurred())
		Expect(state).To(Equal("created"))
		Expect(workerName).To(Equal(dbWorker.Name()))
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
			vol, artifact := createArtifactVolume(team.ID())

			By("verifying the artifact volume was persisted")
			persistedVolume, found, err := database.VolumeRepository.FindVolume(vol.Handle())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedVolume.Handle()).To(Equal(vol.Handle()))
			Expect(persistedVolume.WorkerName()).To(Equal(dbWorker.Name()))
			Expect(persistedVolume.TeamID()).To(Equal(team.ID()))
			Expect(persistedVolume.Type()).To(Equal(db.VolumeTypeArtifact))

			artifactVolume, found, err := artifact.Volume(team.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(artifactVolume.Handle()).To(Equal(vol.Handle()))

			By("verifying the volume is a DaemonSetVolume with a stable key")
			Expect(vol.Key()).To(Equal(vol.Handle()))

			By("step 2: looking up the artifact volume for the next step")
			lookedUpVol, found, err := worker.LookupVolume(ctx, vol.Handle())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			lookedUpASV, ok := lookedUpVol.(*jetbridge.DaemonSetVolume)
			Expect(ok).To(BeTrue(), "LookupVolume should return DaemonSetVolume when artifact store is configured")
			Expect(lookedUpASV.Key()).To(Equal(vol.Handle()))
			Expect(lookedUpASV.DBVolume().Handle()).To(Equal(persistedVolume.Handle()))

			By("step 3: creating a task container that receives the artifact as input")
			fakeExecutor.execStdout = []byte("artifact data received\n")
			container, mounts, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("task-consume-artifact"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   team.ID(),
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
			expectContainerPersisted("task-consume-artifact")

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
			expectSupervisedExec(fakeExecutor.execCalls[0].command, `'/bin/sh' '-c' 'cat /tmp/build/workdir/my-input/data.txt'`)
			Expect(fakeExecutor.execCalls[0].containerName).To(Equal("main"))

			// Remaining calls are artifact-helper sidecar tar commands
			for _, call := range fakeExecutor.execCalls[1:] {
				Expect(call.containerName).To(Equal("artifact-helper"))
			}
		})

		It("passes artifacts through get → task → put pipeline steps", func() {
			By("step 1: get step produces an artifact (resource version)")
			getVol, getArtifact := createArtifactVolume(team.ID())
			Expect(getArtifact.ID()).To(BeNumerically(">", 0))

			By("step 2: task step receives get output as input and produces its own output")
			lookedUpGetVol, found, err := worker.LookupVolume(ctx, getVol.Handle())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			container, mounts, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("task-build-step"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   team.ID(),
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
			expectContainerPersisted("task-build-step")

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
			var taskOutput runtime.Artifact
			for _, mount := range mounts {
				if mount.MountPath == "/tmp/build/workdir/binary" {
					taskOutput = mount.Volume
				}
			}
			Expect(taskOutput).ToNot(BeNil())

			putContainer, putMounts, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("put-upload-step"),
				db.ContainerMetadata{Type: db.ContainerTypePut},
				runtime.ContainerSpec{
					TeamID:   team.ID(),
					TeamName: "main",
					ImageSpec: runtime.ImageSpec{
						ResourceType: "s3",
					},
					Type: db.ContainerTypePut,
					Inputs: []runtime.Input{
						{Artifact: taskOutput, DestinationPath: "/tmp/build/put/binary"},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
			expectContainerPersisted("put-upload-step")

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
			Expect(getVol).ToNot(BeNil(), "get output should be DaemonSetVolume")
			_, isASV := lookedUpGetVol.(*jetbridge.DaemonSetVolume)
			Expect(isASV).To(BeTrue(), "looked up get output should be DaemonSetVolume")
		})
	})

	Describe("artifact persistence across pod restarts", func() {
		It("returns the same DaemonSetVolume key across multiple lookups", func() {
			By("creating an artifact volume")
			vol, _ := createArtifactVolume(team.ID())
			originalKey := vol.Key()
			Expect(originalKey).To(Equal(vol.Handle()))

			By("looking up the volume (simulating a new step after pod restart)")
			vol2, found, err := worker.LookupVolume(ctx, vol.Handle())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			restartedKey := vol2.(*jetbridge.DaemonSetVolume).Key()
			Expect(restartedKey).To(Equal(originalKey),
				"artifact key should be deterministic and survive pod restarts")

			By("looking up the volume again (simulating another step)")
			vol3, found, err := worker.LookupVolume(ctx, vol.Handle())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(vol3.(*jetbridge.DaemonSetVolume).Key()).To(Equal(originalKey),
				"artifact key should remain stable across all lookups")
		})

		It("preserves DB volume association through lookup", func() {
			By("creating an artifact volume with DB state")
			createdVolume, artifact := createArtifactVolume(team.ID())

			By("looking up the volume and verifying DB operations still work")
			vol, found, err := worker.LookupVolume(ctx, createdVolume.Handle())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			lookedUpVolume, ok := vol.(*jetbridge.DaemonSetVolume)
			Expect(ok).To(BeTrue())
			Expect(lookedUpVolume.DBVolume()).ToNot(BeNil())
			Expect(lookedUpVolume.DBVolume().Handle()).To(Equal(createdVolume.Handle()))
			Expect(lookedUpVolume.DBVolume().WorkerName()).To(Equal(dbWorker.Name()))

			By("reloading the exact volume through its artifact association")
			artifactVolume, found, err := artifact.Volume(team.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(artifactVolume.Handle()).To(Equal(createdVolume.Handle()))
			Expect(artifactVolume.WorkerArtifactID()).To(Equal(artifact.ID()))
		})
	})

	Describe("artifact cleanup", func() {
		It("artifact volumes are created as VolumeTypeArtifact for Reaper identification", func() {
			By("creating an artifact volume")
			vol, _ := createArtifactVolume(team.ID())

			By("verifying the volume was created with VolumeTypeArtifact")
			persistedVolume, found, err := database.VolumeRepository.FindVolume(vol.Handle())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedVolume.TeamID()).To(Equal(team.ID()))
			Expect(persistedVolume.WorkerName()).To(Equal(dbWorker.Name()))
			Expect(persistedVolume.Type()).To(Equal(db.VolumeTypeArtifact),
				"artifact volumes must be VolumeTypeArtifact so the Reaper can identify and clean orphans")
		})

		It("orphaned artifacts return not-found when DB record is removed", func() {
			By("creating an artifact volume")
			vol, _ := createArtifactVolume(team.ID())

			By("removing the persisted DB record as the Reaper would")
			destroyingVolume, err := vol.DBVolume().Destroying()
			Expect(err).ToNot(HaveOccurred())
			destroyed, err := destroyingVolume.Destroy()
			Expect(err).ToNot(HaveOccurred())
			Expect(destroyed).To(BeTrue())

			_, found, err := worker.LookupVolume(ctx, vol.Handle())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse(),
				"after Reaper removes the DB record, LookupVolume should return not-found")
		})

		It("artifact volumes from different teams are isolated", func() {
			team2, err := database.TeamFactory.CreateTeam(atc.Team{Name: "artifact-team-2"})
			Expect(err).ToNot(HaveOccurred())

			By("creating artifact for team 1")
			vol1, artifact1 := createArtifactVolume(team.ID())

			team1Volume, found, err := artifact1.Volume(team.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(team1Volume.Handle()).To(Equal(vol1.Handle()))
			_, found, err = artifact1.Volume(team2.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())

			By("creating artifact for team 2")
			vol2, artifact2 := createArtifactVolume(team2.ID())
			Expect(artifact2.ID()).ToNot(Equal(artifact1.ID()))

			team2Volume, found, err := artifact2.Volume(team2.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(team2Volume.Handle()).To(Equal(vol2.Handle()))
			_, found, err = artifact2.Volume(team.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})
	})

	Describe("CreateVolumeForArtifact always returns DaemonSetVolume", func() {
		var noArtifactWorker *jetbridge.Worker

		BeforeEach(func() {
			noCfg := jetbridge.NewConfig("ci-namespace", "")
			noArtifactWorker = jetbridge.NewWorker(dbWorker, fakeClientset, noCfg)
			noArtifactWorker.SetExecutor(fakeExecutor)
			noArtifactWorker.SetVolumeRepo(database.VolumeRepository)
		})

		It("returns a DaemonSetVolume", func() {
			vol, artifact, err := noArtifactWorker.CreateVolumeForArtifact(ctx, team.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(artifact.ID()).To(BeNumerically(">", 0))

			_, isDaemonSet := vol.(*jetbridge.DaemonSetVolume)
			Expect(isDaemonSet).To(BeTrue(),
				"should always return DaemonSetVolume, got %T", vol)
			Expect(vol.Handle()).ToNot(BeEmpty())

			persistedVolume, found, err := database.VolumeRepository.FindVolume(vol.Handle())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedVolume.WorkerArtifactID()).To(Equal(artifact.ID()))
		})
	})
})
