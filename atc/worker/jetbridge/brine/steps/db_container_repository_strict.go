package steps

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBContainerRepositoryObservation struct{ Value string }

func DBContainerRepositoryStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBContainerRepositoryObservation](
			"the production container repository evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBContainerRepositoryObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBContainerRepositoryObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeDBContainerRepository(database, profile)
				return DBContainerRepositoryObservation{Value: value}, err
			},
		),
		brine.DefineCheck[DBContainerRepositoryObservation](
			"the container repository observation is {string}",
			func(in DBContainerRepositoryObservation, p brine.Params, _ *brine.Recorder) error {
				want, _ := p.GetString(0)
				if in.Value != want {
					return fmt.Errorf("expected container repository observation %q, got %q", want, in.Value)
				}
				return nil
			},
		),
	}
}

func observeDBContainerRepository(database JetbridgeDB, profile string) (string, error) {
	switch {
	case profile == "failed-count":
		return observeDestroyFailed(database)
	case profile == "destroying-list":
		return observeFindDestroying(database)
	case strings.HasPrefix(profile, "missing-"):
		return observeRemoveMissing(database, profile)
	case strings.HasPrefix(profile, "remove-"):
		return observeRemoveDestroying(database, profile)
	case strings.HasPrefix(profile, "update-"):
		return observeUpdateMissing(database, profile)
	case strings.HasPrefix(profile, "unknown-"):
		return observeDestroyUnknown(database, profile)
	case strings.HasPrefix(profile, "excess-"):
		return observeDestroyExcess(database, profile)
	default:
		return "", fmt.Errorf("unknown container repository profile %q", profile)
	}
}

func persistContainerWorker(database JetbridgeDB, name string, state db.WorkerState) error {
	_, err := database.WorkerFactory.SaveWorker(atc.Worker{Name: name, Platform: "linux", State: string(state), ResourceTypes: []atc.WorkerResourceType{{Type: "some-base-resource-type", Image: "example/base", Version: "v1"}}}, 0)
	return err
}

func insertContainerRow(database JetbridgeDB, handle string, state string, worker string, missing any) error {
	_, err := database.Conn.Exec(`INSERT INTO containers (handle, state, worker_name, missing_since) VALUES ($1, $2, $3, $4)`, handle, state, worker, missing)
	return err
}

func containerExists(database JetbridgeDB, handle string) (bool, error) {
	var count int
	err := database.Conn.QueryRow(`SELECT count(*) FROM containers WHERE handle = $1`, handle).Scan(&count)
	return count == 1, err
}

func containerMissing(database JetbridgeDB, handle string) (bool, error) {
	var value sql.NullTime
	err := database.Conn.QueryRow(`SELECT missing_since FROM containers WHERE handle = $1`, handle).Scan(&value)
	return value.Valid, err
}

func containerState(database JetbridgeDB, handle string) (string, error) {
	var state string
	err := database.Conn.QueryRow(`SELECT state FROM containers WHERE handle = $1`, handle).Scan(&state)
	return state, err
}

func observeDestroyFailed(database JetbridgeDB) (string, error) {
	if err := persistContainerWorker(database, "repo-worker", db.WorkerStateRunning); err != nil {
		return "", err
	}
	if err := insertContainerRow(database, "failed-1", atc.ContainerStateFailed, "repo-worker", nil); err != nil {
		return "", err
	}
	affected, err := database.ContainerRepository.DestroyFailedContainers()
	var failed, destroying int
	if scanErr := database.Conn.QueryRow(`SELECT count(*) FILTER (WHERE state = 'failed'), count(*) FILTER (WHERE state = 'destroying') FROM containers`).Scan(&failed, &destroying); scanErr != nil {
		return "", scanErr
	}
	return fmt.Sprintf("affected=%d;failed=%d;destroying=%d;error=%t", affected, failed, destroying, err != nil), nil
}

