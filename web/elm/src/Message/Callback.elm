module Message.Callback exposing (Callback(..))

import Browser.Dom
import Concourse
import Concourse.Agent
import Concourse.AgentDiff
import Concourse.AgentDispatcher
import Concourse.AgentReview
import Concourse.AgentTicket
import Concourse.Pagination exposing (Page, Paginated)
import Http
import Message.Message
    exposing
        ( DomID
        , VersionId
        , VersionToggleAction
        , VisibilityAction
        )
import Time


type alias Fetched a =
    Result Http.Error a


type Callback
    = EmptyCallback
    | GotCurrentTime Time.Posix
    | GotCurrentTimeZone Time.Zone
    | BuildTriggered (Fetched Concourse.Build)
    | BuildCommentSet Int String (Fetched ())
    | JobBuildsFetched (Fetched ( Page, Paginated Concourse.Build ))
    | JobFetched (Fetched Concourse.Job)
    | JobsFetched (Fetched (List Concourse.Job))
    | PipelineRunsFetched (Fetched (List Concourse.PipelineRun))
    | PipelineFetched (Fetched Concourse.Pipeline)
    | PipelinesFetched (Fetched (List Concourse.Pipeline))
    | PipelineToggled Concourse.PipelineIdentifier (Fetched ())
    | PipelinesOrdered Concourse.TeamName (Fetched ())
    | UserFetched (Fetched Concourse.User)
    | ResourcesFetched (Fetched (List Concourse.Resource))
    | BuildResourcesFetched (Fetched ( Int, Concourse.BuildResources ))
    | ResourceFetched (Fetched Concourse.Resource)
    | VersionedResourceFetched (Fetched Concourse.VersionedResource)
    | VersionedResourcesFetched (Fetched ( Page, Paginated Concourse.VersionedResource ))
    | VersionedResourceIdFetched (Fetched (Maybe Concourse.VersionedResource))
    | ClusterInfoFetched (Fetched Concourse.ClusterInfo)
    | WallFetched (Fetched Concourse.Wall)
    | PausedToggled (Fetched ())
    | InputToFetched (Fetched ( VersionId, List Concourse.Build ))
    | OutputOfFetched (Fetched ( VersionId, List Concourse.Build ))
    | CausalityFetched (Fetched ( Concourse.CausalityDirection, Maybe Concourse.Causality ))
    | VersionPinned (Fetched ())
    | VersionUnpinned (Fetched ())
    | VersionToggled VersionToggleAction VersionId (Fetched ())
    | Checked (Fetched Concourse.Build)
    | CommentSet (Fetched ())
    | AllTeamsFetched (Fetched (List Concourse.Team))
    | AllJobsFetched (Fetched (List Concourse.Job))
    | AllResourcesFetched (Fetched (List Concourse.Resource))
    | LoggedOut (Fetched ())
    | ScreenResized Browser.Dom.Viewport
    | BuildJobDetailsFetched (Fetched Concourse.Job)
    | BuildFetched (Fetched Concourse.Build)
    | BuildPrepFetched Concourse.BuildId (Fetched Concourse.BuildPrep)
    | BuildHistoryFetched (Fetched (Paginated Concourse.Build))
    | PlanAndResourcesFetched Int (Fetched ( Concourse.BuildPlan, Concourse.BuildResources ))
    | BuildAborted (Fetched ())
    | VisibilityChanged VisibilityAction Concourse.PipelineIdentifier (Fetched ())
    | AllPipelinesFetched (Fetched (List Concourse.Pipeline))
    | GotViewport DomID (Result Browser.Dom.Error Browser.Dom.Viewport)
    | GotElement (Result Browser.Dom.Error Browser.Dom.Element)
    | BuildAgentReviewsFetched (Fetched (List Concourse.AgentReview.BuildReview))
    | TeamAgentReviewsFetched (Fetched (List Concourse.AgentReview.Summary))
    | AgentReviewVerdictSubmitted String (Fetched ())
    | AgentRunMetricsFetched (Fetched (List Concourse.Agent.RunMetric))
    | BuildAgentMetricsFetched (Fetched (List Concourse.Agent.RunMetric))
    | AgentWorkflowsFetched (Fetched (List Concourse.Agent.WorkflowSummary))
    | AgentCostRollupFetched (Fetched Concourse.Agent.CostRollup)
    | AgentDispatcherFetched (Fetched Concourse.AgentDispatcher.Status)
    | AgentDispatcherSet (Fetched Concourse.AgentDispatcher.Status)
    | AgentCredentialsFetched (Fetched (List Concourse.Agent.CredentialStatus))
    | AgentPlatformCredentialsFetched (Fetched (List Concourse.Agent.CredentialStatus))
    | AgentPrincipalsFetched (Fetched (List Concourse.Agent.Principal))
    | AgentPrincipalCreated (Fetched Concourse.Agent.PrincipalCreated)
    | AgentPrincipalRevoked (Fetched ())
    | AgentTicketsFetched (Fetched (List Concourse.AgentTicket.Ticket))
    | AgentTicketFetched (Fetched Concourse.AgentTicket.Detail)
    | AgentTicketSaved Int (Fetched ())
    | AgentTicketTransitioned Int (Fetched ())
    | AgentTicketDispatched Int (Fetched Concourse.AgentTicket.DispatchResult)
    | AgentTicketTaskUpdated Int (Fetched ())
    | AgentTicketMetricsFetched Int (Fetched (List Concourse.Agent.RunMetric))
    | AgentTicketDiffFetched (Fetched Concourse.AgentDiff.DiffPage)
