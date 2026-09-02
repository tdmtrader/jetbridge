package steps

import (
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const (
	remainingVolumeBaseType        = "remaining-volume-base-type"
	remainingVolumeBaseTypeVersion = "remaining-volume-base-version"
)

type DBVolumeRemainingObservation struct {
	Failure string
}

func DBVolumeRemainingStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBVolumeRemainingObservation](
			"remaining production volume behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBVolumeRemainingObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return DBVolumeRemainingObservation{}, fmt.Errorf("remaining volume profile is not a string")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBVolumeRemainingObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBVolumeRemainingObservation{Failure: observeDBVolumeRemaining(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBVolumeRemainingObservation](
			"the remaining volume behavior is exact",
			func(in DBVolumeRemainingObservation, _ brine.Params, _ *brine.Recorder) error {
				if in.Failure != "" {
					return fmt.Errorf("%s", in.Failure)
				}
				return nil
			},
		),
	}
}

type dbVolumeRemainingFixture struct {
	database                   JetbridgeDB
	team                       db.Team
	workers                    []db.Worker
	build                      db.Build
	resourceCache              db.ResourceCache
	initialVolume              db.CreatedVolume
	initialWorkerResourceCache *db.UsedWorkerResourceCache
	volumeSequence             int
}

func newDBVolumeRemainingFixture(database JetbridgeDB) (*dbVolumeRemainingFixture, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "remaining-volume-team"})
	if err != nil {
		return nil, err
	}

	workers := make([]db.Worker, 3)
	for i := range workers {
		worker, err := team.SaveWorker(atc.Worker{
			Name:     fmt.Sprintf("remaining-volume-worker-%d", i),
			Platform: "linux",
			Version:  "1.2.3",
			State:    string(db.WorkerStateRunning),
			ResourceTypes: []atc.WorkerResourceType{{
				Type: remainingVolumeBaseType, Image: "example/remaining-volume", Version: remainingVolumeBaseTypeVersion,
			}},
		}, 0)
		if err != nil {
			return nil, err
		}
		workers[i] = worker
	}

	build, err := team.CreateOneOffBuild()
	if err != nil {
		return nil, err
	}
	resourceCacheFactory := db.NewResourceCacheFactory(database.Conn, database.LockFactory)
	resourceTypeCache, err := resourceCacheFactory.FindOrCreateResourceCache(
		db.ForBuild(build.ID()),
		remainingVolumeBaseType,
		atc.Version{"base": "version"},
		atc.Source{"base": "source"},
		nil,
		nil,
	)
	if err != nil {
		return nil, err
	}
	resourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(
		db.ForBuild(build.ID()),
		"remaining-custom-type",
		atc.Version{"resource": "version"},
		atc.Source{"resource": "source"},
		atc.Params{"resource": "params"},
		resourceTypeCache,
	)
	if err != nil {
		return nil, err
	}

	fixture := &dbVolumeRemainingFixture{
		database: database, team: team, workers: workers, build: build, resourceCache: resourceCache,
	}
	initialVolume, err := fixture.createVolume(0, "/initial")
	if err != nil {
		return nil, err
	}
	initialWorkerResourceCache, err := initialVolume.InitializeResourceCache(resourceCache)
	if err != nil {
		return nil, err
	}
	if initialWorkerResourceCache == nil {
		return nil, fmt.Errorf("initial resource cache was not initialized")
	}
	fixture.initialVolume = initialVolume
	fixture.initialWorkerResourceCache = initialWorkerResourceCache
	return fixture, nil
}

