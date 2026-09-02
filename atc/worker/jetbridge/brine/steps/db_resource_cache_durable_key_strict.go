package steps

import (
	"fmt"
	"regexp"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBResourceCacheDurableKeyObservation struct {
	Profile string
	Failure string
}

func DBResourceCacheDurableKeyStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBResourceCacheDurableKeyObservation](
			"the production resource cache durable-key profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBResourceCacheDurableKeyObservation, error) {
				profile, err := paramAt("the production resource cache durable-key profile {string} is exercised", p, 0)
				if err != nil {
					return DBResourceCacheDurableKeyObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBResourceCacheDurableKeyObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBResourceCacheDurableKeyObservation{
					Profile: profile,
					Failure: observeDBResourceCacheDurableKey(database, profile),
				}, nil
			},
		),
		brine.DefineCheck[DBResourceCacheDurableKeyObservation](
			"the resource cache durable-key observation exactly matches {string}",
			func(in DBResourceCacheDurableKeyObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the resource cache durable-key observation exactly matches {string}", p, 0)
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

func observeDBResourceCacheDurableKey(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	team, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
	if err != nil {
		return err.Error()
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return err.Error()
	}
	tx, err := database.Conn.Begin()
	if err != nil {
		return err.Error()
	}
	if _, err = (db.BaseResourceType{Name: "some-base-type"}).FindOrCreate(tx, false); err != nil {
		_ = tx.Rollback()
		return err.Error()
	}
	if err = tx.Commit(); err != nil {
		return err.Error()
	}

	find := func(typeName string, version atc.Version, source atc.Source, params atc.Params, parent db.ResourceCache) (db.ResourceCache, error) {
		return database.Builder.ResourceCacheFactory.FindOrCreateResourceCache(
			db.ForBuild(build.ID()), typeName, version, source, params, parent,
		)
	}
	base := func() (db.ResourceCache, error) {
		return find("some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, nil)
	}

	switch profile {
	case "survives-delete-and-recreate":
		first, err := base()
		if err != nil {
			return err.Error()
		}
		if first.DurableKey() == "" {
			return "first durable key is empty"
		}
		if _, err = database.Conn.Exec("DELETE FROM resource_cache_uses WHERE resource_cache_id = $1", first.ID()); err != nil {
			return err.Error()
		}
		if _, err = database.Conn.Exec("DELETE FROM resource_caches WHERE id = $1", first.ID()); err != nil {
			return err.Error()
		}
		second, err := base()
		if err != nil {
			return err.Error()
		}
		if second.ID() == first.ID() {
			return fail("recreated row retained id %d", first.ID())
		}
		if second.DurableKey() != first.DurableKey() {
			return fail("recreated key=%q, want %q", second.DurableKey(), first.DurableKey())
		}
		return ""

	case "distinguishes-version", "distinguishes-source", "distinguishes-params":
		first, err := base()
		if err != nil {
			return err.Error()
		}
		typeName := "some-base-type"
		version := atc.Version{"v": "1"}
		source := atc.Source{"s": "x"}
		params := atc.Params{"p": "y"}
		switch profile {
		case "distinguishes-version":
			version = atc.Version{"v": "2"}
		case "distinguishes-source":
			source = atc.Source{"s": "z"}
		case "distinguishes-params":
			params = atc.Params{"p": "z"}
		}
		other, err := find(typeName, version, source, params, nil)
		if err != nil {
			return err.Error()
		}
		if other.DurableKey() == first.DurableKey() {
			return fail("different cache has same key %q", first.DurableKey())
		}
		return ""

	case "distinguishes-custom-type-parent":
		typeV1, err := find("some-base-type", atc.Version{"type": "1"}, atc.Source{"t": "s"}, nil, nil)
		if err != nil {
			return err.Error()
		}
		typeV2, err := find("some-base-type", atc.Version{"type": "2"}, atc.Source{"t": "s"}, nil, nil)
		if err != nil {
			return err.Error()
		}
		if typeV1.DurableKey() == typeV2.DurableKey() {
			return "custom type versions have the same key"
		}
		viaV1, err := find("custom", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, typeV1)
		if err != nil {
			return err.Error()
		}
		viaV2, err := find("custom", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, typeV2)
		if err != nil {
			return err.Error()
		}
		if viaV1.DurableKey() == viaV2.DurableKey() {
			return fail("custom caches have the same key %q", viaV1.DurableKey())
		}
		return ""

	case "readable-after-load-by-id":
		created, err := base()
		if err != nil {
			return err.Error()
		}
		loaded, found, err := database.Builder.ResourceCacheFactory.FindResourceCacheByID(created.ID())
		if err != nil {
			return err.Error()
		}
		if !found {
			return fail("cache %d not found", created.ID())
		}
		if loaded.DurableKey() != created.DurableKey() {
			return fail("loaded key=%q, want %q", loaded.DurableKey(), created.DurableKey())
		}
		return ""

	case "backfills-pre-column-row":
		created, err := base()
		if err != nil {
			return err.Error()
		}
		if _, err = database.Conn.Exec("UPDATE resource_caches SET durable_key = NULL WHERE id = $1", created.ID()); err != nil {
			return err.Error()
		}
		refound, err := base()
		if err != nil {
			return err.Error()
		}
		if refound.ID() != created.ID() {
			return fail("refound id=%d, want %d", refound.ID(), created.ID())
		}
		if refound.DurableKey() != created.DurableKey() {
			return fail("refound key=%q, want %q", refound.DurableKey(), created.DurableKey())
		}
		var stored string
		if err = database.Conn.QueryRow("SELECT durable_key FROM resource_caches WHERE id = $1", created.ID()).Scan(&stored); err != nil {
			return err.Error()
		}
		if stored != created.DurableKey() {
			return fail("stored key=%q, want %q", stored, created.DurableKey())
		}
		return ""

	case "accepted-artifact-key-format":
		cache, err := base()
		if err != nil {
			return err.Error()
		}
		if !regexp.MustCompile(`^rc-[0-9a-f]{64}$`).MatchString(cache.DurableKey()) {
			return fail("key %q does not match artifact format", cache.DurableKey())
		}
		return ""
	default:
		return fail("unknown resource cache durable-key profile %q", profile)
	}
}
