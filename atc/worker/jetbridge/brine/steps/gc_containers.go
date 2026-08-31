package steps

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
)

// GCContainerDefinitions migrates atc/gc/container_collector_test.go and
// atc/gc/volume_collector_test.go — the two collectors that decide when a row
// becomes garbage.
//
// Everything runs against the scenario's own PostgreSQL through the same
// db.ContainerRepository and db.VolumeRepository the ATC wires in production.
// Every assertion reads a table after the sweep. Nothing counts a call.
//
// On the one wrapper in this file, and why it is here when the pilot rejected
// its ancestors.
//
// gc_reclamation.go replaced the ginkgo suite's failing-repository decorators
// with a closed connection, on the grounds that a decorator returning
// errors.New("I am le tired") makes an error-passthrough assertion vacuous —
// the destroyer could have invented an error of its own and passed. That
// objection is exactly right for a scenario whose assertion IS the error.
//
// It does not reach the three scenarios here. Their assertion is not the
// error; it is which ROWS moved while one of Run's four steps was down, and
// every one of those rows is real and read back out of PostgreSQL. The
// wrappers below embed the live repository, delegate all but one method to it,
// and record nothing — there is no expectation to set and no call to count.
// They are the pilot's "the database has gone away" narrowed to one statement.
//
// A closed connection cannot express this, because it takes down all four
// steps at once, and PostgreSQL will not fail one of them on request: the FK
// from volumes to containers is ON DELETE SET NULL (initial_schema:1380), so
// even a volume still attached to a container being deleted does not make the
// delete fail. Checked, not assumed — that FK is also the mechanism by which a
// volume becomes orphaned, which is the last scenario in the feature file.
//
// The scenarios also assert that the sweep REPORTED the failure, and do so
// with a step that takes no parameter — "the sweep reported the failure rather
// than a clean pass" — precisely so that nothing here compares against a
// string this file supplied.

// gcGracePeriod is the missing/hijack grace period the collectors are built with, the
// same one minute the ginkgo suites used. Fixtures backdate a column by an
// hour to be outside it and stamp NOW() to be inside it, so no scenario waits.
const gcGracePeriod = time.Minute

// ContainerGCReady is the container collector and the rows it will sweep.
type ContainerGCReady struct {
	DB        JetbridgeDB
	Repo      db.ContainerRepository
	Configs   db.ResourceConfigFactory
	Collector interface{ Run(context.Context) error }

	Team          db.Team
	Worker        db.Worker
	FinishedBuild db.Build
	RunningBuild  db.Build

	// handle maps a scenario's name for a container to the handle its row
	// actually got — build-step and check-session owners both generate a UUID,
	// so the feature file's vocabulary and the table's have to be bridged.
	// name is the reverse, so a table read reports names a reader can place.
	handle map[string]string
	name   map[string]string
}

// ContainerGCSwept is a completed container-collector pass.
type ContainerGCSwept struct {
	Ready ContainerGCReady
	Err   error
}

// VolumeGCReady is the volume collector and the rows it will sweep.
type VolumeGCReady struct {
	DB        JetbridgeDB
	Collector interface{ Run(context.Context) error }

	Team   db.Team
	Worker db.Worker

	handle map[string]string
	name   map[string]string
}

// VolumeGCSwept is a completed volume-collector pass.
type VolumeGCSwept struct {
	Ready VolumeGCReady
	Err   error
}

func GCContainerDefinitions() []brine.StepDefinition {
	return append(containerCollectorDefinitions(), volumeCollectorDefinitions()...)
}

// -----------------------------------------------------------------------
// The container collector
// -----------------------------------------------------------------------

func containerCollectorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ContainerGCReady](
			"a container collector sweeping a real database",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ContainerGCReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ContainerGCReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				return newContainerGCReady(database)
			},
		),

		// The two builds are made up front and never change afterwards, so
		// "has finished" and "is still running" are settled before any
		// container exists. Ginkgo's fixture flipped interceptible AFTER
		// creating the containers, which works but reads backwards: a build
		// does not become uninterceptible because a collector is about to run.
		makeGCContainer("the container {string} belongs to a build that has finished", gcFinishedBuild),
		makeGCContainer("the container {string} belongs to a build that is still running", gcRunningBuild),

		// RemoveMissingContainers refuses to delete a container whose worker is
		// stalled, so the row has to sit on a second worker in that state. The
		// build is the running one: the point of this row is the worker, and an
		// orphaned container would be moved out of `created` by an earlier step
		// and stop matching the delete for the wrong reason.
		brine.DefineMap[ContainerGCReady, ContainerGCReady](
			"the container {string} sits on a worker that has stalled",
			func(in ContainerGCReady, p brine.Params, _ *brine.Recorder) (ContainerGCReady, error) {
				name, err := paramAt("the container {string} sits on a worker that has stalled", p, 0)
				if err != nil {
					return ContainerGCReady{}, err
				}
				stalled, err := in.DB.WorkerFactory.SaveWorker(atc.Worker{
					Name:     "stalled-worker",
					Platform: "linux",
					State:    string(db.WorkerStateStalled),
				}, 5*time.Minute)
				if err != nil {
					return ContainerGCReady{}, fmt.Errorf("register the stalled worker: %w", err)
				}
				return in, in.createdContainerOn(stalled, in.RunningBuild, name)
			},
		),

		brine.DefineMap[ContainerGCReady, ContainerGCReady](
			"the container {string} failed while it was being created",
			func(in ContainerGCReady, p brine.Params, _ *brine.Recorder) (ContainerGCReady, error) {
				name, err := paramAt("the container {string} failed while it was being created", p, 0)
				if err != nil {
					return ContainerGCReady{}, err
				}
				creating, err := in.Worker.CreateContainer(
					db.NewBuildStepContainerOwner(in.RunningBuild.ID(), atc.PlanID(name), in.Team.ID()),
					db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "some-task"},
				)
				if err != nil {
					return ContainerGCReady{}, fmt.Errorf("create container %q: %w", name, err)
				}
				failed, err := creating.Failed()
				if err != nil {
					return ContainerGCReady{}, fmt.Errorf("mark container %q failed: %w", name, err)
				}
				in.remember(name, failed.Handle())
				return in, nil
			},
		),

		// last_hijack is written by the hijack path (container.go); backdating
		// the column is what the ginkgo suite did too, and it is the only way
		// to be an hour into a one-minute grace period without waiting.
		setGCContainerColumn("the container {string} was last hijacked an hour ago", "last_hijack", gcAnHourAgo),
		setGCContainerColumn("the container {string} was last hijacked a moment ago", "last_hijack", gcJustNow),

		// missing_since is written by the reaper when a worker's report omits a
		// handle the ATC holds a row for.
		setGCContainerColumn("the worker stopped reporting the container {string} an hour ago", "missing_since", gcAnHourAgo),
		setGCContainerColumn("the worker stopped reporting the container {string} a moment ago", "missing_since", gcJustNow),

		// Check containers rank by id DESCENDING within one resource, so the
		// order they are written in the sentence is the order they are created
		// and the LAST one named is the one the cap keeps. Two arities rather
		// than a list because the count is part of what each scenario is
		// saying: three when the hijack exemption needs a row of its own,
		// two when one excess container is all the scenario is about.
		makeGCCheckContainers("the resource {string} has the check containers {string} and {string}, oldest first", 2),
		makeGCCheckContainers("the resource {string} has the check containers {string}, {string} and {string}, oldest first", 3),

		// The three fault injectors. Each replaces the collector with one over
		// a repository whose single named method fails; the other three still
		// reach PostgreSQL, which is the whole point of the scenario.
		failOneGCStep("the collector cannot look up orphaned containers",
			func(r db.ContainerRepository) db.ContainerRepository { return noOrphanLookup{r} }),
		failOneGCStep("the collector cannot destroy failed containers",
			func(r db.ContainerRepository) db.ContainerRepository { return noFailedDestroy{r} }),
		failOneGCStep("the collector cannot delete missing containers",
			func(r db.ContainerRepository) db.ContainerRepository { return noMissingDelete{r} }),

		brine.DefineMap[ContainerGCReady, ContainerGCSwept](
			"the container collector sweeps",
			func(in ContainerGCReady, _ brine.Params, _ *brine.Recorder) (ContainerGCSwept, error) {
				return ContainerGCSwept{Ready: in, Err: in.Collector.Run(context.Background())}, nil
			},
		),

		CheckThat[ContainerGCSwept]("the container collector completed without error",
			func(in ContainerGCSwept) error {
				if in.Err != nil {
					return fmt.Errorf("the container collector failed: %v", in.Err)
				}
				return nil
			}),

		// No parameter, so nothing is compared against text this file supplied.
		// A collector that swallowed the failure would tell the component
		// runner the pass is done, and the runner would wait a full interval
		// before trying again with nothing having said anything was wrong.
		CheckThat[ContainerGCSwept]("the sweep reported the failure rather than a clean pass",
			func(in ContainerGCSwept) error {
				if in.Err == nil {
					return fmt.Errorf("expected the sweep to report a failure, it reported success — " +
						"a step that could not run and said nothing is a whole collection interval " +
						"in which nothing was collected and nothing complained")
				}
				return nil
			}),

		// A state is a membership question over the rows in that state, so a
		// failure lists the whole set: a surprise about one container is nearly
		// always diagnosed by the ones beside it.
		CheckMember[ContainerGCSwept]("the container {string} is now destroying",
			"the containers now marked destroying",
			func(in ContainerGCSwept) ([]string, error) {
				return in.containersInState(atc.ContainerStateDestroying)
			}),

		CheckMember[ContainerGCSwept]("the container {string} is still created",
			"the containers still in the created state",
			func(in ContainerGCSwept) ([]string, error) {
				return in.containersInState(atc.ContainerStateCreated)
			}),

		CheckMember[ContainerGCSwept]("the container {string} is still in the database",
			"the container rows the sweep left behind",
			func(in ContainerGCSwept) ([]string, error) { return in.containerRows() }),

		// Absence, so it is guarded: see checkGCGone.
		checkGCGone[ContainerGCSwept]("the container {string} has been removed from the database",
			"a container",
			func(in ContainerGCSwept) map[string]string { return in.Ready.handle },
			func(in ContainerGCSwept) ([]string, error) { return in.containerRows() }),
	}
}

