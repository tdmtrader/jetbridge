package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/gc"
)

// GCCacheDefinitions migrates the four collectors that reclaim the rows a
// finished build leaves behind: atc/gc/artifacts_collector_test.go,
// resource_cache_use_collector_test.go, resource_cache_collector_test.go and
// resource_config_collector_test.go.
//
// There is no double anywhere in this file, and there was none in the sources
// either — every one of those suites already ran against real PostgreSQL. That
// removes this programme's usual payoff (replace the recording double with a
// working one) and leaves only the sentence, which is why the feature file
// spends more words on WHY each scenario exists than on what it does.
//
// Two things are shared across all four collectors and so are built once:
//
//   - The world. gc_suite_test.go gives every spec a team, a pipeline with one
//     resource, one resource type and two jobs, and a one-off build. The
//     scenarios below build the same shape, because the four rules under test
//     are all stated in terms of it: a config is referenced BY A RESOURCE, a
//     cache is an input TO A JOB, an image cache belongs to A JOB BUILD.
//
//   - The sweep. Three of these collectors are only meaningful in a chain —
//     the ginkgo suite's JustBeforeEach runs the build collector, then the
//     cache-use collector, then the cache collector, because a cache is not
//     collectable until its use is gone and a use is not collectable until its
//     build is non-interceptible. So the Given names the chain and the When is
//     a single sentence for all four.
//
// Rows are anonymous in three of these tables — resource_caches and
// resource_configs have no name column — so the state carries a name-to-id map
// both ways and every check reports the scenario's own vocabulary. A row
// nothing named is printed raw rather than dropped: an unexplained survivor is
// exactly what a reader needs to see.

// configGracePeriod is the window the ResourceConfigCollector is constructed
// with. It matches gc_suite_test.go's `var gracePeriod = time.Hour`, and it is
// load-bearing twice over: a config is only collectable once it has been
// unreferenced for longer than this, and the ginkgo suite's "referenced in
// resource config check sessions" case is spared by this window alone. See the
// feature file.
const configGracePeriod = time.Hour

// gcRunner is the shape every collector in atc/gc shares.
type gcRunner interface{ Run(context.Context) error }

// gcPipelineConfig mirrors gc_suite_test.go's atcConfig. The two jobs are not
// decoration: "a later build of the SAME job" and "a later build of ANOTHER
// job" are the two halves of the image-cache rule, and nothing distinguishes
// them in a one-job pipeline.
var gcPipelineConfig = atc.Config{
	Resources: atc.ResourceConfigs{
		{Name: "some-resource", Type: "some-base-type", Source: atc.Source{"some": "source"}},
	},
	ResourceTypes: atc.ResourceTypes{
		{Name: "some-resource-type", Type: "some-base-type", Source: atc.Source{"some": "source-type"}},
	},
	Jobs: atc.JobConfigs{
		{Name: "some-job"},
		{Name: "some-other-job"},
	},
}

// CacheGCReady is the world a sweep is about to run against: the collector
// chain the Given chose, and the rows the scenario has built for it.
type CacheGCReady struct {
	DB  JetbridgeDB
	Ctx context.Context

	Team     db.Team
	Pipeline db.Pipeline

	collectors []gcRunner

	builds    map[string]db.Build
	buildName map[int]string

	caches    map[string]db.ResourceCache
	cacheName map[int]string

	configs    map[string]db.ResourceConfig
	configName map[int]string
}

// CacheGCSwept is a completed pass: whether the chain was refused, and the
// world it ran against, which the checks read the tables through.
type CacheGCSwept struct {
	Ready CacheGCReady
	Err   error
}

func GCCacheDefinitions() []brine.StepDefinition {
	defs := cacheGCSetupDefinitions()
	defs = append(defs, cacheGCBuildDefinitions()...)
	defs = append(defs, cacheGCCacheDefinitions()...)
	defs = append(defs, cacheGCConfigDefinitions()...)
	defs = append(defs, cacheGCCheckDefinitions()...)
	return defs
}

// -----------------------------------------------------------------------
// The four collector chains, and the one sweep
// -----------------------------------------------------------------------

func cacheGCSetupDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		collectorChain("a garbage collector for expired build artifacts",
			func(database JetbridgeDB) []gcRunner {
				return []gcRunner{gc.NewArtifactCollector(db.NewArtifactLifecycle(database.Conn))}
			}),

		// The build collector runs first because a use is only collectable
		// once its build is non-interceptible, and for a SUCCEEDED build that
		// is the build collector's doing. For a failed or aborted one, Finish
		// already did it — which is the distinction the outline over build
		// states exists to keep.
		collectorChain("a garbage collector for resource cache uses",
			func(database JetbridgeDB) []gcRunner {
				return []gcRunner{
					gc.NewBuildCollector(database.BuildFactory),
					gc.NewResourceCacheUseCollector(db.NewResourceCacheLifecycle(database.Conn)),
				}
			}),

		collectorChain("a garbage collector for unreferenced resource configs",
			func(database JetbridgeDB) []gcRunner {
				return []gcRunner{gc.NewResourceConfigCollector(
					db.NewResourceConfigFactory(database.Conn, database.LockFactory),
					configGracePeriod,
				)}
			}),

		// The full chain, in the order gc_suite_test.go's JustBeforeEach runs
		// it. A cache survives on any one of five references, and the two that
		// the earlier collectors dissolve — the use row and the build-image row
		// — are why this cannot be one collector in isolation.
		collectorChain("a garbage collector for resource caches",
			func(database JetbridgeDB) []gcRunner {
				lifecycle := db.NewResourceCacheLifecycle(database.Conn)
				return []gcRunner{
					gc.NewBuildCollector(database.BuildFactory),
					gc.NewResourceCacheUseCollector(lifecycle),
					gc.NewResourceCacheCollector(lifecycle),
				}
			}),

		// Rows are aged relative to the database's own clock rather than the
		// scenario waiting to become old. `created_at` has no factory that
		// lets a caller choose it, so the row is inserted directly — the same
		// reason artifacts_collector_test.go inserts its own.
		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the artifact {string} was created {string} ago",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				pattern := "the artifact {string} was created {string} ago"
				name, age, err := twoParams(pattern, p)
				if err != nil {
					return CacheGCReady{}, err
				}
				_, err = in.DB.Conn.Exec(
					`INSERT INTO worker_artifacts(name, created_at) VALUES($1, NOW() - $2::interval)`,
					name, age)
				if err != nil {
					return CacheGCReady{}, fmt.Errorf("insert artifact %q aged %q: %w", name, age, err)
				}
				return in, nil
			},
		),

		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the pipeline is paused",
			func(in CacheGCReady, _ brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				if err := in.Pipeline.Pause(""); err != nil {
					return CacheGCReady{}, fmt.Errorf("pause the pipeline: %w", err)
				}
				return in, nil
			},
		),

		// Not a no-op standing in for "do nothing": it really unpauses, so the
		// row that expects the cache to survive is asserting against a state
		// something set rather than against a default it never checked.
		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the pipeline is not paused",
			func(in CacheGCReady, _ brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				if err := in.Pipeline.Unpause(); err != nil {
					return CacheGCReady{}, fmt.Errorf("unpause the pipeline: %w", err)
				}
				return in, nil
			},
		),

		brine.DefineMap[CacheGCReady, CacheGCSwept](
			"garbage collection runs",
			func(in CacheGCReady, _ brine.Params, _ *brine.Recorder) (CacheGCSwept, error) {
				for _, collector := range in.collectors {
					if err := collector.Run(in.Ctx); err != nil {
						return CacheGCSwept{Ready: in, Err: err}, nil
					}
				}
				return CacheGCSwept{Ready: in}, nil
			},
		),
	}
}

func collectorChain(pattern string, chain func(JetbridgeDB) []gcRunner) brine.StepDefinition {
	return brine.DefineMapUsing[brine.Empty, CacheGCReady](
		pattern,
		[]string{"jetbridge-db"},
		func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (CacheGCReady, error) {
			database, ok := res.Get("jetbridge-db").(JetbridgeDB)
			if !ok {
				return CacheGCReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
			}
			world, err := newCacheGCWorld(database)
			if err != nil {
				return CacheGCReady{}, err
			}
			world.collectors = chain(database)
			return world, nil
		},
	)
}

