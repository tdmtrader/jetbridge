package steps

import (
	"fmt"
	"sort"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBTeamQueryNextObservation struct {
	Profile string
	Failure string
}

func DBTeamQueryNextStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBTeamQueryNextObservation](
			"the remaining production team query {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBTeamQueryNextObservation, error) {
				profile, err := paramAt("the remaining production team query {string} is exercised", p, 0)
				if err != nil {
					return DBTeamQueryNextObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBTeamQueryNextObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBTeamQueryNextObservation{Profile: profile, Failure: observeDBTeamQueryNext(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBTeamQueryNextObservation](
			"the remaining production team query exactly matches {string}",
			func(in DBTeamQueryNextObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the remaining production team query exactly matches {string}", p, 0)
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

func observeDBTeamQueryNext(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "query-next-team"})
	if err != nil {
		return err.Error()
	}
	other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "query-next-other"})
	if err != nil {
		return err.Error()
	}

	switch profile {
	case "metadata-full-state-set", "metadata-partial-filter", "metadata-empty-filter", "metadata-team-boundary":
		owner := team
		querying := team
		if profile == "metadata-team-boundary" {
			querying = other
		}
		worker, err := saveQueryNextWorker(database, "metadata-worker", db.WorkerStateRunning)
		if err != nil {
			return err.Error()
		}
		build, err := owner.CreateOneOffBuild()
		if err != nil {
			return err.Error()
		}
		full := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "step", Attempt: "1", PipelineID: 11, JobID: 12, BuildID: build.ID(), WorkingDirectory: "/work", User: "worker"}
		first, err := worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "plan-a", owner.ID()), full)
		if err != nil {
			return err.Error()
		}
		secondCreating, err := worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "plan-b", owner.ID()), full)
		if err != nil {
			return err.Error()
		}
		second, err := secondCreating.Created()
		if err != nil {
			return err.Error()
		}
		thirdCreating, err := worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "plan-c", owner.ID()), full)
		if err != nil {
			return err.Error()
		}
		thirdCreated, err := thirdCreating.Created()
		if err != nil {
			return err.Error()
		}
		third, err := thirdCreated.Destroying()
		if err != nil {
			return err.Error()
		}
		otherMeta := full
		otherMeta.Type = db.ContainerTypeCheck
		unmatched, err := worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "plan-d", owner.ID()), otherMeta)
		if err != nil {
			return err.Error()
		}
		query := full
		expected := []string{first.Handle(), second.Handle(), third.Handle()}
		if profile == "metadata-partial-filter" {
			query = db.ContainerMetadata{Type: db.ContainerTypeTask}
		}
		if profile == "metadata-empty-filter" {
			query = db.ContainerMetadata{}
			expected = append(expected, unmatched.Handle())
		}
		if profile == "metadata-team-boundary" {
			query = full
			expected = nil
		}
		containers, err := querying.FindContainersByMetadata(query)
		if err != nil {
			return err.Error()
		}
		got := containerHandles(containers)
		if !sameStrings(got, expected) {
			return fail("handles got=%v want=%v", got, expected)
		}
		return ""

	case "find-container-present":
		worker, err := saveQueryNextWorker(database, "find-container-worker", db.WorkerStateRunning)
		if err != nil {
			return err.Error()
		}
		container, err := createQueryNextTaskContainer(team, worker, "find-container", true)
		if err != nil {
			return err.Error()
		}
		foundContainer, found, err := team.FindContainerByHandle(container.Handle())
		if err != nil {
			return err.Error()
		}
		if !found || foundContainer == nil || foundContainer.Handle() != container.Handle() {
			return fail("found=%t container=%v", found, foundContainer)
		}
		return ""

	case "artifact-volume-present":
		worker, err := saveQueryNextWorker(database, "artifact-worker", db.WorkerStateRunning)
		if err != nil {
			return err.Error()
		}
		if _, err := database.Conn.Exec("INSERT INTO worker_artifacts (id, name) VALUES ($1, '')", 18001); err != nil {
			return err.Error()
		}
		if _, err := database.Conn.Exec("INSERT INTO volumes (handle, team_id, worker_name, worker_artifact_id, state) VALUES ($1, $2, $3, $4, $5)", "artifact-volume", team.ID(), worker.Name(), 18001, db.VolumeStateCreated); err != nil {
			return err.Error()
		}
		volume, found, err := team.FindVolumeForWorkerArtifact(18001)
		if err != nil {
			return err.Error()
		}
		if !found || volume == nil || volume.Handle() != "artifact-volume" || volume.WorkerArtifactID() != 18001 {
			return fail("found=%t volume=%v", found, volume)
		}
		return ""

	case "container-worker-creating", "container-worker-created":
		worker, err := saveQueryNextWorker(database, "container-owner-worker", db.WorkerStateRunning)
		if err != nil {
			return err.Error()
		}
		created := profile == "container-worker-created"
		container, err := createQueryNextTaskContainer(team, worker, "owner-lookup", created)
		if err != nil {
			return err.Error()
		}
		got, found, err := team.FindWorkerForContainer(container.Handle())
		if err != nil {
			return err.Error()
		}
		if !found || got == nil || got.Name() != worker.Name() {
			return fail("found=%t worker=%v", found, got)
		}
		return ""

	case "private-builds-pagination", "private-builds-team-boundary":
		return observeQueryNextBuilds(team, other, profile)

	case "time-builds-zero-limit":
		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "time-pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 1, false)
		if err != nil {
			return err.Error()
		}
		job, found, err := pipeline.Job("job")
		if err != nil || !found {
			return fail("job found=%t err=%v", found, err)
		}
		if _, err := job.CreateBuild("query-next"); err != nil {
			return err.Error()
		}
		builds, _, err := team.BuildsWithTime(db.Page{})
		if err != nil {
			return err.Error()
		}
		if len(builds) != 0 {
			return fail("got %d builds, want zero", len(builds))
		}
		return ""

	case "builds-from-past-end", "builds-invalid-range":
		builds := []db.Build{}
		for range 4 {
			build, err := team.CreateOneOffBuild()
			if err != nil {
				return err.Error()
			}
			builds = append(builds, build)
		}
		if profile == "builds-from-past-end" {
			got, _, err := team.Builds(db.Page{Limit: 50, From: db.NewIntPtr(builds[3].ID() + 1)})
			if err != nil {
				return err.Error()
			}
			if len(got) != 0 {
				return fail("got %d builds, want zero", len(got))
			}
			return ""
		}
		_, _, err = team.Builds(db.Page{Limit: 50, From: db.NewIntPtr(builds[3].ID()), To: db.NewIntPtr(builds[2].ID())})
		if err == nil {
			return "invalid range was accepted"
		}
		return ""

	case "check-containers-present", "check-containers-shared-config", "is-check-container-check", "is-check-container-task", "check-container-inside-team", "check-container-outside-team", "task-container-inside-team", "task-container-outside-team":
		return observeQueryNextContainerRelations(database, team, other, profile)

	case "cache-running-worker", "cache-stalled-worker", "cache-two-running-workers", "cache-before-prune-workers", "cache-before-prune-volume", "cache-before-prune-row", "cache-after-prune-workers", "cache-after-prune-volume", "cache-after-prune-row":
		return observeQueryNextCache(database, team, profile)
	}

	return fail("unknown profile %q", profile)
}

