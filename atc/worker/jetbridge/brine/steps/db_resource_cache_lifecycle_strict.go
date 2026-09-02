package steps

import (
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBResourceCacheLifecycleObservation struct {
	Profile string
	Failure string
}

func DBResourceCacheLifecycleStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBResourceCacheLifecycleObservation](
			"the production resource cache lifecycle profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBResourceCacheLifecycleObservation, error) {
				profile, err := paramAt("the production resource cache lifecycle profile {string} is exercised", p, 0)
				if err != nil {
					return DBResourceCacheLifecycleObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBResourceCacheLifecycleObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBResourceCacheLifecycleObservation{Profile: profile, Failure: observeDBResourceCacheLifecycle(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBResourceCacheLifecycleObservation](
			"the resource cache lifecycle observation exactly matches {string}",
			func(in DBResourceCacheLifecycleObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the resource cache lifecycle observation exactly matches {string}", p, 0)
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

func observeDBResourceCacheLifecycle(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	logger := lager.NewLogger("brine-resource-cache-lifecycle")
	lifecycle := db.NewResourceCacheLifecycle(database.Conn)
	w, err := newCacheGCWorld(database)
	if err != nil {
		return err.Error()
	}

	cacheCount := func() (int, error) {
		var count int
		err := database.Conn.QueryRow("SELECT COUNT(*) FROM resource_caches").Scan(&count)
		return count, err
	}
	assertCount := func(want int) string {
		got, err := cacheCount()
		if err != nil {
			return err.Error()
		}
		if got != want {
			return fail("resource cache count=%d, want %d", got, want)
		}
		return ""
	}
	cleanInvalid := func() string {
		if err := lifecycle.CleanUpInvalidCaches(logger); err != nil {
			return fail("clean invalid caches: %v", err)
		}
		return ""
	}
	cleanUses := func() string {
		if err := lifecycle.CleanUsesForFinishedBuilds(logger); err != nil {
			return fail("clean finished build uses: %v", err)
		}
		return ""
	}
	newCache := func(build db.Build, name string) (db.ResourceCache, error) {
		return w.createCache(name, build, atc.Source{"cache": name}, atc.Version{"cache": name})
	}
	newJobBuild := func(jobName string) (db.Build, error) {
		return w.startJobBuild(fmt.Sprintf("%s-%d", jobName, time.Now().UnixNano()), jobName)
	}
	switch profile {
	case "job-use-survives-dirty-clean":
		build, err := newJobBuild("some-job")
		if err != nil {
			return err.Error()
		}
		if _, err := newCache(build, "job-use"); err != nil {
			return err.Error()
		}
		if err := lifecycle.CleanDirtyInMemoryBuildUses(logger); err != nil {
			return err.Error()
		}
		if failure := cleanInvalid(); failure != "" {
			return failure
		}
		return assertCount(1)

	case "recent-memory-use-survives", "expired-memory-use-is-reclaimed":
		created := time.Now().Add(-time.Hour)
		want := 1
		if profile == "expired-memory-use-is-reclaimed" {
			created = time.Now().Add(-25 * time.Hour)
			want = 0
		}
		_, err := database.Builder.ResourceCacheFactory.FindOrCreateResourceCache(
			db.ForInMemoryBuild(99, created), "some-base-type", atc.Version{"v": profile},
			atc.Source{"profile": profile}, nil, nil,
		)
		if err != nil {
			return err.Error()
		}
		if err := lifecycle.CleanDirtyInMemoryBuildUses(logger); err != nil {
			return err.Error()
		}
		if failure := cleanInvalid(); failure != "" {
			return failure
		}
		return assertCount(want)

	case "deleted-build-cache-is-reclaimed":
		build, err := w.Team.CreateOneOffBuild()
		if err != nil {
			return err.Error()
		}
		if _, err := newCache(build, profile); err != nil {
			return err.Error()
		}
		if _, err := build.Delete(); err != nil {
			return err.Error()
		}
		if failure := cleanInvalid(); failure != "" {
			return failure
		}
		return assertCount(0)

	case "previous-job-image-is-reclaimed":
		first, err := newJobBuild("some-job")
		if err != nil {
			return err.Error()
		}
		firstCache, err := newCache(first, "first-image")
		if err != nil {
			return err.Error()
		}
		if err := first.SaveImageResourceVersion(firstCache); err != nil {
			return err.Error()
		}
		if err := first.SetInterceptible(false); err != nil {
			return err.Error()
		}
		if err := first.Finish(db.BuildStatusFailed); err != nil {
			return err.Error()
		}
		if failure := cleanUses(); failure != "" {
			return failure
		}
		second, err := newJobBuild("some-job")
		if err != nil {
			return err.Error()
		}
		secondCache, err := newCache(second, "second-image")
		if err != nil {
			return err.Error()
		}
		if err := second.SaveImageResourceVersion(secondCache); err != nil {
			return err.Error()
		}
		if err := second.SetInterceptible(false); err != nil {
			return err.Error()
		}
		if err := second.Finish(db.BuildStatusSucceeded); err != nil {
			return err.Error()
		}
		if failure := cleanUses(); failure != "" {
			return failure
		}
		if failure := cleanInvalid(); failure != "" {
			return failure
		}
		return assertCount(1)

	case "unconfigured-type-cache-is-reclaimed":
		build, err := newJobBuild("some-job")
		if err != nil {
			return err.Error()
		}
		cache, err := newCache(build, profile)
		if err != nil {
			return err.Error()
		}
		if err := build.SetInterceptible(false); err != nil {
			return err.Error()
		}
		if failure := cleanUses(); failure != "" {
			return failure
		}
		_, err = database.Builder.ResourceConfigFactory.FindOrCreateResourceConfig(
			"some-type", atc.Source{"some": "source"}, cache,
		)
		if err != nil {
			return err.Error()
		}
		if err := db.NewResourceConfigCheckSessionLifecycle(database.Conn).CleanInactiveResourceConfigCheckSessions(); err != nil {
			return err.Error()
		}
		if err := database.Builder.ResourceConfigFactory.CleanUnreferencedConfigs(0); err != nil {
			return err.Error()
		}
		if failure := cleanInvalid(); failure != "" {
			return failure
		}
		return assertCount(0)

	case "next-input-cache-survives":
		build, err := newJobBuild("some-job")
		if err != nil {
			return err.Error()
		}
		resource, err := w.checkedResource()
		if err != nil {
			return err.Error()
		}
		cache, err := w.createCache(profile, build, resource.Source(), atc.Version{"some": "version"})
		if err != nil {
			return err.Error()
		}
		if err := w.makeNextInput(cache, "some-job"); err != nil {
			return err.Error()
		}
		if err := build.Finish(db.BuildStatusSucceeded); err != nil {
			return err.Error()
		}
		if err := build.SetInterceptible(false); err != nil {
			return err.Error()
		}
		if failure := cleanUses(); failure != "" {
			return failure
		}
		if failure := cleanInvalid(); failure != "" {
			return failure
		}
		return assertCount(1)

	case "unused-resource-cache-is-reclaimed":
		build, err := newJobBuild("some-job")
		if err != nil {
			return err.Error()
		}
		if _, err := newCache(build, profile); err != nil {
			return err.Error()
		}
		if err := build.Finish(db.BuildStatusSucceeded); err != nil {
			return err.Error()
		}
		if err := build.SetInterceptible(false); err != nil {
			return err.Error()
		}
		if failure := cleanUses(); failure != "" {
			return failure
		}
		if failure := cleanInvalid(); failure != "" {
			return failure
		}
		return assertCount(0)
	default:
		return fail("unknown resource cache lifecycle profile %q", profile)
	}
}
