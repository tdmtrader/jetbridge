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

type DBVolumeRepositoryObservation struct{ Value string }

func DBVolumeRepositoryStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBVolumeRepositoryObservation](
			"the real volume repository evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBVolumeRepositoryObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return DBVolumeRepositoryObservation{}, fmt.Errorf("expected volume repository profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBVolumeRepositoryObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeDBVolumeRepository(database, profile)
				return DBVolumeRepositoryObservation{Value: value}, err
			},
		),
		CheckString[DBVolumeRepositoryObservation](
			"the volume repository observation is {string}",
			"volume repository observation",
			func(observation DBVolumeRepositoryObservation) (string, error) { return observation.Value, nil },
		),
	}
}

func observeDBVolumeRepository(database JetbridgeDB, profile string) (string, error) {
	switch {
	case strings.HasPrefix(profile, "team/"):
		return observeVolumeTeam(database, profile)
	case strings.HasPrefix(profile, "orphan/"):
		return observeVolumeOrphan(database, profile)
	case profile == "failed/count":
		return observeVolumeFailed(database)
	case profile == "destroying/list":
		return observeDestroyingVolume(database)
	case strings.HasPrefix(profile, "create/"):
		return observeVolumeCreate(database, profile)
	case strings.HasPrefix(profile, "find/"):
		return observeVolumeFind(database, profile)
	case strings.HasPrefix(profile, "remove-destroying/"):
		return observeRemoveDestroying(database, profile)
	case strings.HasPrefix(profile, "remove-missing/"):
		return observeRemoveMissing(database, profile)
	case strings.HasPrefix(profile, "update-missing/"):
		return observeUpdateMissing(database, profile)
	case strings.HasPrefix(profile, "unknown/"):
		return observeDestroyUnknown(database, profile)
	default:
		return "", fmt.Errorf("unknown volume repository profile %q", profile)
	}
}

type strictVolumeEnvironment struct {
	DB     JetbridgeDB
	Team   db.Team
	Worker db.Worker
}

func newStrictVolumeEnvironment(database JetbridgeDB, suffix string, resourceType bool) (strictVolumeEnvironment, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "volume-team-" + suffix})
	if err != nil {
		return strictVolumeEnvironment{}, err
	}
	payload := atc.Worker{
		Name:     "volume-worker-" + suffix,
		Platform: "linux",
		Version:  "1.2.3",
		State:    string(db.WorkerStateRunning),
	}
	if resourceType {
		payload.ResourceTypes = []atc.WorkerResourceType{{
			Type: "registry-image", Image: "registry-image", Version: "1.0",
		}}
	}
	if _, err := database.WorkerFactory.SaveWorker(payload, 0); err != nil {
		return strictVolumeEnvironment{}, err
	}
	worker, found, err := database.WorkerFactory.GetWorker(payload.Name)
	if err != nil {
		return strictVolumeEnvironment{}, err
	}
	if !found {
		return strictVolumeEnvironment{}, fmt.Errorf("worker %q was not found", payload.Name)
	}
	return strictVolumeEnvironment{DB: database, Team: team, Worker: worker}, nil
}

func createStrictVolume(env strictVolumeEnvironment, handle string, state db.VolumeState) (db.CreatingVolume, db.CreatedVolume, error) {
	creating, err := env.DB.VolumeRepository.CreateVolumeWithHandle(handle, env.Team.ID(), env.Worker.Name(), db.VolumeTypeArtifact)
	if err != nil {
		return nil, nil, err
	}
	if state == db.VolumeStateCreating {
		return creating, nil, nil
	}
	if state == db.VolumeStateFailed {
		_, err := creating.Failed()
		return creating, nil, err
	}
	created, err := creating.Created()
	if err != nil {
		return nil, nil, err
	}
	if state == db.VolumeStateDestroying {
		_, err := created.Destroying()
		return creating, created, err
	}
	return creating, created, nil
}