func saveQueryNextWorker(database JetbridgeDB, name string, state db.WorkerState) (db.Worker, error) {
	worker := atc.Worker{
		Name: name, Platform: "linux", State: string(state), Version: "1.0",
		ResourceTypes: []atc.WorkerResourceType{{Type: "some-base-resource-type", Image: "image", Version: "version"}},
	}
	return database.WorkerFactory.SaveWorker(worker, 5*time.Minute)
}

func createQueryNextTaskContainer(team db.Team, worker db.Worker, plan atc.PlanID, markCreated bool) (db.Container, error) {
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return nil, err
	}
	creating, err := worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), plan, team.ID()), db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: string(plan)})
	if err != nil {
		return nil, err
	}
	if !markCreated {
		return creating, nil
	}
	return creating.Created()
}

func containerHandles(containers []db.Container) []string {
	handles := make([]string, 0, len(containers))
	for _, container := range containers {
		handles = append(handles, container.Handle())
	}
	sort.Strings(handles)
	return handles
}

func sameStrings(got, want []string) bool {
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func observeQueryNextBuilds(team, other db.Team, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	if profile == "private-builds-team-boundary" {
		mine := []int{}
		for range 3 {
			build, err := team.CreateOneOffBuild()
			if err != nil {
				return err.Error()
			}
			mine = append(mine, build.ID())
			if _, err := other.CreateOneOffBuild(); err != nil {
				return err.Error()
			}
		}
		got, _, err := team.PrivateAndPublicBuilds(db.Page{Limit: 10})
		if err != nil {
			return err.Error()
		}
		if !sameInts(buildAPIIDs(got), mine) {
			return fail("ids got=%v want=%v", buildAPIIDs(got), mine)
		}
		return ""
	}

	all := []db.Build{}
	for range 3 {
		build, err := team.CreateOneOffBuild()
		if err != nil {
			return err.Error()
		}
		all = append(all, build)
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "builds-pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 1, false)
	if err != nil {
		return err.Error()
	}
	job, found, err := pipeline.Job("job")
	if err != nil || !found {
		return fail("job found=%t err=%v", found, err)
	}
	for range 2 {
		build, err := job.CreateBuild("query-next")
		if err != nil {
			return err.Error()
		}
		all = append(all, build)
	}
	page1, pagination1, err := team.PrivateAndPublicBuilds(db.Page{Limit: 2})
	if err != nil {
		return err.Error()
	}
	if !sameInts(buildAPIIDs(page1), []int{all[4].ID(), all[3].ID()}) || pagination1.Newer != nil || pagination1.Older == nil || pagination1.Older.To == nil || *pagination1.Older.To != all[2].ID() {
		return fail("page1 ids=%v pagination=%#v", buildAPIIDs(page1), pagination1)
	}
	page2, pagination2, err := team.PrivateAndPublicBuilds(*pagination1.Older)
	if err != nil {
		return err.Error()
	}
	if !sameInts(buildAPIIDs(page2), []int{all[2].ID(), all[1].ID()}) || pagination2.Newer == nil || pagination2.Newer.From == nil || *pagination2.Newer.From != all[3].ID() || pagination2.Older == nil || pagination2.Older.To == nil || *pagination2.Older.To != all[0].ID() {
		return fail("page2 ids=%v pagination=%#v", buildAPIIDs(page2), pagination2)
	}
	page3, pagination3, err := team.PrivateAndPublicBuilds(*pagination2.Older)
	if err != nil {
		return err.Error()
	}
	if !sameInts(buildAPIIDs(page3), []int{all[0].ID()}) || pagination3.Newer == nil || pagination3.Newer.From == nil || *pagination3.Newer.From != all[1].ID() || pagination3.Older != nil {
		return fail("page3 ids=%v pagination=%#v", buildAPIIDs(page3), pagination3)
	}
	page2Again, pagination2Again, err := team.PrivateAndPublicBuilds(*pagination3.Newer)
	if err != nil {
		return err.Error()
	}
	if !sameInts(buildAPIIDs(page2Again), []int{all[2].ID(), all[1].ID()}) || pagination2Again.Newer == nil || pagination2Again.Newer.From == nil || *pagination2Again.Newer.From != all[3].ID() || pagination2Again.Older == nil || pagination2Again.Older.To == nil || *pagination2Again.Older.To != all[0].ID() {
		return fail("newer page ids=%v pagination=%#v", buildAPIIDs(page2Again), pagination2Again)
	}
	return ""
}

func buildAPIIDs(builds []db.BuildForAPI) []int {
	ids := make([]int, 0, len(builds))
	for _, build := range builds {
		ids = append(ids, build.ID())
	}
	return ids
}

func sameInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

type queryNextCheckFixture struct {
	pipeline  db.Pipeline
	resource  db.Resource
	container db.CreatingContainer
	worker    db.Worker
}

func createQueryNextCheckFixture(database JetbridgeDB, team db.Team, name string, teamWorker bool) (queryNextCheckFixture, error) {
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: name}, atc.Config{Resources: atc.ResourceConfigs{{Name: "resource", Type: "some-base-resource-type", Source: atc.Source{"source": "same"}}}}, 1, false)
	if err != nil {
		return queryNextCheckFixture{}, err
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return queryNextCheckFixture{}, fmt.Errorf("resource found=%t err=%v", found, err)
	}
	config, err := database.Builder.ResourceConfigFactory.FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return queryNextCheckFixture{}, err
	}
	resourceID := resource.ID()
	scope, err := config.FindOrCreateScope(&resourceID)
	if err != nil {
		return queryNextCheckFixture{}, err
	}
	if err := resource.SetResourceConfigScope(scope); err != nil {
		return queryNextCheckFixture{}, err
	}
	workerName := name + "-worker"
	var worker db.Worker
	if teamWorker {
		worker, err = team.SaveWorker(atc.Worker{Name: workerName, Platform: "linux", Version: "1.0", ResourceTypes: []atc.WorkerResourceType{{Type: "some-base-resource-type", Image: "image", Version: "version"}}}, 5*time.Minute)
	} else {
		worker, err = saveQueryNextWorker(database, workerName, db.WorkerStateRunning)
	}
	if err != nil {
		return queryNextCheckFixture{}, err
	}
	container, err := worker.CreateContainer(db.NewResourceConfigCheckSessionContainerOwner(config.ID(), config.OriginBaseResourceType().ID, db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: time.Hour}), db.ContainerMetadata{Type: db.ContainerTypeCheck})
	if err != nil {
		return queryNextCheckFixture{}, err
	}
	return queryNextCheckFixture{pipeline: pipeline, resource: resource, container: container, worker: worker}, nil
}

