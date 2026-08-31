package steps

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
)

// GCReclamationDefinitions migrates atc/gc/destroyer_test.go and
// atc/gc/worker_collector_test.go — the two halves of "what does a sweep
// leave behind".
//
// There is no double anywhere in this file. The destroyer runs against the
// scenario's own PostgreSQL through the same db.ContainerRepository and
// db.VolumeRepository the ATC wires in production, and the failure scenarios
// use a connection that has been closed rather than a repository decorated to
// return a sentinel. That is not fastidiousness: the two decorators in the
// ginkgo suite (failRemoveDestroyingContainers and friends) each returned
// errors.New("I am le tired"), and a destroyer that returned an error of its
// own invention instead of the database's would have passed them. A closed
// connection makes the error the database's, so "was refused, saying closed"
// is a statement about passthrough and not about a string the test supplied.
//
// Every assertion here reads a table. Containers are created through
// db.NewFixedHandleContainerOwner so a row's handle is the name the scenario
// gives it; volume handles are generated, so the state carries the mapping
// both ways and the checks report the scenario's names rather than UUIDs a
// reader cannot place.

// GCReady is the destroyer and the rows it will sweep, before it runs.
type GCReady struct {
	DB        JetbridgeDB
	Destroyer gc.Destroyer
	Worker    db.Worker
	Second    db.Worker
	Team      db.Team

	// volumeHandle maps a scenario's name for a volume to the handle its row
	// actually got; volumeName is the reverse, so a table read can be reported
	// in the scenario's own vocabulary.
	volumeHandle map[string]string
	volumeName   map[string]string
}

// GCSwept is what a sweep left behind: the errors it reported, and — for the
// read half of the destroyer's surface — the answer it gave.
type GCSwept struct {
	Ready        GCReady
	ContainerErr error
	VolumeErr    error

	Offered  []string
	OfferErr error
	Asked    bool
}

// WorkerGCReady is the worker collector and the workers registered for it.
type WorkerGCReady struct {
	DB        JetbridgeDB
	Collector interface{ Run(context.Context) error }
	Ctx       context.Context
}

// WorkerGCSwept is a completed collector pass.
type WorkerGCSwept struct {
	Ready WorkerGCReady
	Err   error
}

func GCReclamationDefinitions() []brine.StepDefinition {
	return append(destroyerDefinitions(), workerCollectorDefinitions()...)
}

// -----------------------------------------------------------------------
// The destroyer
// -----------------------------------------------------------------------

func destroyerDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, GCReady](
			"a destroyer sweeping a worker's containers and volumes",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (GCReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return GCReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}

				team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "gc-reclamation-team"})
				if err != nil {
					return GCReady{}, fmt.Errorf("create team: %w", err)
				}
				worker, err := database.PersistNamedWorker("gc-worker")
				if err != nil {
					return GCReady{}, err
				}

				return GCReady{
					DB:           database,
					Destroyer:    gc.NewDestroyer(lagertest.NewTestLogger("destroyer"), database.ContainerRepository, database.VolumeRepository),
					Worker:       worker,
					Team:         team,
					volumeHandle: map[string]string{},
					volumeName:   map[string]string{},
				}, nil
			},
		),

		brine.DefineMap[GCReady, GCReady](
			"the container {string} on that worker is being destroyed",
			func(in GCReady, p brine.Params, _ *brine.Recorder) (GCReady, error) {
				handle, err := paramAt("the container {string} on that worker is being destroyed", p, 0)
				if err != nil {
					return GCReady{}, err
				}
				return in, in.destroyingContainer(in.Worker, handle)
			},
		),

		brine.DefineMap[GCReady, GCReady](
			"the container {string} on a second worker is being destroyed",
			func(in GCReady, p brine.Params, _ *brine.Recorder) (GCReady, error) {
				handle, err := paramAt("the container {string} on a second worker is being destroyed", p, 0)
				if err != nil {
					return GCReady{}, err
				}
				second, err := in.secondWorker()
				if err != nil {
					return GCReady{}, err
				}
				in.Second = second
				return in, in.destroyingContainer(second, handle)
			},
		),

		brine.DefineMap[GCReady, GCReady](
			"the volume {string} on that worker is being destroyed",
			func(in GCReady, p brine.Params, _ *brine.Recorder) (GCReady, error) {
				name, err := paramAt("the volume {string} on that worker is being destroyed", p, 0)
				if err != nil {
					return GCReady{}, err
				}
				return in, in.volume(in.Worker, name, true)
			},
		),

		// The discriminator in the "which volumes are waiting" scenario: a
		// created volume is one a step may still be reading from, and offering
		// it for reclamation would have the reaper delete live data off the
		// node.
		brine.DefineMap[GCReady, GCReady](
			"the volume {string} on that worker is still in use",
			func(in GCReady, p brine.Params, _ *brine.Recorder) (GCReady, error) {
				name, err := paramAt("the volume {string} on that worker is still in use", p, 0)
				if err != nil {
					return GCReady{}, err
				}
				return in, in.volume(in.Worker, name, false)
			},
		),

		brine.DefineMap[GCReady, GCReady](
			"the volume {string} on a second worker is being destroyed",
			func(in GCReady, p brine.Params, _ *brine.Recorder) (GCReady, error) {
				name, err := paramAt("the volume {string} on a second worker is being destroyed", p, 0)
				if err != nil {
					return GCReady{}, err
				}
				second, err := in.secondWorker()
				if err != nil {
					return GCReady{}, err
				}
				in.Second = second
				return in, in.volume(second, name, true)
			},
		),

		// The destroyer keeps its repositories over a connection that has been
		// closed. The scenario's own connection is untouched, so the checks
		// that follow can still read the tables and show the rows survived.
		brine.DefineMap[GCReady, GCReady](
			"the database behind the destroyer has gone away",
			func(in GCReady, _ brine.Params, _ *brine.Recorder) (GCReady, error) {
				closed, err := in.DB.ClosedConn()
				if err != nil {
					return GCReady{}, err
				}
				in.Destroyer = gc.NewDestroyer(
					lagertest.NewTestLogger("destroyer"),
					db.NewContainerRepository(closed),
					db.NewVolumeRepository(closed),
				)
				return in, nil
			},
		),

		// The four sweeps. Each one is a single transition, and each performs
		// BOTH halves of the destroyer's write surface, because the rule under
		// test is the same rule for containers and for volumes and the two
		// functions implementing it are copies of each other. A scenario that
		// swept only one would leave the other free to drift.
		brine.DefineMap[GCReady, GCSwept](
			"the worker reports that it still holds the container {string} and the volume {string}",
			func(in GCReady, p brine.Params, _ *brine.Recorder) (GCSwept, error) {
				pattern := "the worker reports that it still holds the container {string} and the volume {string}"
				containerHandle, volumeName, err := twoParams(pattern, p)
				if err != nil {
					return GCSwept{}, err
				}
				volumeHandle, ok := in.volumeHandle[volumeName]
				if !ok {
					return GCSwept{}, fmt.Errorf("no volume named %q was created by this scenario", volumeName)
				}
				return in.sweep([]string{containerHandle}, []string{volumeHandle}, in.Worker.Name()), nil
			},
		),

		brine.DefineMap[GCReady, GCSwept](
			"the worker reports that it holds nothing at all",
			func(in GCReady, _ brine.Params, _ *brine.Recorder) (GCSwept, error) {
				return in.sweep([]string{}, []string{}, in.Worker.Name()), nil
			},
		),

		// nil, not empty. The whole content of this step is that difference.
		brine.DefineMap[GCReady, GCSwept](
			"no report from the worker ever arrives",
			func(in GCReady, _ brine.Params, _ *brine.Recorder) (GCSwept, error) {
				return in.sweep(nil, nil, in.Worker.Name()), nil
			},
		),

		brine.DefineMap[GCReady, GCSwept](
			"the destroyer is asked to reclaim for a worker with no name",
			func(in GCReady, _ brine.Params, _ *brine.Recorder) (GCSwept, error) {
				return in.sweep([]string{}, []string{}, ""), nil
			},
		),

		brine.DefineMap[GCReady, GCSwept](
			"the destroyer is asked which volumes are waiting to be reclaimed",
			func(in GCReady, _ brine.Params, _ *brine.Recorder) (GCSwept, error) {
				offered, err := in.Destroyer.FindDestroyingVolumesForGc(in.Worker.Name())
				return GCSwept{Ready: in, Offered: offered, OfferErr: err, Asked: true}, nil
			},
		),

		CheckThat[GCSwept]("the sweep completed without error",
			func(in GCSwept) error {
				if in.ContainerErr != nil {
					return fmt.Errorf("reclaiming containers failed: %v", in.ContainerErr)
				}
				if in.VolumeErr != nil {
					return fmt.Errorf("reclaiming volumes failed: %v", in.VolumeErr)
				}
				return nil
			}),

		// A refusal is an outcome: the reaper branches on it, and a sweep that
		// reported success would have it move on believing the rows are gone.
		// So the getter reports a missing error rather than comparing against
		// an empty string, which would read as an ordinary text mismatch.
		CheckContains[GCSwept]("reclaiming the containers was refused, saying {string}",
			"the refusal",
			func(in GCSwept) (string, error) { return refusal(in.ContainerErr, "reclaiming the containers") }),

		CheckContains[GCSwept]("reclaiming the volumes was refused, saying {string}",
			"the refusal",
			func(in GCSwept) (string, error) { return refusal(in.VolumeErr, "reclaiming the volumes") }),

		CheckContains[GCSwept]("asking which volumes are waiting was refused, saying {string}",
			"the refusal",
			func(in GCSwept) (string, error) {
				return refusal(in.OfferErr, "asking which volumes are waiting")
			}),

		// What survived is membership in the rows the database still holds,
		// and a surprise about one row is nearly always diagnosed by the
		// others beside it — which is why these list the whole table rather
		// than answering yes or no about one handle.
		CheckMember[GCSwept]("the container {string} survived the sweep",
			"the container rows still in the database",
			func(in GCSwept) ([]string, error) { return in.containerHandles() }),

		CheckNotMember[GCSwept]("the container {string} has been reclaimed",
			"the container rows still in the database",
			func(in GCSwept) ([]string, error) { return in.containerHandles() }),

		CheckMember[GCSwept]("the volume {string} survived the sweep",
			"the volume rows still in the database",
			func(in GCSwept) ([]string, error) { return in.volumeNames() }),

		CheckNotMember[GCSwept]("the volume {string} has been reclaimed",
			"the volume rows still in the database",
			func(in GCSwept) ([]string, error) { return in.volumeNames() }),

		CheckCount[GCSwept]("{int} volumes are waiting to be reclaimed",
			"volumes offered for reclamation",
			func(in GCSwept) ([]string, error) { return in.offeredNames() }),

		CheckMember[GCSwept]("the volume {string} is waiting to be reclaimed",
			"the volumes offered for reclamation",
			func(in GCSwept) ([]string, error) { return in.offeredNames() }),

		CheckNotMember[GCSwept]("the volume {string} is not waiting to be reclaimed",
			"the volumes offered for reclamation",
			func(in GCSwept) ([]string, error) { return in.offeredNames() }),
	}
}