func newCacheGCWorld(database JetbridgeDB) (CacheGCReady, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "gc-cache-team"})
	if err != nil {
		return CacheGCReady{}, fmt.Errorf("create team: %w", err)
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "some-pipeline"}, gcPipelineConfig, db.ConfigVersion(0), false)
	if err != nil {
		return CacheGCReady{}, fmt.Errorf("save pipeline: %w", err)
	}
	return CacheGCReady{
		DB:         database,
		Ctx:        context.Background(),
		Team:       team,
		Pipeline:   pipeline,
		builds:     map[string]db.Build{},
		buildName:  map[int]string{},
		caches:     map[string]db.ResourceCache{},
		cacheName:  map[int]string{},
		configs:    map[string]db.ResourceConfig{},
		configName: map[int]string{},
	}, nil
}

// -----------------------------------------------------------------------
// Builds
// -----------------------------------------------------------------------

// The build states are separate sentences rather than one parameterised one
// for the same reason the pilot's worker states are: each names a distinct
// REASON a build's rows are or are not garbage, and a reader of an Examples
// table should see the reason without decoding a column of enum values.
//
// The three reasons, which the ginkgo suite ran through one JustBeforeEach and
// so could not tell apart:
//
//	still running  interceptible stays true; nothing is collectable
//	succeeded      Finish leaves interceptible true; the BUILD COLLECTOR
//	               clears it, subject to its grace periods
//	failed/aborted Finish clears interceptible ITSELF, immediately, because a
//	               non-succeeded build has no container state worth hijacking
//
// The third is why "the latest failed build of a job" is a row of its own:
// the build collector deliberately spares the latest completed build of a job
// (constructBuildFilter), so if Finish did not clear the flag that build's
// uses would leak until the failed grace period elapsed.

func cacheGCBuildDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		oneOffBuild("the build {string} is a one-off build that is still running", nil, false),
		oneOffBuild("the build {string} is a one-off build that succeeded", finishAs(db.BuildStatusSucceeded), false),
		oneOffBuild("the build {string} is a one-off build that failed", finishAs(db.BuildStatusFailed), false),
		oneOffBuild("the build {string} is a one-off build that was aborted", finishAs(db.BuildStatusAborted), false),
		// The 24-hour arm of CleanBuildImageResourceCaches is on the BUILD's
		// end_time, so the build is backdated rather than the image row.
		oneOffBuild("the build {string} is a one-off build that succeeded more than a day ago",
			finishAs(db.BuildStatusSucceeded), true),

		jobBuild("the build {string} is a build of the job {string} that succeeded", db.BuildStatusSucceeded, false),
		jobBuild("the build {string} is a build of the job {string} that failed", db.BuildStatusFailed, false),
		// A job build old enough for the day-old image rule to have reached it,
		// had that rule not been restricted to one-off builds. Without the age
		// the sibling in that scenario proves nothing: the 24-hour predicate
		// would spare it whether or not the job_id predicate existed.
		jobBuild("the build {string} is a build of the job {string} that succeeded more than a day ago",
			db.BuildStatusSucceeded, true),

		// The image recorded BEFORE the build finishes, which is the order
		// resource_cache_collector_test.go used and the order that matters:
		// Build.Finish deletes its job's image records with a LOWER build id,
		// and a build whose own record does not yet exist cannot show whether
		// that comparison is strict.
		jobBuildRecordingImage(
			"the build {string} is a build of the job {string} that recorded the image cache {string} and then succeeded",
			db.BuildStatusSucceeded),
		jobBuildRecordingImage(
			"the build {string} is a build of the job {string} that recorded the image cache {string} and then failed",
			db.BuildStatusFailed),
	}
}

func finishAs(status db.BuildStatus) func(db.Build) error {
	return func(build db.Build) error { return build.Finish(status) }
}

func oneOffBuild(pattern string, finish func(db.Build) error, backdate bool) brine.StepDefinition {
	return brine.DefineMap[CacheGCReady, CacheGCReady](pattern,
		func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return CacheGCReady{}, err
			}
			build, err := in.Team.CreateOneOffBuild()
			if err != nil {
				return CacheGCReady{}, fmt.Errorf("create one-off build %q: %w", name, err)
			}
			if finish != nil {
				if err := finish(build); err != nil {
					return CacheGCReady{}, fmt.Errorf("finish build %q: %w", name, err)
				}
			}
			if backdate {
				if _, err := in.DB.Conn.Exec(
					`UPDATE builds SET end_time = NOW() - '25 hours'::interval WHERE id = $1`,
					build.ID()); err != nil {
					return CacheGCReady{}, fmt.Errorf("backdate build %q: %w", name, err)
				}
			}
			in.remember(name, build)
			return in, nil
		},
	)
}

