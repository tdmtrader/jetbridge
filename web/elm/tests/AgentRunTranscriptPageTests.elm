module AgentRunTranscriptPageTests exposing (all)

import AgentTickets.AgentRunTranscript as Page
import Application.Models exposing (Session)
import Data
import Expect
import HoverState
import Message.Callback as Callback
import RemoteData
import Routes
import ScreenSize
import Set
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (text)
import Time
import UserState


sampleNdjson : String
sampleNdjson =
    String.join "\n"
        [ """{"ts":"2026-07-19T00:00:01Z","event":"step.start","data":{"step_name":"implement","build_id":4567,"plan_id":"p1"}}"""
        , """{"ts":"2026-07-19T00:00:05Z","event":"gate.result","data":{"gate":"build","component":"web","scope":"affected","status":"ok","duration_seconds":12.0,"summary":"built"}}"""
        , """{"ts":"2026-07-19T00:00:08Z","event":"judge.score","data":{"total":8.0,"max_total":10.0,"model":"claude","dimensions":[]}}"""
        , """{"ts":"2026-07-19T00:00:09Z","event":"step.end","data":{"step_name":"implement","status":"ok","summary":"done","wall_time_seconds":9,"cost_usd":0.05,"turns":3}}"""
        ]


loaded : Page.Model
loaded =
    let
        ( m0, _ ) =
            Page.init { id = 12, buildId = 4567 }

        ( m1, _ ) =
            Page.handleCallback (Callback.AgentRunEventsFetched 12 4567 (Ok sampleNdjson)) ( m0, [] )
    in
    m1


all : Test
all =
    describe "AgentRunTranscript page"
        [ test "renders a timeline entry for the gate result" <|
            \_ ->
                Page.view sampleSession loaded
                    |> Query.fromHtml
                    |> Query.has [ text "build" ]
        , test "renders a judge entry" <|
            \_ ->
                Page.view sampleSession loaded
                    |> Query.fromHtml
                    |> Query.has [ text "judge" ]
        ]


sampleSession : Session
sampleSession =
    { expandedTeamsInAllPipelines = Set.empty
    , collapsedTeamsInFavorites = Set.empty
    , pipelines = RemoteData.NotAsked
    , hovered = HoverState.NoHover
    , sideBarState =
        { isOpen = False
        , width = 275
        }
    , draggingSideBar = False
    , screenSize = ScreenSize.Desktop
    , userState = UserState.UserStateLoggedOut
    , clusterName = ""
    , version = ""
    , jetbridgeVersion = ""
    , concourseVersion = ""
    , featureFlags = Data.featureFlags
    , turbulenceImgSrc = ""
    , notFoundImgSrc = ""
    , csrfToken = ""
    , authToken = ""
    , pipelineRunningKeyframes = ""
    , timeZone = Time.utc
    , favoritedPipelines = Set.empty
    , favoritedInstanceGroups = Set.empty
    , route = Routes.AgentRunTranscript { id = 12, buildId = 4567 }
    }
