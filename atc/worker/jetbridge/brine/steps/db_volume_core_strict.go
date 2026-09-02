package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const (
	strictVolumeBaseType        = "strict-volume-base-type"
	strictVolumeBaseTypeVersion = "strict-volume-base-version"
)

type DBVolumeCoreObservation struct {
	Profile string
	Failure string
}

func DBVolumeCoreStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBVolumeCoreObservation](
			"the production volume behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBVolumeCoreObservation, error) {
				profile, err := paramAt("the production volume behavior {string} is exercised", p, 0)
				if err != nil {
					return DBVolumeCoreObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBVolumeCoreObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBVolumeCoreObservation{Profile: profile, Failure: observeDBVolumeCore(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBVolumeCoreObservation](
			"the volume behavior exactly matches {string}",
			func(in DBVolumeCoreObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the volume behavior exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if profile != in.Profile {
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

type strictVolumeFixture struct {
	team              db.Team
	worker            db.Worker
	creatingContainer db.CreatingContainer
	createdContainer  db.CreatedContainer
}

func newStrictVolumeFixture(database JetbridgeDB) (*strictVolumeFixture, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-volume-team"})
	if err != nil {
		return nil, err
	}
	worker, err := team.SaveWorker(atc.Worker{
		Name: "strict-volume-worker", Platform: "linux", Version: "1.2.3",
		ResourceTypes: []atc.WorkerResourceType{{
			Type: strictVolumeBaseType, Image: "/strict/volume/image", Version: strictVolumeBaseTypeVersion,
		}},
	}, 0)
	if err != nil {
		return nil, err
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return nil, err
	}
	creatingContainer, err := worker.CreateContainer(
		db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("strict-volume-plan"), team.ID()),
		db.ContainerMetadata{Type: "task", StepName: "strict-volume-step"},
	)
	if err != nil {
		return nil, err
	}
	createdContainer, err := creatingContainer.Created()
	if err != nil {
		return nil, err
	}
	return &strictVolumeFixture{
		team: team, worker: worker, creatingContainer: creatingContainer, createdContainer: createdContainer,
	}, nil
}

func (f *strictVolumeFixture) createVolume(database JetbridgeDB, path string) (db.CreatingVolume, db.CreatedVolume, error) {
	creating, err := database.VolumeRepository.CreateContainerVolume(f.team.ID(), f.worker.Name(), f.creatingContainer, path)
	if err != nil {
		return nil, nil, err
	}
	created, err := creating.Created()
	return creating, created, err
}

func observeDBVolumeCore(database JetbridgeDB, profile string) string {
	f, err := newStrictVolumeFixture(database)
	if err != nil {
		return err.Error()
	}
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }

	switch profile {
	case "failed-wrong-state":
		creating, created, err := f.createVolume(database, "/failed-wrong-state")
		if err != nil || created == nil {
			return fail("create volume: created=%v err=%v", created, err)
		}
		_, err = creating.Failed()
		want := db.ErrVolumeMarkStateFailed{State: db.VolumeStateFailed}
		if err != want {
			return fail("error got=%v want=%v", err, want)
		}
	case "failed-missing":
		creating, created, err := f.createVolume(database, "/failed-missing")
		if err != nil {
			return err.Error()
		}
		destroying, err := created.Destroying()
		if err != nil {
			return err.Error()
		}
		deleted, err := destroying.Destroy()
		if err != nil || !deleted {
			return fail("destroy: deleted=%t err=%v", deleted, err)
		}
		_, err = creating.Failed()
		want := db.ErrVolumeMarkStateFailed{State: db.VolumeStateFailed}
		if err != want {
			return fail("error got=%v want=%v", err, want)
		}
	case "failed-return", "failed-idempotent":
		creating, err := database.VolumeRepository.CreateContainerVolume(f.team.ID(), f.worker.Name(), f.creatingContainer, "/failed-idempotent")
		if err != nil {
			return err.Error()
		}
		if _, err := creating.Failed(); err != nil {
			return err.Error()
		}
		failed, err := creating.Failed()
		if profile == "failed-return" && failed == nil {
			return "second transition returned a nil failed volume"
		}
		if profile == "failed-idempotent" && err != nil {
			return fail("second transition failed: %v", err)
		}
	case "created-wrong-state":
		creating, created, err := f.createVolume(database, "/created-wrong-state")
		if err != nil {
			return err.Error()
		}
		if _, err := created.Destroying(); err != nil {
			return err.Error()
		}
		_, err = creating.Created()
		want := db.ErrVolumeMarkCreatedFailed{Handle: creating.Handle()}
		if err != want {
			return fail("error got=%v want=%v", err, want)
		}
	case "created-missing":
		creating, created, err := f.createVolume(database, "/created-missing")
		if err != nil {
			return err.Error()
		}
		destroying, err := created.Destroying()
		if err != nil {
			return err.Error()
		}
		deleted, err := destroying.Destroy()
		if err != nil || !deleted {
			return fail("destroy: deleted=%t err=%v", deleted, err)
		}
		_, err = creating.Created()
		want := db.ErrVolumeMarkCreatedFailed{Handle: creating.Handle()}
		if err != want {
			return fail("error got=%v want=%v", err, want)
		}
	case "created-persisted":
		_, created, err := f.createVolume(database, "/created-persisted")
		if err != nil || created == nil {
			return fail("create volume: created=%v err=%v", created, err)
		}
		volumes, err := database.VolumeRepository.FindVolumesForContainer(f.createdContainer)
		if err != nil {
			return err.Error()
		}
		if len(volumes) != 1 || volumes[0].Handle() != created.Handle() || volumes[0].Path() != "/created-persisted" {
			return fail("persisted volumes=%v", strictVolumeSummaries(volumes))
		}
	case "created-idempotent":
		creating, created, err := f.createVolume(database, "/created-idempotent")
		if err != nil || created == nil {
			return fail("create volume: created=%v err=%v", created, err)
		}
		created, err = creating.Created()
		if err != nil || created == nil {
			return fail("second transition: created=%v err=%v", created, err)
		}
	case "artifact-fields", "artifact-association":
		creating, err := database.VolumeRepository.CreateVolume(f.team.ID(), f.worker.Name(), db.VolumeTypeArtifact)
		if err != nil {
			return err.Error()
		}
		created, err := creating.Created()
		if err != nil {
			return err.Error()
		}
		artifact, err := created.InitializeArtifact("strict-artifact", 0)
		if err != nil {
			return err.Error()
		}
		if profile == "artifact-fields" {
			if artifact.ID() == 0 || artifact.Name() != "strict-artifact" || artifact.BuildID() != 0 || artifact.CreatedAt().IsZero() {
				return fail("artifact id=%d name=%q build=%d created=%v", artifact.ID(), artifact.Name(), artifact.BuildID(), artifact.CreatedAt())
			}
		} else {
			foundVolume, found, err := database.VolumeRepository.FindVolume(created.Handle())
			if err != nil || !found || foundVolume.WorkerArtifactID() != artifact.ID() {
				return fail("association found=%t artifact-id=%d want=%d err=%v", found, strictVolumeArtifactID(foundVolume), artifact.ID(), err)
			}
		}
	case "task-cache-replaces":
		job, _, err := saveJobForStrictTeam(f.team, "strict-task-cache-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, "job")
		if err != nil {
			return err.Error()
		}
		_, oldVolume, err := f.createVolume(database, "/old-task-cache")
		if err != nil {
			return err.Error()
		}
		if err := oldVolume.InitializeTaskCache(job.ID(), "task", "cache-path"); err != nil {
			return err.Error()
		}
		_, newVolume, err := f.createVolume(database, "/new-task-cache")
		if err != nil {
			return err.Error()
		}
		if err := newVolume.InitializeTaskCache(job.ID(), "task", "cache-path"); err != nil {
			return err.Error()
		}
		taskCache, err := db.NewTaskCacheFactory(database.Conn).FindOrCreate(job.ID(), "task", "cache-path")
		if err != nil {
			return err.Error()
		}
		foundVolume, found, err := database.VolumeRepository.FindTaskCacheVolume(f.team.ID(), f.worker.Name(), taskCache)
		if err != nil || !found || foundVolume.Handle() != newVolume.Handle() || foundVolume.Handle() == oldVolume.Handle() {
			return fail("replacement found=%t handle=%v want=%s old=%s err=%v", found, strictVolumeHandle(foundVolume), newVolume.Handle(), oldVolume.Handle(), err)
		}
	case "container-fields":
		_, created, err := f.createVolume(database, "/container-fields")
		if err != nil {
			return err.Error()
		}
		if failure := strictCheckContainerVolume(created, f.creatingContainer.Handle(), "/container-fields"); failure != "" {
			return failure
		}
		_, found, err := database.VolumeRepository.FindContainerVolume(f.team.ID(), f.worker.Name(), f.creatingContainer, "/container-fields")
		if err != nil || found == nil {
			return fail("refetch found=%v err=%v", found, err)
		}
		return strictCheckContainerVolume(found, f.creatingContainer.Handle(), "/container-fields")
	case "child-fields", "parent-protected", "child-lifecycle":
		_, parent, err := f.createVolume(database, "/parent")
		if err != nil {
			return err.Error()
		}
		childCreating, err := parent.CreateChildForContainer(f.creatingContainer, "/child")
		if err != nil {
			return err.Error()
		}
		child, err := childCreating.Created()
		if err != nil {
			return err.Error()
		}
		switch profile {
		case "child-fields":
			if failure := strictCheckChildVolume(child, f.creatingContainer.Handle(), "/child", parent.Handle()); failure != "" {
				return failure
			}
			_, found, err := database.VolumeRepository.FindContainerVolume(f.team.ID(), f.worker.Name(), f.creatingContainer, "/child")
			if err != nil || found == nil {
				return fail("refetch found=%v err=%v", found, err)
			}
			return strictCheckChildVolume(found, f.creatingContainer.Handle(), "/child", parent.Handle())
		case "parent-protected":
			_, err := parent.Destroying()
			if err != db.ErrVolumeCannotBeDestroyedWithChildrenPresent {
				return fail("destroy parent error got=%v want=%v", err, db.ErrVolumeCannotBeDestroyedWithChildrenPresent)
			}
		case "child-lifecycle":
			if _, err := parent.Destroying(); err == nil {
				return "parent was destroyable while child existed"
			}
			childDestroying, err := child.Destroying()
			if err != nil {
				return err.Error()
			}
			deleted, err := childDestroying.Destroy()
			if err != nil || !deleted {
				return fail("destroy child: deleted=%t err=%v", deleted, err)
			}
			parentDestroying, err := parent.Destroying()
			if err != nil {
				return err.Error()
			}
			deleted, err = parentDestroying.Destroy()
			if err != nil || !deleted {
				return fail("destroy parent: deleted=%t err=%v", deleted, err)
			}
		}
	case "base-resource-type-fields":
		used, found, err := (db.WorkerBaseResourceType{Name: strictVolumeBaseType, WorkerName: f.worker.Name()}).Find(database.Conn)
		if err != nil || !found {
			return fail("base resource type found=%t err=%v", found, err)
		}
		creating, err := database.VolumeRepository.CreateBaseResourceTypeVolume(used)
		if err != nil {
			return err.Error()
		}
		created, err := creating.Created()
		if err != nil {
			return err.Error()
		}
		if created.Type() != db.VolumeTypeResourceType {
			return fail("type got=%q want=%q", created.Type(), db.VolumeTypeResourceType)
		}
		base, err := created.BaseResourceType()
		if err != nil || base == nil || base.Name != strictVolumeBaseType || base.Version != strictVolumeBaseTypeVersion {
			return fail("base=%+v err=%v", base, err)
		}
		_, refetched, err := database.VolumeRepository.FindBaseResourceTypeVolume(used)
		if err != nil || refetched == nil {
			return fail("refetch volume=%v err=%v", refetched, err)
		}
		base, err = refetched.BaseResourceType()
		if err != nil || base == nil || base.Name != strictVolumeBaseType || base.Version != strictVolumeBaseTypeVersion {
			return fail("refetched base=%+v err=%v", base, err)
		}
	case "task-identifier":
		job, pipeline, err := saveJobForStrictTeam(f.team, "strict-task-id-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, "job")
		if err != nil {
			return err.Error()
		}
		taskCache, err := db.NewTaskCacheFactory(database.Conn).FindOrCreate(job.ID(), "task", "cache-path")
		if err != nil {
			return err.Error()
		}
		workerTaskCache, err := db.NewWorkerTaskCacheFactory(database.Conn).FindOrCreate(db.WorkerTaskCache{WorkerName: f.worker.Name(), TaskCache: taskCache})
		if err != nil {
			return err.Error()
		}
		creating, err := database.VolumeRepository.CreateTaskCacheVolume(f.team.ID(), workerTaskCache)
		if err != nil {
			return err.Error()
		}
		created, err := creating.Created()
		if err != nil {
			return err.Error()
		}
		pipelineID, pipelineRef, jobName, stepName, err := created.TaskIdentifier()
		if err != nil || pipelineID != pipeline.ID() || pipelineRef.Name != pipeline.Name() || jobName != job.Name() || stepName != "task" {
			return fail("identifier pipeline=%d/%+v job=%q step=%q err=%v", pipelineID, pipelineRef, jobName, stepName, err)
		}
	default:
		return fail("unknown volume profile %q", profile)
	}
	return ""
}

func strictVolumeSummaries(volumes []db.CreatedVolume) []string {
	summaries := make([]string, len(volumes))
	for i, volume := range volumes {
		summaries[i] = fmt.Sprintf("%s:%s", volume.Handle(), volume.Path())
	}
	return summaries
}

func strictVolumeArtifactID(volume db.CreatedVolume) int {
	if volume == nil {
		return 0
	}
	return volume.WorkerArtifactID()
}

func strictVolumeHandle(volume db.CreatedVolume) any {
	if volume == nil {
		return nil
	}
	return volume.Handle()
}

func strictCheckContainerVolume(volume db.CreatedVolume, containerHandle, path string) string {
	if volume.Type() != db.VolumeTypeContainer || volume.ContainerHandle() != containerHandle || volume.Path() != path {
		return fmt.Sprintf("container volume type=%q container=%q path=%q", volume.Type(), volume.ContainerHandle(), volume.Path())
	}
	return ""
}

func strictCheckChildVolume(volume db.CreatedVolume, containerHandle, path, parentHandle string) string {
	if failure := strictCheckContainerVolume(volume, containerHandle, path); failure != "" {
		return failure
	}
	if volume.ParentHandle() != parentHandle {
		return fmt.Sprintf("parent handle got=%q want=%q", volume.ParentHandle(), parentHandle)
	}
	return ""
}