func jobBuild(pattern string, status db.BuildStatus, backdate bool) brine.StepDefinition {
	return brine.DefineMap[CacheGCReady, CacheGCReady](pattern,
		func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
			name, jobName, err := twoParams(pattern, p)
			if err != nil {
				return CacheGCReady{}, err
			}
			build, err := in.startJobBuild(name, jobName)
			if err != nil {
				return CacheGCReady{}, err
			}
			if err := build.Finish(status); err != nil {
				return CacheGCReady{}, fmt.Errorf("finish build %q: %w", name, err)
			}
			if backdate {
				if _, err := in.DB.Conn.Exec(
					`UPDATE builds SET end_time = NOW() - '25 hours'::interval WHERE id = $1`,
					build.ID()); err != nil {
					return CacheGCReady{}, fmt.Errorf("backdate build %q: %w", name, err)
				}
			}
			return in, nil
		},
	)
}

// jobBuildRecordingImage creates a job build, records its image cache, and only
// then finishes it — the order the ginkgo suite used, and the one that puts the
// build's own image record in reach of the delete Finish performs.
func jobBuildRecordingImage(pattern string, status db.BuildStatus) brine.StepDefinition {
	return brine.DefineMap[CacheGCReady, CacheGCReady](pattern,
		func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return CacheGCReady{}, err
			}
			jobName, err := paramAt(pattern, p, 1)
			if err != nil {
				return CacheGCReady{}, err
			}
			cacheName, err := paramAt(pattern, p, 2)
			if err != nil {
				return CacheGCReady{}, err
			}

			build, err := in.startJobBuild(name, jobName)
			if err != nil {
				return CacheGCReady{}, err
			}
			cache, err := in.createCache(cacheName, build,
				atc.Source{"cache": cacheName}, atc.Version{"cache": cacheName})
			if err != nil {
				return CacheGCReady{}, err
			}
			if err := build.SaveImageResourceVersion(cache); err != nil {
				return CacheGCReady{}, fmt.Errorf("record %q as the image for build %q: %w", cacheName, name, err)
			}
			if err := build.Finish(status); err != nil {
				return CacheGCReady{}, fmt.Errorf("finish build %q: %w", name, err)
			}
			return in, nil
		},
	)
}

func (w CacheGCReady) startJobBuild(name, jobName string) (db.Build, error) {
	job, found, err := w.Pipeline.Job(jobName)
	if err != nil {
		return nil, fmt.Errorf("look up job %q: %w", jobName, err)
	}
	if !found {
		return nil, fmt.Errorf("the pipeline has no job %q", jobName)
	}
	build, err := job.CreateBuild("someone")
	if err != nil {
		return nil, fmt.Errorf("create build %q of job %q: %w", name, jobName, err)
	}
	w.remember(name, build)
	return build, nil
}

func (w CacheGCReady) remember(name string, build db.Build) {
	w.builds[name] = build
	w.buildName[build.ID()] = name
}

func (w CacheGCReady) build(name string) (db.Build, error) {
	build, ok := w.builds[name]
	if !ok {
		return nil, fmt.Errorf("no build named %q was created by this scenario", name)
	}
	return build, nil
}

// -----------------------------------------------------------------------
// Resource caches
// -----------------------------------------------------------------------

func cacheGCCacheDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// A plain cache: its own source, so each named cache is a distinct row
		// and a scenario can talk about one without the others moving.
		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the cache {string} was created for the build {string}",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				name, buildName, err := twoParams("the cache {string} was created for the build {string}", p)
				if err != nil {
					return CacheGCReady{}, err
				}
				build, err := in.build(buildName)
				if err != nil {
					return CacheGCReady{}, err
				}
				_, err = in.createCache(name, build, atc.Source{"cache": name}, atc.Version{"cache": name})
				return in, err
			},
		),

		// The image a build ran on. build_image_resource_caches is the fourth
		// of the five references that keep a cache alive, and the only one that
		// outlives the build's own use row — which is the whole point of the
		// image-cache scenarios: they run after the use has already gone.
		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the cache {string} was recorded as the image for the build {string}",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				name, buildName, err := twoParams("the cache {string} was recorded as the image for the build {string}", p)
				if err != nil {
					return CacheGCReady{}, err
				}
				build, err := in.build(buildName)
				if err != nil {
					return CacheGCReady{}, err
				}
				cache, err := in.createCache(name, build, atc.Source{"cache": name}, atc.Version{"cache": name})
				if err != nil {
					return CacheGCReady{}, err
				}
				if err := build.SaveImageResourceVersion(cache); err != nil {
					return CacheGCReady{}, fmt.Errorf("record %q as the image for build %q: %w", name, buildName, err)
				}
				return in, nil
			},
		),

		// This one must NOT get its own source. The next-build-inputs arm joins
		// resource_caches to the RESOURCE's config and to a version in the
		// resource's scope, so a cache with a source of its own would never
		// match and the scenario would pass for the wrong reason. Checking the
		// resource first is what gives it a config to share.
		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the cache {string} holds the pipeline resource's version for the build {string}",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				pattern := "the cache {string} holds the pipeline resource's version for the build {string}"
				name, buildName, err := twoParams(pattern, p)
				if err != nil {
					return CacheGCReady{}, err
				}
				build, err := in.build(buildName)
				if err != nil {
					return CacheGCReady{}, err
				}
				resource, err := in.checkedResource()
				if err != nil {
					return CacheGCReady{}, err
				}
				_, err = in.createCache(name, build, resource.Source(), atc.Version{"some": "version"})
				return in, err
			},
		),

		// What the scheduler has decided the job will run next. The version row
		// is inserted directly, exactly as resource_cache_collector_test.go
		// does, because SaveNextInputMapping names a version by digest and
		// there is no factory that produces one for an arbitrary version in a
		// scope that has never been checked for real.
		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the cache {string} is the next input for the job {string}",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				pattern := "the cache {string} is the next input for the job {string}"
				name, jobName, err := twoParams(pattern, p)
				if err != nil {
					return CacheGCReady{}, err
				}
				cache, ok := in.caches[name]
				if !ok {
					return CacheGCReady{}, fmt.Errorf("no cache named %q was created by this scenario", name)
				}
				return in, in.makeNextInput(cache, jobName)
			},
		),
	}
}

// createCache creates a resource cache used by a build, and records the name
// the scenario gave it in both directions.
func (w CacheGCReady) createCache(name string, build db.Build, source atc.Source, version atc.Version) (db.ResourceCache, error) {
	cache, err := w.DB.Builder.ResourceCacheFactory.FindOrCreateResourceCache(
		db.ForBuild(build.ID()),
		"some-base-type",
		version,
		source,
		nil,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create cache %q: %w", name, err)
	}
	w.caches[name] = cache
	w.cacheName[cache.ID()] = name
	return cache, nil
}

// makeNextInput puts the cache's version into the resource's scope and tells
// the job that version is its next input.
func (w CacheGCReady) makeNextInput(cache db.ResourceCache, jobName string) error {
	resource, err := w.checkedResource()
	if err != nil {
		return err
	}

	marshaled, err := json.Marshal(cache.Version())
	if err != nil {
		return fmt.Errorf("marshal the cache's version: %w", err)
	}

	// The version JSON is bound twice rather than once. It lands in a jsonb
	// column and it is also hashed as text, and a single parameter used both
	// ways makes PostgreSQL deduce two types for it and refuse the statement.
	// The digest has to be over the raw string, because that is what
	// resource_caches.version_digest is computed from and what the collector's
	// join compares against.
	var versionSHA256 string
	err = w.DB.Conn.QueryRow(
		`INSERT INTO resource_config_versions (version, version_sha256, metadata, resource_config_scope_id)
		 VALUES ($1::jsonb, encode(digest($2::text, 'sha256'), 'hex'), 'null'::jsonb, $3)
		 RETURNING version_sha256`,
		string(marshaled), string(marshaled), resource.ResourceConfigScopeID(),
	).Scan(&versionSHA256)
	if err != nil {
		return fmt.Errorf("save the version the job will run next: %w", err)
	}

	job, found, err := w.Pipeline.Job(jobName)
	if err != nil {
		return fmt.Errorf("look up job %q: %w", jobName, err)
	}
	if !found {
		return fmt.Errorf("the pipeline has no job %q", jobName)
	}

	err = job.SaveNextInputMapping(db.InputMapping{
		"whatever": db.InputResult{
			Input: &db.AlgorithmInput{
				AlgorithmVersion: db.AlgorithmVersion{
					Version:    db.ResourceVersion(versionSHA256),
					ResourceID: resource.ID(),
				},
			},
		},
	}, true)
	if err != nil {
		return fmt.Errorf("save the next input for job %q: %w", jobName, err)
	}
	return nil
}

