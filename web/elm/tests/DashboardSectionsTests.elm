module DashboardSectionsTests exposing (all)

import Application.Application as Application
import Common
import Concourse
import Dashboard.Group.Models exposing (Card(..), cardName)
import Dashboard.Sections as Sections
import Data
import Expect
import Html.Attributes as Attr
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message as Msgs exposing (DomID(..))
import Message.Subscription exposing (Delivery(..))
import Message.TopLevelMessage as ApplicationMsgs
import Set
import Test exposing (Test, describe, test)
import Test.Html.Event as Event
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, id, text)
import Url


all : Test
all =
    describe "dashboard sections"
        [ describe "sectionsWithinTeam"
            [ test "gathers cards that share a name prefix into a section" <|
                \_ ->
                    [ "jetbridge", "landing-compose", "loupe", "landing-land" ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Nothing, [ "jetbridge", "loupe" ] )
                            , ( Just "landing", [ "landing-compose", "landing-land" ] )
                            ]
            , test "leaves a prefix only one card has with the team" <|
                \_ ->
                    [ "k8s-e2e", "landing-compose", "landing-land" ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Nothing, [ "k8s-e2e" ] )
                            , ( Just "landing", [ "landing-compose", "landing-land" ] )
                            ]
            , test "puts a pipeline named exactly the prefix in its section" <|
                \_ ->
                    [ "piano", "jetbridge", "jetbridge-release" ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Nothing, [ "piano" ] )
                            , ( Just "jetbridge", [ "jetbridge", "jetbridge-release" ] )
                            ]
            , test "does not split a team whose cards all share one prefix" <|
                \_ ->
                    [ "pipeline-1", "pipeline-2", "pipeline-3" ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Nothing, [ "pipeline-1", "pipeline-2", "pipeline-3" ] ) ]
            , test "does not split a team with no shared prefix" <|
                \_ ->
                    [ "jetbridge", "loupe", "k8s-e2e" ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Nothing, [ "jetbridge", "loupe", "k8s-e2e" ] ) ]
            , test "shows only named sections when every card is in one" <|
                \_ ->
                    [ "live-hangar-a", "landing-land", "live-hangar-b", "landing-compose" ]
                        |> sectionNames
                        |> Expect.equal
                            [ ( Just "live", [ "live-hangar-a", "live-hangar-b" ] )
                            , ( Just "landing", [ "landing-land", "landing-compose" ] )
                            ]
            , test "places each section where its first card is in the team's ordering" <|
                \_ ->
                    [ "b-1", "solo", "a-1", "b-2", "a-2" ]
                        |> sectionNames
                        |> List.map Tuple.first
                        |> Expect.equal [ Nothing, Just "b", Just "a" ]
            , test "counts an instance group as one card" <|
                \_ ->
                    [ InstanceGroupCard (pipelineNamed 1 "live-hangar") [ pipelineNamed 2 "live-hangar" ]
                    , PipelineCard (pipelineNamed 3 "jetbridge")
                    ]
                        |> Sections.sectionsWithinTeam
                        |> List.map .name
                        |> Expect.equal [ Nothing ]
            ]
        , describe "namePrefix"
            [ test "stops at the first dash or underscore" <|
                \_ ->
                    [ "live-hangar-a", "ci_build-x", "landing" ]
                        |> List.map Sections.namePrefix
                        |> Expect.equal [ Just "live", Just "ci", Nothing ]
            , test "needs a name on both sides of the separator" <|
                \_ ->
                    [ "-x", "x-", "_" ]
                        |> List.map Sections.namePrefix
                        |> Expect.equal [ Nothing, Nothing, Nothing ]
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
        ]


sectionNames : List String -> List ( Maybe String, List String )
sectionNames names =
    names
        |> List.indexedMap (\i n -> PipelineCard (pipelineNamed i n))
        |> Sections.sectionsWithinTeam
        |> List.map (\s -> ( s.name, List.map cardName s.cards ))


pipelineNamed : Int -> String -> Dashboard.Group.Models.Pipeline
pipelineNamed id name =
    Data.dashboardPipeline "team" id |> Data.withName name


pipelines : List Concourse.Pipeline
pipelines =
    [ Data.pipeline "team" 1 |> Data.withName "jetbridge"
    , Data.pipeline "team" 2 |> Data.withName "landing-compose"
    , Data.pipeline "team" 3 |> Data.withName "landing-land"
    ]


dashboardAt : String -> Application.Model
dashboardAt =
    dashboardWith Common.initCustomOpts


dashboardWith : { query : Maybe String, fragment : Maybe String } -> String -> Application.Model
dashboardWith opts path =
    Common.initCustom opts path
        |> Application.handleCallback (Callback.AllPipelinesFetched (Ok pipelines))
        |> Tuple.first
        |> Common.withAllPipelinesVisible


dashboard : Application.Model
dashboard =
    dashboardAt "/"


pipelineCard : String -> Test.Html.Selector.Selector
pipelineCard name =
    containing [ class "pipeline-card", attribute <| Attr.attribute "data-pipeline-name" name ]
