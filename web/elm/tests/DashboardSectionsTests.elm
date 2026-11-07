module DashboardSectionsTests exposing (all)

import Application.Application as Application
import Colors
import Common
import Concourse
import Concourse.BuildStatus exposing (BuildStatus(..))
import Concourse.PipelineStatus as PipelineStatus exposing (PipelineStatus(..))
import Dashboard.Group.Models exposing (Card(..), cardName)
import Dashboard.Sections as Sections
import Data
import Expect
import Html.Attributes as Attr
import Json.Decode
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message as Msgs exposing (DomID(..))
import Message.Subscription exposing (Delivery(..))
import Message.TopLevelMessage as ApplicationMsgs
import Set
import Test exposing (Test, describe, test)
import Test.Html.Event as Event
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, id, style, text)
import Time
import Url


all : Test
all =
    describe "dashboard sections"
        [ describe "sectionsWithinTeam"
            [ test "does not group pipelines by their name prefix" <|
                \_ ->
                    [ ( "jetbridge", Nothing ), ( "landing-compose", Nothing ), ( "loupe", Nothing ), ( "landing-land", Nothing ) ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Nothing, [ "jetbridge", "landing-compose", "loupe", "landing-land" ] ) ]
            , test "puts a pipeline that sets display.section in that section, alone if need be" <|
                \_ ->
                    [ ( "jetbridge", Just "core" ), ( "loupe", Nothing ), ( "piano", Nothing ) ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Nothing, [ "loupe", "piano" ] )
                            , ( Just "core", [ "jetbridge" ] )
                            ]
            , test "a pipeline without display.section stays with the team, whatever its name" <|
                \_ ->
                    [ ( "landing-compose", Just "landing" ), ( "landing-land", Nothing ), ( "loupe", Nothing ) ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Nothing, [ "landing-land", "loupe" ] )
                            , ( Just "landing", [ "landing-compose" ] )
                            ]
            , test "places each section where its first card is in the team's ordering" <|
                \_ ->
                    [ ( "b-1", Just "b" ), ( "solo", Nothing ), ( "a-1", Just "a" ), ( "b-2", Just "b" ), ( "a-2", Just "a" ) ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Nothing, [ "solo" ] )
                            , ( Just "b", [ "b-1", "b-2" ] )
                            , ( Just "a", [ "a-1", "a-2" ] )
                            ]
            , test "shows a section every card sets explicitly" <|
                \_ ->
                    [ ( "jetbridge", Just "core" ), ( "loupe", Just "core" ) ]
                        |> sectionNames
                        |> Expect.equal [ ( Just "core", [ "jetbridge", "loupe" ] ) ]
            , test "an instance group is in the section one of its instances sets" <|
                \_ ->
                    [ InstanceGroupCard (pipelineNamed 1 "hangar") [ pipelineNamed 2 "hangar" |> Data.withDashboardSection "live" ]
                    , PipelineCard (pipelineNamed 3 "jetbridge")
                    ]
                        |> Sections.sectionsWithinTeam
                        |> List.map .name
                        |> Expect.equal [ Nothing, Just "live" ]
            , test "an instance group takes the first section any of its instances sets" <|
                \_ ->
                    [ InstanceGroupCard (pipelineNamed 1 "hangar")
                        [ pipelineNamed 2 "hangar" |> Data.withDashboardSection "live"
                        , pipelineNamed 3 "hangar" |> Data.withDashboardSection "other"
                        ]
                    ]
                        |> Sections.sectionsWithinTeam
                        |> List.map .name
                        |> Expect.equal [ Just "live" ]
            , test "a team with no pipelines is one unnamed section" <|
                \_ ->
                    []
                        |> Sections.sectionsWithinTeam
                        |> List.map .name
                        |> Expect.equal [ Nothing ]
            ]
        , describe "statusSummary"
            [ test "counts failed, errored and aborted pipelines as failing" <|
                \_ ->
                    [ PipelineStatusSucceeded since
                    , PipelineStatusAborted since
                    , PipelineStatusErrored since
                    , PipelineStatusFailed since
                    ]
                        |> Sections.statusSummary
                        |> Expect.equal
                            { pipelineCount = 4
                            , failing = 3
                            , mostImportant = Just (PipelineStatusFailed since)
                            }
            , test "a failing pipeline that is running again still counts as failing" <|
                \_ ->
                    [ PipelineStatusErrored PipelineStatus.Running ]
                        |> Sections.statusSummary
                        |> .failing
                        |> Expect.equal 1
            , test "paused, pending, running and succeeded pipelines are not failing" <|
                \_ ->
                    [ PipelineStatusPaused
                    , PipelineStatusPending False
                    , PipelineStatusPending True
                    , PipelineStatusSucceeded PipelineStatus.Running
                    ]
                        |> Sections.statusSummary
                        |> Expect.equal
                            { pipelineCount = 4
                            , failing = 0
                            , mostImportant = Just (PipelineStatusSucceeded PipelineStatus.Running)
                            }
            , test "ranks errored above aborted" <|
                \_ ->
                    [ PipelineStatusAborted since, PipelineStatusErrored since ]
                        |> Sections.statusSummary
                        |> .mostImportant
                        |> Expect.equal (Just (PipelineStatusErrored since))
            , test "ranks pending above paused" <|
                \_ ->
                    [ PipelineStatusPaused, PipelineStatusPending False ]
                        |> Sections.statusSummary
                        |> .mostImportant
                        |> Expect.equal (Just (PipelineStatusPending False))
            , test "an empty section has no status" <|
                \_ ->
                    []
                        |> Sections.statusSummary
                        |> .mostImportant
                        |> Expect.equal Nothing
            ]
        , describe "display.section"
            [ test "is decoded from a pipeline's display config" <|
                \_ ->
                    """{"id":1,"name":"jetbridge","paused":false,"archived":false,"public":true,"team_name":"main","last_updated":0,"display":{"section":"core"}}"""
                        |> Json.Decode.decodeString Concourse.decodePipeline
                        |> Result.map .dashboardSection
                        |> Expect.equal (Ok (Just "core"))
            , test "survives the pipeline cache" <|
                \_ ->
                    Data.pipeline "main" 1
                        |> Data.withDashboardSection "core"
                        |> Concourse.encodePipeline
                        |> Json.Decode.decodeValue Concourse.decodePipeline
                        |> Result.map .dashboardSection
                        |> Expect.equal (Ok (Just "core"))
            , test "places a card on the dashboard" <|
                \_ ->
                    Common.init "/"
                        |> Application.handleCallback
                            (Callback.AllPipelinesFetched
                                (Ok
                                    [ Data.pipeline "team" 1 |> Data.withName "jetbridge" |> Data.withDashboardSection "core"
                                    , Data.pipeline "team" 2 |> Data.withName "loupe"
                                    ]
                                )
                            )
                        |> Tuple.first
                        |> Common.withAllPipelinesVisible
                        |> Common.queryView
                        |> Query.findAll [ class "dashboard-team-group" ]
                        |> Expect.all
                            [ Query.count (Expect.equal 2)
                            , Query.index 0 >> Query.has [ pipelineCard "loupe" ]
                            , Query.index 1 >> Query.find [ class "dashboard-team-name" ] >> Query.has [ text "team / core" ]
                            , Query.index 1 >> Query.has [ pipelineCard "jetbridge" ]
                            ]
            ]
        , describe "a team that sets no display.section"
            [ test "renders as one team group, whatever its pipeline names" <|
                \_ ->
                    unsectionedDashboardAt "/"
                        |> Common.queryView
                        |> Query.findAll [ class "dashboard-team-group" ]
                        |> Expect.all
                            [ Query.count (Expect.equal 1)
                            , Query.index 0 >> Query.find [ class "dashboard-team-name" ] >> Query.has [ text "team" ]
                            , Query.index 0 >> Query.has [ pipelineCard "landing-compose" ]
                            , Query.index 0 >> Query.has [ pipelineCard "landing-land" ]
                            ]
            , test "offers no collapse control" <|
                \_ ->
                    unsectionedDashboardAt "/"
                        |> Common.queryView
                        |> Expect.all
                            [ Query.hasNot [ class "dashboard-section-toggle" ]
                            , Query.hasNot [ class "dashboard-section-title" ]
                            , Query.hasNot [ id "dashboard-section-header-team" ]
                            ]
            , test "ignores a stored collapse of its own section" <|
                \_ ->
                    unsectionedDashboardAt "/"
                        |> Application.handleDelivery
                            (CollapsedDashboardSectionsReceived (Ok (Set.singleton "team")))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.has [ pipelineCard "jetbridge" ]
            , test "offers no collapse control in the high-density view" <|
                \_ ->
                    unsectionedDashboardAt "/hd"
                        |> Common.queryView
                        |> Expect.all
                            [ Query.hasNot [ class "dashboard-section-toggle" ]
                            , Query.hasNot [ class "dashboard-section-title" ]
                            , Query.has [ class "card", containing [ text "landing-land" ] ]
                            ]
            ]
        , describe "on the dashboard"
            [ test "loads the collapsed sections on page load" <|
                \_ ->
                    Application.init Data.flags
                        { protocol = Url.Http
                        , host = ""
                        , port_ = Nothing
                        , path = "/"
                        , query = Nothing
                        , fragment = Nothing
                        }
                        |> Tuple.second
                        |> Common.contains Effects.LoadCollapsedDashboardSections
            , test "renders a named section under its team's own section" <|
                \_ ->
                    dashboard
                        |> Common.queryView
                        |> Query.findAll [ class "dashboard-team-group" ]
                        |> Expect.all
                            [ Query.count (Expect.equal 2)
                            , Query.index 0 >> Query.has [ attribute <| Attr.attribute "data-team-name" "team" ]
                            , Query.index 0 >> Query.find [ class "dashboard-team-name" ] >> Query.has [ text "team" ]
                            , Query.index 0 >> Query.has [ pipelineCard "jetbridge" ]
                            , Query.index 0 >> Query.hasNot [ pipelineCard "landing-land" ]
                            , Query.index 1 >> Query.find [ class "dashboard-team-name" ] >> Query.has [ text "team / landing" ]
                            , Query.index 1 >> Query.has [ pipelineCard "landing-compose" ]
                            , Query.index 1 >> Query.has [ pipelineCard "landing-land" ]
                            ]
            , test "clicking a section header asks to collapse it" <|
                \_ ->
                    dashboard
                        |> Common.queryView
                        |> Query.find [ id "dashboard-section-header-team/landing" ]
                        |> Event.simulate Event.click
                        |> Event.expect (ApplicationMsgs.Update <| Msgs.Click <| DashboardSectionHeader "team/landing")
            , test "collapsing a section hides its cards and stores it" <|
                \_ ->
                    dashboard
                        |> Application.update (ApplicationMsgs.Update <| Msgs.Click <| DashboardSectionHeader "team/landing")
                        |> Expect.all
                            [ Tuple.second
                                >> Common.contains
                                    (Effects.SaveCollapsedDashboardSections (Set.singleton "team/landing"))
                            , Tuple.first
                                >> Common.queryView
                                >> Query.findAll [ class "dashboard-team-group" ]
                                >> Query.index 1
                                >> Expect.all
                                    [ Query.hasNot [ class "pipeline-card" ]
                                    , Query.has [ class "dashboard-section-count", containing [ text "2 pipelines" ] ]
                                    , Query.find [ id "dashboard-section-header-team/landing" ]
                                        >> Query.has [ attribute <| Attr.attribute "aria-expanded" "false" ]
                                    ]
                            , Tuple.first
                                >> Common.queryView
                                >> Query.findAll [ class "dashboard-team-group" ]
                                >> Query.index 0
                                >> Query.has [ pipelineCard "jetbridge" ]
                            ]
            , test "clicking a collapsed section header expands it" <|
                \_ ->
                    dashboard
                        |> Application.update (ApplicationMsgs.Update <| Msgs.Click <| DashboardSectionHeader "team/landing")
                        |> Tuple.first
                        |> Application.update (ApplicationMsgs.Update <| Msgs.Click <| DashboardSectionHeader "team/landing")
                        |> Expect.all
                            [ Tuple.second >> Common.contains (Effects.SaveCollapsedDashboardSections Set.empty)
                            , Tuple.first
                                >> Common.queryView
                                >> Query.has [ pipelineCard "landing-land" ]
                            ]
            , test "a team's own section collapses too" <|
                \_ ->
                    dashboard
                        |> Application.handleDelivery
                            (CollapsedDashboardSectionsReceived (Ok (Set.singleton "team")))
                        |> Tuple.first
                        |> Common.queryView
                        |> Expect.all
                            [ Query.hasNot [ pipelineCard "jetbridge" ]
                            , Query.has [ pipelineCard "landing-land" ]
                            ]
            , test "restores collapsed sections from storage" <|
                \_ ->
                    dashboard
                        |> Application.handleDelivery
                            (CollapsedDashboardSectionsReceived (Ok (Set.singleton "team/landing")))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.hasNot [ pipelineCard "landing-land" ]
            , test "a search shows matches in collapsed sections" <|
                \_ ->
                    dashboardWith { query = Just "search=landing", fragment = Nothing } "/"
                        |> Application.handleDelivery
                            (CollapsedDashboardSectionsReceived (Ok (Set.fromList [ "team", "team/landing" ])))
                        |> Tuple.first
                        |> Common.queryView
                        |> Expect.all
                            [ Query.has [ pipelineCard "landing-land" ]
                            , Query.hasNot [ class "dashboard-section-toggle" ]
                            ]
            , test "a dragged card can only be dropped within its own section" <|
                \_ ->
                    dashboard
                        |> Application.update
                            (ApplicationMsgs.Update <|
                                Msgs.DragStart (PipelineCard (pipelineNamed 2 "landing-compose"))
                            )
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.findAll [ class "dashboard-team-group" ]
                        |> Expect.all
                            [ Query.index 0 >> Query.hasNot [ class "drop-area" ]
                            , Query.index 1 >> Query.has [ class "drop-area" ]
                            ]
            , test "collapses in the high-density view" <|
                \_ ->
                    dashboardAt "/hd"
                        |> Application.handleDelivery
                            (CollapsedDashboardSectionsReceived (Ok (Set.singleton "team/landing")))
                        |> Tuple.first
                        |> Common.queryView
                        |> Expect.all
                            [ Query.has [ text "team / landing" ]
                            , Query.has [ class "dashboard-section-count", containing [ text "2 pipelines" ] ]
                            , Query.hasNot [ class "card", containing [ text "landing-land" ] ]
                            , Query.has [ class "card", containing [ text "jetbridge" ] ]
                            ]
            ]
        , describe "a collapsed section's status"
            [ test "says how many of its pipelines are failing" <|
                \_ ->
                    collapsedLanding "/" [ jobWithStatus 2 BuildStatusSucceeded, jobWithStatus 3 BuildStatusFailed ]
                        |> landingHeader
                        |> Query.has [ class "dashboard-section-failing", containing [ text "1 failing" ] ]
            , test "shows the most important status in the dashboard's status colour" <|
                \_ ->
                    collapsedLanding "/" [ jobWithStatus 2 BuildStatusAborted, jobWithStatus 3 BuildStatusFailed ]
                        |> landingHeader
                        |> Query.find [ class "dashboard-section-status" ]
                        |> Expect.all
                            [ Query.has [ style "background-color" Colors.failure ]
                            , Query.has [ attribute <| Attr.attribute "data-status" "failed" ]
                            ]
            , test "counts errored and aborted pipelines as failing" <|
                \_ ->
                    collapsedLanding "/" [ jobWithStatus 2 BuildStatusErrored, jobWithStatus 3 BuildStatusAborted ]
                        |> landingHeader
                        |> Query.has [ class "dashboard-section-failing", containing [ text "2 failing" ] ]
            , test "says nothing is failing when nothing is" <|
                \_ ->
                    collapsedLanding "/" [ jobWithStatus 2 BuildStatusSucceeded, jobWithStatus 3 BuildStatusSucceeded ]
                        |> landingHeader
                        |> Expect.all
                            [ Query.hasNot [ class "dashboard-section-failing" ]
                            , Query.find [ class "dashboard-section-status" ]
                                >> Query.has [ style "background-color" Colors.success ]
                            ]
            , test "does not count a paused pipeline as failing" <|
                \_ ->
                    Common.initCustom Common.initCustomOpts "/"
                        |> Application.handleCallback
                            (Callback.AllPipelinesFetched
                                (Ok
                                    [ Data.pipeline "team" 1 |> Data.withName "jetbridge"
                                    , Data.pipeline "team" 2 |> Data.withName "landing-compose" |> Data.withDashboardSection "landing"
                                    , Data.pipeline "team" 3 |> Data.withName "landing-land" |> Data.withDashboardSection "landing" |> Data.withPaused True
                                    ]
                                )
                            )
                        |> Tuple.first
                        |> Application.handleCallback (Callback.AllJobsFetched (Ok [ jobWithStatus 3 BuildStatusFailed ]))
                        |> Tuple.first
                        |> Common.withAllPipelinesVisible
                        |> Application.handleDelivery
                            (CollapsedDashboardSectionsReceived (Ok (Set.singleton "team/landing")))
                        |> Tuple.first
                        |> landingHeader
                        |> Query.hasNot [ class "dashboard-section-failing" ]
            , test "is not shown while the section is expanded" <|
                \_ ->
                    dashboardAt "/"
                        |> Application.handleCallback (Callback.AllJobsFetched (Ok [ jobWithStatus 3 BuildStatusFailed ]))
                        |> Tuple.first
                        |> landingHeader
                        |> Expect.all
                            [ Query.hasNot [ class "dashboard-section-failing" ]
                            , Query.hasNot [ class "dashboard-section-status" ]
                            ]
            , test "is shown in the high-density view" <|
                \_ ->
                    collapsedLanding "/hd" [ jobWithStatus 2 BuildStatusSucceeded, jobWithStatus 3 BuildStatusErrored ]
                        |> Common.queryView
                        |> Query.find [ id "dashboard-section-header-team/landing" ]
                        |> Expect.all
                            [ Query.has [ class "dashboard-section-failing", containing [ text "1 failing" ] ]
                            , Query.find [ class "dashboard-section-status" ]
                                >> Query.has [ style "background-color" Colors.error ]
                            ]
            ]
        ]