func observeFindDestroying(database JetbridgeDB) (string, error) {
	if err := persistContainerWorker(database, "repo-worker", db.WorkerStateRunning); err != nil {
		return "", err
	}
	if err := insertContainerRow(database, "destroying-1", atc.ContainerStateDestroying, "repo-worker", nil); err != nil {
		return "", err
	}
	if err := insertContainerRow(database, "created-1", atc.ContainerStateCreated, "repo-worker", nil); err != nil {
		return "", err
	}
	handles, err := database.ContainerRepository.FindDestroyingContainers("repo-worker")
	sort.Strings(handles)
	return fmt.Sprintf("handles=%s;error=%t", strings.Join(handles, ","), err != nil), nil
}

func seedMissingContainers(database JetbridgeDB) error {
	if err := persistContainerWorker(database, "running-worker", db.WorkerStateRunning); err != nil {
		return err
	}
	rows := []struct {
		handle  string
		state   string
		missing any
	}{
		{"created-1", atc.ContainerStateCreated, nil},
		{"created-2", atc.ContainerStateCreated, time.Now().Add(-5 * time.Minute)},
		{"failed-3", atc.ContainerStateFailed, time.Now().Add(-5 * time.Minute)},
		{"destroying-4", atc.ContainerStateDestroying, time.Now().Add(-10 * time.Minute)},
	}
	for _, row := range rows {
		if err := insertContainerRow(database, row.handle, row.state, "running-worker", row.missing); err != nil {
			return err
		}
	}
	return nil
}

func observeRemoveMissing(database JetbridgeDB, profile string) (string, error) {
	if err := seedMissingContainers(database); err != nil {
		return "", err
	}
	grace := 3 * time.Minute
	if profile == "missing-none" {
		grace = 7 * time.Minute
	}
	if profile == "missing-stalled-preserved" || profile == "missing-running-count" {
		if err := persistContainerWorker(database, "stalled-worker", db.WorkerStateStalled); err != nil {
			return "", err
		}
		if err := insertContainerRow(database, "stalled-5", atc.ContainerStateCreated, "stalled-worker", time.Now().Add(-10*time.Minute)); err != nil {
			return "", err
		}
		if _, err := database.Conn.Exec(`UPDATE containers SET worker_name = 'stalled-worker' WHERE handle = 'failed-3'`); err != nil {
			return "", err
		}
	}
	affected, err := database.ContainerRepository.RemoveMissingContainers(grace)
	switch profile {
	case "missing-none":
		var count int
		scanErr := database.Conn.QueryRow(`SELECT count(*) FROM containers`).Scan(&count)
		if scanErr != nil {
			return "", scanErr
		}
		return fmt.Sprintf("affected=%d;remaining=%d;error=%t", affected, count, err != nil), nil
	case "missing-expired":
		expired, e1 := containerExists(database, "created-2")
		other, e2 := containerExists(database, "failed-3")
		if e1 != nil {
			return "", e1
		}
		if e2 != nil {
			return "", e2
		}
		return fmt.Sprintf("affected=%d;expired=%t;other=%t;error=%t", affected, expired, other, err != nil), nil
	case "missing-running-count":
		return fmt.Sprintf("affected=%d;error=%t", affected, err != nil), nil
	case "missing-stalled-preserved":
		running, e1 := containerExists(database, "created-2")
		stalled, e2 := containerExists(database, "stalled-5")
		if e1 != nil {
			return "", e1
		}
		if e2 != nil {
			return "", e2
		}
		return fmt.Sprintf("affected=%d;running=%t;stalled=%t;error=%t", affected, running, stalled, err != nil), nil
	}
	return "", fmt.Errorf("unknown missing profile %q", profile)
}