func observeVolumeTeam(database JetbridgeDB, profile string) (string, error) {
	env, err := newStrictVolumeEnvironment(database, strings.ReplaceAll(profile, "/", "-"), false)
	if err != nil {
		return "", err
	}
	if profile == "team/task-cache" {
		pipeline, _, err := env.Team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 0, false)
		if err != nil {
			return "", err
		}
		job, found, err := pipeline.Job("job")
		if err != nil || !found {
			return "", volumeRepositoryError(err, fmt.Errorf("job was not found"))
		}
		_, created, err := createStrictVolume(env, "task-cache-volume", db.VolumeStateCreated)
		if err != nil {
			return "", err
		}
		if err := created.InitializeTaskCache(job.ID(), "step", "path"); err != nil {
			return "", err
		}
		volumes, err := database.VolumeRepository.GetTeamVolumes(env.Team.ID())
		if err != nil {
			return "", err
		}
		matches := len(volumes) == 1 && volumes[0].Handle() == created.Handle() && volumes[0].Type() == db.VolumeTypeTaskCache
		return fmt.Sprintf("count=%d;matches=%t", len(volumes), matches), nil
	}

	otherTeam, err := database.TeamFactory.CreateTeam(atc.Team{Name: "volume-other-team-" + strings.ReplaceAll(profile, "/", "-")})
	if err != nil {
		return "", err
	}
	firstHandles := []string{"team-first-1", "team-first-2"}
	for _, handle := range firstHandles {
		if _, _, err := createStrictVolume(env, handle, db.VolumeStateCreated); err != nil {
			return "", err
		}
	}
	otherCreating, err := database.VolumeRepository.CreateVolumeWithHandle("team-second-1", otherTeam.ID(), env.Worker.Name(), db.VolumeTypeArtifact)
	if err != nil {
		return "", err
	}
	if _, err := otherCreating.Created(); err != nil {
		return "", err
	}
	if profile == "team/expired" {
		_, err := database.WorkerFactory.SaveWorker(atc.Worker{
			Name: env.Worker.Name(), Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
		}, -10*time.Minute)
		if err != nil {
			return "", err
		}
	}
	first, err := database.VolumeRepository.GetTeamVolumes(env.Team.ID())
	if err != nil {
		return "", err
	}
	second, err := database.VolumeRepository.GetTeamVolumes(otherTeam.ID())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("first=%s;second=%s", strictVolumeHandles(first), strictVolumeHandles(second)), nil
}

func strictVolumeHandles(volumes []db.CreatedVolume) string {
	handles := make([]string, 0, len(volumes))
	for _, volume := range volumes {
		handles = append(handles, volume.Handle())
	}
	sort.Strings(handles)
	return strings.Join(handles, ",")
}

func observeVolumeOrphan(database JetbridgeDB, profile string) (string, error) {
	env, err := newStrictVolumeEnvironment(database, strings.ReplaceAll(profile, "/", "-"), false)
	if err != nil {
		return "", err
	}
	parent, _, err := createStrictVolume(env, "parent", db.VolumeStateCreated)
	if err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "child", db.VolumeStateCreated); err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "orphan", db.VolumeStateCreated); err != nil {
		return "", err
	}
	if _, err := database.Conn.Exec("UPDATE volumes SET parent_id = $1, parent_state = $2 WHERE handle = $3", parent.ID(), db.VolumeStateCreated, "child"); err != nil {
		return "", err
	}
	volumes, err := database.VolumeRepository.GetOrphanedVolumes()
	if err != nil {
		return "", err
	}
	handles := strictVolumeHandles(volumes)
	if profile == "orphan/exact" {
		return "handles=" + handles, nil
	}
	return fmt.Sprintf("parent=%t;child=%t", strings.Contains(","+handles+",", ",parent,"), strings.Contains(","+handles+",", ",child,")), nil
}

func observeVolumeFailed(database JetbridgeDB) (string, error) {
	env, err := newStrictVolumeEnvironment(database, "failed", false)
	if err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "failed-volume", db.VolumeStateFailed); err != nil {
		return "", err
	}
	count, err := database.VolumeRepository.DestroyFailedVolumes()
	return fmt.Sprintf("count=%d", count), err
}

func observeDestroyingVolume(database JetbridgeDB) (string, error) {
	env, err := newStrictVolumeEnvironment(database, "destroying", false)
	if err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "destroying-volume", db.VolumeStateDestroying); err != nil {
		return "", err
	}
	handles, err := database.VolumeRepository.GetDestroyingVolumes(env.Worker.Name())
	if err != nil {
		return "", err
	}
	sort.Strings(handles)
	return "handles=" + strings.Join(handles, ","), nil
}