since : PipelineStatus.StatusDetails
since =
    PipelineStatus.Since (Time.millisToPosix 0)


sectionNames : List ( String, Maybe String ) -> List ( Maybe String, List String )
sectionNames named =
    named
        |> List.indexedMap
            (\i ( n, section ) ->
                PipelineCard (pipelineNamed i n |> (\p -> { p | dashboardSection = section }))
            )
        |> Sections.sectionsWithinTeam
        |> List.map (\s -> ( s.name, List.map cardName s.cards ))


pipelineNamed : Int -> String -> Dashboard.Group.Models.Pipeline
pipelineNamed id name =
    Data.dashboardPipeline "team" id |> Data.withName name


pipelines : List Concourse.Pipeline
pipelines =
    [ Data.pipeline "team" 1 |> Data.withName "jetbridge"
    , Data.pipeline "team" 2 |> Data.withName "landing-compose" |> Data.withDashboardSection "landing"
    , Data.pipeline "team" 3 |> Data.withName "landing-land" |> Data.withDashboardSection "landing"
    ]


{-| The same names as `pipelines`, two of them sharing a prefix, with no
`display.section` anywhere.
-}
unsectionedPipelines : List Concourse.Pipeline
unsectionedPipelines =
    List.map (\p -> { p | dashboardSection = Nothing }) pipelines