// sweep performs both halves of a destroyer pass and records what each said.
// Neither error fails the step: whether the sweep was refused is what the
// scenario goes on to assert.
func (g GCReady) sweep(containerHandles, volumeHandles []string, workerName string) GCSwept {
	return GCSwept{
		Ready:        g,
		ContainerErr: g.Destroyer.DestroyContainers(workerName, containerHandles),
		VolumeErr:    g.Destroyer.DestroyVolumes(workerName, volumeHandles),
	}
}

func refusal(err error, what string) (string, error) {
	if err == nil {
		return "", fmt.Errorf("expected %s to be refused, it reported success — "+
			"a caller told a failed sweep succeeded moves on believing the rows are gone", what)
	}
	return err.Error(), nil
}

func (g GCReady) secondWorker() (db.Worker, error) {
	if g.Second != nil {
		return g.Second, nil
	}
	return g.DB.PersistNamedWorker("gc-second-worker")
}

// destroyingContainer creates a container and walks it to destroying, the
// state a sweep is entitled to reclaim. The handle is fixed to the scenario's
// name so the feature file and the table agree.
func (g GCReady) destroyingContainer(worker db.Worker, handle string) error {
	creating, err := worker.CreateContainer(
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "some-task"},
	)
	if err != nil {
		return fmt.Errorf("create container %q: %w", handle, err)
	}
	created, err := creating.Created()
	if err != nil {
		return fmt.Errorf("mark container %q created: %w", handle, err)
	}
	if _, err := created.Destroying(); err != nil {
		return fmt.Errorf("mark container %q destroying: %w", handle, err)
	}
	return nil
}

