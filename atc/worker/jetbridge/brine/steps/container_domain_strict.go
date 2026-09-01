package steps

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/concourse/concourse/atc"
)

func observeStrictContainerDomain(database JetbridgeDB, profile string) (string, error) {
	switch profile {
	case "creating-metadata":
		creating, err := newDomainContainer(database, database.Conn, profile)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("equal=%t", reflect.DeepEqual(creating.Metadata(), containerDomainMetadata)), nil
	case "created-return":
		creating, err := newDomainContainer(database, database.Conn, profile)
		if err != nil {
			return "", err
		}
		created, createErr := creating.Created()
		state, err := domainContainerState(database, creating.Handle())
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("returned=%t;error=%t;state=%s", created != nil, createErr != nil, state), nil
	case "created-volumes":
		return observeStrictContainerVolumes(database)
	case "created-metadata":
		creating, err := newDomainContainer(database, database.Conn, profile)
		if err != nil {
			return "", err
		}
		created, err := creating.Created()
		if err != nil {
			return "", err
		}
		if created == nil {
			return "", fmt.Errorf("container domain result: Created returned nil")
		}
		return fmt.Sprintf("equal=%t", reflect.DeepEqual(created.Metadata(), containerDomainMetadata)), nil
	case "destroying-return", "destroying-metadata":
		creating, err := newDomainContainer(database, database.Conn, profile)
		if err != nil {
			return "", err
		}
		created, err := creating.Created()
		if err != nil {
			return "", err
		}
		if created == nil {
			return "", fmt.Errorf("container domain result: Created returned nil")
		}
		destroying, destroyErr := created.Destroying()
		if profile == "destroying-metadata" {
			if destroyErr != nil {
				return "", destroyErr
			}
			if destroying == nil {
				return "", fmt.Errorf("container domain result: Destroying returned nil")
			}
			return fmt.Sprintf("equal=%t", reflect.DeepEqual(destroying.Metadata(), containerDomainMetadata)), nil
		}
		state, err := domainContainerState(database, creating.Handle())
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("returned=%t;error=%t;state=%s", destroying != nil, destroyErr != nil, state), nil
	}
	if strings.HasPrefix(profile, "fail-") {
		return observeStrictContainerFailed(database, strings.TrimPrefix(profile, "fail-"))
	}
	if strings.HasPrefix(profile, "destroy-") {
		return observeStrictContainerDestroy(database, strings.TrimPrefix(profile, "destroy-"))
	}
	return "", fmt.Errorf("unknown strict container domain profile %q", profile)
}

func observeStrictContainerVolumes(database JetbridgeDB) (string, error) {
	creating, err := newDomainContainer(database, database.Conn, "created-volumes")
	if err != nil {
		return "", err
	}
	created, err := creating.Created()
	if err != nil {
		return "", err
	}
	if created == nil {
		return "", fmt.Errorf("container domain result: Created returned nil")
	}
	var teamID int
	if err := database.Conn.QueryRow("SELECT team_id FROM containers WHERE handle = $1", creating.Handle()).Scan(&teamID); err != nil {
		return "", err
	}
	wantHandles := []string{}
	for _, path := range []string{"some-path-1", "some-path-2"} {
		volume, err := database.VolumeRepository.CreateContainerVolume(teamID, creating.WorkerName(), creating, path)
		if err != nil {
			return "", err
		}
		if _, err = volume.Created(); err != nil {
			return "", err
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
	return fmt.Sprintf(
		"count=%d;handles=%t;paths=%t", len(volumes),
		reflect.DeepEqual(gotHandles, wantHandles),
		reflect.DeepEqual(gotPaths, []string{"some-path-1", "some-path-2"}),
	), nil
}

func observeStrictContainerFailed(database JetbridgeDB, profile string) (string, error) {
	creating, err := newDomainContainer(database, database.Conn, "fail-"+profile)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(profile, "already-") {
		if _, err := creating.Failed(); err != nil {
			return "", err
		}
	} else if strings.HasPrefix(profile, "created-") || strings.HasPrefix(profile, "destroying-") {
		created, err := creating.Created()
		if err != nil {
			return "", err
		}
		if created == nil {
			return "", fmt.Errorf("container domain result: Created returned nil")
		}
		if strings.HasPrefix(profile, "destroying-") {
			var destroying interface{}
			destroying, err = created.Destroying()
			if err != nil {
				return "", err
			}
			if destroying == nil {
				return "", fmt.Errorf("container domain result: Destroying returned nil")
			}
		}
	}
	failed, failedErr := creating.Failed()
	if strings.HasSuffix(profile, "-error") {
		return fmt.Sprintf("error=%t", failedErr != nil), nil
	}
	failedHandles, err := strictFailedContainerHandles(database)
	if err != nil {
		return "", err
	}
	handlePresent := false
	for _, handle := range failedHandles {
		if failed != nil && handle == failed.Handle() {
			handlePresent = true
		}
	}
	state, err := domainContainerState(database, creating.Handle())
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(profile, "not-marked") {
		return fmt.Sprintf("failed-count=%d;returned=%t;state=%s", len(failedHandles), failed != nil, state), nil
	}
	return fmt.Sprintf(
		"failed-count=%d;handle-present=%t;returned=%t;state=%s",
		len(failedHandles), handlePresent, failed != nil, state,
	), nil
}

func strictFailedContainerHandles(database JetbridgeDB) ([]string, error) {
	rows, err := database.Conn.Query("SELECT handle FROM containers WHERE state = $1", atc.ContainerStateFailed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	handles := []string{}
	for rows.Next() {
		var handle string
		if err := rows.Scan(&handle); err != nil {
			return nil, err
		}
		handles = append(handles, handle)
	}
	return handles, rows.Err()
}

func observeStrictContainerDestroy(database JetbridgeDB, profile string) (string, error) {
	parts := strings.Split(profile, "-")
	if len(parts) != 2 || (parts[0] != "destroying" && parts[0] != "failed") || (parts[1] != "live" && parts[1] != "missing") {
		return "", fmt.Errorf("unknown strict destroy profile %q", profile)
	}
	creating, err := newDomainContainer(database, database.Conn, "destroy-"+profile)
	if err != nil {
		return "", err
	}
	var terminal interface{ Destroy() (bool, error) }
	if parts[0] == "failed" {
		terminal, err = creating.Failed()
	} else {
		created, createErr := creating.Created()
		if createErr != nil {
			return "", createErr
		}
		if created == nil {
			return "", fmt.Errorf("container domain result: Created returned nil")
		}
		terminal, err = created.Destroying()
	}
	if err != nil {
		return "", err
	}
	if terminal == nil {
		return "", fmt.Errorf("container domain result: terminal container returned nil")
	}
	if parts[1] == "missing" {
		if _, err := terminal.Destroy(); err != nil {
			return "", err
		}
	}
	destroyed, destroyErr := terminal.Destroy()
	_, stateErr := domainContainerState(database, creating.Handle())
	present := stateErr == nil
	if stateErr != nil && stateErr.Error() != "container row missing" {
		return "", stateErr
	}
	return fmt.Sprintf("destroyed=%t;error=%t;present=%t", destroyed, destroyErr != nil, present), nil
}