// checkedResource returns the pipeline's resource with a config and a scope
// attached, running a check for it the first time it is asked for. A resource
// straight out of SavePipeline has resource_config_id NULL — it references
// nothing until something checks it — which is itself the reason the config
// scenarios have to do this rather than assume.
func (w CacheGCReady) checkedResource() (db.Resource, error) {
	resource, found, err := w.Pipeline.Resource("some-resource")
	if err != nil {
		return nil, fmt.Errorf("look up the pipeline's resource: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("the pipeline has no resource %q", "some-resource")
	}
	if resource.ResourceConfigID() != 0 {
		return resource, nil
	}

	scenario := &dbtest.Scenario{Team: w.Team, Pipeline: w.Pipeline}
	if err := w.DB.Builder.WithResourceVersions("some-resource")(scenario); err != nil {
		return nil, fmt.Errorf("check the pipeline's resource: %w", err)
	}

	resource, found, err = w.Pipeline.Resource("some-resource")
	if err != nil {
		return nil, fmt.Errorf("re-read the pipeline's resource: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("the pipeline lost its resource after a check")
	}
	if resource.ResourceConfigID() == 0 {
		return nil, fmt.Errorf("the pipeline's resource still references no config after a check")
	}
	return resource, nil
}

func (w CacheGCReady) checkedResourceType() (db.ResourceType, error) {
	resourceType, found, err := w.Pipeline.ResourceType("some-resource-type")
	if err != nil {
		return nil, fmt.Errorf("look up the pipeline's resource type: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("the pipeline has no resource type %q", "some-resource-type")
	}
	if resourceType.ResourceConfigID() != 0 {
		return resourceType, nil
	}

	scenario := &dbtest.Scenario{Team: w.Team, Pipeline: w.Pipeline}
	if err := w.DB.Builder.WithResourceTypeVersions("some-resource-type")(scenario); err != nil {
		return nil, fmt.Errorf("check the pipeline's resource type: %w", err)
	}

	resourceType, found, err = w.Pipeline.ResourceType("some-resource-type")
	if err != nil {
		return nil, fmt.Errorf("re-read the pipeline's resource type: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("the pipeline lost its resource type after a check")
	}
	if resourceType.ResourceConfigID() == 0 {
		return nil, fmt.Errorf("the pipeline's resource type still references no config after a check")
	}
	return resourceType, nil
}

// -----------------------------------------------------------------------
// Resource configs
// -----------------------------------------------------------------------

func cacheGCConfigDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// A config reached through a cache. Note which factory: the cache path
		// calls findOrCreateResourceConfig with updateLastReferenced FALSE, so
		// this config's last_referenced stays at the column's 1970 default and
		// it is past any grace period the moment it exists. The scenarios age
		// every config anyway, so no reader has to know that — but it is why
		// the ginkgo suite's cache cases collect at all.
		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the config {string} is referenced by a resource cache",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				name, err := paramAt("the config {string} is referenced by a resource cache", p, 0)
				if err != nil {
					return CacheGCReady{}, err
				}
				build, err := in.Team.CreateOneOffBuild()
				if err != nil {
					return CacheGCReady{}, fmt.Errorf("create a build to hold the cache for %q: %w", name, err)
				}
				cache, err := in.createCache("cache-for-"+name, build,
					atc.Source{"config": name}, atc.Version{"config": name})
				if err != nil {
					return CacheGCReady{}, err
				}
				in.rememberConfig(name, cache.ResourceConfig())
				return in, nil
			},
		),

		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the config {string} is referenced by a pipeline resource",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				name, err := paramAt("the config {string} is referenced by a pipeline resource", p, 0)
				if err != nil {
					return CacheGCReady{}, err
				}
				resource, err := in.checkedResource()
				if err != nil {
					return CacheGCReady{}, err
				}
				in.nameConfigID(name, resource.ResourceConfigID())
				return in, nil
			},
		),

		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the config {string} is referenced by a pipeline resource type",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				name, err := paramAt("the config {string} is referenced by a pipeline resource type", p, 0)
				if err != nil {
					return CacheGCReady{}, err
				}
				resourceType, err := in.checkedResourceType()
				if err != nil {
					return CacheGCReady{}, err
				}
				in.nameConfigID(name, resourceType.ResourceConfigID())
				return in, nil
			},
		),

		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the config {string} is referenced by nothing",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				name, err := paramAt("the config {string} is referenced by nothing", p, 0)
				if err != nil {
					return CacheGCReady{}, err
				}
				_, err = in.orphanConfig(name)
				return in, err
			},
		),

		// A container checking a resource holds its config through
		// resource_config_check_sessions, and that foreign key is ON DELETE
		// RESTRICT — the only one of these references the collector's own
		// query does not know about.
		brine.DefineMap[CacheGCReady, CacheGCReady](
			"the config {string} is held by a container's check session",
			func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
				name, err := paramAt("the config {string} is held by a container's check session", p, 0)
				if err != nil {
					return CacheGCReady{}, err
				}
				config, err := in.orphanConfig(name)
				if err != nil {
					return CacheGCReady{}, err
				}

				worker, err := in.DB.WorkerFactory.SaveWorker(atc.Worker{
					Name: "check-session-worker",
					ResourceTypes: []atc.WorkerResourceType{{
						Type:    "some-base-type",
						Image:   "/path/to/image",
						Version: "some-brt-version",
					}},
				}, 0)
				if err != nil {
					return CacheGCReady{}, fmt.Errorf("register the worker holding the check session: %w", err)
				}

				_, err = worker.CreateContainer(
					db.NewResourceConfigCheckSessionContainerOwner(
						config.ID(),
						config.OriginBaseResourceType().ID,
						db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: 10 * time.Minute},
					),
					db.ContainerMetadata{},
				)
				if err != nil {
					return CacheGCReady{}, fmt.Errorf("open a check session on config %q: %w", name, err)
				}
				return in, nil
			},
		),

		ageConfigs("the config {string} was last referenced longer ago than the grace period", false),
		ageConfigs("every config was last referenced longer ago than the grace period", true),
	}
}