func observeRemoveDestroying(database JetbridgeDB, profile string) (string, error) {
	if err := persistContainerWorker(database, "repo-worker", db.WorkerStateRunning); err != nil {
		return "", err
	}
	handles := []string{"ignore-1", "ignore-2"}
	target := "target"
	switch profile {
	case "remove-empty-gone", "remove-empty-count":
		handles = []string{}
		if err := insertContainerRow(database, target, atc.ContainerStateDestroying, "repo-worker", nil); err != nil {
			return "", err
		}
	case "remove-creating-stays", "remove-creating-count":
		if err := insertContainerRow(database, target, atc.ContainerStateCreating, "repo-worker", nil); err != nil {
			return "", err
		}
	case "remove-ignored-stay", "remove-ignored-count":
		for _, handle := range handles {
			if err := insertContainerRow(database, handle, atc.ContainerStateDestroying, "repo-worker", nil); err != nil {
				return "", err
			}
		}
	default:
		if err := insertContainerRow(database, target, atc.ContainerStateDestroying, "repo-worker", nil); err != nil {
			return "", err
		}
	}
	affected, err := database.ContainerRepository.RemoveDestroyingContainers("repo-worker", handles)
	switch profile {
	case "remove-destroying-gone", "remove-empty-gone", "remove-creating-stays":
		exists, scanErr := containerExists(database, target)
		if scanErr != nil {
			return "", scanErr
		}
		return fmt.Sprintf("affected=%d;exists=%t;error=%t", affected, exists, err != nil), nil
	case "remove-ignored-stay":
		var count int
		scanErr := database.Conn.QueryRow(`SELECT count(*) FROM containers`).Scan(&count)
		if scanErr != nil {
			return "", scanErr
		}
		return fmt.Sprintf("affected=%d;remaining=%d;error=%t", affected, count, err != nil), nil
	default:
		return fmt.Sprintf("affected=%d;error=%t", affected, err != nil), nil
	}
}

func seedUpdateMissing(database JetbridgeDB) error {
	if err := persistContainerWorker(database, "repo-worker", db.WorkerStateRunning); err != nil {
		return err
	}
	if err := insertContainerRow(database, "h1", atc.ContainerStateDestroying, "repo-worker", nil); err != nil {
		return err
	}
	if err := insertContainerRow(database, "h2", atc.ContainerStateDestroying, "repo-worker", nil); err != nil {
		return err
	}
	return insertContainerRow(database, "h3", atc.ContainerStateCreated, "repo-worker", time.Date(2018, 9, 24, 0, 0, 0, 0, time.UTC))
}

func observeUpdateMissing(database JetbridgeDB, profile string) (string, error) {
	if err := seedUpdateMissing(database); err != nil {
		return "", err
	}
	handles := []string{"h1"}
	if profile == "update-creating-unmarked" {
		if _, err := database.Conn.Exec(`UPDATE containers SET state = 'creating', missing_since = NULL WHERE handle = 'h3'`); err != nil {
			return "", err
		}
	}
	if profile == "update-full-unchanged" {
		handles = []string{"h1", "h2"}
	}
	if profile == "update-reported-clears" {
		handles = []string{"h1", "h2", "h3"}
	}
	err := database.ContainerRepository.UpdateContainersMissingSince("repo-worker", handles)
	h1, e := containerMissing(database, "h1")
	if e != nil {
		return "", e
	}
	h2, e := containerMissing(database, "h2")
	if e != nil {
		return "", e
	}
	h3, e := containerMissing(database, "h3")
	if e != nil {
		return "", e
	}
	switch profile {
	case "update-creating-unmarked":
		return fmt.Sprintf("h3-missing=%t;error=%t", h3, err != nil), nil
	case "update-subset-marks":
		return fmt.Sprintf("h1=%t;h2=%t;h3=%t;error=%t", h1, h2, h3, err != nil), nil
	case "update-full-unchanged":
		return fmt.Sprintf("h1=%t;h2=%t;error=%t", h1, h2, err != nil), nil
	default:
		return fmt.Sprintf("h1=%t;h2=%t;h3=%t;error=%t", h1, h2, h3, err != nil), nil
	}
}