func (fixture *dbVolumeRemainingFixture) createVolume(worker int, path string) (db.CreatedVolume, error) {
	fixture.volumeSequence++
	creatingContainer, err := fixture.workers[worker].CreateContainer(
		db.NewBuildStepContainerOwner(
			fixture.build.ID(),
			atc.PlanID(fmt.Sprintf("remaining-volume-plan-%d", fixture.volumeSequence)),
			fixture.team.ID(),
		),
		db.ContainerMetadata{Type: "get", StepName: fmt.Sprintf("remaining-volume-step-%d", fixture.volumeSequence)},
	)
	if err != nil {
		return nil, err
	}
	if _, err := creatingContainer.Created(); err != nil {
		return nil, err
	}
	creatingVolume, err := fixture.database.VolumeRepository.CreateContainerVolume(
		fixture.team.ID(), fixture.workers[worker].Name(), creatingContainer, path,
	)
	if err != nil {
		return nil, err
	}
	return creatingVolume.Created()
}

func (fixture *dbVolumeRemainingFixture) invalidateSourceWorker() error {
	_, err := fixture.team.SaveWorker(atc.Worker{
		Name:          fixture.workers[0].Name(),
		Platform:      "linux",
		Version:       "1.2.3",
		State:         string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{},
	}, 0)
	return err
}

func (fixture *dbVolumeRemainingFixture) streamChain() error {
	streamedOne, err := fixture.createVolume(1, "/stream-one")
	if err != nil {
		return err
	}
	workerCacheOne, err := streamedOne.InitializeStreamedResourceCache(fixture.resourceCache, fixture.initialWorkerResourceCache.ID)
	if err != nil {
		return err
	}
	if workerCacheOne == nil {
		return fmt.Errorf("first streamed cache was not initialized")
	}
	streamedTwo, err := fixture.createVolume(2, "/stream-two")
	if err != nil {
		return err
	}
	workerCacheTwo, err := streamedTwo.InitializeStreamedResourceCache(fixture.resourceCache, workerCacheOne.ID)
	if err != nil {
		return err
	}
	if workerCacheTwo == nil {
		return fmt.Errorf("second streamed cache was not initialized")
	}
	return nil
}

type dbVolumeReplacement struct {
	fixture       *dbVolumeRemainingFixture
	oldCache      *db.UsedWorkerResourceCache
	newVolume     db.CreatedVolume
	newCache      *db.UsedWorkerResourceCache
	initializeErr error
}

func newDBVolumeReplacement(database JetbridgeDB) (*dbVolumeReplacement, error) {
	fixture, err := newDBVolumeRemainingFixture(database)
	if err != nil {
		return nil, err
	}
	streamed, err := fixture.createVolume(1, "/old-stream")
	if err != nil {
		return nil, err
	}
	oldCache, err := streamed.InitializeStreamedResourceCache(fixture.resourceCache, fixture.initialWorkerResourceCache.ID)
	if err != nil {
		return nil, err
	}
	if streamed.Type() != db.VolumeTypeResource || oldCache == nil {
		return nil, fmt.Errorf("old stream type=%q cache=%v", streamed.Type(), oldCache)
	}
	workers, err := fixture.team.FindWorkersForResourceCache(fixture.resourceCache.ID(), time.Now().Add(-100*time.Second))
	if err != nil || len(workers) != 2 {
		return nil, fmt.Errorf("pre-delete workers=%d err=%v", len(workers), err)
	}
	if err := fixture.workers[0].Delete(); err != nil {
		return nil, err
	}
	newVolume, err := fixture.createVolume(1, "/replacement")
	if err != nil {
		return nil, err
	}
	newCache, initializeErr := newVolume.InitializeResourceCache(fixture.resourceCache)
	return &dbVolumeReplacement{
		fixture: fixture, oldCache: oldCache, newVolume: newVolume, newCache: newCache, initializeErr: initializeErr,
	}, nil
}

