package atc_test

import (
	"github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// templateRouteEffects is the checked-in answer to "how do pipeline templates
// affect your endpoint?" for every route in atc.Routes.
//
// The refusals themselves live at the db boundary and reach clients as typed
// errors, so this table wraps no handler and costs nothing at runtime. Its only
// job is to make the question unskippable: a route that nobody has classified
// fails this suite rather than shipping with an unexamined relationship to
// templates and runs.
//
// The buckets are defined by the route -- what the path targets and what the
// method does -- not by the guard, so an entry can be checked against
// atc/routes.go alone. An entry is not a claim that the refusal is implemented
// today.
//
//	native:         exists because of templates and numbered runs.
//	refuses-work:   creates builds, or mutates job/resource/version state, on
//	                the pipeline named in the path. A base template holds no
//	                such state and must refuse.
//	mutates-config: mutates the pipeline row named in the path, or that row's
//	                config. On a template this rewrites the source every future
//	                run is materialized from; on a run payload it must be
//	                refused outright.
//	inert:          only reads, or has no persisted pipeline target at all.
var templateRouteEffects = map[string][]string{
	"native": {
		atc.CreatePipelineRun,
		atc.ListPipelineRuns,
		atc.GetPipelineRun,
	},
	"refuses-work": {
		atc.CheckPrototype,
		atc.CheckResource,
		atc.CheckResourceType,
		atc.CheckResourceWebHook,
		atc.ClearResourceCache,
		atc.ClearResourceTypeVersions,
		atc.ClearResourceVersions,
		atc.ClearTaskCache,
		atc.CopyResourceVersions,
		atc.CreateJobBuild,
		atc.CreatePipelineBuild,
		atc.DisableResourceVersion,
		atc.EnableResourceVersion,
		atc.PauseJob,
		atc.PinResourceVersion,
		atc.RerunJobBuild,
		atc.ScheduleJob,
		atc.SetPinCommentOnResource,
		atc.UnpauseJob,
		atc.UnpinResource,
	},
	"mutates-config": {
		atc.ArchivePipeline,
		atc.DeletePipeline,
		atc.ExposePipeline,
		atc.HidePipeline,
		atc.OrderPipelines,
		atc.OrderPipelinesWithinGroup,
		atc.PausePipeline,
		atc.RenamePipeline,
		atc.SaveConfig,
		atc.UnpausePipeline,
	},
	"inert": {
		atc.AbortBuild,
		atc.BuildEvents,
		atc.BuildResources,
		atc.ClearWall,
		atc.CreateArtifact,
		atc.CreateBuild,
		atc.DeleteWorker,
		atc.DestroyTeam,
		atc.DownloadCLI,
		atc.GetArtifact,
		atc.GetBuild,
		atc.GetBuildPlan,
		atc.GetBuildPreparation,
		atc.GetCC,
		atc.GetConfig,
		atc.GetContainer,
		atc.GetDownstreamResourceCausality,
		atc.GetHealth,
		atc.GetInfo,
		atc.GetInfoCreds,
		atc.GetJob,
		atc.GetJobBuild,
		atc.GetLogLevel,
		atc.GetOpenIDConfiguration,
		atc.GetPipeline,
		atc.GetResource,
		atc.GetResourceVersion,
		atc.GetSigningKeys,
		atc.GetTeam,
		atc.GetUpstreamResourceCausality,
		atc.GetUser,
		atc.GetVersionsDB,
		atc.GetWall,
		atc.HijackContainer,
		atc.JobBadge,
		atc.ListActiveUsersSince,
		atc.ListAllJobs,
		atc.ListAllPipelines,
		atc.ListAllResources,
		atc.ListBuildArtifacts,
		atc.ListBuilds,
		atc.ListBuildsWithVersionAsInput,
		atc.ListBuildsWithVersionAsOutput,
		atc.ListContainers,
		atc.ListDeprecatedScopes,
		atc.ListJobBuilds,
		atc.ListJobInputs,
		atc.ListJobs,
		atc.ListPipelineBuilds,
		atc.ListPipelines,
		atc.ListResourceTypes,
		atc.ListResourceVersions,
		atc.ListResources,
		atc.ListSharedForResource,
		atc.ListSharedForResourceType,
		atc.ListTeamBuilds,
		atc.ListTeams,
		atc.ListVolumes,
		atc.ListWorkers,
		atc.MainJobBadge,
		atc.PipelineBadge,
		atc.RegisterWorker,
		atc.RenameTeam,
		atc.SetBuildComment,
		atc.SetLogLevel,
		atc.SetTeam,
		atc.SetWall,
	},
}

var _ = Describe("Pipeline template route effects", func() {
	It("accounts for every route in atc.Routes exactly once", func() {
		// This fails if an endpoint was added without anyone answering how
		// pipeline templates affect it, or if an entry names a route that no
		// longer exists.
		var classified []string
		for _, routes := range templateRouteEffects {
			classified = append(classified, routes...)
		}

		var routeNames []string
		for _, route := range atc.Routes {
			routeNames = append(routeNames, route.Name)
		}

		Expect(classified).To(ConsistOf(routeNames))
	})

	It("keeps the run endpoints as the only template-native routes", func() {
		// This fails if a route that exists solely to drive templates and runs
		// was filed under a bucket that hides it from the template question.
		Expect(templateRouteEffects["native"]).To(ConsistOf(
			atc.CreatePipelineRun,
			atc.ListPipelineRuns,
			atc.GetPipelineRun,
		))
	})

	It("files every build-creating endpoint under refuses-work", func() {
		// This fails if a build-creating endpoint is recorded as inert, which is
		// the classification that silently skips the template question.
		Expect(templateRouteEffects["refuses-work"]).To(ContainElements(
			atc.CreateJobBuild,
			atc.RerunJobBuild,
			atc.CreatePipelineBuild,
		))
	})
})
