package accessor

import (
	"fmt"

	"github.com/concourse/concourse/atc"
)

const (
	MemberRole   = "member"
	OwnerRole    = "owner"
	OperatorRole = "pipeline-operator"
	ViewerRole   = "viewer"
)

var DefaultRoles = map[string]string{
	atc.SaveConfig:                     MemberRole,
	atc.GetConfig:                      ViewerRole,
	atc.GetCC:                          ViewerRole,
	atc.GetBuild:                       ViewerRole,
	atc.GetBuildPlan:                   ViewerRole,
	atc.CreateBuild:                    MemberRole,
	atc.ListBuilds:                     ViewerRole,
	atc.BuildEvents:                    ViewerRole,
	atc.BuildResources:                 ViewerRole,
	atc.AbortBuild:                     OperatorRole,
	atc.GetBuildPreparation:            ViewerRole,
	atc.GetJob:                         ViewerRole,
	atc.CreateJobBuild:                 OperatorRole,
	atc.RerunJobBuild:                  OperatorRole,
	atc.SetBuildComment:                OperatorRole,
	atc.ListAllJobs:                    ViewerRole,
	atc.ListJobs:                       ViewerRole,
	atc.ListJobBuilds:                  ViewerRole,
	atc.ListJobInputs:                  ViewerRole,
	atc.GetJobBuild:                    ViewerRole,
	atc.PauseJob:                       OperatorRole,
	atc.UnpauseJob:                     OperatorRole,
	atc.ScheduleJob:                    OperatorRole,
	atc.GetVersionsDB:                  ViewerRole,
	atc.JobBadge:                       ViewerRole,
	atc.MainJobBadge:                   ViewerRole,
	atc.ClearTaskCache:                 OperatorRole,
	atc.ListAllResources:               ViewerRole,
	atc.ListResources:                  ViewerRole,
	atc.ListResourceTypes:              ViewerRole,
	atc.GetResource:                    ViewerRole,
	atc.UnpinResource:                  OperatorRole,
	atc.SetPinCommentOnResource:        OperatorRole,
	atc.CheckResource:                  OperatorRole,
	atc.CheckResourceWebHook:           OperatorRole,
	atc.CheckResourceType:              OperatorRole,
	atc.CheckPrototype:                 OperatorRole,
	atc.ListResourceVersions:           ViewerRole,
	atc.GetResourceVersion:             ViewerRole,
	atc.EnableResourceVersion:          OperatorRole,
	atc.DisableResourceVersion:         OperatorRole,
	atc.PinResourceVersion:             OperatorRole,
	atc.ListBuildsWithVersionAsInput:   ViewerRole,
	atc.ListBuildsWithVersionAsOutput:  ViewerRole,
	atc.GetDownstreamResourceCausality: ViewerRole,
	atc.GetUpstreamResourceCausality:   ViewerRole,
	atc.ClearResourceCache:             OperatorRole,
	atc.CopyResourceVersions:           OperatorRole,
	atc.ListDeprecatedScopes:           ViewerRole,
	atc.ListAllPipelines:               ViewerRole,
	atc.ListPipelines:                  ViewerRole,
	atc.GetPipeline:                    ViewerRole,
	atc.DeletePipeline:                 MemberRole,
	atc.OrderPipelines:                 MemberRole,
	atc.OrderPipelinesWithinGroup:      MemberRole,
	atc.PausePipeline:                  OperatorRole,
	atc.ArchivePipeline:                MemberRole,
	atc.UnpausePipeline:                OperatorRole,
	atc.ExposePipeline:                 MemberRole,
	atc.HidePipeline:                   MemberRole,
	atc.RenamePipeline:                 MemberRole,
	atc.ListPipelineBuilds:             ViewerRole,
	atc.CreatePipelineBuild:            MemberRole,
	atc.PipelineBadge:                  ViewerRole,
	atc.CreatePipelineRun:              MemberRole,
	atc.ListPipelineRuns:               ViewerRole,
	atc.GetPipelineRun:                 ViewerRole,
	atc.RegisterWorker:                 MemberRole,
	atc.ListWorkers:                    ViewerRole,
	atc.DeleteWorker:                   MemberRole,
	atc.SetLogLevel:                    MemberRole,
	atc.GetLogLevel:                    ViewerRole,
	atc.DownloadCLI:                    ViewerRole,
	atc.GetInfo:                        ViewerRole,
	atc.GetInfoCreds:                   ViewerRole,
	atc.GetHealth:                      ViewerRole,
	atc.ListContainers:                 ViewerRole,
	atc.GetContainer:                   ViewerRole,
	atc.HijackContainer:                MemberRole,
	atc.ListVolumes:                    ViewerRole,
	atc.ListTeams:                      ViewerRole,
	atc.GetTeam:                        ViewerRole,
	atc.SetTeam:                        OwnerRole,
	atc.RenameTeam:                     OwnerRole,
	atc.DestroyTeam:                    OwnerRole,
	atc.ListTeamBuilds:                 ViewerRole,
	atc.CreateArtifact:                 MemberRole,
	atc.GetArtifact:                    MemberRole,
	atc.ListBuildArtifacts:             ViewerRole,
	atc.GetWall:                        ViewerRole,
	// Agent review/feedback routes. Every route wrapped in
	// CheckAuthorizationHandler needs an entry here: a missing entry
	// resolves to requiredRole "" and hasRequiredRole's default case,
	// making the route admin-only. (The atc/integration suite's login
	// user is on the main team and therefore admin, so it would not
	// catch a regression here.)
}

// EffectiveRole reports the role an action requires once custom assignments
// are layered over the defaults. It is the one rule RequiredRole enforces at
// request time, so validation and enforcement cannot drift apart.
func EffectiveRole(customRoles map[string]string, action string) string {
	if role := customRoles[action]; role != "" {
		return role
	}
	return DefaultRoles[action]
}

// ValidateCustomRoles refuses an assignment that would make creating a
// pipeline run a weaker capability than setting a pipeline config.
//
// A run parameter value is interpolated verbatim into the materialized payload
// config (atc/run_config.go), which is then saved and credential-evaluated as
// an ordinary pipeline. A ((...)) reference supplied as a parameter value
// therefore resolves against the team's own secrets at check/get/put time, so
// creating a run carries exactly the trust that setting a pipeline does. The
// stock roles already agree; this refuses the one configuration that would
// turn that equivalence into a privilege escalation.
func ValidateCustomRoles(customRoles map[string]string) error {
	runRole := EffectiveRole(customRoles, atc.CreatePipelineRun)
	saveRole := EffectiveRole(customRoles, atc.SaveConfig)

	if !RoleHasRequiredRole(runRole, saveRole) {
		return fmt.Errorf(
			"%s may not require a weaker role (%s) than %s (%s): a run parameter value is interpolated verbatim into the materialized pipeline config, so creating a run carries the same trust as setting one",
			atc.CreatePipelineRun, runRole, atc.SaveConfig, saveRole,
		)
	}

	return nil
}
