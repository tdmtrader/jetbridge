module Dashboard.Sections exposing
    ( StatusSummary
    , TeamSection
    , collapseKey
    , sectionsWithinTeam
    , statusSummary
    , toggle
    )

{-| A team's dashboard cards split into sections.

A pipeline whose config sets `display.section` is in that section, alone if
need be; every other card stays with the team, in the team's own, unnamed
section. Sections come only from `display.section`: a pipeline's name never
places it, so renaming a pipeline does not move it. An instance group is in
the first section any of its instances sets.

A team none of whose pipelines sets `display.section` is one unnamed section,
shown exactly as a team was before there were sections.

The sections keep the team's pipeline ordering (`fly order-pipelines`): the
team's own cards come first, then each named section in the position of its
first card, and cards keep their order inside a section.

-}

import Concourse
import Concourse.PipelineStatus exposing (PipelineStatus(..))
import Dashboard.Group.Models exposing (Card(..))
import List.Extra
import Set exposing (Set)


type alias TeamSection =
    { name : Maybe String
    , cards : List Card
    }


sectionsWithinTeam : List Card -> List TeamSection
sectionsWithinTeam cards =
    let
        ( loose, named ) =
            List.partition (explicitSection >> (==) Nothing) cards

        namedSections =
            named
                |> List.Extra.gatherEqualsBy explicitSection
                |> List.map
                    (\( first, rest ) ->
                        { name = explicitSection first, cards = first :: rest }
                    )
    in
    if List.isEmpty named then
        [ { name = Nothing, cards = cards } ]

    else if List.isEmpty loose then
        namedSections

    else
        { name = Nothing, cards = loose } :: namedSections


explicitSection : Card -> Maybe String
explicitSection card =
    case card of
        PipelineCard p ->
            p.dashboardSection

        InstancedPipelineCard p ->
            p.dashboardSection

        InstanceGroupCard p ps ->
            List.filterMap .dashboardSection (p :: ps) |> List.head


{-| What a collapsed section's header says about the pipelines it hides:
how many there are, how many are failing, and the status that matters most
among them, which colours the header's status indicator.
-}
type alias StatusSummary =
    { pipelineCount : Int
    , failing : Int
    , mostImportant : Maybe PipelineStatus
    }


{-| Summarises the statuses of a section's pipelines, each worked out the way
its card works it out (`Dashboard.Pipeline.pipelineStatus`).
-}
statusSummary : List PipelineStatus -> StatusSummary
statusSummary statuses =
    { pipelineCount = List.length statuses
    , failing = List.Extra.count isFailing statuses
    , mostImportant = List.Extra.minimumBy importance statuses
    }


{-| A pipeline is failing while its most important job last finished failed,
errored or aborted, even if a new build is running. A paused pipeline is not
failing: its status is paused whatever its jobs last did.
-}
isFailing : PipelineStatus -> Bool
isFailing status =
    case status of
        PipelineStatusFailed _ ->
            True

        PipelineStatusErrored _ ->
            True

        PipelineStatusAborted _ ->
            True

        _ ->
            False


{-| Lower is more important. The failing statuses rank as a card's jobs do
(`Concourse.BuildStatus.ordering`); a pipeline that has run ranks above one
that has not, and both above one that is paused or archived.
-}
importance : PipelineStatus -> Int
importance status =
    case status of
        PipelineStatusFailed _ ->
            0

        PipelineStatusErrored _ ->
            1

        PipelineStatusAborted _ ->
            2

        PipelineStatusSucceeded _ ->
            3

        PipelineStatusPending _ ->
            4

        PipelineStatusPaused ->
            5

        PipelineStatusArchived ->
            6


{-| Identifies a section across page loads. Team names cannot contain `/`, so
a team's own section and its named sections never collide.
-}
collapseKey : Concourse.TeamName -> Maybe String -> String
collapseKey teamName name =
    case name of
        Just n ->
            teamName ++ "/" ++ n

        Nothing ->
            teamName


toggle : String -> Set String -> Set String
toggle key set =
    if Set.member key set then
        Set.remove key set

    else
        Set.insert key set
