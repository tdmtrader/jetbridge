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
        [ """{"type":"system","subtype":"init","session_id":"s1","tools":["Bash"]}"""
        , """{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"git commit -m wip"}}]}}"""
        , """{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok"}]}}"""
        , """{"type":"result","subtype":"success","result":"\\"done\\"","model":"m9","total_cost_usd":0.75,"num_turns":4,"is_error":false,"usage":{"input_tokens":100,"output_tokens":50}}"""
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
        [ test "renders a tool-call entry naming the tool and its command" <|
            \_ ->
                Page.view sampleSession loaded
                    |> Query.fromHtml
                    |> Query.has [ text "tool Bash · git commit -m wip" ]
        , test "renders the terminal result summary" <|
            \_ ->
                Page.view sampleSession loaded
                    |> Query.fromHtml
                    |> Query.has [ text "result: success (4 turns, $0.75)" ]
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