// ageConfigs backdates last_referenced past the grace period. Backdating the
// column is what resource_config_collector_test.go does too, and its comment
// there — "tightly coupled but better than a flaky sleep test" — is still the
// honest description: the alternative is a scenario that waits an hour.
func ageConfigs(pattern string, all bool) brine.StepDefinition {
	return brine.DefineMap[CacheGCReady, CacheGCReady](pattern,
		func(in CacheGCReady, p brine.Params, _ *brine.Recorder) (CacheGCReady, error) {
			aged := fmt.Sprintf("NOW() - '%d seconds'::interval", int(configGracePeriod.Seconds())+60)
			if all {
				if _, err := in.DB.Conn.Exec(`UPDATE resource_configs SET last_referenced = ` + aged); err != nil {
					return CacheGCReady{}, fmt.Errorf("age every config: %w", err)
				}
				return in, nil
			}

			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return CacheGCReady{}, err
			}
			config, ok := in.configs[name]
			if !ok {
				return CacheGCReady{}, fmt.Errorf("no config named %q was created by this scenario", name)
			}
			if _, err := in.DB.Conn.Exec(
				`UPDATE resource_configs SET last_referenced = `+aged+` WHERE id = $1`,
				config.ID()); err != nil {
				return CacheGCReady{}, fmt.Errorf("age config %q: %w", name, err)
			}
			return in, nil
		},
	)
}

func (w CacheGCReady) orphanConfig(name string) (db.ResourceConfig, error) {
	config, err := w.DB.Builder.ResourceConfigFactory.FindOrCreateResourceConfig(
		"some-base-type", atc.Source{"config": name}, nil)
	if err != nil {
		return nil, fmt.Errorf("create config %q: %w", name, err)
	}
	w.rememberConfig(name, config)
	return config, nil
}