func observeDestroyUnknown(database JetbridgeDB, profile string) (string, error) {
	if err := persistContainerWorker(database, "repo-worker", db.WorkerStateRunning); err != nil {
		return "", err
	}
	if err := insertContainerRow(database, "h1", atc.ContainerStateDestroying, "repo-worker", nil); err != nil {
		return "", err
	}
	if err := insertContainerRow(database, "h2", atc.ContainerStateCreated, "repo-worker", nil); err != nil {
		return "", err
	}
	reported := []string{"h3", "h4"}
	if profile == "unknown-noop" {
		reported = []string{"h1", "h2"}
	}
	affected, err := database.ContainerRepository.DestroyUnknownContainers("repo-worker", reported)
	var destroying int
	if scanErr := database.Conn.QueryRow(`SELECT count(*) FROM containers WHERE state = 'destroying'`).Scan(&destroying); scanErr != nil {
		return "", scanErr
	}
	return fmt.Sprintf("affected=%d;destroying=%d;error=%t", affected, destroying, err != nil), nil
}

func checkOwner(database JetbridgeDB, source string) (db.ContainerOwner, error) {
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"source": source}, nil)
	if err != nil {
		return nil, err
	}
	return db.NewResourceConfigCheckSessionContainerOwner(config.ID(), config.OriginBaseResourceType().ID, db.ContainerOwnerExpiries{Min: time.Minute, Max: time.Hour}), nil
}

func createChecks(database JetbridgeDB, worker db.Worker, owner db.ContainerOwner, count int) ([]string, error) {
	handles := make([]string, 0, count)
	for i := 0; i < count; i++ {
		creating, err := worker.CreateContainer(owner, db.ContainerMetadata{Type: db.ContainerTypeCheck})
		if err != nil {
			return nil, err
		}
		if _, err := creating.Created(); err != nil {
			return nil, err
		}
		handles = append(handles, creating.Handle())
	}
	return handles, nil
}

func statesFor(database JetbridgeDB, handles []string) (string, error) {
	states := make([]string, 0, len(handles))
	for _, handle := range handles {
		state, err := containerState(database, handle)
		if err != nil {
			return "", err
		}
		states = append(states, state)
	}
	return strings.Join(states, ","), nil
}

func observeDestroyExcess(database JetbridgeDB, profile string) (string, error) {
	if err := persistContainerWorker(database, "repo-worker", db.WorkerStateRunning); err != nil {
		return "", err
	}
	worker, found, err := database.WorkerFactory.GetWorker("repo-worker")
	if err != nil || !found {
		return "", fmt.Errorf("load repo worker: found=%t: %w", found, err)
	}
	ownerA, err := checkOwner(database, "a")
	if err != nil {
		return "", err
	}
	count := 4
	if profile == "excess-small" {
		count = 2
	} else if profile == "excess-partition" {
		count = 3
	}
	handlesA, err := createChecks(database, worker, ownerA, count)
	if err != nil {
		return "", err
	}
	if profile == "excess-hijack" {
		if _, err := database.Conn.Exec(`UPDATE containers SET last_hijack = NOW() WHERE handle = $1`, handlesA[0]); err != nil {
			return "", err
		}
	}
	var handlesB []string
	if profile == "excess-partition" {
		ownerB, err := checkOwner(database, "b")
		if err != nil {
			return "", err
		}
		handlesB, err = createChecks(database, worker, ownerB, 3)
		if err != nil {
			return "", err
		}
	}
	affected, callErr := database.ContainerRepository.DestroyExcessCheckContainers(2, 5*time.Minute)
	statesA, err := statesFor(database, handlesA)
	if err != nil {
		return "", err
	}
	if profile == "excess-partition" {
		statesB, err := statesFor(database, handlesB)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("affected=%d;a=%s;b=%s;error=%t", affected, statesA, statesB, callErr != nil), nil
	}
	return fmt.Sprintf("affected=%d;states=%s;error=%t", affected, statesA, callErr != nil), nil
}