func newContainerGCReady(database JetbridgeDB) (ContainerGCReady, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "gc-collector-team"})
	if err != nil {
		return ContainerGCReady{}, fmt.Errorf("create team: %w", err)
	}

	// The base resource type has to exist before the worker registers a
	// resource type over it and before any resource config names it.
	if err := createBaseResourceType(database); err != nil {
		return ContainerGCReady{}, err
	}

	worker, err := database.WorkerFactory.SaveWorker(atc.Worker{
		Name:     "some-worker",
		Platform: "linux",
		ResourceTypes: []atc.WorkerResourceType{
			{Type: gcBaseResourceTypeName, Image: "/some-image", Version: "some-version"},
		},
	}, 5*time.Minute)
	if err != nil {
		return ContainerGCReady{}, fmt.Errorf("register the worker: %w", err)
	}

	finished, err := team.CreateOneOffBuild()
	if err != nil {
		return ContainerGCReady{}, fmt.Errorf("create the finished build: %w", err)
	}
	if err := finished.SetInterceptible(false); err != nil {
		return ContainerGCReady{}, fmt.Errorf("finish the build: %w", err)
	}
	running, err := team.CreateOneOffBuild()
	if err != nil {
		return ContainerGCReady{}, fmt.Errorf("create the running build: %w", err)
	}

	repo := database.ContainerRepository
	return ContainerGCReady{
		DB:            database,
		Repo:          repo,
		Configs:       db.NewResourceConfigFactory(database.Conn, database.LockFactory),
		Collector:     gc.NewContainerCollector(repo, gcGracePeriod, gcGracePeriod),
		Team:          team,
		Worker:        worker,
		FinishedBuild: finished,
		RunningBuild:  running,
		handle:        map[string]string{},
		name:          map[string]string{},
	}, nil
}

const gcBaseResourceTypeName = "some-base-type"

func createBaseResourceType(database JetbridgeDB) error {
	tx, err := database.Conn.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer db.Rollback(tx)

	if _, err := (db.BaseResourceType{Name: gcBaseResourceTypeName}).FindOrCreate(tx, false); err != nil {
		return fmt.Errorf("create the base resource type: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit the base resource type: %w", err)
	}
	return nil
}

