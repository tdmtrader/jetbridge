package steps

import (
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBResourceConfigCheckSessionLifecycleObservation struct {
	Profile string
	Failure string
}

func DBResourceConfigCheckSessionLifecycleStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBResourceConfigCheckSessionLifecycleObservation](
			"the production inactive check-session cleanup profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBResourceConfigCheckSessionLifecycleObservation, error) {
				profile, err := paramAt("the production inactive check-session cleanup profile {string} is exercised", p, 0)
				if err != nil {
					return DBResourceConfigCheckSessionLifecycleObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBResourceConfigCheckSessionLifecycleObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBResourceConfigCheckSessionLifecycleObservation{
					Profile: profile,
					Failure: observeDBResourceConfigCheckSessionLifecycle(database, profile),
				}, nil
			},
		),
		brine.DefineCheck[DBResourceConfigCheckSessionLifecycleObservation](
			"the inactive check-session cleanup observation exactly matches {string}",
			func(in DBResourceConfigCheckSessionLifecycleObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the inactive check-session cleanup observation exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

type checkSessionLifecycleCheckable interface {
	ID() int
	Type() string
	Source() atc.Source
	SetResourceConfigScope(db.ResourceConfigScope) error
}

func observeDBResourceConfigCheckSessionLifecycle(database JetbridgeDB, profile string) string {
	parts := strings.SplitN(profile, "-", 2)
	if len(parts) != 2 {
		return fmt.Sprintf("unknown profile %q", profile)
	}
	state, kind := parts[0], parts[1]
	if kind == "resource" && strings.HasSuffix(profile, "resource-type") {
		kind = "resource-type"
	}
	if strings.HasSuffix(profile, "resource-type") {
		state = strings.TrimSuffix(profile, "-resource-type")
	}
	if strings.HasSuffix(profile, "prototype") {
		state = strings.TrimSuffix(profile, "-prototype")
		kind = "prototype"
	}

	config := atc.Config{
		Resources: atc.ResourceConfigs{{
			Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "source"},
		}},
		ResourceTypes: atc.ResourceTypes{{
			Name: "some-type", Type: "some-base-resource-type", Source: atc.Source{"some-type": "source"},
		}},
		Prototypes: atc.Prototypes{{
			Name: "some-prototype", Type: "some-base-resource-type", Source: atc.Source{"some-prototype": "source"},
		}},
	}
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "check-session-lifecycle-team"})
	if err != nil {
		return err.Error()
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "check-session-lifecycle-pipeline"}, config, 0, false)
	if err != nil {
		return err.Error()
	}
	worker, err := database.WorkerFactory.SaveWorker(atc.Worker{
		Name:     "check-session-lifecycle-worker",
		Platform: "linux",
		Version:  "1.2.3",
		State:    string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{{
			Type: "some-base-resource-type", Image: "example/base", Version: "v1",
		}},
	}, 0)
	if err != nil {
		return err.Error()
	}

	checkable, err := loadCheckSessionLifecycleCheckable(pipeline, kind)
	if err != nil {
		return err.Error()
	}
	configFactory := db.NewResourceConfigFactory(database.Conn, database.LockFactory)
	resourceConfig, err := configFactory.FindOrCreateResourceConfig(checkable.Type(), checkable.Source(), nil)
	if err != nil {
		return err.Error()
	}
	var scopeResourceID *int
	if kind == "resource" {
		resourceID := checkable.ID()
		scopeResourceID = &resourceID
	}
	scope, err := resourceConfig.FindOrCreateScope(scopeResourceID)
	if err != nil {
		return err.Error()
	}
	if err := checkable.SetResourceConfigScope(scope); err != nil {
		return err.Error()
	}

	findOrCreate := func() (int, error) {
		fresh, found, err := configFactory.FindResourceConfigByID(resourceConfig.ID())
		if err != nil || !found {
			return 0, fmt.Errorf("load resource config: found=%t: %w", found, err)
		}
		owner := db.NewResourceConfigCheckSessionContainerOwner(
			fresh.ID(),
			fresh.OriginBaseResourceType().ID,
			db.ContainerOwnerExpiries{Min: time.Minute, Max: time.Minute},
		)
		query, found, err := owner.Find(database.Conn)
		if err != nil {
			return 0, err
		}
		if found {
			ids, ok := query["resource_config_check_session_id"].([]int)
			if !ok || len(ids) != 1 {
				return 0, fmt.Errorf("existing session ids are %#v", query["resource_config_check_session_id"])
			}
			return ids[0], nil
		}
		tx, err := database.Conn.Begin()
		if err != nil {
			return 0, err
		}
		defer db.Rollback(tx)
		created, err := owner.Create(tx, worker.Name())
		if err != nil {
			return 0, err
		}
		id, ok := created["resource_config_check_session_id"].(int)
		if !ok {
			return 0, fmt.Errorf("created session id is %#v", created["resource_config_check_session_id"])
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return id, nil
	}

	oldID, err := findOrCreate()
	if err != nil {
		return err.Error()
	}
	switch state {
	case "active":
	case "inactive":
		switch kind {
		case "resource":
			config.Resources = nil
		case "resource-type":
			config.ResourceTypes = nil
		case "prototype":
			config.Prototypes = nil
		default:
			return fmt.Sprintf("unknown kind %q", kind)
		}
		pipeline, _, err = team.SavePipeline(atc.PipelineRef{Name: pipeline.Name()}, config, pipeline.ConfigVersion(), false)
		if err != nil {
			return err.Error()
		}
	case "paused":
		if err := pipeline.Pause(""); err != nil {
			return err.Error()
		}
	default:
		return fmt.Sprintf("unknown state %q", state)
	}

	if err := db.NewResourceConfigCheckSessionLifecycle(database.Conn).CleanInactiveResourceConfigCheckSessions(); err != nil {
		return err.Error()
	}
	newID, err := findOrCreate()
	if err != nil {
		return err.Error()
	}
	wantSame := state == "active"
	if gotSame := oldID == newID; gotSame != wantSame {
		return fmt.Sprintf("old session=%d new session=%d same=%t, want same=%t", oldID, newID, gotSame, wantSame)
	}
	return ""
}

func loadCheckSessionLifecycleCheckable(pipeline db.Pipeline, kind string) (checkSessionLifecycleCheckable, error) {
	switch kind {
	case "resource":
		resource, found, err := pipeline.Resource("some-resource")
		if err != nil || !found {
			return nil, fmt.Errorf("load resource: found=%t: %w", found, err)
		}
		return resource, nil
	case "resource-type":
		resourceType, found, err := pipeline.ResourceType("some-type")
		if err != nil || !found {
			return nil, fmt.Errorf("load resource type: found=%t: %w", found, err)
		}
		return resourceType, nil
	case "prototype":
		prototype, found, err := pipeline.Prototype("some-prototype")
		if err != nil || !found {
			return nil, fmt.Errorf("load prototype: found=%t: %w", found, err)
		}
		return prototype, nil
	default:
		return nil, fmt.Errorf("unknown checkable kind %q", kind)
	}
}