func strictVolumeBaseResourceType(env strictVolumeEnvironment) (*db.UsedWorkerBaseResourceType, error) {
	used, found, err := db.NewWorkerBaseResourceTypeFactory(env.DB.Conn).Find("registry-image", env.Worker)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("worker base resource type was not found")
	}
	return used, nil
}

func observeVolumeCreate(database JetbridgeDB, profile string) (string, error) {
	env, err := newStrictVolumeEnvironment(database, strings.ReplaceAll(profile, "/", "-"), profile == "create/base-resource-type")
	if err != nil {
		return "", err
	}
	switch profile {
	case "create/base-resource-type":
		used, err := strictVolumeBaseResourceType(env)
		if err != nil {
			return "", err
		}
		volume, err := database.VolumeRepository.CreateBaseResourceTypeVolume(used)
		if err != nil {
			return "", err
		}
		var teamID sql.NullInt64
		if err := database.Conn.QueryRow("SELECT team_id FROM volumes WHERE handle = $1", volume.Handle()).Scan(&teamID); err != nil {
			return "", err
		}
		return fmt.Sprintf("team-set=%t", teamID.Valid), nil
	case "create/generic":
		volume, err := database.VolumeRepository.CreateVolume(env.Team.ID(), env.Worker.Name(), db.VolumeTypeArtifact)
		if err != nil {
			return "", err
		}
		var teamID sql.NullInt64
		var worker string
		if err := database.Conn.QueryRow("SELECT team_id, worker_name FROM volumes WHERE handle = $1", volume.Handle()).Scan(&teamID, &worker); err != nil {
			return "", err
		}
		return fmt.Sprintf("team=%t;worker=%t", teamID.Valid && teamID.Int64 == int64(env.Team.ID()), worker == env.Worker.Name()), nil
	case "create/fixed-handle":
		volume, err := database.VolumeRepository.CreateVolumeWithHandle("fixed-handle", env.Team.ID(), env.Worker.Name(), db.VolumeTypeArtifact)
		if err != nil {
			return "", err
		}
		return "handle=" + volume.Handle(), nil
	default:
		return "", fmt.Errorf("unknown create profile %q", profile)
	}
}

func observeVolumeFind(database JetbridgeDB, profile string) (string, error) {
	env, err := newStrictVolumeEnvironment(database, strings.ReplaceAll(profile, "/", "-"), true)
	if err != nil {
		return "", err
	}
	if profile == "find/base-created" || profile == "find/base-creating" {
		used, err := strictVolumeBaseResourceType(env)
		if err != nil {
			return "", err
		}
		existing, err := database.VolumeRepository.CreateBaseResourceTypeVolume(used)
		if err != nil {
			return "", err
		}
		if profile == "find/base-created" {
			if _, err := existing.Created(); err != nil {
				return "", err
			}
		}
		creating, created, err := database.VolumeRepository.FindBaseResourceTypeVolume(used)
		if err != nil {
			return "", err
		}
		if profile == "find/base-created" {
			return fmt.Sprintf("creating=%t;created=%t;handle=%t", creating != nil, created != nil, created != nil && created.Handle() == existing.Handle()), nil
		}
		return fmt.Sprintf("creating=%t;created=%t;handle=%t", creating != nil, created != nil, creating != nil && creating.Handle() == existing.Handle()), nil
	}

	build, err := env.Team.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	cache, err := db.NewResourceCacheFactory(database.Conn, database.LockFactory).FindOrCreateResourceCache(
		db.ForBuild(build.ID()), "registry-image", atc.Version{"digest": "one"}, atc.Source{"repository": "example/image"}, nil, nil,
	)
	if err != nil {
		return "", err
	}
	_, created, err := createStrictVolume(env, "resource-cache-volume", db.VolumeStateCreated)
	if err != nil {
		return "", err
	}
	if _, err := created.InitializeResourceCache(cache); err != nil {
		return "", err
	}
	foundVolume, found, err := database.VolumeRepository.FindResourceCacheVolume(env.Worker.Name(), cache, time.Now())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("found=%t;handle=%t", found, foundVolume != nil && foundVolume.Handle() == created.Handle()), nil
}