func (w CacheGCReady) rememberConfig(name string, config db.ResourceConfig) {
	w.configs[name] = config
	w.configName[config.ID()] = name
}

// nameConfigID names a config the scenario did not create directly — a
// resource's or a resource type's, which only exist as an id on that row.
func (w CacheGCReady) nameConfigID(name string, id int) {
	w.configName[id] = name
}

// -----------------------------------------------------------------------
// What the sweep left behind
// -----------------------------------------------------------------------

func cacheGCCheckDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		CheckThat[CacheGCSwept]("garbage collection completed without error",
			func(in CacheGCSwept) error {
				if in.Err != nil {
					return fmt.Errorf("a collector in the chain failed: %v", in.Err)
				}
				return nil
			}),

		CheckMember[CacheGCSwept]("the artifact {string} survived the sweep",
			"the artifact rows still in the database",
			func(in CacheGCSwept) ([]string, error) { return in.artifactNames() }),

		CheckNotMember[CacheGCSwept]("the artifact {string} has been reclaimed",
			"the artifact rows still in the database",
			func(in CacheGCSwept) ([]string, error) { return in.artifactNames() }),

		CheckMember[CacheGCSwept]("the cache {string} survived the sweep",
			"the resource cache rows still in the database",
			func(in CacheGCSwept) ([]string, error) { return in.cacheNames() }),

		CheckNotMember[CacheGCSwept]("the cache {string} has been reclaimed",
			"the resource cache rows still in the database",
			func(in CacheGCSwept) ([]string, error) { return in.cacheNames() }),

		CheckMember[CacheGCSwept]("the config {string} survived the sweep",
			"the resource config rows still in the database",
			func(in CacheGCSwept) ([]string, error) { return in.configNames() }),

		CheckNotMember[CacheGCSwept]("the config {string} has been reclaimed",
			"the resource config rows still in the database",
			func(in CacheGCSwept) ([]string, error) { return in.configNames() }),

		// Uses are counted per build rather than globally. The ginkgo suite
		// counted the whole table, which cannot distinguish "this build's uses
		// were released" from "somebody's were" — and the one spec that needed
		// the distinction had to express it as a table that was non-zero and
		// then zero across two sweeps.
		CheckMember[CacheGCSwept]("the cache uses of the build {string} are still held",
			"the builds whose cache uses survive",
			func(in CacheGCSwept) ([]string, error) { return in.buildsHoldingUses() }),

		CheckNotMember[CacheGCSwept]("the cache uses of the build {string} have been released",
			"the builds whose cache uses survive",
			func(in CacheGCSwept) ([]string, error) { return in.buildsHoldingUses() }),
	}
}

func (s CacheGCSwept) artifactNames() ([]string, error) {
	return s.strings(`SELECT name FROM worker_artifacts ORDER BY name`, "artifacts")
}

func (s CacheGCSwept) cacheNames() ([]string, error) {
	return s.named(`SELECT id FROM resource_caches ORDER BY id`, s.Ready.cacheName, "resource caches")
}

func (s CacheGCSwept) configNames() ([]string, error) {
	return s.named(`SELECT id FROM resource_configs ORDER BY id`, s.Ready.configName, "resource configs")
}

func (s CacheGCSwept) buildsHoldingUses() ([]string, error) {
	return s.named(
		`SELECT DISTINCT build_id FROM resource_cache_uses WHERE build_id IS NOT NULL ORDER BY build_id`,
		s.Ready.buildName, "resource cache uses")
}

// named reads a column of ids and reports each one by the name the scenario
// gave it. An id nothing named is shown raw rather than dropped.
func (s CacheGCSwept) named(query string, names map[int]string, what string) ([]string, error) {
	rows, err := s.Ready.DB.Conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("read the %s the sweep left behind: %w", what, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read a row id: %w", err)
		}
		if name, ok := names[id]; ok {
			out = append(out, name)
		} else {
			out = append(out, fmt.Sprintf("#%d (unnamed)", id))
		}
	}
	return out, rows.Err()
}

func (s CacheGCSwept) strings(query, what string) ([]string, error) {
	rows, err := s.Ready.DB.Conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("read the %s the sweep left behind: %w", what, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("read a row: %w", err)
		}
		out = append(out, value)
	}
	return out, rows.Err()
}
