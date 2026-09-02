package steps

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBContainerRepositoryFinalObservation struct {
	Value string
}

func DBContainerRepositoryFinalStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBContainerRepositoryFinalObservation](
			"the live production container repository evaluates {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBContainerRepositoryFinalObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBContainerRepositoryFinalObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, ok := p.GetString(0)
				if !ok {
					return DBContainerRepositoryFinalObservation{}, fmt.Errorf("container repository profile is not a string")
				}
				value, err := observeDBContainerRepositoryFinal(database, profile)
				return DBContainerRepositoryFinalObservation{Value: value}, err
			},
		),
		brine.DefineCheck[DBContainerRepositoryFinalObservation](
			"its exact remaining observation is {string}",
			func(in DBContainerRepositoryFinalObservation, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("container repository observation is not a string")
				}
				if in.Value != want {
					return fmt.Errorf("container repository result got %q, want %q", in.Value, want)
				}
				return nil
			},
		),
	}
}

func observeDBContainerRepositoryFinal(database JetbridgeDB, profile string) (string, error) {
	switch profile {
	case "live-check-session", "refreshed-worker-type":
		return observeLiveCheckContainer(database, profile)
	case "no-failed-count":
		affected, _ := database.ContainerRepository.DestroyFailedContainers()
		return fmt.Sprintf("affected=%d", affected), nil
	case "unknown-preserves-created":
		return observeUnknownContainerPreservation(database)
	default:
		return "", fmt.Errorf("unknown final container repository profile %q", profile)
	}
}

func observeLiveCheckContainer(database JetbridgeDB, profile string) (string, error) {
	workerPayload := atc.Worker{
		Name:     "final-repository-worker",
		Platform: "linux",
		State:    string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{
			{Type: "some-base-resource-type", Image: "example/base", Version: "v1"},
		},
	}
	worker, err := database.WorkerFactory.SaveWorker(workerPayload, 0)
	if err != nil {
		return "", err
	}
	resourceConfig, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(
		"some-base-resource-type",
		atc.Source{"some": "source"},
		nil,
	)
	if err != nil {
		return "", err
	}
	_, err = worker.CreateContainer(
		db.NewResourceConfigCheckSessionContainerOwner(
			resourceConfig.ID(),
			resourceConfig.OriginBaseResourceType().ID,
			db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: time.Hour},
		),
		db.ContainerMetadata{Type: db.ContainerTypeCheck},
	)
	if err != nil {
		return "", err
	}
	if profile == "live-check-session" {
		if _, err := database.Conn.Exec(
			`UPDATE resource_config_check_sessions SET expires_at = NOW() + INTERVAL '1 hour' WHERE resource_config_id = $1`,
			resourceConfig.ID(),
		); err != nil {
			return "", err
		}
	} else {
		if _, err := database.WorkerFactory.SaveWorker(workerPayload, 0); err != nil {
			return "", err
		}
	}
	if err := db.NewResourceConfigCheckSessionLifecycle(database.Conn).CleanExpiredResourceConfigCheckSessions(); err != nil {
		return "", err
	}
	creating, created, destroying, err := database.ContainerRepository.FindOrphanedContainers()
	return fmt.Sprintf(
		"creating=%d;created=%d;destroying=%d;error=%t",
		len(creating), len(created), len(destroying), err != nil,
	), nil
}

func observeUnknownContainerPreservation(database JetbridgeDB) (string, error) {
	workerName := "final-repository-worker"
	if _, err := database.WorkerFactory.SaveWorker(atc.Worker{
		Name: workerName, Platform: "linux", State: string(db.WorkerStateRunning),
	}, 0); err != nil {
		return "", err
	}
	for _, row := range []struct {
		handle string
		state  string
	}{
		{"some-handle1", atc.ContainerStateDestroying},
		{"some-handle2", atc.ContainerStateCreated},
	} {
		if _, err := database.Conn.Exec(
			`INSERT INTO containers (handle, state, worker_name) VALUES ($1, $2, $3)`,
			row.handle, row.state, workerName,
		); err != nil {
			return "", err
		}
	}
	_, callErr := database.ContainerRepository.DestroyUnknownContainers(
		workerName,
		[]string{"some-handle3", "some-handle4"},
	)
	rows, err := database.Conn.Query(`SELECT handle FROM containers WHERE state = $1`, atc.ContainerStateCreated)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var handles []string
	for rows.Next() {
		var handle string
		if err := rows.Scan(&handle); err != nil {
			return "", err
		}
		handles = append(handles, handle)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sort.Strings(handles)
	return fmt.Sprintf("created=%s;error=%t", strings.Join(handles, ","), callErr != nil), nil
}