// gcWhichBuild selects one of the scenario's two builds without the call sites
// having to name a field.
type gcWhichBuild func(ContainerGCReady) db.Build

func gcFinishedBuild(in ContainerGCReady) db.Build { return in.FinishedBuild }
func gcRunningBuild(in ContainerGCReady) db.Build  { return in.RunningBuild }

func makeGCContainer(pattern string, pick gcWhichBuild) brine.StepDefinition {
	return brine.DefineMap[ContainerGCReady, ContainerGCReady](pattern,
		func(in ContainerGCReady, p brine.Params, _ *brine.Recorder) (ContainerGCReady, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return ContainerGCReady{}, err
			}
			return in, in.createdContainerOn(in.Worker, pick(in), name)
		},
	)
}

// createdContainerOn builds a container owned by a build and walks it to
// `created` — the state both the orphan rule and the missing rule select on.
func (c ContainerGCReady) createdContainerOn(worker db.Worker, build db.Build, name string) error {
	creating, err := worker.CreateContainer(
		db.NewBuildStepContainerOwner(build.ID(), atc.PlanID(name), c.Team.ID()),
		db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "some-task"},
	)
	if err != nil {
		return fmt.Errorf("create container %q: %w", name, err)
	}
	created, err := creating.Created()
	if err != nil {
		return fmt.Errorf("mark container %q created: %w", name, err)
	}
	c.remember(name, created.Handle())
	return nil
}

// makeGCCheckContainers creates `count` check containers for one resource, in
// the order the sentence names them. They share a resource config, so they
// share a check session, so they are one partition for the cap's window
// function — which ranks by id DESCENDING, making the last one named the
// newest and the one the cap keeps.
func makeGCCheckContainers(pattern string, count int) brine.StepDefinition {
	return brine.DefineMap[ContainerGCReady, ContainerGCReady](pattern,
		func(in ContainerGCReady, p brine.Params, _ *brine.Recorder) (ContainerGCReady, error) {
			resource, err := paramAt(pattern, p, 0)
			if err != nil {
				return ContainerGCReady{}, err
			}

			config, err := in.Configs.FindOrCreateResourceConfig(
				gcBaseResourceTypeName, atc.Source{"repository": resource}, nil)
			if err != nil {
				return ContainerGCReady{}, fmt.Errorf("create the resource config for %q: %w", resource, err)
			}

			for i := 1; i <= count; i++ {
				name, err := paramAt(pattern, p, i)
				if err != nil {
					return ContainerGCReady{}, err
				}
				owner := db.NewResourceConfigCheckSessionContainerOwner(
					config.ID(),
					config.OriginBaseResourceType().ID,
					db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: time.Hour},
				)
				creating, err := in.Worker.CreateContainer(owner,
					db.ContainerMetadata{Type: db.ContainerTypeCheck})
				if err != nil {
					return ContainerGCReady{}, fmt.Errorf("create check container %q: %w", name, err)
				}
				created, err := creating.Created()
				if err != nil {
					return ContainerGCReady{}, fmt.Errorf("mark check container %q created: %w", name, err)
				}
				in.remember(name, created.Handle())
			}
			return in, nil
		},
	)
}

// gcAnHourAgo and gcJustNow are the two sides of the one-minute grace period.
const (
	gcAnHourAgo = "NOW() - '1 hour'::interval"
	gcJustNow   = "NOW()"
)

func setGCContainerColumn(pattern, column, value string) brine.StepDefinition {
	return brine.DefineMap[ContainerGCReady, ContainerGCReady](pattern,
		func(in ContainerGCReady, p brine.Params, _ *brine.Recorder) (ContainerGCReady, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return ContainerGCReady{}, err
			}
			handle, ok := in.handle[name]
			if !ok {
				return ContainerGCReady{}, fmt.Errorf(
					"no container named %q was created by this scenario, so %q cannot be set on it", name, column)
			}
			// column and value are literals from this file, never from a
			// scenario; the handle is the only value that comes from the line.
			res, err := in.DB.Conn.Exec(
				fmt.Sprintf(`UPDATE containers SET %s = %s WHERE handle = $1`, column, value), handle)
			if err != nil {
				return ContainerGCReady{}, fmt.Errorf("set %s on container %q: %w", column, name, err)
			}
			return in, mustHaveTouchedOneGCRow(res, column, name)
		},
	)
}

