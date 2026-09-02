package steps

import (
	"fmt"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBResourceConfigFactoryObservation struct {
	Profile string
	Failure string
}

func DBResourceConfigFactoryStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBResourceConfigFactoryObservation](
			"the production resource config factory profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBResourceConfigFactoryObservation, error) {
				profile, err := paramAt("the production resource config factory profile {string} is exercised", p, 0)
				if err != nil {
					return DBResourceConfigFactoryObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBResourceConfigFactoryObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBResourceConfigFactoryObservation{Profile: profile, Failure: observeDBResourceConfigFactory(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBResourceConfigFactoryObservation](
			"the resource config factory observation exactly matches {string}",
			func(in DBResourceConfigFactoryObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the resource config factory observation exactly matches {string}", p, 0)
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

func observeDBResourceConfigFactory(database JetbridgeDB, profile string) string {
	factory := db.NewResourceConfigFactory(database.Conn, database.LockFactory)
	newBaseConfig := func() (db.ResourceConfig, error) {
		return factory.FindOrCreateResourceConfig("some-base-resource-type", atc.Source{"some": "unique-source"}, nil)
	}

	switch profile {
	case "recent-reference":
		resourceConfig, err := newBaseConfig()
		if err != nil {
			return err.Error()
		}
		if delta := time.Since(resourceConfig.LastReferenced()); delta < -time.Second || delta > time.Minute {
			return fmt.Sprintf("last referenced delta=%s, want within one minute", delta)
		}
		return ""

	case "idempotent-create":
		first, err := newBaseConfig()
		if err != nil {
			return err.Error()
		}
		second, err := newBaseConfig()
		if err != nil {
			return err.Error()
		}
		if first.ID() != second.ID() || !first.LastReferenced().Equal(second.LastReferenced()) {
			return fmt.Sprintf("first=(%d,%s) second=(%d,%s)", first.ID(), first.LastReferenced(), second.ID(), second.LastReferenced())
		}
		return ""

	case "cleanup-zero", "cleanup-grace":
		resourceConfig, err := newBaseConfig()
		if err != nil {
			return err.Error()
		}
		grace := time.Duration(0)
		wantSame := false
		if profile == "cleanup-grace" {
			grace = time.Hour
			wantSame = true
		}
		if err := factory.CleanUnreferencedConfigs(grace); err != nil {
			return err.Error()
		}
		recreated, err := newBaseConfig()
		if err != nil {
			return err.Error()
		}
		if gotSame := recreated.ID() == resourceConfig.ID(); gotSame != wantSame {
			return fmt.Sprintf("original=%d recreated=%d same=%t, want same=%t", resourceConfig.ID(), recreated.ID(), gotSame, wantSame)
		}
		return ""

	case "find-base-config":
		created, err := newBaseConfig()
		if err != nil {
			return err.Error()
		}
		foundConfig, found, err := factory.FindResourceConfigByID(created.ID())
		if err != nil || !found || foundConfig == nil {
			return fmt.Sprintf("found=%t nil=%t err=%v", found, foundConfig == nil, err)
		}
		if foundConfig.ID() != created.ID() || foundConfig.CreatedByBaseResourceType() == nil || created.CreatedByBaseResourceType() == nil || *foundConfig.CreatedByBaseResourceType() != *created.CreatedByBaseResourceType() {
			return fmt.Sprintf("loaded base config does not match created config %d", created.ID())
		}
		return ""

	case "find-custom-config":
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "resource-config-factory-team"})
		if err != nil {
			return err.Error()
		}
		build, err := team.CreateOneOffBuild()
		if err != nil {
			return err.Error()
		}
		cacheFactory := db.NewResourceCacheFactory(database.Conn, database.LockFactory)
		imageCache, err := cacheFactory.FindOrCreateResourceCache(db.ForBuild(build.ID()), "some-base-resource-type", atc.Version{}, atc.Source{}, nil, nil)
		if err != nil {
			return err.Error()
		}
		created, err := factory.FindOrCreateResourceConfig("some-type", atc.Source{}, imageCache)
		if err != nil {
			return err.Error()
		}
		foundConfig, found, err := factory.FindResourceConfigByID(created.ID())
		if err != nil || !found || foundConfig == nil {
			return fmt.Sprintf("found=%t nil=%t err=%v", found, foundConfig == nil, err)
		}
		foundCache, createdCache := foundConfig.CreatedByResourceCache(), created.CreatedByResourceCache()
		if foundCache == nil || createdCache == nil || foundCache.ID() != createdCache.ID() || foundCache.ResourceConfig().ID() != createdCache.ResourceConfig().ID() {
			return fmt.Sprintf("loaded custom config does not match created config %d", created.ID())
		}
		return ""

	case "find-missing-config":
		resourceConfig, found, err := factory.FindResourceConfigByID(123)
		if err != nil || found || resourceConfig != nil {
			return fmt.Sprintf("found=%t nil=%t err=%v", found, resourceConfig == nil, err)
		}
		return ""

	case "concurrent-delete-create":
		database.Conn.SetMaxOpenConns(2)
		done := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					if err := factory.CleanUnreferencedConfigs(0); err != nil {
						errs <- err
						return
					}
				}
			}
		}()
		go func() {
			defer wg.Done()
			defer close(done)
			for i := 0; i < 100; i++ {
				if _, err := newBaseConfig(); err != nil {
					errs <- err
					return
				}
			}
		}()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err.Error()
			}
		}
		return ""

	default:
		return fmt.Sprintf("unknown profile %q", profile)
	}
}
