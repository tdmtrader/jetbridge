package steps

import (
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBVolumeRepositoryFinalObservation struct{ Value string }

func DBVolumeRepositoryFinalStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBVolumeRepositoryFinalObservation](
			"the final production volume repository evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBVolumeRepositoryFinalObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return DBVolumeRepositoryFinalObservation{}, fmt.Errorf("expected volume repository profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBVolumeRepositoryFinalObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeDBVolumeRepositoryFinal(database, profile)
				if err != nil {
					return DBVolumeRepositoryFinalObservation{}, fmt.Errorf("final volume repository observation: %w", err)
				}
				return DBVolumeRepositoryFinalObservation{Value: value}, nil
			},
		),
		CheckString[DBVolumeRepositoryFinalObservation](
			"the final volume repository observation is {string}",
			"final volume repository observation",
			func(observation DBVolumeRepositoryFinalObservation) (string, error) { return observation.Value, nil },
		),
	}
}

func observeDBVolumeRepositoryFinal(database JetbridgeDB, profile string) (string, error) {
	switch {
	case profile == "destroying-empty":
		return observeDestroyedVolumeLookup(database)
	case profile == "base-no-team":
		return observeBaseVolumeWithoutTeam(database)
	case strings.HasPrefix(profile, "remove-"):
		return observeRemoveDestroyingWithoutError(database, profile)
	case strings.HasPrefix(profile, "update-"):
		return observeUpdateMissingWithoutError(database, profile)
	default:
		return "", fmt.Errorf("unknown final volume repository profile %q", profile)
	}
}

func observeDestroyedVolumeLookup(database JetbridgeDB) (string, error) {
	worker, err := database.PersistNamedWorker("final-volume-destroy-worker")
	if err != nil {
		return "", err
	}
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "final-volume-team"})
	if err != nil {
		return "", err
	}
	creating, err := database.VolumeRepository.CreateVolume(team.ID(), worker.Name(), db.VolumeTypeArtifact)
	if err != nil {
		return "", err
	}
	created, err := creating.Created()
	if err != nil {
		return "", err
	}
	destroying, err := created.Destroying()
	if err != nil {
		return "", err
	}
	destroyed, err := destroying.Destroy()
	if err != nil {
		return "", err
	}
	if !destroyed {
		return "", fmt.Errorf("persisted destroying volume was not destroyed")
	}
	volumes, err := database.VolumeRepository.GetDestroyingVolumes(worker.Name())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("empty=%t", len(volumes) == 0), nil
}

func observeBaseVolumeWithoutTeam(database JetbridgeDB) (string, error) {
	statements := sq.StatementBuilder.PlaceholderFormat(sq.Dollar).RunWith(database.Conn)
	payload := atc.Worker{
		Name:     "final-volume-base-worker",
		Platform: "linux",
		Version:  "1.2.3",
		State:    string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{{
			Type: "some-base-resource-type", Image: "some-image", Version: "some-version",
		}},
	}
	if _, err := database.WorkerFactory.SaveWorker(payload, 0); err != nil {
		return "", err
	}
	worker, found, err := database.WorkerFactory.GetWorker(payload.Name)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("persisted worker was not found")
	}
	usedType, found, err := db.NewWorkerBaseResourceTypeFactory(database.Conn).Find("some-base-resource-type", worker)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("persisted worker base resource type was not found")
	}
	volume, err := database.VolumeRepository.CreateBaseResourceTypeVolume(usedType)
	if err != nil {
		return "", err
	}
	var teamID int
	err = statements.Select("team_id").From("volumes").
		Where(sq.Eq{"handle": volume.Handle()}).
		RunWith(database.Conn).
		QueryRow().
		Scan(&teamID)
	return fmt.Sprintf("team-id-null=%t", err != nil && strings.Contains(err.Error(), "Scan error")), nil
}

func observeRemoveDestroyingWithoutError(database JetbridgeDB, profile string) (string, error) {
	statements := sq.StatementBuilder.PlaceholderFormat(sq.Dollar).RunWith(database.Conn)
	worker, err := database.PersistNamedWorker("final-volume-remove-worker")
	if err != nil {
		return "", err
	}
	handles := []string{"some-handle1", "some-handle2"}
	state := db.VolumeStateDestroying
	insertedHandles := []string{"123-456-abc-def"}
	switch profile {
	case "remove-unreported":
	case "remove-empty-report":
		handles = []string{}
	case "remove-creating":
		state = db.VolumeStateCreating
	case "remove-protected":
		insertedHandles = []string{"some-handle1", "some-handle2"}
	default:
		return "", fmt.Errorf("unknown remove profile %q", profile)
	}
	for _, handle := range insertedHandles {
		_, err := statements.Insert("volumes").SetMap(map[string]any{
			"state":       state,
			"handle":      handle,
			"worker_name": worker.Name(),
		}).RunWith(database.Conn).Exec()
		if err != nil {
			return "", err
		}
	}
	if _, err := database.VolumeRepository.RemoveDestroyingVolumes(worker.Name(), handles); err != nil {
		return "", err
	}
	return "error=nil", nil
}

func observeUpdateMissingWithoutError(database JetbridgeDB, profile string) (string, error) {
	statements := sq.StatementBuilder.PlaceholderFormat(sq.Dollar).RunWith(database.Conn)
	worker, err := database.PersistNamedWorker("final-volume-update-worker")
	if err != nil {
		return "", err
	}
	today := time.Now()
	rows := []map[string]any{
		{"handle": "some-handle1", "state": db.VolumeStateDestroying, "worker_name": worker.Name()},
		{"handle": "some-handle2", "state": db.VolumeStateDestroying, "worker_name": worker.Name()},
		{"handle": "some-handle3", "state": db.VolumeStateCreated, "worker_name": worker.Name(), "missing_since": today},
	}
	for _, row := range rows {
		if _, err := statements.Insert("volumes").SetMap(row).Exec(); err != nil {
			return "", err
		}
	}
	var handles []string
	switch profile {
	case "update-subset":
		handles = []string{"some-handle1"}
	case "update-full":
		handles = []string{"some-handle1", "some-handle2"}
	case "update-restored":
		handles = []string{"some-handle1", "some-handle2", "some-handle3"}
	default:
		return "", fmt.Errorf("unknown update profile %q", profile)
	}
	if err := database.VolumeRepository.UpdateVolumesMissingSince(worker.Name(), handles); err != nil {
		return "", err
	}
	return "error=nil", nil
}