func failOneGCStep(pattern string, wrap func(db.ContainerRepository) db.ContainerRepository) brine.StepDefinition {
	return brine.DefineMap[ContainerGCReady, ContainerGCReady](pattern,
		func(in ContainerGCReady, _ brine.Params, _ *brine.Recorder) (ContainerGCReady, error) {
			in.Collector = gc.NewContainerCollector(wrap(in.Repo), gcGracePeriod, gcGracePeriod)
			return in, nil
		},
	)
}

// errGCStepUnavailable is what one wrapped repository method returns. Nothing
// asserts on its text — see the header, and "the sweep reported the failure
// rather than a clean pass", which takes no parameter.
var errGCStepUnavailable = errors.New("this repository call is unavailable")

type noOrphanLookup struct{ db.ContainerRepository }

func (noOrphanLookup) FindOrphanedContainers() ([]db.CreatingContainer, []db.CreatedContainer, []db.DestroyingContainer, error) {
	return nil, nil, nil, errGCStepUnavailable
}

type noFailedDestroy struct{ db.ContainerRepository }

func (noFailedDestroy) DestroyFailedContainers() (int, error) { return 0, errGCStepUnavailable }

type noMissingDelete struct{ db.ContainerRepository }

func (noMissingDelete) RemoveMissingContainers(time.Duration) (int, error) {
	return 0, errGCStepUnavailable
}

func (c ContainerGCSwept) containersInState(state string) ([]string, error) {
	return queryGCNames(c.Ready.DB, c.Ready.name,
		`SELECT handle FROM containers WHERE state = $1 ORDER BY handle`, state)
}

func (c ContainerGCSwept) containerRows() ([]string, error) {
	return queryGCNames(c.Ready.DB, c.Ready.name, `SELECT handle FROM containers ORDER BY handle`)
}

// -----------------------------------------------------------------------
// The volume collector
// -----------------------------------------------------------------------

func volumeCollectorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, VolumeGCReady](
			"a volume collector sweeping a real database",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (VolumeGCReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return VolumeGCReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "gc-volume-team"})
				if err != nil {
					return VolumeGCReady{}, fmt.Errorf("create team: %w", err)
				}
				// Running, because GetOrphanedVolumes only considers volumes on
				// a running worker — a stalled worker's volumes are still its
				// own.
				worker, err := database.PersistNamedWorker("some-worker")
				if err != nil {
					return VolumeGCReady{}, err
				}
				return VolumeGCReady{
					DB:        database,
					Collector: gc.NewVolumeCollector(database.VolumeRepository, gcGracePeriod),
					Team:      team,
					Worker:    worker,
					handle:    map[string]string{},
					name:      map[string]string{},
				}, nil
			},
		),

		brine.DefineMap[VolumeGCReady, VolumeGCReady](
			"the volume {string} is held by a container that is still around",
			func(in VolumeGCReady, p brine.Params, _ *brine.Recorder) (VolumeGCReady, error) {
				name, err := paramAt("the volume {string} is held by a container that is still around", p, 0)
				if err != nil {
					return VolumeGCReady{}, err
				}
				return in, in.volume(name, gcVolumeCreated, gcHolderStays)
			},
		),

		// The container is deleted, and the FK's ON DELETE SET NULL is what
		// detaches the volume — the collector never touches container_id. That
		// is why a volume can be orphaned at all.
		brine.DefineMap[VolumeGCReady, VolumeGCReady](
			"the volume {string} is held by a container that has since been destroyed",
			func(in VolumeGCReady, p brine.Params, _ *brine.Recorder) (VolumeGCReady, error) {
				name, err := paramAt("the volume {string} is held by a container that has since been destroyed", p, 0)
				if err != nil {
					return VolumeGCReady{}, err
				}
				return in, in.volume(name, gcVolumeCreated, gcHolderDestroyed)
			},
		),

		brine.DefineMap[VolumeGCReady, VolumeGCReady](
			"the volume {string} failed while it was being created",
			func(in VolumeGCReady, p brine.Params, _ *brine.Recorder) (VolumeGCReady, error) {
				name, err := paramAt("the volume {string} failed while it was being created", p, 0)
				if err != nil {
					return VolumeGCReady{}, err
				}
				return in, in.volume(name, gcVolumeFailed, gcHolderStays)
			},
		),

		setGCVolumeColumn("the worker stopped reporting the volume {string} an hour ago", gcAnHourAgo),
		setGCVolumeColumn("the worker stopped reporting the volume {string} a moment ago", gcJustNow),

		brine.DefineMap[VolumeGCReady, VolumeGCSwept](
			"the volume collector sweeps",
			func(in VolumeGCReady, _ brine.Params, _ *brine.Recorder) (VolumeGCSwept, error) {
				return VolumeGCSwept{Ready: in, Err: in.Collector.Run(context.Background())}, nil
			},
		),

		CheckThat[VolumeGCSwept]("the volume collector completed without error",
			func(in VolumeGCSwept) error {
				if in.Err != nil {
					return fmt.Errorf("the volume collector failed: %v", in.Err)
				}
				return nil
			}),

		CheckMember[VolumeGCSwept]("the volume {string} is now destroying",
			"the volumes now marked destroying",
			func(in VolumeGCSwept) ([]string, error) {
				return in.volumesInState(string(db.VolumeStateDestroying))
			}),

		CheckMember[VolumeGCSwept]("the volume {string} is still created",
			"the volumes still in the created state",
			func(in VolumeGCSwept) ([]string, error) {
				return in.volumesInState(string(db.VolumeStateCreated))
			}),

		CheckMember[VolumeGCSwept]("the volume {string} is still in the database",
			"the volume rows the sweep left behind",
			func(in VolumeGCSwept) ([]string, error) { return in.volumeRows() }),

		checkGCGone[VolumeGCSwept]("the volume {string} has been removed from the database",
			"a volume",
			func(in VolumeGCSwept) map[string]string { return in.Ready.handle },
			func(in VolumeGCSwept) ([]string, error) { return in.volumeRows() }),
	}
}

type gcVolumeEnd int

const (
	gcVolumeCreated gcVolumeEnd = iota
	gcVolumeFailed
)

type gcHolderFate int

const (
	gcHolderStays gcHolderFate = iota
	gcHolderDestroyed
)

// volume builds a container volume on a holder container of its own, so no two
// volumes in a scenario share a fate through their holder.
//
// A holder that stays is left in `creating`, exactly as the ginkgo suites left
// it. That is load-bearing: markOrphanedVolumesAsDestroying runs before
// RemoveMissingVolumes, and a volume moved to `destroying` no longer matches
// the created/failed filter the missing rule selects on — so a volume whose
// holder had vanished would survive a grace-period scenario for a reason that
// has nothing to do with the grace period.
func (v VolumeGCReady) volume(name string, end gcVolumeEnd, fate gcHolderFate) error {
	creating, err := v.Worker.CreateContainer(
		db.NewFixedHandleContainerOwner("holder-"+name),
		db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "some-task"},
	)
	if err != nil {
		return fmt.Errorf("create the container holding volume %q: %w", name, err)
	}

	creatingVolume, err := v.DB.VolumeRepository.CreateContainerVolume(
		v.Team.ID(), v.Worker.Name(), creating, "/vol/"+name)
	if err != nil {
		return fmt.Errorf("create volume %q: %w", name, err)
	}

	if end == gcVolumeFailed {
		failed, err := creatingVolume.Failed()
		if err != nil {
			return fmt.Errorf("mark volume %q failed: %w", name, err)
		}
		v.remember(name, failed.Handle())
		return nil
	}

	created, err := creatingVolume.Created()
	if err != nil {
		return fmt.Errorf("mark volume %q created: %w", name, err)
	}
	v.remember(name, created.Handle())

	if fate == gcHolderDestroyed {
		createdHolder, err := creating.Created()
		if err != nil {
			return fmt.Errorf("mark the holder of %q created: %w", name, err)
		}
		destroying, err := createdHolder.Destroying()
		if err != nil {
			return fmt.Errorf("mark the holder of %q destroying: %w", name, err)
		}
		destroyed, err := destroying.Destroy()
		if err != nil {
			return fmt.Errorf("destroy the holder of %q: %w", name, err)
		}
		if !destroyed {
			return fmt.Errorf("the container holding volume %q was already gone", name)
		}
	}
	return nil
}