func observeDBVolumeRemaining(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }

	if profile == "worker-delete-cascades" {
		fixture, err := newDBVolumeRemainingFixture(database)
		if err != nil {
			return err.Error()
		}
		volume, err := fixture.createVolume(0, "/cascade")
		if err != nil {
			return err.Error()
		}
		handle := volume.Handle()
		if err := fixture.workers[0].Delete(); err != nil {
			return err.Error()
		}
		_, found, err := database.VolumeRepository.FindVolume(handle)
		if err != nil || found {
			return fail("volume after worker delete found=%t err=%v", found, err)
		}
		return ""
	}

	if profile == "nested-resource-type" || profile == "streamed-resource-type" {
		return observeDBVolumeResourceType(database, profile)
	}

	if profile == "replacement-created" || profile == "replacement-resource-type" ||
		profile == "replacement-found-before" || profile == "invalid-stream-retained-before" ||
		profile == "replacement-found-after" || profile == "invalid-stream-retained-after" {
		replacement, err := newDBVolumeReplacement(database)
		if err != nil {
			return err.Error()
		}
		switch profile {
		case "replacement-created":
			if replacement.initializeErr != nil || replacement.newCache == nil || replacement.newCache.WorkerBaseResourceTypeID == 0 {
				return fail("replacement cache=%v err=%v", replacement.newCache, replacement.initializeErr)
			}
		case "replacement-resource-type":
			if replacement.newVolume.Type() != db.VolumeTypeResource {
				return fail("replacement type=%q", replacement.newVolume.Type())
			}
		case "replacement-found-before", "replacement-found-after":
			validBefore := time.Now().Add(-100 * time.Second)
			if profile == "replacement-found-after" {
				validBefore = time.Now().Add(100 * time.Second)
			}
			foundVolume, found, err := database.VolumeRepository.FindResourceCacheVolume(
				replacement.fixture.workers[1].Name(), replacement.fixture.resourceCache, validBefore,
			)
			if err != nil || !found || foundVolume == nil || foundVolume.Handle() != replacement.newVolume.Handle() {
				return fail("replacement lookup found=%t handle=%v want=%q err=%v", found, remainingVolumeHandle(foundVolume), replacement.newVolume.Handle(), err)
			}
		case "invalid-stream-retained-before", "invalid-stream-retained-after":
			invalidCache, found, err := (db.WorkerResourceCache{}).FindByID(database.Conn, replacement.oldCache.ID)
			if err != nil || !found || invalidCache == nil || invalidCache.WorkerBaseResourceTypeID != 0 {
				return fail("invalid cache found=%t cache=%v err=%v", found, invalidCache, err)
			}
		}
		return ""
	}

	fixture, err := newDBVolumeRemainingFixture(database)
	if err != nil {
		return err.Error()
	}
	switch profile {
	case "duplicate-container":
		duplicate, err := fixture.createVolume(0, "/duplicate")
		if err != nil {
			return err.Error()
		}
		_, err = duplicate.InitializeResourceCache(fixture.resourceCache)
		if err != nil || duplicate.Type() != db.VolumeTypeContainer {
			return fail("duplicate type=%q err=%v", duplicate.Type(), err)
		}
	case "stream-chain-before-invalidation", "stream-chain-after-invalidation":
		if err := fixture.streamChain(); err != nil {
			return err.Error()
		}
		if err := fixture.invalidateSourceWorker(); err != nil {
			return err.Error()
		}
		validBefore := time.Now().Add(-100 * time.Second)
		wantFound := true
		if profile == "stream-chain-after-invalidation" {
			validBefore = time.Now().Add(100 * time.Second)
			wantFound = false
		}
		for _, worker := range fixture.workers {
			_, found, err := database.VolumeRepository.FindResourceCacheVolume(worker.Name(), fixture.resourceCache, validBefore)
			if err != nil || found != wantFound {
				return fail("worker %q found=%t want=%t err=%v", worker.Name(), found, wantFound, err)
			}
		}
	case "invalid-source-refused", "source-worker-before-invalidation", "source-worker-after-invalidation":
		if err := fixture.invalidateSourceWorker(); err != nil {
			return err.Error()
		}
		if profile == "invalid-source-refused" {
			streamed, err := fixture.createVolume(1, "/invalid-source")
			if err != nil {
				return err.Error()
			}
			_, err = streamed.InitializeStreamedResourceCache(fixture.resourceCache, fixture.initialWorkerResourceCache.ID)
			if err != nil || streamed.Type() != db.VolumeTypeContainer {
				return fail("invalid stream type=%q err=%v", streamed.Type(), err)
			}
			return ""
		}
		validBefore := time.Now().Add(-100 * time.Second)
		if profile == "source-worker-after-invalidation" {
			validBefore = time.Now().Add(100 * time.Second)
		}
		workers, err := fixture.team.FindWorkersForResourceCache(fixture.resourceCache.ID(), validBefore)
		if err != nil {
			return err.Error()
		}
		if profile == "source-worker-before-invalidation" {
			if len(workers) != 1 || workers[0].Name() != fixture.workers[0].Name() {
				return fail("earlier workers=%v", remainingVolumeWorkerNames(workers))
			}
		} else if len(workers) != 0 {
			return fail("later workers=%v", remainingVolumeWorkerNames(workers))
		}
	default:
		return fail("unknown remaining volume profile %q", profile)
	}
	return ""
}