// volume creates a container volume and leaves it either created or
// destroying.
//
// The container holding it stays in `creating`, exactly as the ginkgo suite
// left it. That is load-bearing rather than incidental: a sweep only reclaims
// rows in the destroying state, so a holder left creating cannot be swept out
// from under its volume and confuse a scenario about which rule reclaimed
// what.
func (g GCReady) volume(worker db.Worker, name string, destroying bool) error {
	creating, err := worker.CreateContainer(
		db.NewFixedHandleContainerOwner("holder-"+name),
		db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "some-task"},
	)
	if err != nil {
		return fmt.Errorf("create the container holding volume %q: %w", name, err)
	}

	creatingVolume, err := g.DB.VolumeRepository.CreateContainerVolume(
		g.Team.ID(), worker.Name(), creating, "/vol/"+name)
	if err != nil {
		return fmt.Errorf("create volume %q: %w", name, err)
	}
	createdVolume, err := creatingVolume.Created()
	if err != nil {
		return fmt.Errorf("mark volume %q created: %w", name, err)
	}

	handle := createdVolume.Handle()
	if destroying {
		destroyingVolume, err := createdVolume.Destroying()
		if err != nil {
			return fmt.Errorf("mark volume %q destroying: %w", name, err)
		}
		handle = destroyingVolume.Handle()
	}

	g.volumeHandle[name] = handle
	g.volumeName[handle] = name
	return nil
}

// containerHandles is every container row the scenario's database still holds.
// The database is scenario-scoped, so this is this scenario's rows and nothing
// else — including the second worker's, which is the point in the scenario
// that has one.
func (g GCSwept) containerHandles() ([]string, error) {
	return g.handles(`SELECT handle FROM containers ORDER BY handle`, nil)
}

// volumeNames is every volume row still held, reported by the name the
// scenario gave it. A handle nothing named is shown raw rather than dropped:
// an unexplained row is exactly what a reader needs to see.
func (g GCSwept) volumeNames() ([]string, error) {
	return g.handles(`SELECT handle FROM volumes ORDER BY handle`, g.Ready.volumeName)
}

func (g GCSwept) offeredNames() ([]string, error) {
	if !g.Asked {
		return nil, fmt.Errorf("nothing asked the destroyer which volumes are waiting, " +
			"so there is no answer to check")
	}
	names := make([]string, 0, len(g.Offered))
	for _, handle := range g.Offered {
		names = append(names, translate(handle, g.Ready.volumeName))
	}
	return names, nil
}