func observeQueryNextContainerRelations(database JetbridgeDB, team, other db.Team, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	if profile == "is-check-container-task" || profile == "task-container-inside-team" || profile == "task-container-outside-team" {
		worker, err := saveQueryNextWorker(database, "relation-task-worker", db.WorkerStateRunning)
		if err != nil {
			return err.Error()
		}
		container, err := createQueryNextTaskContainer(team, worker, "relation-task", true)
		if err != nil {
			return err.Error()
		}
		if profile == "is-check-container-task" {
			is, err := team.IsCheckContainer(container.Handle())
			if err != nil {
				return err.Error()
			}
			if is {
				return "task container reported as check container"
			}
			return ""
		}
		querying := team
		want := true
		if profile == "task-container-outside-team" {
			querying, want = other, false
		}
		got, err := querying.IsContainerWithinTeam(container.Handle(), false)
		if err != nil {
			return err.Error()
		}
		if got != want {
			return fail("within team got=%t want=%t", got, want)
		}
		return ""
	}

	fixture, err := createQueryNextCheckFixture(database, team, "relations", true)
	if err != nil {
		return err.Error()
	}
	switch profile {
	case "check-containers-present":
		containers, expiries, err := team.FindCheckContainers(lager.NewLogger("db-team-query-next"), atc.PipelineRef{Name: fixture.pipeline.Name()}, fixture.resource.Name())
		if err != nil {
			return err.Error()
		}
		if len(containers) != 1 || containers[0].ID() != fixture.container.ID() || len(expiries) != 1 || expiries[fixture.container.ID()].IsZero() {
			return fail("containers=%v expiries=%v", containerHandles(containers), expiries)
		}
	case "check-containers-shared-config":
		otherPipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "relations-shared", InstanceVars: atc.InstanceVars{"branch": "main"}}, atc.Config{Resources: atc.ResourceConfigs{{Name: "resource", Type: fixture.resource.Type(), Source: fixture.resource.Source()}}}, 1, false)
		if err != nil {
			return err.Error()
		}
		otherResource, found, err := otherPipeline.Resource("resource")
		if err != nil || !found {
			return fail("shared resource found=%t err=%v", found, err)
		}
		config, err := database.Builder.ResourceConfigFactory.FindOrCreateResourceConfig(otherResource.Type(), otherResource.Source(), nil)
		if err != nil {
			return err.Error()
		}
		id := otherResource.ID()
		scope, err := config.FindOrCreateScope(&id)
		if err != nil {
			return err.Error()
		}
		if err := otherResource.SetResourceConfigScope(scope); err != nil {
			return err.Error()
		}
		containers, expiries, err := team.FindCheckContainers(lager.NewLogger("db-team-query-next"), atc.PipelineRef{Name: otherPipeline.Name(), InstanceVars: otherPipeline.InstanceVars()}, otherResource.Name())
		if err != nil {
			return err.Error()
		}
		if len(containers) != 1 || containers[0].ID() != fixture.container.ID() || len(expiries) != 1 || expiries[fixture.container.ID()].IsZero() {
			return fail("shared containers=%v expiries=%v", containerHandles(containers), expiries)
		}
	case "is-check-container-check":
		is, err := team.IsCheckContainer(fixture.container.Handle())
		if err != nil {
			return err.Error()
		}
		if !is {
			return "check container reported as non-check"
		}
	case "check-container-inside-team", "check-container-outside-team":
		querying, want := team, true
		if profile == "check-container-outside-team" {
			querying, want = other, false
		}
		got, err := querying.IsContainerWithinTeam(fixture.container.Handle(), true)
		if err != nil {
			return err.Error()
		}
		if got != want {
			return fail("within team got=%t want=%t", got, want)
		}
	}
	return ""
}

