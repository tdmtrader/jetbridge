module Build.Build exposing
    ( bodyId
    , changeToBuild
    , documentTitle
    , getScrollBehavior
    , getUpdateMessage
    , handleCallback
    , handleDelivery
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

import Api.Endpoints as Endpoints
import Application.Models exposing (Session)
import Assets
import Build.AgentReview
import Build.Header.Header as Header
import Build.Header.Models exposing (BuildPageType(..), CommentBarVisibility(..), CurrentOutput(..), commentBarIsVisible)
import Build.Models exposing (Model, toMaybe)
import Build.Output.Models exposing (OutputModel)
import Build.Output.Output
import Build.Shortcuts as Shortcuts
import Build.StepTree.Models as STModels
import Build.StepTree.StepTree as StepTree
import Build.Styles as Styles
import Concourse
import Concourse.Agent
import Concourse.AgentReview
import Concourse.BuildStatus exposing (BuildStatus(..))
import DateFormat
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import HoverState
import Html exposing (Html)
import Html.Attributes
    exposing
        ( attribute
        , class
        , classList
        , id
        , style
        , tabindex
        , title
        )
import Html.Lazy
import Http
import List.Extra
import Login.Login as Login
import Maybe.Extra
import Message.Callback exposing (Callback(..))
import Message.Effects as Effects exposing (Effect(..))
import Message.Message exposing (DomID(..), Message(..))
import Message.ScrollDirection as ScrollDirection
import Message.Subscription as Subscription exposing (Delivery(..), Interval(..), Subscription(..))
import Message.TopLevelMessage exposing (TopLevelMessage(..))
import Routes
import Set exposing (Set)
import SideBar.SideBar as SideBar
import StrictEvents exposing (onScroll)
import String
import Time
import Tooltip
import UpdateMsg exposing (UpdateMsg)
import UserState
import Views.CommentBar as CommentBar exposing (State(..))
import Views.Icon as Icon
import Views.LoadingIndicator as LoadingIndicator
import Views.NotAuthorized as NotAuthorized
import Views.Spinner as Spinner
import Views.Styles
import Views.TopBar as TopBar


bodyId : String
bodyId =
    "build-body"


type alias Flags =
    { highlight : Routes.Highlight
    , pageType : BuildPageType
    , fromBuildPage : Maybe Build.Header.Models.BuildPageType
    }


type ScrollBehavior
    = ScrollWindow
    | ScrollToID String
    | NoScroll


init : Flags -> ( Model, List Effect )
init flags =
    changeToBuild
        flags
        ( { page = flags.pageType
          , id = 0
          , name =
                case flags.pageType of
                    OneOffBuildPage id ->
                        String.fromInt id

                    JobBuildPage { buildName } ->
                        buildName
          , now = Nothing
          , job = Nothing
          , disableManualTrigger = False
          , history = []
          , nextPage = Nothing
          , comment = Hidden ""
          , prep = Nothing
          , duration = { startedAt = Nothing, finishedAt = Nothing }
          , status = BuildStatusPending
          , output = Empty
          , autoScroll = True
          , isScrollToIdInProgress = False
          , previousKeyPress = Nothing
          , isTriggerBuildKeyDown = False
          , showHelp = False
          , highlight = flags.highlight
          , authorized = True
          , fetchingHistory = False
          , scrolledToCurrentBuild = False
          , shiftDown = False
          , isUserMenuExpanded = False
          , hasLoadedYet = False
          , notFound = False
          , reapTime = Nothing
          , createdBy = Nothing
          , agentReviews = []
          , agentRunMetrics = []
          , agentFetchedBuildId = Nothing
          , agentReviewLoadError = False
          , agentReviewPanelExpanded = True
          , expandedFindings = Set.empty
          , showObservations = Nothing
          , agentReviewNotes = Dict.empty
          , verdictErrors = Set.empty
          , expandedDescriptions = Set.empty
          }
        , [ GetCurrentTime
          , GetCurrentTimeZone
          , FetchAllPipelines
          ]
        )


subscriptions : Model -> List Subscription
subscriptions model =
    let
        buildEventsUrl =
            model.output
                |> toMaybe
                |> Maybe.andThen .eventStreamUrlPath
    in
    [ OnClockTick OneSecond
    , OnClockTick FiveSeconds
    , OnKeyDown
    , OnKeyUp
    , OnElementVisible
    , OnScrolledToId
    ]
        ++ (case buildEventsUrl of
                Nothing ->
                    []

                Just url ->
                    [ Subscription.FromEventSource ( url, [ "end", "event" ] ) ]
           )


changeToBuild : Flags -> ET Model
changeToBuild { highlight, pageType, fromBuildPage } ( model, effects ) =
    let
        newModel =
            { model | page = pageType }
    in
    (if fromBuildPage == Just pageType then
        ( newModel, effects )

     else
        ( { newModel
            | prep = Nothing
            , output = Empty
            , autoScroll = True
            , highlight = highlight
            , agentRunMetrics = []
          }
        , case pageType of
            OneOffBuildPage buildId ->
                effects
                    ++ [ CloseBuildEventStream, FetchBuild 0 buildId ]

            JobBuildPage jbi ->
                effects
                    ++ [ CloseBuildEventStream, FetchJobBuild jbi ]
        )
    )
        |> Header.changeToBuild pageType


extractTitle : Model -> String
extractTitle model =
    case ( model.hasLoadedYet, model.job, model.page ) of
        ( True, Just { jobName }, _ ) ->
            jobName ++ " #" ++ model.name

        ( _, _, JobBuildPage { jobName, buildName } ) ->
            jobName ++ " #" ++ buildName

        ( _, _, OneOffBuildPage id ) ->
            "#" ++ String.fromInt id


getUpdateMessage : Model -> UpdateMsg
getUpdateMessage model =
    if model.notFound then
        UpdateMsg.NotFound

    else
        UpdateMsg.AOK


handleCallback : Callback -> ET Model
handleCallback action ( model, effects ) =
    (case action of
        BuildFetched (Ok build) ->
            handleBuildFetched build ( model, effects )

        BuildFetched (Err err) ->
            case err of
                Http.BadStatus { status } ->
                    if status.code == 401 then
                        ( model, effects ++ [ RedirectToLogin ] )

                    else if status.code == 404 then
                        ( { model
                            | prep = Nothing
                            , notFound = True
                          }
                        , effects
                        )

                    else
                        ( model, effects )

                _ ->
                    ( model, effects )

        BuildAborted (Ok ()) ->
            ( model, effects )

        BuildPrepFetched buildId (Ok buildPrep) ->
            if buildId == model.id then
                handleBuildPrepFetched buildPrep ( model, effects )

            else
                ( model, effects )

        BuildPrepFetched _ (Err err) ->
            case err of
                Http.BadStatus { status } ->
                    if status.code == 401 then
                        ( { model | authorized = False }, effects )

                    else
                        ( model, effects )

                _ ->
                    ( model, effects )

        PlanAndResourcesFetched buildId (Ok planAndResources) ->
            updateOutput
                (Build.Output.Output.planAndResourcesFetched
                    buildId
                    planAndResources
                )
                ( model
                , effects
                    ++ [ Effects.OpenBuildEventStream
                            { url =
                                Endpoints.BuildEventStream
                                    |> Endpoints.Build buildId
                                    |> Endpoints.toString []
                            , eventTypes = [ "end", "event" ]
                            }
                       , SyncStickyBuildLogHeaders
                       ]
                )

        PlanAndResourcesFetched _ (Err err) ->
            case err of
                Http.BadStatus { status } ->
                    let
                        isAborted =
                            model.status == BuildStatusAborted
                    in
                    if status.code == 404 && isAborted then
                        ( { model | output = Cancelled }
                        , effects
                        )

                    else if status.code == 401 then
                        ( { model | authorized = False }, effects )

                    else
                        ( model, effects )

                _ ->
                    ( model, effects )

        BuildJobDetailsFetched (Ok job) ->
            ( { model | disableManualTrigger = job.disableManualTrigger }
            , effects
            )

        BuildJobDetailsFetched (Err _) ->
            -- https://github.com/concourse/concourse/issues/3201
            ( model, effects )

        BuildAgentReviewsFetched (Ok reviews) ->
            ( { model | agentReviews = reviews, agentReviewLoadError = False }, effects )

        BuildAgentReviewsFetched (Err _) ->
            -- Missing review (empty list) is a normal state and renders
            -- nothing; an API error renders a quiet one-line notice instead
            -- of breaking the page.
            ( { model | agentReviewLoadError = True }, effects )

        BuildAgentMetricsFetched (Ok rows) ->
            -- The callback carries no build id, so a slow response for the
            -- previous build can land after an in-app switch; each row
            -- carries its build id, so keep only the current build's.
            ( { model | agentRunMetrics = List.filter (\r -> r.buildId == model.id) rows }, effects )

        BuildAgentMetricsFetched (Err _) ->
            -- The cost chip is best-effort provenance; a fetch error just
            -- leaves it hidden.
            ( model, effects )

        AgentReviewVerdictSubmitted findingId (Ok ()) ->
            ( { model | verdictErrors = Set.remove findingId model.verdictErrors }
            , effects ++ [ FetchBuildAgentReviews model.id ]
            )

        AgentReviewVerdictSubmitted findingId (Err _) ->
            ( { model | verdictErrors = Set.insert findingId model.verdictErrors }, effects )

        _ ->
            ( model, effects )
    )
        |> Header.handleCallback action


handleDelivery : { a | hovered : HoverState.HoverState } -> Delivery -> ET Model
handleDelivery session delivery ( model, effects ) =
    (case delivery of
        ClockTicked OneSecond time ->
            ( { model | now = Just time }, effects )

        ClockTicked FiveSeconds _ ->
            ( model, effects ++ [ Effects.FetchAllPipelines ] )

        WindowResized _ _ ->
            ( model, effects ++ [ SyncStickyBuildLogHeaders ] )

        EventsReceived (Ok envelopes) ->
            let
                eventSourceClosed =
                    model.output
                        |> toMaybe
                        |> Maybe.map (.eventSourceOpened >> not)
                        |> Maybe.withDefault False

                buildStatus =
                    envelopes
                        |> List.filterMap
                            (\{ data } ->
                                case data of
                                    STModels.BuildStatus status date ->
                                        Just ( status, date )

                                    _ ->
                                        Nothing
                            )
                        |> List.Extra.last

                ( newModel, newEffects ) =
                    updateOutput
                        (Build.Output.Output.handleEnvelopes envelopes)
                        (if eventSourceClosed && (envelopes |> List.map .data |> List.member STModels.NetworkError) then
                            ( { model | authorized = False }, effects )

                         else
                            case getScrollBehavior model of
                                ScrollWindow ->
                                    ( model
                                    , effects
                                        ++ [ Effects.Scroll
                                                ScrollDirection.ToBottom
                                                bodyId
                                           ]
                                    )

                                ScrollToID id ->
                                    ( { model
                                        | highlight = Routes.HighlightNothing
                                        , autoScroll = False
                                        , isScrollToIdInProgress = True
                                      }
                                    , effects
                                        ++ [ Effects.Scroll
                                                (ScrollDirection.ToId id)
                                                bodyId
                                           ]
                                    )

                                NoScroll ->
                                    ( model, effects )
                        )
            in
            case ( model.hasLoadedYet, buildStatus ) of
                ( True, Just ( status, _ ) ) ->
                    ( newModel
                    , (if Concourse.BuildStatus.isRunning model.status then
                        newEffects ++ [ SetFavIcon (Just status) ]

                       else
                        newEffects
                      )
                        ++ (if
                                Concourse.BuildStatus.isRunning model.status
                                    && not (Concourse.BuildStatus.isRunning status)
                            then
                                -- agent steps ingest their metrics as they
                                -- complete, so a build watched live holds the
                                -- page-load snapshot; refresh the spend chip
                                -- once the build finishes.
                                [ FetchBuildAgentMetrics model.id ]

                            else
                                []
                           )
                    )

                _ ->
                    ( newModel, newEffects )

        ScrolledToId _ ->
            ( { model | isScrollToIdInProgress = False }, effects )

        _ ->
            ( model, effects )
    )
        |> Tooltip.handleDelivery session delivery
        |> Shortcuts.handleDelivery delivery
        |> Header.handleDelivery delivery
        |> handleDeliveryCommentBar delivery


handleDeliveryCommentBar : Delivery -> ET Model
handleDeliveryCommentBar delivery ( model, effects ) =
    case model.comment of
        Hidden _ ->
            ( model, effects )

        Visible commentBar ->
            let
                ( updatedCommentBar, updatedEffects ) =
                    CommentBar.handleDelivery delivery commentBar
            in
            ( { model | comment = Visible updatedCommentBar }
            , effects ++ updatedEffects
            )


update : Message -> ET Model
update msg ( model, effects ) =
    (case msg of
        Click (BuildTab id name) ->
            ( model
            , effects
                ++ [ NavigateTo <|
                        Routes.toString <|
                            Routes.buildRoute id name model.job
                   ]
            )

        Click TriggerBuildButton ->
            (model.job
                |> Maybe.map (DoTriggerBuild >> (::) >> Tuple.mapSecond)
                |> Maybe.withDefault identity
            )
                ( model, effects )

        Click AbortBuildButton ->
            ( model, DoAbortBuild model.id :: effects )

        Click (StepHeader id) ->
            updateOutput
                (Build.Output.Output.handleStepTreeMsg <| StepTree.toggleStep id)
                ( model, effects ++ [ SyncStickyBuildLogHeaders ] )

        Click (StepInitialization id) ->
            updateOutput
                (Build.Output.Output.handleStepTreeMsg <| StepTree.toggleStepInitialization id)
                ( model, effects ++ [ SyncStickyBuildLogHeaders ] )

        Click (StepSubHeader id i) ->
            updateOutput
                (Build.Output.Output.handleStepTreeMsg <| StepTree.toggleStepSubHeader id i)
                ( model, effects ++ [ SyncStickyBuildLogHeaders ] )

        Click (StepTab id tab) ->
            updateOutput
                (Build.Output.Output.handleStepTreeMsg <| StepTree.switchTab id tab)
                ( model, effects )

        SetHighlight id line ->
            updateOutput
                (Build.Output.Output.handleStepTreeMsg <| StepTree.setHighlight id line)
                ( model, effects )

        ExtendHighlight id line ->
            updateOutput
                (Build.Output.Output.handleStepTreeMsg <| StepTree.extendHighlight id line)
                ( model, effects )

        GoToRoute route ->
            ( model, effects ++ [ NavigateTo <| Routes.toString <| route ] )

        Scrolled { scrollHeight, scrollTop, clientHeight } ->
            ( { model
                | autoScroll =
                    (scrollHeight - (scrollTop + clientHeight) <= 1)
                        && not model.isScrollToIdInProgress
              }
            , effects
            )

        ToggleAgentReviewPanel ->
            ( { model | agentReviewPanelExpanded = not model.agentReviewPanelExpanded }, effects )

        ToggleAgentReviewFinding findingId ->
            ( { model
                | expandedFindings =
                    if Set.member findingId model.expandedFindings then
                        Set.remove findingId model.expandedFindings

                    else
                        Set.insert findingId model.expandedFindings
              }
            , effects
            )

        ToggleAgentReviewObservations open ->
            ( { model | showObservations = Just open }, effects )

        ToggleAgentReviewFindingBody findingId ->
            ( { model
                | expandedDescriptions =
                    if Set.member findingId model.expandedDescriptions then
                        Set.remove findingId model.expandedDescriptions

                    else
                        Set.insert findingId model.expandedDescriptions
              }
            , effects
            )

        AgentReviewVerdictClicked params ->
            -- A blank findingId can't disambiguate one finding from another, so
            -- a verdict keyed on it would misattribute human triage feedback.
            -- The card renders blank-id findings read-only, but guard here too.
            -- A blank reviewSnapshotId names no review, and feedback is keyed
            -- by exactly that, so it is dropped for the same reason.
            if params.findingId == "" || params.reviewSnapshotId == "" then
                ( model, effects )

            else
                ( model
                , effects
                    ++ [ SubmitAgentReviewVerdict
                            { reviewSnapshotId = params.reviewSnapshotId
                            , findingId = params.findingId
                            , verdict = params.verdict
                            , notes = Dict.get params.findingId model.agentReviewNotes |> Maybe.withDefault ""
                            , reviewer = params.reviewer
                            }
                       ]
                )

        AgentReviewNoteChanged findingId note ->
            if findingId == "" then
                ( model, effects )

            else
                ( { model | agentReviewNotes = Dict.insert findingId note model.agentReviewNotes }, effects )

        _ ->
            ( model, effects )
    )
        |> Header.update msg
        |> updateCommentBar msg


updateCommentBar : Message -> ET Model
updateCommentBar msg ( model, effects ) =
    case model.comment of
        Hidden _ ->
            ( model, effects )

        Visible commentBar ->
            let
                ( updatedCommentBar, updatedEffects ) =
                    CommentBar.update msg (\content -> SetBuildComment model.id content) commentBar
            in
            ( { model | comment = Visible updatedCommentBar }
            , effects ++ updatedEffects
            )


getScrollBehavior : Model -> ScrollBehavior
getScrollBehavior model =
    case model.highlight of
        Routes.HighlightLine stepID lineNumber ->
            ScrollToID <| stepID ++ ":" ++ String.fromInt lineNumber

        Routes.HighlightRange stepID beginning end ->
            if beginning <= end then
                ScrollToID <| stepID ++ ":" ++ String.fromInt beginning

            else
                NoScroll

        Routes.HighlightNothing ->
            if model.autoScroll then
                if model.hasLoadedYet then
                    case model.status of
                        BuildStatusSucceeded ->
                            NoScroll

                        BuildStatusPending ->
                            NoScroll

                        _ ->
                            ScrollWindow

                else
                    NoScroll

            else
                NoScroll


updateOutput :
    (OutputModel -> ( OutputModel, List Effect ))
    -> ET Model
updateOutput updater ( model, effects ) =
    case model.output of
        Output output ->
            let
                ( newOutput, outputEffects ) =
                    updater output

                newModel =
                    { model
                        | output =
                            -- model.output must be equal-by-reference
                            -- to its previous value when passed
                            -- into `Html.Lazy.lazy3` below.
                            if newOutput /= output then
                                Output newOutput

                            else
                                model.output
                    }
            in
            ( newModel, effects ++ outputEffects )

        _ ->
            ( model, effects )


handleBuildFetched : Concourse.Build -> ET Model
handleBuildFetched build ( model, effects ) =
    let
        agentRefetch =
            model.agentFetchedBuildId /= Just build.id

        withBuild =
            { model
                | reapTime = build.reapTime
                , output =
                    if model.hasLoadedYet then
                        model.output

                    else
                        Empty
                , agentFetchedBuildId = Just build.id
                , agentRunMetrics =
                    if agentRefetch then
                        []

                    else
                        model.agentRunMetrics
                , agentReviews =
                    if agentRefetch then
                        []

                    else
                        model.agentReviews
                , agentReviewLoadError =
                    if agentRefetch then
                        False

                    else
                        model.agentReviewLoadError
            }

        fetchJobAndHistory =
            case ( model.job, build.job ) of
                ( Nothing, Just buildJob ) ->
                    [ FetchBuildJobDetails buildJob
                    , FetchBuildHistory buildJob Nothing
                    ]

                _ ->
                    []

        fetchAgentReviews =
            -- Fetch agent reviews/metrics once per build id, clearing the
            -- previous build's rows when the id changes. model.id can't
            -- serve as the guard: Header.changeToBuild stamps it with the
            -- target build BEFORE that build's BuildFetched arrives, so an
            -- id-comparison guard never fires on in-app build switches and
            -- the previous build's reviews and spend would stick. Tracking
            -- the id we last fetched for survives the switch and still
            -- suppresses the 1s pending-poll spam.
            if agentRefetch then
                [ FetchBuildAgentReviews build.id
                , FetchBuildAgentMetrics build.id
                ]

            else
                []

        ( newModel, cmd ) =
            if build.status == BuildStatusPending then
                ( withBuild, effects ++ pollUntilStarted build.id )

            else if build.reapTime == Nothing then
                case model.prep of
                    Nothing ->
                        initBuildOutput build ( withBuild, effects )

                    Just _ ->
                        let
                            ( newNewModel, newEffects ) =
                                initBuildOutput build ( withBuild, effects )
                        in
                        ( newNewModel
                        , newEffects
                            ++ [ FetchBuildPrep 1000 build.id ]
                        )

            else
                ( withBuild, effects )
    in
    if not model.hasLoadedYet || build.id == model.id then
        ( newModel
        , cmd
            ++ fetchJobAndHistory
            ++ fetchAgentReviews
            ++ SetFavIcon (Just build.status)
            :: (commentBarIsVisible model.comment
                    |> Maybe.map
                        (\commentBar ->
                            case commentBar.state of
                                Editing _ ->
                                    []

                                _ ->
                                    [ Focus bodyId ]
                        )
                    |> Maybe.withDefault [ Focus bodyId ]
               )
        )

    else
        ( model, effects )


pollUntilStarted : Int -> List Effect
pollUntilStarted buildId =
    [ FetchBuild 1000 buildId
    , FetchBuildPrep 1000 buildId
    ]


initBuildOutput : Concourse.Build -> ET Model
initBuildOutput build ( model, effects ) =
    let
        ( output, outputCmd ) =
            Build.Output.Output.init model.highlight build
    in
    ( { model | output = Output output }
    , effects ++ outputCmd
    )


handleBuildPrepFetched : Concourse.BuildPrep -> ET Model
handleBuildPrepFetched buildPrep ( model, effects ) =
    ( { model | prep = Just buildPrep }
    , effects
    )


documentTitle : Model -> String
documentTitle =
    extractTitle


view : Session -> Model -> Html Message
view session model =
    let
        route =
            case model.page of
                OneOffBuildPage buildId ->
                    Routes.OneOffBuild
                        { id = buildId
                        , highlight = model.highlight
                        }

                JobBuildPage buildId ->
                    Routes.Build
                        { id = buildId
                        , highlight = model.highlight
                        , groups = []
                        }
    in
    Html.div
        (id "page-including-top-bar" :: Views.Styles.pageIncludingTopBar)
        [ Html.div
            (id "top-bar-app" :: Views.Styles.topBar False)
            (SideBar.sideBarIcon session
                :: breadcrumbs session model
                ++ [ Login.view session.userState model ]
            )
        , Html.div
            (id "page-below-top-bar" :: Views.Styles.pageBelowTopBar route)
            [ SideBar.view session
                (model.job
                    |> Maybe.map
                        (\j ->
                            { pipelineName = j.pipelineName
                            , pipelineInstanceVars = j.pipelineInstanceVars
                            , teamName = j.teamName
                            }
                        )
                )
            , viewBuildPage session model
            ]
        ]


tooltip : Model -> Session -> Maybe Tooltip.Tooltip
tooltip model session =
    tryAll
        [ model.output
            |> toMaybe
            |> Maybe.andThen .steps
            |> Maybe.andThen (\steps -> StepTree.tooltip steps session)
        , Header.tooltip model session
        ]


tryAll : List (Maybe a) -> Maybe a
tryAll maybes =
    List.foldl
        (\prev cur ->
            case prev of
                Nothing ->
                    cur

                _ ->
                    prev
        )
        Nothing
        maybes


breadcrumbs : Session -> Model -> List (Html Message)
breadcrumbs session model =
    case ( model.job, model.page ) of
        ( Just jobId, _ ) ->
            TopBar.breadcrumbs session <|
                Routes.Job
                    { id = jobId
                    , page = Nothing
                    , groups = Routes.getGroups session.route
                    }

        ( _, JobBuildPage buildId ) ->
            TopBar.breadcrumbs session <|
                Routes.Build
                    { id = buildId
                    , highlight = model.highlight
                    , groups = Routes.getGroups session.route
                    }

        _ ->
            [ Html.text "" ]


viewBuildPage : Session -> Model -> Html Message
viewBuildPage session model =
    if model.hasLoadedYet then
        Html.div
            [ class "with-fixed-header"
            , attribute "data-build-name" model.name
            , style "flex-grow" "1"
            , style "display" "flex"
            , style "flex-direction" "column"
            , style "overflow" "hidden"
            ]
            [ Header.view session model
            , agentCostBar model.agentRunMetrics
            , body session model
            ]

    else
        LoadingIndicator.view


agentCostBar : List Concourse.Agent.RunMetric -> Html Message
agentCostBar agentRunMetrics =
    let
        totalCost =
            agentRunMetrics |> List.map .costUsd |> List.sum

        runCount =
            List.length agentRunMetrics
    in
    if totalCost > 0 then
        Html.div
            [ id "build-agent-cost-bar"
            , style "display" "flex"
            , style "align-items" "center"
            , style "gap" "6px"
            , style "padding" "6px 12px"
            , style "background" "#1b222b"
            , style "border-bottom" "1px solid #2d3a48"
            , style "color" "#9aa39b"
            , style "font-size" "13px"
            ]
            [ Html.span
                [ id "build-agent-cost"
                , style "margin-left" "auto"
                , style "font-family" "monospace"
                , style "color" "#b0b0b0"
                ]
                [ Html.text
                    ("agent spend $"
                        ++ formatUsd totalCost
                        ++ " · "
                        ++ String.fromInt runCount
                        ++ (if runCount == 1 then
                                " run"

                            else
                                " runs"
                           )
                    )
                ]
            ]

    else
        Html.text ""


{-| Same rendering as the other agent surfaces: cents-precision dollars.
-}
formatUsd : Float -> String
formatUsd amount =
    let
        cents =
            round (amount * 100)

        absCents =
            abs cents

        dollars =
            absCents // 100

        remainder =
            modBy 100 absCents

        fraction =
            if remainder < 10 then
                "0" ++ String.fromInt remainder

            else
                String.fromInt remainder

        sign =
            if cents < 0 then
                "-"

            else
                ""
    in
    sign ++ String.fromInt dollars ++ "." ++ fraction


body :
    Session
    ->
        { a
            | prep : Maybe Concourse.BuildPrep
            , job : Maybe Concourse.JobIdentifier
            , status : BuildStatus
            , duration : Concourse.BuildDuration
            , reapTime : Maybe Time.Posix
            , id : Int
            , name : String
            , output : CurrentOutput
            , authorized : Bool
            , showHelp : Bool
            , agentReviews : List Concourse.AgentReview.BuildReview
            , agentReviewLoadError : Bool
            , agentReviewPanelExpanded : Bool
            , expandedFindings : Set String
            , showObservations : Maybe Bool
            , agentReviewNotes : Dict String String
            , verdictErrors : Set String
            , expandedDescriptions : Set String
        }
    -> Html Message
body session ({ prep, output, authorized, showHelp } as params) =
    Html.div
        ([ class "scrollable-body build-body"
         , id bodyId
         , tabindex 0
         , onScroll Scrolled
         ]
            ++ Styles.body
        )
    <|
        if authorized then
            [ viewBuildPrep prep
            , Build.AgentReview.view (reviewerName session) params
            , Html.Lazy.lazy3
                viewBuildOutput
                session.timeZone
                (Build.Output.Output.filterHoverState session.hovered)
                output
            , Shortcuts.keyboardHelp showHelp
            ]
                ++ tombstone session.timeZone params

        else
            [ NotAuthorized.view ]


reviewerName : Session -> String
reviewerName session =
    case session.userState of
        UserState.UserStateLoggedIn user ->
            user.userName

        _ ->
            "anonymous"


tombstone :
    Time.Zone
    ->
        { a
            | job : Maybe Concourse.JobIdentifier
            , status : BuildStatus
            , duration : Concourse.BuildDuration
            , reapTime : Maybe Time.Posix
            , id : Int
            , name : String
        }
    -> List (Html Message)
tombstone timeZone model =
    let
        maybeBirthDate =
            Maybe.Extra.or model.duration.startedAt model.duration.finishedAt
    in
    case ( maybeBirthDate, model.reapTime ) of
        ( Just birthDate, Just reapTime ) ->
            [ Html.div
                [ class "tombstone" ]
                [ Html.div [ class "heading" ] [ Html.text "RIP" ]
                , Html.div
                    [ class "job-name" ]
                    [ model.job
                        |> Maybe.map .jobName
                        |> Maybe.withDefault "one-off build"
                        |> Html.text
                    ]
                , Html.div
                    [ class "build-name" ]
                    [ Html.text <| "build #" ++ model.name ]
                , Html.div
                    [ class "date" ]
                    [ Html.text <|
                        mmDDYY timeZone birthDate
                            ++ "-"
                            ++ mmDDYY timeZone reapTime
                    ]
                , Html.div
                    [ class "epitaph" ]
                    [ Html.text <|
                        case model.status of
                            BuildStatusSucceeded ->
                                "It passed, and now it has passed on."

                            BuildStatusFailed ->
                                "It failed, and now has been forgotten."

                            BuildStatusErrored ->
                                "It errored, but has found forgiveness."

                            BuildStatusAborted ->
                                "It was never given a chance."

                            _ ->
                                "I'm not dead yet."
                    ]
                ]
            , Html.div
                [ class "explanation" ]
                [ Html.text "This log has been "
                , Html.a
                    [ Html.Attributes.href "https://concourse-ci.org/concourse-web.html#build-log-retention" ]
                    [ Html.text "reaped." ]
                ]
            ]

        _ ->
            []


mmDDYY : Time.Zone -> Time.Posix -> String
mmDDYY =
    DateFormat.format
        [ DateFormat.monthFixed
        , DateFormat.text "/"
        , DateFormat.dayOfMonthFixed
        , DateFormat.text "/"
        , DateFormat.yearNumberLastTwo
        ]


viewBuildOutput : Time.Zone -> HoverState.HoverState -> CurrentOutput -> Html Message
viewBuildOutput timeZone hovered output =
    case output of
        Output o ->
            Build.Output.Output.view
                { timeZone = timeZone, hovered = hovered }
                o

        Cancelled ->
            Html.div
                Styles.errorLog
                [ Html.text "build cancelled" ]

        Empty ->
            Html.div [] []


viewBuildPrep : Maybe Concourse.BuildPrep -> Html Message
viewBuildPrep buildPrep =
    case buildPrep of
        Just prep ->
            Html.div [ class "build-step" ]
                [ Html.div
                    [ class "header"
                    , style "display" "flex"
                    , style "align-items" "center"
                    ]
                    [ Icon.icon
                        { sizePx = 14, image = Assets.CogsIcon }
                        [ style "margin" "7px"
                        , style "margin-right" "2px"
                        , style "background-size" "contain"
                        ]
                    , Html.h3 [] [ Html.text "preparing build" ]
                    ]
                , Html.div []
                    [ Html.ul
                        [ class "prep-status-list"
                        , style "font-size" "14px"
                        ]
                        ([ viewBuildPrepLi "checking pipeline is not paused" prep.pausedPipeline Dict.empty
                         , viewBuildPrepLi "checking job is not paused" prep.pausedJob Dict.empty
                         ]
                            ++ viewBuildPrepInputs prep.inputs
                            ++ [ viewBuildPrepLi "waiting for a suitable set of input versions" prep.inputsSatisfied prep.missingInputReasons
                               , viewBuildPrepLi "checking max-in-flight is not reached" prep.maxRunningBuilds Dict.empty
                               ]
                        )
                    ]
                ]

        Nothing ->
            Html.div [] []


viewBuildPrepInputs : Dict String Concourse.BuildPrepStatus -> List (Html Message)
viewBuildPrepInputs inputs =
    List.map viewBuildPrepInput (Dict.toList inputs)


viewBuildPrepInput : ( String, Concourse.BuildPrepStatus ) -> Html Message
viewBuildPrepInput ( name, status ) =
    viewBuildPrepLi ("discovering any new versions of " ++ name) status Dict.empty


viewBuildPrepDetails : Dict String String -> Html Message
viewBuildPrepDetails details =
    Html.ul [ class "details" ]
        (List.map viewDetailItem (Dict.toList details))


viewDetailItem : ( String, String ) -> Html Message
viewDetailItem ( name, status ) =
    Html.li []
        [ Html.text (name ++ " - " ++ status) ]


viewBuildPrepLi :
    String
    -> Concourse.BuildPrepStatus
    -> Dict String String
    -> Html Message
viewBuildPrepLi text status details =
    Html.li
        [ classList
            [ ( "prep-status", True )
            , ( "inactive", status == Concourse.BuildPrepStatusUnknown )
            ]
        ]
        [ Html.div
            [ style "align-items" "center"
            , style "display" "flex"
            ]
            [ viewBuildPrepStatus status
            , Html.span []
                [ Html.text text ]
            ]
        , viewBuildPrepDetails details
        ]


viewBuildPrepStatus : Concourse.BuildPrepStatus -> Html Message
viewBuildPrepStatus status =
    case status of
        Concourse.BuildPrepStatusUnknown ->
            Html.div
                [ title "thinking..." ]
                [ Spinner.spinner
                    { sizePx = 12
                    , margin = "0 8px 0 0"
                    }
                ]

        Concourse.BuildPrepStatusBlocking ->
            Html.div
                [ title "blocking" ]
                [ Spinner.spinner
                    { sizePx = 12
                    , margin = "0 8px 0 0"
                    }
                ]

        Concourse.BuildPrepStatusNotBlocking ->
            Icon.icon
                { sizePx = 12
                , image = Assets.NotBlockingCheckIcon
                }
                [ style "margin-right" "8px"
                , style "background-size" "contain"
                , title "not blocking"
                ]