func observeRemoveDestroying(database JetbridgeDB, profile string) (string, error) {
	env, err := newStrictVolumeEnvironment(database, strings.ReplaceAll(profile, "/", "-"), false)
	if err != nil {
		return "", err
	}
	state := db.VolumeStateDestroying
	handles := []string{"keep-one", "keep-two"}
	if strings.Contains(profile, "creating") {
		state = db.VolumeStateCreating
	}
	if strings.Contains(profile, "reported") {
		handles = []string{"target", "keep-two"}
	}
	if strings.Contains(profile, "empty") {
		handles = []string{}
	}
	if _, _, err := createStrictVolume(env, "target", state); err != nil {
		return "", err
	}
	count, err := database.VolumeRepository.RemoveDestroyingVolumes(env.Worker.Name(), handles)
	if err != nil {
		return "", err
	}
	exists, err := strictVolumeExists(database, "target")
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(profile, "count") {
		return fmt.Sprintf("count=%d", count), nil
	}
	return fmt.Sprintf("exists=%t", exists), nil
}

func strictVolumeExists(database JetbridgeDB, handle string) (bool, error) {
	var count int
	err := database.Conn.QueryRow("SELECT COUNT(*) FROM volumes WHERE handle = $1", handle).Scan(&count)
	return count == 1, err
}

func setStrictVolumeMissing(database JetbridgeDB, handle string, at time.Time) error {
	_, err := database.Conn.Exec("UPDATE volumes SET missing_since = $1 WHERE handle = $2", at, handle)
	return err
}

func observeRemoveMissing(database JetbridgeDB, profile string) (string, error) {
	env, err := newStrictVolumeEnvironment(database, strings.ReplaceAll(profile, "/", "-"), false)
	if err != nil {
		return "", err
	}
	now := time.Now()
	if strings.HasPrefix(profile, "remove-missing/parent-") {
		_, _, err := createStrictVolume(env, "alive", db.VolumeStateCreated)
		if err != nil {
			return "", err
		}
		parent, _, err := createStrictVolume(env, "parent", db.VolumeStateCreated)
		if err != nil {
			return "", err
		}
		if _, _, err := createStrictVolume(env, "child", db.VolumeStateCreated); err != nil {
			return "", err
		}
		if _, err := database.Conn.Exec("UPDATE volumes SET parent_id = $1, parent_state = $2 WHERE handle = $3", parent.ID(), db.VolumeStateCreated, "child"); err != nil {
			return "", err
		}
		if err := setStrictVolumeMissing(database, "parent", now.Add(-10*time.Minute)); err != nil {
			return "", err
		}
		count, err := database.VolumeRepository.RemoveMissingVolumes(3 * time.Minute)
		if err != nil {
			return "", err
		}
		if profile == "remove-missing/parent-count" {
			return fmt.Sprintf("count=%d", count), nil
		}
		handles, err := strictAllVolumeHandles(database)
		return "handles=" + strings.Join(handles, ","), err
	}

	if _, _, err := createStrictVolume(env, "live", db.VolumeStateCreated); err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "recent", db.VolumeStateCreated); err != nil {
		return "", err
	}
	if err := setStrictVolumeMissing(database, "recent", now); err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "old-failed", db.VolumeStateFailed); err != nil {
		return "", err
	}
	if err := setStrictVolumeMissing(database, "old-failed", now.Add(-5*time.Minute)); err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "old-destroying", db.VolumeStateDestroying); err != nil {
		return "", err
	}
	if err := setStrictVolumeMissing(database, "old-destroying", now.Add(-10*time.Minute)); err != nil {
		return "", err
	}
	grace := 3 * time.Minute
	if profile == "remove-missing/no-expired" {
		grace = 7 * time.Minute
	}
	count, err := database.VolumeRepository.RemoveMissingVolumes(grace)
	if err != nil {
		return "", err
	}
	if profile == "remove-missing/expired-right" {
		handles, err := strictAllVolumeHandles(database)
		return "handles=" + strings.Join(handles, ","), err
	}
	return fmt.Sprintf("count=%d", count), nil
}