type queryNextCacheFixture struct {
	database JetbridgeDB
	team     db.Team
	cache    db.ResourceCache
	workers  map[string]db.Worker
	volumes  map[string]db.CreatedVolume
	source   *db.UsedWorkerResourceCache
	streamed *db.UsedWorkerResourceCache
	prunedAt time.Time
}

func createQueryNextCache(database JetbridgeDB, team db.Team) (queryNextCacheFixture, error) {
	fixture := queryNextCacheFixture{database: database, team: team, workers: map[string]db.Worker{}, volumes: map[string]db.CreatedVolume{}}
	worker, err := saveQueryNextWorker(database, "cache-worker-1", db.WorkerStateRunning)
	if err != nil {
		return fixture, err
	}
	fixture.workers[worker.Name()] = worker
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return fixture, err
	}
	fixture.cache, err = database.Builder.ResourceCacheFactory.FindOrCreateResourceCache(db.ForBuild(build.ID()), "some-base-resource-type", atc.Version{"version": "1"}, atc.Source{"repository": "strict"}, atc.Params{}, nil)
	if err != nil {
		return fixture, err
	}
	volume, err := database.VolumeRepository.CreateVolume(team.ID(), worker.Name(), db.VolumeTypeResource)
	if err != nil {
		return fixture, err
	}
	created, err := volume.Created()
	if err != nil {
		return fixture, err
	}
	fixture.volumes[worker.Name()] = created
	fixture.source, err = created.InitializeResourceCache(fixture.cache)
	return fixture, err
}

