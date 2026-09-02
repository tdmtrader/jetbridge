package steps

import (
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBWorkerResourceCacheObservation struct{ Value string }

func DBWorkerResourceCacheStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBWorkerResourceCacheObservation](
			"the production worker resource cache handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBWorkerResourceCacheObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBWorkerResourceCacheObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeDBWorkerResourceCache(database, profile)
				return DBWorkerResourceCacheObservation{Value: value}, err
			},
		),
		CheckString[DBWorkerResourceCacheObservation]("the worker resource cache result is {string}", "worker resource cache result", func(in DBWorkerResourceCacheObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

type dbWorkerResourceCacheFixture struct {
	database      JetbridgeDB
	workers       []db.Worker
	workerTypes   []*db.UsedWorkerBaseResourceType
	resourceCache db.ResourceCache
}

func newDBWorkerResourceCacheFixture(database JetbridgeDB) (dbWorkerResourceCacheFixture, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "worker-resource-cache-team"})
	if err != nil {
		return dbWorkerResourceCacheFixture{}, err
	}
	workers := make([]db.Worker, 3)
	workerTypes := make([]*db.UsedWorkerBaseResourceType, 3)
	for i := range workers {
		name := fmt.Sprintf("worker-resource-cache-%d", i)
		if _, err := database.WorkerFactory.SaveWorker(atc.Worker{
			Name: name, Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
			ResourceTypes: []atc.WorkerResourceType{{Type: "global-base-type", Image: "example/base", Version: "base-v1"}},
		}, 0); err != nil {
			return dbWorkerResourceCacheFixture{}, err
		}
		worker, found, err := database.WorkerFactory.GetWorker(name)
		if err != nil || !found {
			if err == nil {
				err = fmt.Errorf("worker %q was not found", name)
			}
			return dbWorkerResourceCacheFixture{}, err
		}
		workers[i] = worker
	}

	build, err := team.CreateOneOffBuild()
	if err != nil {
		return dbWorkerResourceCacheFixture{}, err
	}
	resourceCacheFactory := db.NewResourceCacheFactory(database.Conn, database.LockFactory)
	resourceTypeCache, err := resourceCacheFactory.FindOrCreateResourceCache(
		db.ForBuild(build.ID()), "global-base-type", atc.Version{"some-type": "version"},
		atc.Source{"some-type": "source"}, nil, nil,
	)
	if err != nil {
		return dbWorkerResourceCacheFixture{}, err
	}
	resourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(
		db.ForBuild(build.ID()), "some-type", atc.Version{"some": "version"},
		atc.Source{"some": "source"}, atc.Params{"some": "params"}, resourceTypeCache,
	)
	if err != nil {
		return dbWorkerResourceCacheFixture{}, err
	}
	for i, worker := range workers {
		workerType, found, err := (db.WorkerBaseResourceType{
			Name: resourceCache.BaseResourceType().Name, WorkerName: worker.Name(),
		}).Find(database.Conn)
		if err != nil || !found {
			if err == nil {
				err = fmt.Errorf("base resource type on worker %q was not found", worker.Name())
			}
			return dbWorkerResourceCacheFixture{}, err
		}
		workerTypes[i] = workerType
	}
	return dbWorkerResourceCacheFixture{database: database, workers: workers, workerTypes: workerTypes, resourceCache: resourceCache}, nil
}

func (fixture dbWorkerResourceCacheFixture) create(worker int, sourceType int) (*db.UsedWorkerResourceCache, bool, error) {
	tx, err := fixture.database.Conn.Begin()
	if err != nil {
		return nil, false, err
	}
	defer db.Rollback(tx)
	cache, valid, err := (db.WorkerResourceCache{
		WorkerName: fixture.workers[worker].Name(), ResourceCache: fixture.resourceCache,
	}).FindOrCreate(tx, fixture.workerTypes[sourceType].ID)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return cache, valid, nil
}

func observeDBWorkerResourceCache(database JetbridgeDB, profile string) (string, error) {
	fixture, err := newDBWorkerResourceCacheFixture(database)
	if err != nil {
		return "", err
	}
	initial, valid, err := fixture.create(0, 0)
	if err != nil {
		return "", err
	}
	if profile == "create" {
		return fmt.Sprintf("valid=%t;cache=%t", valid, initial != nil), nil
	}
	switch profile {
	case "same-source":
		cache, valid, err := fixture.create(0, 0)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("valid=%t;cache=%t;same=%t", valid, cache != nil, cache != nil && cache.ID == initial.ID && cache.WorkerBaseResourceTypeID == initial.WorkerBaseResourceTypeID), nil
	case "different-source":
		cache, valid, err := fixture.create(0, 1)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("valid=%t;cache=%t;same=%t", valid, cache != nil, cache != nil && cache.ID == initial.ID && cache.WorkerBaseResourceTypeID == initial.WorkerBaseResourceTypeID), nil
	}

	workerOne, valid, err := fixture.create(1, 0)
	if err != nil {
		return "", err
	}
	if profile == "other-worker" {
		return fmt.Sprintf("valid=%t;cache=%t;different=%t;source=%t", valid, workerOne != nil, workerOne != nil && workerOne.ID != initial.ID, workerOne != nil && workerOne.WorkerBaseResourceTypeID == fixture.workerTypes[0].ID), nil
	}
	if err := fixture.workers[0].Delete(); err != nil {
		return "", err
	}

	workerCache := db.WorkerResourceCache{WorkerName: fixture.workers[1].Name(), ResourceCache: fixture.resourceCache}
	switch profile {
	case "invalid-before", "invalid-after":
		validBefore := time.Now().Add(-100 * time.Second)
		if profile == "invalid-after" {
			validBefore = time.Now().Add(100 * time.Second)
		}
		cache, found, err := workerCache.Find(database.Conn, validBefore)
		if err != nil {
			return "", err
		}
		if profile == "invalid-after" {
			return fmt.Sprintf("found=%t;cache=%t", found, cache != nil), nil
		}
		return fmt.Sprintf("found=%t;cache=%t;same=%t;source-zero=%t", found, cache != nil, cache != nil && cache.ID == workerOne.ID, cache != nil && cache.WorkerBaseResourceTypeID == 0), nil
	case "replacement", "invalid-remains":
		replacement, valid, err := fixture.create(1, 2)
		if err != nil {
			return "", err
		}
		if profile == "replacement" {
			return fmt.Sprintf("valid=%t;cache=%t;different=%t;source=%t", valid, replacement != nil, replacement != nil && replacement.ID != initial.ID, replacement != nil && replacement.WorkerBaseResourceTypeID == fixture.workerTypes[2].ID), nil
		}
		invalid, found, err := (db.WorkerResourceCache{}).FindByID(database.Conn, workerOne.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("found=%t;cache=%t;source-zero=%t", found, invalid != nil, invalid != nil && invalid.WorkerBaseResourceTypeID == 0), nil
	default:
		return "", fmt.Errorf("unknown worker resource cache profile %q", profile)
	}
}