func setGCVolumeColumn(pattern, value string) brine.StepDefinition {
	return brine.DefineMap[VolumeGCReady, VolumeGCReady](pattern,
		func(in VolumeGCReady, p brine.Params, _ *brine.Recorder) (VolumeGCReady, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return VolumeGCReady{}, err
			}
			handle, ok := in.handle[name]
			if !ok {
				return VolumeGCReady{}, fmt.Errorf(
					"no volume named %q was created by this scenario, so missing_since cannot be set on it", name)
			}
			res, err := in.DB.Conn.Exec(
				fmt.Sprintf(`UPDATE volumes SET missing_since = %s WHERE handle = $1`, value), handle)
			if err != nil {
				return VolumeGCReady{}, fmt.Errorf("set missing_since on volume %q: %w", name, err)
			}
			return in, mustHaveTouchedOneGCRow(res, "missing_since", name)
		},
	)
}

func (v VolumeGCSwept) volumesInState(state string) ([]string, error) {
	return queryGCNames(v.Ready.DB, v.Ready.name,
		`SELECT handle FROM volumes WHERE state = $1 ORDER BY handle`, state)
}

func (v VolumeGCSwept) volumeRows() ([]string, error) {
	return queryGCNames(v.Ready.DB, v.Ready.name, `SELECT handle FROM volumes ORDER BY handle`)
}

// -----------------------------------------------------------------------
// Shared
// -----------------------------------------------------------------------

func (c ContainerGCReady) remember(name, handle string) {
	c.handle[name] = handle
	c.name[handle] = name
}

func (v VolumeGCReady) remember(name, handle string) {
	v.handle[name] = handle
	v.name[handle] = name
}

// mustHaveTouchedOneGCRow turns a fixture that quietly wrote nothing into a
// failure at the step that wrote it. Without this, a backdated column that
// never landed shows up much later as an outcome assertion failing for a
// reason the message cannot explain.
func mustHaveTouchedOneGCRow(res interface{ RowsAffected() (int64, error) }, column, name string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count the rows %s on %q touched: %w", column, name, err)
	}
	if affected != 1 {
		return fmt.Errorf("setting %s on %q updated %d rows, expected exactly 1", column, name, affected)
	}
	return nil
}

// queryGCNames reads handles out of the scenario's own database and reports them
// by the names the scenario gave them. A handle nothing named is shown raw
// rather than dropped: an unexplained row is exactly what a reader needs.
func queryGCNames(database JetbridgeDB, names map[string]string, query string, args ...any) ([]string, error) {
	rows, err := database.Conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("read the rows the sweep left behind: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var handle string
		if err := rows.Scan(&handle); err != nil {
			return nil, fmt.Errorf("read a handle: %w", err)
		}
		out = append(out, translate(handle, names))
	}
	return out, rows.Err()
}

// checkGCGone backs the two "has been removed from the database" sentences.
//
// An absence passes on an empty table, and it also passes on a typo. Both are
// the same hole: the assertion says nothing unless the row was really there
// first. Every scenario using this has a surviving sibling asserted beside it,
// which covers the empty table; the guard here covers the typo, and costs one
// map lookup to turn a scenario that silently proves nothing into a failure
// that names why.
func checkGCGone[T any](
	pattern, subject string,
	created func(T) map[string]string,
	remaining func(T) ([]string, error),
) brine.StepDefinition {
	return brine.DefineCheck[T](pattern, func(in T, p brine.Params, _ *brine.Recorder) error {
		name, err := paramAt(pattern, p, 0)
		if err != nil {
			return err
		}
		if _, ok := created(in)[name]; !ok {
			return fmt.Errorf("this scenario never created %s named %q, so its absence proves nothing", subject, name)
		}
		rows, err := remaining(in)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row == name {
				return fmt.Errorf("expected %s %q to have been deleted, but the row is still there: %v",
					subject, name, rows)
			}
		}
		return nil
	})
}
