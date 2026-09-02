package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBResourceConfigFinalObservation struct {
	Profile string
	Failure string
}

func DBResourceConfigFinalStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBResourceConfigFinalObservation](
			"the production resource config evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBResourceConfigFinalObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return DBResourceConfigFinalObservation{}, fmt.Errorf("expected resource config profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBResourceConfigFinalObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBResourceConfigFinalObservation{Profile: profile, Failure: observeDBResourceConfigFinal(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBResourceConfigFinalObservation](
			"the resource config scope observation exactly matches {string}",
			func(observation DBResourceConfigFinalObservation, p brine.Params, _ *brine.Recorder) error {
				profile, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected resource config profile")
				}
				if observation.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", observation.Profile, profile)
				}
				if observation.Failure != "" {
					return fmt.Errorf("resource config scope observation: %s", observation.Failure)
				}
				return nil
			},
		),
	}
}

func observeDBResourceConfigFinal(database JetbridgeDB, profile string) string {
	oldGlobal := atc.EnableGlobalResources
	defer func() { atc.EnableGlobalResources = oldGlobal }()

	unique := profile == "unique-global-none" || profile == "unique-resource-unique"
	withResource := profile == "nonunique-resource-unique" || profile == "nonunique-resource-global" || profile == "unique-resource-unique"
	global := profile == "nonunique-resource-global"
	if profile != "nonunique-global-none" && profile != "nonunique-resource-unique" && profile != "nonunique-resource-global" && profile != "unique-global-none" && profile != "unique-resource-unique" {
		return fmt.Sprintf("unknown profile %q", profile)
	}
	atc.EnableGlobalResources = global

	factory := db.NewResourceConfigFactory(database.Conn, database.LockFactory)
	_, err := database.WorkerFactory.SaveWorker(atc.Worker{
		Name: "resource-config-final-worker", Platform: "linux", Version: "1.0", State: string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{
			{Type: "shared-base-type", Image: "example/shared", Version: "v1"},
			{Type: "unique-base-type", Image: "example/unique", Version: "v1", UniqueVersionHistory: true},
		},
	}, 0)
	if err != nil {
		return err.Error()
	}

	resourceType := "shared-base-type"
	if unique {
		resourceType = "unique-base-type"
	}
	config, err := factory.FindOrCreateResourceConfig(resourceType, atc.Source{"some": "source"}, nil)
	if err != nil {
		return err.Error()
	}
	dummy, err := factory.FindOrCreateResourceConfig(resourceType, atc.Source{"some": "dummy-source"}, nil)
	if err != nil {
		return err.Error()
	}
	if _, err := dummy.FindOrCreateScope(nil); err != nil {
		return err.Error()
	}

	var firstResourceID, secondResourceID int
	if withResource {
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "resource-config-final-team"})
		if err != nil {
			return err.Error()
		}
		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "resource-config-final-pipeline"}, atc.Config{
			Resources: atc.ResourceConfigs{
				{Name: "first-resource", Type: resourceType, Source: atc.Source{"some": "source"}},
				{Name: "second-resource", Type: resourceType, Source: atc.Source{"some": "source"}},
			},
		}, 1, false)
		if err != nil {
			return err.Error()
		}
		first, found, err := pipeline.Resource("first-resource")
		if err != nil || !found {
			return fmt.Sprintf("first resource found=%t err=%v", found, err)
		}
		second, found, err := pipeline.Resource("second-resource")
		if err != nil || !found {
			return fmt.Sprintf("second resource found=%t err=%v", found, err)
		}
		firstResourceID, secondResourceID = first.ID(), second.ID()
	}

	var resourceID *int
	if withResource {
		resourceID = &firstResourceID
	}
	before, err := resourceConfigScopeSequence(database)
	if err != nil {
		return err.Error()
	}
	created, err := config.FindOrCreateScope(resourceID)
	if err != nil {
		return err.Error()
	}
	afterCreate, err := resourceConfigScopeSequence(database)
	if err != nil {
		return err.Error()
	}
	found, err := config.FindOrCreateScope(resourceID)
	if err != nil {
		return err.Error()
	}
	afterFind, err := resourceConfigScopeSequence(database)
	if err != nil {
		return err.Error()
	}

	wantResource := withResource && !global
	if created.ResourceConfig() == nil || created.ResourceConfig().ID() != config.ID() {
		return "created scope lost its resource config"
	}
	if (created.ResourceID() != nil) != wantResource {
		return fmt.Sprintf("created resource id present=%t, want %t", created.ResourceID() != nil, wantResource)
	}
	if wantResource && *created.ResourceID() != firstResourceID {
		return fmt.Sprintf("created resource id=%d, want %d", *created.ResourceID(), firstResourceID)
	}
	if found.ID() != created.ID() || afterFind != afterCreate || afterCreate != before+1 {
		return fmt.Sprintf("scope ids=(%d,%d) sequence=(%d,%d,%d)", created.ID(), found.ID(), before, afterCreate, afterFind)
	}

	if profile == "nonunique-resource-unique" {
		other, err := config.FindOrCreateScope(&secondResourceID)
		if err != nil {
			return err.Error()
		}
		afterOther, err := resourceConfigScopeSequence(database)
		if err != nil {
			return err.Error()
		}
		if other.ID() != created.ID()+1 || other.ResourceConfig() == nil || other.ResourceConfig().ID() != config.ID() || other.ResourceID() == nil || *other.ResourceID() != secondResourceID || afterOther != afterCreate+1 {
			return fmt.Sprintf("other scope id=%d resource=%v sequence=%d", other.ID(), other.ResourceID(), afterOther)
		}
	}
	if profile == "nonunique-resource-global" {
		other, err := config.FindOrCreateScope(&secondResourceID)
		if err != nil {
			return err.Error()
		}
		afterOther, err := resourceConfigScopeSequence(database)
		if err != nil {
			return err.Error()
		}
		if other.ID() != created.ID() || other.ResourceID() != nil || other.ResourceConfig() == nil || other.ResourceConfig().ID() != config.ID() || afterOther != afterCreate {
			return fmt.Sprintf("shared scope id=%d resource=%v sequence=%d", other.ID(), other.ResourceID(), afterOther)
		}
	}
	return ""
}

func resourceConfigScopeSequence(database JetbridgeDB) (int, error) {
	var sequence int
	err := database.Conn.QueryRow("SELECT last_value FROM resource_config_scopes_id_seq").Scan(&sequence)
	return sequence, err
}