func (fixture *queryNextCacheFixture) addSecond() error {
	worker, err := saveQueryNextWorker(fixture.database, "cache-worker-2", db.WorkerStateRunning)
	if err != nil {
		return err
	}
	fixture.workers[worker.Name()] = worker
	volume, err := fixture.database.VolumeRepository.CreateVolume(fixture.team.ID(), worker.Name(), db.VolumeTypeResource)
	if err != nil {
		return err
	}
	created, err := volume.Created()
	if err != nil {
		return err
	}
	fixture.volumes[worker.Name()] = created
	fixture.streamed, err = created.InitializeStreamedResourceCache(fixture.cache, fixture.source.ID)
	return err
}

func observeQueryNextCache(database JetbridgeDB, team db.Team, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	fixture, err := createQueryNextCache(database, team)
	if err != nil {
		return err.Error()
	}
	if profile == "cache-stalled-worker" {
		worker := atc.Worker{Name: "cache-worker-1", Platform: "linux", State: string(db.WorkerStateStalled), Version: "1.0", ResourceTypes: []atc.WorkerResourceType{{Type: "some-base-resource-type", Image: "image", Version: "version"}}}
		if _, err := database.WorkerFactory.SaveWorker(worker, 5*time.Minute); err != nil {
			return err.Error()
		}
	}
	needsSecond := profile != "cache-running-worker" && profile != "cache-stalled-worker"
	if needsSecond {
		if err := fixture.addSecond(); err != nil {
			return err.Error()
		}
	}
	if profile == "cache-before-prune-workers" || profile == "cache-before-prune-volume" || profile == "cache-before-prune-row" || profile == "cache-after-prune-workers" || profile == "cache-after-prune-volume" || profile == "cache-after-prune-row" {
		if err := fixture.workers["cache-worker-1"].Delete(); err != nil {
			return err.Error()
		}
		fixture.prunedAt = time.Now()
	}
	lookupAt := time.Now()
	if !fixture.prunedAt.IsZero() {
		lookupAt = fixture.prunedAt.Add(-100 * time.Second)
		if profile == "cache-after-prune-workers" || profile == "cache-after-prune-volume" || profile == "cache-after-prune-row" {
			lookupAt = fixture.prunedAt.Add(100 * time.Second)
		}
	}
	switch profile {
	case "cache-running-worker", "cache-stalled-worker", "cache-two-running-workers", "cache-before-prune-workers", "cache-after-prune-workers":
		workers, err := team.FindWorkersForResourceCache(fixture.cache.ID(), lookupAt)
		if err != nil {
			return err.Error()
		}
		names := []string{}
		for _, worker := range workers {
			names = append(names, worker.Name())
		}
		sort.Strings(names)
		want := []string{"cache-worker-1"}
		if profile == "cache-stalled-worker" || profile == "cache-after-prune-workers" {
			want = nil
		}
		if profile == "cache-two-running-workers" {
			want = []string{"cache-worker-1", "cache-worker-2"}
		}
		if profile == "cache-before-prune-workers" {
			want = []string{"cache-worker-2"}
		}
		if !sameStrings(names, want) {
			return fail("workers got=%v want=%v", names, want)
		}
	case "cache-before-prune-volume", "cache-after-prune-volume":
		volume, found, err := database.VolumeRepository.FindResourceCacheVolume("cache-worker-2", fixture.cache, lookupAt)
		if err != nil {
			return err.Error()
		}
		wantFound := profile == "cache-before-prune-volume"
		if found != wantFound || (found && (volume == nil || volume.Handle() != fixture.volumes["cache-worker-2"].Handle())) {
			return fail("found=%t volume=%v wantFound=%t", found, volume, wantFound)
		}
	case "cache-before-prune-row", "cache-after-prune-row":
		volume, found, err := database.VolumeRepository.FindVolume(fixture.volumes["cache-worker-2"].Handle())
		if err != nil {
			return err.Error()
		}
		if !found || volume == nil || volume.WorkerResourceCacheID() != fixture.streamed.ID {
			return fail("found=%t cache-id=%d want=%d", found, volume.WorkerResourceCacheID(), fixture.streamed.ID)
		}
	}
	return ""
}
