package jetbridge_test

import (
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

var _ = Describe("Behavioral Worker Tests", func() {
	var (
		database      jetbridgeDB
		dbWorker      db.Worker
		team          db.Team
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()

		var err error
		dbWorker, err = persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		team, err = database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
		Expect(err).NotTo(HaveOccurred())

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{
					{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
				},
			},
		}
		fakeClientset = fake.NewSimpleClientset(node)
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		worker.SetVolumeRepo(database.VolumeRepository)
	})

	createVolume := func(handle string, volumeType db.VolumeType) db.CreatedVolume {
		GinkgoHelper()
		creating, err := database.VolumeRepository.CreateVolumeWithHandle(handle, team.ID(), dbWorker.Name(), volumeType)
		Expect(err).NotTo(HaveOccurred())
		created, err := creating.Created()
		Expect(err).NotTo(HaveOccurred())
		return created
	}

	// -----------------------------------------------------------------------
	// RC-02: Cached volume lookup returns DaemonSetVolume
	// -----------------------------------------------------------------------
	Describe("RC-02: LookupVolume returns DaemonSetVolume", func() {
		BeforeEach(func() {
			createVolume("cached-vol-1", db.VolumeTypeArtifact)
		})

		It("returns a DaemonSetVolume backed by the persisted volume", func() {
			vol, found, err := worker.LookupVolume(ctx, "cached-vol-1")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(vol).NotTo(BeNil())
			Expect(vol.Handle()).To(Equal("cached-vol-1"))

			dsVol, ok := vol.(*jetbridge.DaemonSetVolume)
			Expect(ok).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)
			Expect(dsVol.Key()).To(Equal("cached-vol-1"))
			Expect(dsVol.DBVolume().WorkerName()).To(Equal(dbWorker.Name()))
		})

		Context("with ArtifactLocator populated", func() {
			BeforeEach(func() {
				locator := jetbridge.NewArtifactLocator()
				locator.Record(jetbridge.ArtifactKey("cached-vol-1"), "node-42", "container/output")
				worker.SetArtifactLocator(locator)
			})

			It("returns volume with its persisted worker source", func() {
				vol, found, err := worker.LookupVolume(ctx, "cached-vol-1")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(vol.Source()).To(Equal(dbWorker.Name()))
			})
		})

		Context("without ArtifactLocator", func() {
			It("returns volume with its persisted worker source", func() {
				vol, found, err := worker.LookupVolume(ctx, "cached-vol-1")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(vol.Source()).To(Equal(dbWorker.Name()))
			})
		})
	})

	// -----------------------------------------------------------------------
	// RC-03: Cache hit short-circuit
	// -----------------------------------------------------------------------
	Describe("RC-03: Cache hit short-circuit", func() {
		BeforeEach(func() {
			createVolume("cache-hit-vol", db.VolumeTypeArtifact)
		})

		It("returns a cached volume without creating a pod", func() {
			vol, found, err := worker.LookupVolume(ctx, "cache-hit-vol")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(vol).NotTo(BeNil())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(pods.Items).To(BeEmpty())
		})

		It("persists the resource-cache association", func() {
			_, err := database.WorkerFactory.SaveWorker(atc.Worker{
				Name: "k8s-worker-1", Platform: "linux", Version: "1.2.3",
				State: string(db.WorkerStateRunning),
				ResourceTypes: []atc.WorkerResourceType{{
					Type: "some-type", Image: "some-image", Version: "some-version",
				}},
			}, 0)
			Expect(err).NotTo(HaveOccurred())

			build, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			cache, err := database.ResourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()),
				"some-type",
				atc.Version{"version": "v1"},
				atc.Source{"uri": "example.invalid"},
				nil,
				nil,
			)
			Expect(err).NotTo(HaveOccurred())

			vol, found, err := worker.LookupVolume(ctx, "cache-hit-vol")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			result, err := vol.InitializeResourceCache(ctx, cache)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(BeNil())
			Expect(result.ID).NotTo(BeZero())

			var workerResourceCacheID, resourceCacheID int
			err = database.Conn.QueryRow(`
				SELECT v.worker_resource_cache_id, wrc.resource_cache_id
				FROM volumes v
				JOIN worker_resource_caches wrc ON wrc.id = v.worker_resource_cache_id
				WHERE v.handle = $1
			`, "cache-hit-vol").Scan(&workerResourceCacheID, &resourceCacheID)
			Expect(err).NotTo(HaveOccurred())
			Expect(workerResourceCacheID).To(Equal(result.ID))
			Expect(resourceCacheID).To(Equal(cache.ID()))
		})
	})

	// -----------------------------------------------------------------------
	// RC-05: Cache invalidation
	// -----------------------------------------------------------------------
	Describe("RC-05: Cache invalidation", func() {
		It("returns not found when no persisted volume has the handle", func() {
			vol, found, err := worker.LookupVolume(ctx, "expired-vol")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(vol).To(BeNil())
		})

		It("returns not found when volumeRepo is nil", func() {
			freshWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			vol, found, err := freshWorker.LookupVolume(ctx, "any-vol")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(vol).To(BeNil())
		})
	})

	// -----------------------------------------------------------------------
	// CO-09: Output recording - CreateVolumeForArtifact
	// -----------------------------------------------------------------------
	Describe("CO-09: CreateVolumeForArtifact returns DaemonSetVolume with ArtifactKey", func() {
		It("persists an artifact volume and returns its database artifact", func() {
			vol, artifact, err := worker.CreateVolumeForArtifact(ctx, team.ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(artifact).NotTo(BeNil())

			dsVol, ok := vol.(*jetbridge.DaemonSetVolume)
			Expect(ok).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)
			Expect(dsVol.Key()).To(Equal(jetbridge.ArtifactKey(vol.Handle())))
			Expect(dsVol.Handle()).NotTo(BeEmpty())

			var teamID, workerArtifactID int
			var workerName, state string
			err = database.Conn.QueryRow(`
				SELECT team_id, worker_name, state, worker_artifact_id
				FROM volumes
				WHERE handle = $1
			`, vol.Handle()).Scan(&teamID, &workerName, &state, &workerArtifactID)
			Expect(err).NotTo(HaveOccurred())
			Expect(teamID).To(Equal(team.ID()))
			Expect(workerName).To(Equal(dbWorker.Name()))
			Expect(state).To(Equal(string(db.VolumeStateCreated)))
			Expect(workerArtifactID).To(Equal(artifact.ID()))

			persisted, found, err := database.VolumeRepository.FindVolume(vol.Handle())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persisted.Type()).To(Equal(db.VolumeTypeArtifact))
		})
	})

	// -----------------------------------------------------------------------
	// LR-03: ATC restart resilience
	// -----------------------------------------------------------------------
	Describe("LR-03: ATC restart resilience - LookupVolume without ArtifactLocator", func() {
		BeforeEach(func() {
			createVolume("resilient-vol", db.VolumeTypeArtifact)
		})

		It("reconstructs the volume from persisted state", func() {
			restartedWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			restartedWorker.SetVolumeRepo(db.NewVolumeRepository(database.Conn))

			vol, found, err := restartedWorker.LookupVolume(ctx, "resilient-vol")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(vol).NotTo(BeNil())
			Expect(vol.Handle()).To(Equal("resilient-vol"))
		})
	})

	// -----------------------------------------------------------------------
	// LR-04: Container reuse
	// -----------------------------------------------------------------------
	Describe("LR-04: Container reuse when createdContainer already exists", func() {
		var existing db.CreatedContainer

		BeforeEach(func() {
			creating, err := dbWorker.CreateContainer(
				db.NewFixedHandleContainerOwner("reused-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "my-task"},
			)
			Expect(err).NotTo(HaveOccurred())
			existing, err = creating.Created()
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns the same persisted container without inserting another row", func() {
			owner := db.NewFixedHandleContainerOwner("reused-handle")
			metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "my-task"}
			spec := runtime.ContainerSpec{
				TeamID: team.ID(), TeamName: team.Name(), Dir: "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			}

			container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
			Expect(err).NotTo(HaveOccurred())
			Expect(container.DBContainer().ID()).To(Equal(existing.ID()))

			var count int
			err = database.Conn.QueryRow(`SELECT count(*) FROM containers WHERE handle = $1`, "reused-handle").Scan(&count)
			Expect(err).NotTo(HaveOccurred())
			Expect(count).To(Equal(1))
		})
	})

	Describe("FindOrCreateContainer returns volume mounts matching spec", func() {
		It("returns mounts for Dir, inputs, outputs, and caches", func() {
			owner := db.NewFixedHandleContainerOwner("mount-test-handle")
			metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "mount-test"}
			spec := runtime.ContainerSpec{
				TeamID: team.ID(), TeamName: team.Name(), Dir: "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				Inputs:    []runtime.Input{{DestinationPath: "/workdir/input-a"}},
				Outputs:   runtime.OutputPaths{"out-b": "/workdir/out-b"},
				Caches:    []string{"my-cache"},
			}

			_, mounts, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
			Expect(err).NotTo(HaveOccurred())
			Expect(mounts).To(HaveLen(4))

			mountPaths := make([]string, len(mounts))
			for i, mount := range mounts {
				mountPaths[i] = mount.MountPath
			}
			Expect(mountPaths).To(ContainElements("/workdir", "/workdir/input-a", "/workdir/out-b"))
		})
	})

	Describe("LookupVolume propagates DB errors", func() {
		It("returns the real repository error", func() {
			worker.SetVolumeRepo(db.NewVolumeRepository(closedJetbridgeCloneConn()))
			_, _, err := worker.LookupVolume(ctx, "any")
			Expect(err).To(MatchError(ContainSubstring("closed")))
		})
	})

	Describe("SkipResourceCache", func() {
		It("returns false to enable caching in DaemonSet mode", func() {
			Expect(worker.SkipResourceCache()).To(BeFalse())
		})
	})

	Describe("CreateVolumeForArtifact without volumeRepo", func() {
		It("returns an error indicating volume repository not configured", func() {
			freshWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			_, _, err := freshWorker.CreateVolumeForArtifact(ctx, team.ID())
			Expect(err).To(MatchError(ContainSubstring("volume repository not configured")))
		})
	})

	Describe("LookupVolume passes handle to FindVolume", func() {
		It("finds only the exact persisted handle", func() {
			createVolume("specific-handle-abc", db.VolumeTypeArtifact)

			vol, found, err := worker.LookupVolume(ctx, "specific-handle-abc")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(vol.Handle()).To(Equal("specific-handle-abc"))

			_, found, err = worker.LookupVolume(ctx, "specific-handle-ab")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})
	})
})
