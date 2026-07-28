module Message.Message exposing
    ( CommentBarButtonKind(..)
    , DomID(..)
    , DropTarget(..)
    , Message(..)
    , PipelinesSection(..)
    , VersionId
    , VersionToggleAction(..)
    , VisibilityAction(..)
    )

import Concourse
import Concourse.Pagination exposing (Page)
import Dashboard.Group.Models
import Routes exposing (StepID)
import StrictEvents


type Message
    = -- Top Bar
      FilterMsg String
    | FocusMsg
    | BlurMsg
      -- Pipeline
    | ToggleGroup Concourse.PipelineGroup
      -- Dashboard
    | DragStart Dashboard.Group.Models.Card
    | DragOver DropTarget
    | DragEnd
      -- Resource
    | EditComment String
    | FocusTextArea
    | BlurTextArea
      -- Build
    | ScrollBuilds StrictEvents.WheelEvent
    | RevealCurrentBuildInHistory
    | SetHighlight String Int
    | ExtendHighlight String Int
      -- Comment Bar
    | EditCommentBar DomID String
    | FocusCommentBar DomID
    | BlurCommentBar DomID
      -- Download Fly Page
    | PlatformSelected String
    | GetHostname
      -- Agent Review
    | ToggleAgentReviewPanel
    | ToggleAgentReviewFinding String
    | ToggleAgentReviewFindingBody String
    | ToggleAgentReviewObservations Bool
    | AgentReviewVerdictClicked
        { reviewSnapshotId : String
        , findingId : String
        , verdict : String
        , reviewer : String
        }
    | AgentReviewNoteChanged String String
    | AgentReviewsUnevaluatedToggled Bool
    | AgentReviewsPipelineFilterChanged String
    | AgentRunExpandToggled String
    | AgentSectionNavClicked String
      -- Agent Ticket page (edit + disposition)
    | ClickAgentTicketEdit
    | AgentTicketTitleChanged String
    | AgentTicketBodyChanged String
    | ClickAgentTicketSave
    | ClickAgentTicketCancel
    | ClickAgentTicketTransition String
    | ConfirmAgentTicketTransition
    | CancelAgentTicketTransition
    | ClickAgentTicketDispatch
    | ConfirmAgentTicketDispatch
    | CancelAgentTicketDispatch
    | DismissAgentTicketDispatchNotice
      -- Agent Ticket queue page (client-side filter + sort)
    | AgentTicketsFilterChanged String
    | AgentTicketsSortToggled
      -- Agent workflow function pages
    | AgentWorkflowInputChanged String String
    | AgentWorkflowVersionSelected String
    | AgentWorkflowRunFilterChanged String
    | AgentWorkflowStartClicked
    | AgentWorkflowPromoteClicked Int
    | AgentWorkflowRunCancelClicked
    | AgentWorkflowRunRetryClicked
    | AgentWaitAnswerChanged String String
      -- Agent transcripts (run page): plan id, then plan id + entry index
    | AgentTranscriptToggled String
    | AgentTranscriptEntryToggled String Int
    | AgentWaitResolveClicked String
    | AgentSnapshotPinClicked
    | AgentSnapshotUnpinClicked
    | AgentExperimentCancelClicked
      -- Agent Ticket queue page (dispatcher runtime control)
    | AgentDispatcherModeClicked String
    | ConfirmAgentDispatcherMode String
    | CancelAgentDispatcherMode
      -- common
    | Hover (Maybe DomID)
    | Click DomID
    | GoToRoute Routes.Route
    | Scrolled StrictEvents.ScrollState
    | NoOp


type DomID
    = ToggleJobButton
    | CommentBar DomID
    | CommentBarButton CommentBarButtonKind DomID
    | BuildComment
    | ToggleBuildCommentButton
    | TriggerBuildButton
    | AbortBuildButton
    | RerunBuildButton
    | JobName
    | JobBuildLink Concourse.BuildName
    | PreviousPageButton
    | NextPageButton
    | CheckButton Bool
    | EditButton
    | SaveCommentButton
    | ResourceCommentTextarea
    | ChangedStepLabel StepID String
    | StepState StepID
    | PinIcon
    | PinMenuDropDown String
    | PinButton VersionId
    | PinBar
    | PipelineCardName PipelinesSection Concourse.DatabaseID
    | InstanceGroupCardName PipelinesSection Concourse.TeamName String
    | PipelineCardNameHD Concourse.DatabaseID
    | InstanceGroupCardNameHD Concourse.TeamName String
    | PipelineCardInstanceVar PipelinesSection Concourse.DatabaseID String String
    | PipelineCardInstanceVars PipelinesSection Concourse.DatabaseID Concourse.InstanceVars
    | PipelineStatusIcon PipelinesSection Concourse.DatabaseID
    | PipelineCardFavoritedIcon PipelinesSection Concourse.DatabaseID
    | InstanceGroupCardFavoritedIcon PipelinesSection Concourse.InstanceGroupIdentifier
    | PipelineCardPauseToggle PipelinesSection Concourse.DatabaseID
    | TopBarPipelineName Concourse.DatabaseID
    | TopBarPinIcon
    | TopBarFavoritedIcon Concourse.DatabaseID
    | TopBarPauseToggle Concourse.PipelineIdentifier
    | VisibilityButton PipelinesSection Concourse.DatabaseID
    | CopyTokenButton
    | SendTokenButton
    | CopyTokenInput
    | JobGroup Int
    | StepTab String Int
    | StepHeader String
    | StepSubHeader String Int
    | StepInitialization String
    | StepVersion String
    | ShowSearchButton
    | ClearSearchButton
    | LoginButton
    | LogoutButton
    | UserMenu
    | UserDisplayName String
    | CreatedBy String
    | PaginationButton Page
    | VersionHeader VersionId
    | VersionToggle VersionId
    | BuildTab Int String
    | JobPreview PipelinesSection Concourse.DatabaseID Concourse.JobName
    | PipelinePreview PipelinesSection Concourse.DatabaseID
    | SideBarIcon
    | SideBarResizeHandle
    | SideBarTeam PipelinesSection String
    | SideBarPipeline PipelinesSection Concourse.DatabaseID
    | SideBarInstancedPipeline PipelinesSection Concourse.DatabaseID
    | SideBarInstanceGroup PipelinesSection Concourse.TeamName String
    | SideBarPipelineFavoritedIcon Concourse.DatabaseID
    | SideBarInstanceGroupFavoritedIcon Concourse.InstanceGroupIdentifier
    | Dashboard
    | DashboardGroup String
    | InputsTo VersionId
    | OutputsOf VersionId


type PipelinesSection
    = FavoritesSection
    | AllPipelinesSection


type VersionToggleAction
    = Enable
    | Disable


type VisibilityAction
    = Expose
    | Hide


type CommentBarButtonKind
    = Edit
    | Save


type alias VersionId =
    Concourse.VersionedResourceIdentifier


type DropTarget
    = Before Dashboard.Group.Models.Card
    | End