func strictAllVolumeHandles(database JetbridgeDB) ([]string, error) {
	rows, err := database.Conn.Query("SELECT handle FROM volumes ORDER BY handle")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var handles []string
	for rows.Next() {
		var handle string
		if err := rows.Scan(&handle); err != nil {
			return nil, err
		}
		handles = append(handles, handle)
	}
	return handles, rows.Err()
}

func observeUpdateMissing(database JetbridgeDB, profile string) (string, error) {
	env, err := newStrictVolumeEnvironment(database, strings.ReplaceAll(profile, "/", "-"), false)
	if err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "one", db.VolumeStateDestroying); err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "two", db.VolumeStateDestroying); err != nil {
		return "", err
	}
	thirdState := db.VolumeStateCreated
	if profile == "update-missing/creating" {
		thirdState = db.VolumeStateCreating
	}
	if _, _, err := createStrictVolume(env, "three", thirdState); err != nil {
		return "", err
	}
	original := time.Date(2018, 9, 24, 0, 0, 0, 0, time.UTC)
	if thirdState == db.VolumeStateCreated {
		if err := setStrictVolumeMissing(database, "three", original); err != nil {
			return "", err
		}
	}
	reported := []string{"one"}
	if profile == "update-missing/full" {
		reported = []string{"one", "two"}
	}
	if profile == "update-missing/clear" {
		reported = []string{"one", "two", "three"}
	}
	if err := database.VolumeRepository.UpdateVolumesMissingSince(env.Worker.Name(), reported); err != nil {
		return "", err
	}
	one, _, err := strictMissingSince(database, "one")
	if err != nil {
		return "", err
	}
	two, _, err := strictMissingSince(database, "two")
	if err != nil {
		return "", err
	}
	three, thirdTime, err := strictMissingSince(database, "three")
	if err != nil {
		return "", err
	}
	switch profile {
	case "update-missing/creating":
		return fmt.Sprintf("three=%t", three), nil
	case "update-missing/subset":
		return fmt.Sprintf("one=%t;two=%t;three-old=%t", one, two, three && thirdTime.Equal(original)), nil
	case "update-missing/full":
		return fmt.Sprintf("one=%t;two=%t", one, two), nil
	case "update-missing/clear":
		return fmt.Sprintf("one=%t;two=%t;three=%t", one, two, three), nil
	default:
		return "", fmt.Errorf("unknown update missing profile %q", profile)
	}
}

func strictMissingSince(database JetbridgeDB, handle string) (bool, time.Time, error) {
	var missing sql.NullTime
	err := database.Conn.QueryRow("SELECT missing_since FROM volumes WHERE handle = $1", handle).Scan(&missing)
	return missing.Valid, missing.Time, err
}

func observeDestroyUnknown(database JetbridgeDB, profile string) (string, error) {
	env, err := newStrictVolumeEnvironment(database, strings.ReplaceAll(profile, "/", "-"), false)
	if err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "one", db.VolumeStateDestroying); err != nil {
		return "", err
	}
	if _, _, err := createStrictVolume(env, "two", db.VolumeStateCreated); err != nil {
		return "", err
	}
	reported := []string{"three", "four"}
	if profile == "unknown/noop" {
		reported = []string{"one", "two"}
	}
	count, err := database.VolumeRepository.DestroyUnknownVolumes(env.Worker.Name(), reported)
	if err != nil {
		return "", err
	}
	if profile == "unknown/preserve" {
		exists, err := strictVolumeExists(database, "two")
		return fmt.Sprintf("count=%d;created=%t", count, exists), err
	}
	handles, err := strictDestroyingVolumeHandles(database, env.Worker.Name())
	if err != nil {
		return "", err
	}
	sort.Strings(handles)
	return fmt.Sprintf("count=%d;destroying=%s", count, strings.Join(handles, ",")), nil
}

func strictDestroyingVolumeHandles(database JetbridgeDB, workerName string) ([]string, error) {
	rows, err := database.Conn.Query(`SELECT handle FROM volumes WHERE worker_name = $1 AND state = $2`, workerName, db.VolumeStateDestroying)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var handles []string
	for rows.Next() {
		var handle string
		if err := rows.Scan(&handle); err != nil {
			return nil, err
		}
		handles = append(handles, handle)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return handles, nil
}

func volumeRepositoryError(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
