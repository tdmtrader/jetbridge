package steps

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type ContainerDomainObservation struct{ Value string }

var containerDomainMetadata = db.ContainerMetadata{
	Type:     db.ContainerTypeTask,
	StepName: "some-step-name", Attempt: "1.2.3",
	PipelineID: 123, JobID: 456, BuildID: 789,
	PipelineName: "some-pipeline", JobName: "some-job", BuildName: "some-build",
	WorkingDirectory: "/some/work/dir", User: "some-user",
}

func ContainerDomainDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ContainerDomainObservation](
			"a real database container evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (ContainerDomainObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ContainerDomainObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeStrictContainerDomain(database, profile)
				return ContainerDomainObservation{Value: value}, err
			},
		),
		CheckString[ContainerDomainObservation]("the container domain result is {string}", "container domain result", func(in ContainerDomainObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeContainerDomain(database JetbridgeDB, profile string) (string, error) {
	if profile == "lifecycle" {
		creating, err := newDomainContainer(database, database.Conn, "container-lifecycle")
		if err != nil {
			return "", err
		}
		creatingMetadata := reflect.DeepEqual(creating.Metadata(), containerDomainMetadata)
		created, err := creating.Created()
		if err != nil {
			return "", err
		}
		createdMetadata := reflect.DeepEqual(created.Metadata(), containerDomainMetadata)
		var teamID int
		if err := database.Conn.QueryRow("SELECT team_id FROM containers WHERE handle = $1", creating.Handle()).Scan(&teamID); err != nil {
			return "", err
		}
		wantHandles := make([]string, 0, 2)
		for _, path := range []string{"some-path-1", "some-path-2"} {
			volume, createErr := database.VolumeRepository.CreateContainerVolume(teamID, creating.WorkerName(), creating, path)
			if createErr != nil {
				return "", createErr
			}
			if _, createErr = volume.Created(); createErr != nil {
				return "", createErr
			}
			wantHandles = append(wantHandles, volume.Handle())
		}
		volumes, err := database.VolumeRepository.FindVolumesForContainer(created)
		if err != nil {
			return "", err
		}
		gotHandles, gotPaths := []string{}, []string{}
		for _, volume := range volumes {
			gotHandles = append(gotHandles, volume.Handle())
			gotPaths = append(gotPaths, volume.Path())
		}
		sort.Strings(wantHandles)
		sort.Strings(gotHandles)
		sort.Strings(gotPaths)
		volumesMatch := reflect.DeepEqual(gotHandles, wantHandles) && reflect.DeepEqual(gotPaths, []string{"some-path-1", "some-path-2"})
		destroying, err := created.Destroying()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("creating-metadata=%t;created=%t;created-metadata=%t;volumes=%t;destroying=%t;destroying-metadata=%t",
			creatingMetadata, created != nil, createdMetadata, volumesMatch, destroying != nil, reflect.DeepEqual(destroying.Metadata(), containerDomainMetadata)), nil
	}

	if len(profile) > len("fail-") && profile[:len("fail-")] == "fail-" {
		return observeContainerFailed(database, profile[len("fail-"):])
	}
	if len(profile) > len("destroy-") && profile[:len("destroy-")] == "destroy-" {
		return observeContainerDestroy(database, profile[len("destroy-"):])
	}
	return "", fmt.Errorf("unknown container domain profile %q", profile)
}

func newDomainContainer(database JetbridgeDB, conn db.DbConn, suffix string) (db.CreatingContainer, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "container-domain-" + suffix})
	if err != nil {
		return nil, err
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return nil, err
	}
	workerName := "container-domain-worker-" + suffix
	if _, err = database.PersistNamedWorker(workerName); err != nil {
		return nil, err
	}
	cache, err := db.NewWorkerCache(lager.NewLogger("brine-container-domain"), conn, time.Hour)
	if err != nil {
		return nil, err
	}
	worker, found, err := db.NewWorkerFactory(conn, cache).GetWorker(workerName)
	if err != nil || !found {
		return nil, firstError(err, fmt.Errorf("worker %q not found through container connection", workerName))
	}
	return worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("plan"), team.ID()), containerDomainMetadata)
}

func observeContainerFailed(database JetbridgeDB, state string) (string, error) {
	conn := database.Conn
	if state == "closed" {
		conn = database.runner.OpenConn()
	}
	creating, err := newDomainContainer(database, conn, "fail-"+state)
	if err != nil {
		return "", err
	}
	if state == "already-failed" {
		if _, err := creating.Failed(); err != nil {
			return "", err
		}
	} else if state == "created" || state == "destroying" {
		created, createErr := creating.Created()
		if createErr != nil {
			return "", createErr
		}
		if state == "destroying" {
			if _, createErr = created.Destroying(); createErr != nil {
				return "", createErr
			}
		}
	} else if state == "closed" {
		if err := conn.Close(); err != nil {
			return "", err
		}
	}
	failed, failedErr := creating.Failed()
	dbState, err := domainContainerState(database, creating.Handle())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("returned=%t;error=%t;state=%s", failed != nil, failedErr != nil, dbState), nil
}

func observeContainerDestroy(database JetbridgeDB, profile string) (string, error) {
	var state, condition string
	for _, candidate := range []string{"destroying", "failed"} {
		prefix := candidate + "-"
		if len(profile) > len(prefix) && profile[:len(prefix)] == prefix {
			state, condition = candidate, profile[len(prefix):]
		}
	}
	if state == "" || (condition != "live" && condition != "missing" && condition != "closed") {
		return "", fmt.Errorf("unknown destroy profile %q", profile)
	}
	conn := database.Conn
	if condition == "closed" {
		conn = database.runner.OpenConn()
	}
	creating, err := newDomainContainer(database, conn, "destroy-"+profile)
	if err != nil {
		return "", err
	}
	var terminal interface{ Destroy() (bool, error) }
	if state == "failed" {
		terminal, err = creating.Failed()
	} else {
		var created db.CreatedContainer
		created, err = creating.Created()
		if err == nil {
			terminal, err = created.Destroying()
		}
	}
	if err != nil {
		return "", err
	}
	if condition == "missing" {
		if _, err := terminal.Destroy(); err != nil {
			return "", err
		}
	} else if condition == "closed" {
		if err := conn.Close(); err != nil {
			return "", err
		}
	}
	destroyed, destroyErr := terminal.Destroy()
	_, presentErr := domainContainerState(database, creating.Handle())
	present := presentErr == nil
	if presentErr != nil && presentErr.Error() != "container row missing" {
		return "", presentErr
	}
	return fmt.Sprintf("destroyed=%t;error=%t;present=%t", destroyed, destroyErr != nil, present), nil
}

func domainContainerState(database JetbridgeDB, handle string) (string, error) {
	var state string
	err := database.Conn.QueryRow("SELECT state FROM containers WHERE handle = $1", handle).Scan(&state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("container row missing")
		}
		return "", err
	}
	return state, nil
}
