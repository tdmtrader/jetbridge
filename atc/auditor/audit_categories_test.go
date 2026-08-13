package auditor_test

import (
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/auditor"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// auditCategories is the checked-in answer to "which audit flag does this route
// fall under?" for every route in atc.Routes. The auditor's switch is the only
// place that mapping is written down, it panics on any action it does not name,
// and it is reached on every single API request -- so a route that lands in the
// wrong arm is invisible to whoever is reading the audit log, and a route that
// lands in no arm takes the web node down. Adding a route means adding it here.
var auditCategories = map[string][]string{
	"build": {
		atc.GetBuild,
		atc.GetBuildPlan,
		atc.CreateBuild,
		atc.RerunJobBuild,
		atc.SetBuildComment,
		atc.ListBuilds,
		atc.BuildEvents,
		atc.BuildResources,
		atc.AbortBuild,
		atc.GetBuildPreparation,
		atc.ListBuildsWithVersionAsInput,
		atc.ListBuildsWithVersionAsOutput,
		atc.CreateArtifact,
		atc.GetArtifact,
		atc.ListBuildArtifacts,
	},
	"container": {
		atc.ListContainers,
		atc.GetContainer,
		atc.HijackContainer,
	},
	"job": {
		atc.GetJob,
		atc.CreateJobBuild,
		atc.ListAllJobs,
		atc.ListJobs,
		atc.ListJobBuilds,
		atc.ListJobInputs,
		atc.GetJobBuild,
		atc.PauseJob,
		atc.UnpauseJob,
		atc.ScheduleJob,
		atc.JobBadge,
		atc.MainJobBadge,
	},
	"pipeline": {
		atc.ListAllPipelines,
		atc.ListPipelines,
		atc.GetPipeline,
		atc.DeletePipeline,
		atc.OrderPipelines,
		atc.OrderPipelinesWithinGroup,
		atc.PausePipeline,
		atc.ArchivePipeline,
		atc.UnpausePipeline,
		atc.ExposePipeline,
		atc.HidePipeline,
		atc.RenamePipeline,
		atc.ListPipelineBuilds,
		atc.CreatePipelineBuild,
		atc.PipelineBadge,
	},
	"resource": {
		atc.ListAllResources,
		atc.ListResources,
		atc.ListResourceTypes,
		atc.GetResource,
		atc.UnpinResource,
		atc.SetPinCommentOnResource,
		atc.CheckResource,
		atc.CheckResourceWebHook,
		atc.CheckResourceType,
		atc.CheckPrototype,
		atc.ListResourceVersions,
		atc.GetResourceVersion,
		atc.EnableResourceVersion,
		atc.DisableResourceVersion,
		atc.PinResourceVersion,
		atc.ClearResourceCache,
		atc.GetDownstreamResourceCausality,
		atc.GetUpstreamResourceCausality,
		atc.ListSharedForResource,
		atc.ListSharedForResourceType,
		atc.ClearResourceVersions,
		atc.ClearResourceTypeVersions,
		atc.CopyResourceVersions,
		atc.ListDeprecatedScopes,
	},
	"system": {
		atc.SaveConfig,
		atc.GetConfig,
		atc.GetCC,
		atc.GetVersionsDB,
		atc.ClearTaskCache,
		atc.SetLogLevel,
		atc.GetLogLevel,
		atc.DownloadCLI,
		atc.GetInfo,
		atc.GetHealth,
		atc.GetInfoCreds,
		atc.ListActiveUsersSince,
		atc.GetUser,
		atc.GetWall,
		atc.SetWall,
		atc.ClearWall,
		atc.GetOpenIDConfiguration,
		atc.GetSigningKeys,
	},
	"team": {
		atc.ListTeams,
		atc.SetTeam,
		atc.RenameTeam,
		atc.DestroyTeam,
		atc.ListTeamBuilds,
		atc.GetTeam,
	},
	"worker": {
		atc.RegisterWorker,
		atc.ListWorkers,
		atc.DeleteWorker,
	},
	"volume": {
		atc.ListVolumes,
	},
}

// auditorForCategory builds the auditor production wires in with exactly one
// category switched on, so whatever it logs is that category's membership.
func auditorForCategory(category string, logger lager.Logger) auditor.Auditor {
	on := map[string]bool{category: true}

	return auditor.NewAuditor(
		on["build"],
		on["container"],
		on["job"],
		on["pipeline"],
		on["resource"],
		on["system"],
		on["team"],
		on["worker"],
		on["volume"],
		logger,
	)
}

var _ = Describe("Audit categories", func() {
	var req *http.Request

	auditAllRoutes := func(category string) []string {
		GinkgoHelper()

		logger := lagertest.NewTestLogger("audit-categories")
		aud := auditorForCategory(category, logger)

		for _, route := range atc.Routes {
			aud.Audit(route.Name, "test", req)
		}

		var logged []string
		for _, log := range logger.Logs() {
			logged = append(logged, log.Data["action"].(string))
		}

		return logged
	}

	BeforeEach(func() {
		var err error
		req, err = http.NewRequest("GET", "localhost:8080", nil)
		Expect(err).NotTo(HaveOccurred())
	})

	for category, expected := range auditCategories {
		It("logs exactly the checked-in routes for "+category, func() {
			Expect(auditAllRoutes(category)).To(ConsistOf(expected))
		})
	}

	It("accounts for every route in atc.Routes exactly once", func() {
		var categorised []string
		for _, actions := range auditCategories {
			categorised = append(categorised, actions...)
		}

		var routeNames []string
		for _, route := range atc.Routes {
			routeNames = append(routeNames, route.Name)
		}

		Expect(categorised).To(ConsistOf(routeNames))
	})

	Describe("the public OIDC discovery endpoints", func() {
		It("audits them as system, not worker", func() {
			Expect(auditAllRoutes("system")).To(ContainElements(
				atc.GetOpenIDConfiguration,
				atc.GetSigningKeys,
			))

			Expect(auditAllRoutes("worker")).NotTo(ContainElement(
				atc.GetOpenIDConfiguration,
			))
			Expect(auditAllRoutes("worker")).NotTo(ContainElement(
				atc.GetSigningKeys,
			))
		})
	})
})