jobWithStatus : Int -> BuildStatus -> Concourse.Job
jobWithStatus pipelineId status =
    Data.job pipelineId
        |> Data.withFinishedBuild (Just (Data.jobBuild status))
        |> (\j -> { j | transitionBuild = Just (Data.jobBuild status) })


collapsedLanding : String -> List Concourse.Job -> Application.Model
collapsedLanding path jobs =
    dashboardAt path
        |> Application.handleCallback (Callback.AllJobsFetched (Ok jobs))
        |> Tuple.first
        |> Application.handleDelivery
            (CollapsedDashboardSectionsReceived (Ok (Set.singleton "team/landing")))
        |> Tuple.first


landingHeader : Application.Model -> Query.Single ApplicationMsgs.TopLevelMessage
landingHeader =
    Common.queryView
        >> Query.find [ id "dashboard-section-header-team/landing" ]


dashboardAt : String -> Application.Model
dashboardAt =
    dashboardWith Common.initCustomOpts


dashboardWith : { query : Maybe String, fragment : Maybe String } -> String -> Application.Model
dashboardWith opts path =
    dashboardOf pipelines opts path


unsectionedDashboardAt : String -> Application.Model
unsectionedDashboardAt =
    dashboardOf unsectionedPipelines Common.initCustomOpts


dashboardOf : List Concourse.Pipeline -> { query : Maybe String, fragment : Maybe String } -> String -> Application.Model
dashboardOf ps opts path =
    Common.initCustom opts path
        |> Application.handleCallback (Callback.AllPipelinesFetched (Ok ps))
        |> Tuple.first
        |> Common.withAllPipelinesVisible


dashboard : Application.Model
dashboard =
    dashboardAt "/"


pipelineCard : String -> Test.Html.Selector.Selector
pipelineCard name =
    containing [ class "pipeline-card", attribute <| Attr.attribute "data-pipeline-name" name ]