func observeDBVolumeResourceType(database JetbridgeDB, profile string) string {
	fixture, err := newDBVolumeRemainingFixture(database)
	if err != nil {
		return err.Error()
	}
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	volume := fixture.initialVolume
	if profile == "streamed-resource-type" {
		volume, err = fixture.createVolume(1, "/resource-type-stream")
		if err != nil {
			return err.Error()
		}
		if volume.Type() != db.VolumeTypeContainer {
			return fail("streamed volume pre-initialize type=%q", volume.Type())
		}
		if _, err := volume.InitializeStreamedResourceCache(fixture.resourceCache, fixture.initialWorkerResourceCache.ID); err != nil {
			return err.Error()
		}
	} else if volume.Type() != db.VolumeTypeResource {
		return fail("local volume type=%q", volume.Type())
	}

	resourceType, err := volume.ResourceType()
	if err != nil {
		return err.Error()
	}
	if failure := checkRemainingVolumeResourceType(resourceType); failure != "" {
		return failure
	}
	if profile == "nested-resource-type" {
		refetched, found, err := database.VolumeRepository.FindResourceCacheVolume(
			fixture.workers[0].Name(), fixture.resourceCache, time.Now(),
		)
		if err != nil || !found || refetched == nil || refetched.Type() != db.VolumeTypeResource {
			return fail("refetched found=%t volume=%v err=%v", found, remainingVolumeHandle(refetched), err)
		}
		resourceType, err = refetched.ResourceType()
		if err != nil {
			return err.Error()
		}
		return checkRemainingVolumeResourceType(resourceType)
	}
	return ""
}

func checkRemainingVolumeResourceType(resourceType *db.VolumeResourceType) string {
	if resourceType == nil || resourceType.ResourceType == nil || resourceType.ResourceType.WorkerBaseResourceType == nil {
		return fmt.Sprintf("resource type tree=%+v", resourceType)
	}
	base := resourceType.ResourceType.WorkerBaseResourceType
	if base.Name != remainingVolumeBaseType || base.Version != remainingVolumeBaseTypeVersion ||
		fmt.Sprint(resourceType.ResourceType.Version) != fmt.Sprint(atc.Version{"base": "version"}) ||
		fmt.Sprint(resourceType.Version) != fmt.Sprint(atc.Version{"resource": "version"}) {
		return fmt.Sprintf("resource type base=%s/%s type-version=%v version=%v", base.Name, base.Version, resourceType.ResourceType.Version, resourceType.Version)
	}
	return ""
}

func remainingVolumeWorkerNames(workers []db.Worker) []string {
	names := make([]string, len(workers))
	for i, worker := range workers {
		names[i] = worker.Name()
	}
	return names
}

func remainingVolumeHandle(volume db.CreatedVolume) any {
	if volume == nil {
		return nil
	}
	return volume.Handle()
}