func (g GCSwept) handles(query string, names map[string]string) ([]string, error) {
	rows, err := g.Ready.DB.Conn.Query(query)
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

func translate(handle string, names map[string]string) string {
	if name, ok := names[handle]; ok {
		return name
	}
	return handle
}

// -----------------------------------------------------------------------
// The worker collector
// -----------------------------------------------------------------------

func workerCollectorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, WorkerGCReady](
			"a collector for workers that have stopped heartbeating",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (WorkerGCReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return WorkerGCReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				return WorkerGCReady{
					DB:        database,
					Collector: gc.NewWorkerCollector(db.NewWorkerLifecycle(database.Conn)),
					Ctx:       context.Background(),
				}, nil
			},
		),

		// A negative lease backdates `expires`, so a worker can be
		// unresponsive without the scenario waiting to become so. That is the
		// shape DeleteUnresponsiveEphemeralWorkers selects on:
		// `WHERE ephemeral AND expires < NOW()`.
		registerWorker("the worker {string} is ephemeral and has stopped heartbeating", true, -time.Minute),
		registerWorker("the worker {string} is ephemeral and still heartbeating", true, 5*time.Minute),
		registerWorker("the worker {string} is persistent and has stopped heartbeating", false, -time.Minute),
		// The bystander. Each outline row is its own database, so the survivors
		// cannot vouch for the reclaimed one — "has been reclaimed" is an
		// absence, and an absence passes on an empty table. Registering a
		// worker nobody expects the sweep to touch means a fixture that stopped
		// inserting fails here rather than passing on the vacuum.
		registerWorker("the worker {string} is persistent and still heartbeating", false, 5*time.Minute),

		// The workers stay registered over the scenario's own connection; only
		// the collector loses its database. So a survivor below means the
		// sweep reclaimed nothing, not that the reader could not see it.
		brine.DefineMap[WorkerGCReady, WorkerGCReady](
			"the database the collector reads has gone away",
			func(in WorkerGCReady, _ brine.Params, _ *brine.Recorder) (WorkerGCReady, error) {
				closed, err := in.DB.ClosedConn()
				if err != nil {
					return WorkerGCReady{}, err
				}
				in.Collector = gc.NewWorkerCollector(db.NewWorkerLifecycle(closed))
				return in, nil
			},
		),

		brine.DefineMap[WorkerGCReady, WorkerGCSwept](
			"the collector sweeps for unresponsive workers",
			func(in WorkerGCReady, _ brine.Params, _ *brine.Recorder) (WorkerGCSwept, error) {
				return WorkerGCSwept{Ready: in, Err: in.Collector.Run(in.Ctx)}, nil
			},
		),

		CheckThat[WorkerGCSwept]("the collector completed without error",
			func(in WorkerGCSwept) error {
				if in.Err != nil {
					return fmt.Errorf("the collector failed: %v", in.Err)
				}
				return nil
			}),

		CheckContains[WorkerGCSwept]("the collector's sweep was refused, saying {string}",
			"the refusal",
			func(in WorkerGCSwept) (string, error) { return refusal(in.Err, "the collector's sweep") }),

		CheckMember[WorkerGCSwept]("the worker {string} is still registered",
			"the workers still registered",
			func(in WorkerGCSwept) ([]string, error) { return in.registeredWorkers() }),

		CheckNotMember[WorkerGCSwept]("the worker {string} has been reclaimed",
			"the workers still registered",
			func(in WorkerGCSwept) ([]string, error) { return in.registeredWorkers() }),
	}
}

// registerWorker backs the three worker states the outline varies over. They
// stay three sentences rather than one parameterised one because each names a
// distinct reason a row is or is not garbage, and a reader of the Examples
// table should be able to see the reason without decoding two columns.
func registerWorker(pattern string, ephemeral bool, ttl time.Duration) brine.StepDefinition {
	return brine.DefineMap[WorkerGCReady, WorkerGCReady](pattern,
		func(in WorkerGCReady, p brine.Params, _ *brine.Recorder) (WorkerGCReady, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return WorkerGCReady{}, err
			}
			_, err = in.DB.WorkerFactory.SaveWorker(atc.Worker{
				Name:             name,
				Ephemeral:        ephemeral,
				Platform:         "some-platform",
				ActiveContainers: 1,
				StartTime:        55,
			}, ttl)
			if err != nil {
				return WorkerGCReady{}, fmt.Errorf("register worker %q: %w", name, err)
			}
			return in, nil
		},
	)
}

func (w WorkerGCSwept) registeredWorkers() ([]string, error) {
	rows, err := w.Ready.DB.Conn.Query(`SELECT name FROM workers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read the workers the sweep left behind: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read a worker name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
