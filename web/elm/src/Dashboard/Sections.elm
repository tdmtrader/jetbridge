module Dashboard.Sections exposing
    ( TeamSection
    , collapseKey
    , namePrefix
    , sectionsWithinTeam
    , toggle
    )

{-| A team's dashboard cards split into sections.

A pipeline whose config sets `display.section` is in that section, alone if
need be. Any other card belongs to the section named by the prefix of its
pipeline name, up to the first `-` or `_` (`landing-compose` and
`landing-land` are in section `landing`); a pipeline whose whole name is
that prefix belongs to it too. A prefix forms a section only when at least
two cards would be in it, counting the cards that set it explicitly; every
other card stays with the team, in the team's own, unnamed section. An
instance group is in the first section any of its instances sets.

Splitting a team by prefix only happens when it separates something: a team
whose cards would all land in one prefix section, with nothing left over
and nothing set explicitly, is shown unsectioned.

The sections keep the team's pipeline ordering (`fly order-pipelines`): the
team's own cards come first, then each named section in the position of its
first card, and cards keep their order inside a section.

-}

import Concourse
import Dashboard.Group.Models exposing (Card(..), cardName)
import Dict
import List.Extra
import Set exposing (Set)


type alias TeamSection =
    { name : Maybe String
    , cards : List Card
    }


sectionsWithinTeam : List Card -> List TeamSection
sectionsWithinTeam cards =
    let
        prefixOf card =
            namePrefix (cardName card)
                |> Maybe.withDefault (cardName card)

        counts =
            List.foldl
                (\card ->
                    Dict.update
                        (explicitSection card |> Maybe.withDefault (prefixOf card))
                        (Maybe.withDefault 0 >> (+) 1 >> Just)
                )
                Dict.empty
                cards

        sectionOf card =
            case explicitSection card of
                Just name ->
                    Just name

                Nothing ->
                    if (Dict.get (prefixOf card) counts |> Maybe.withDefault 0) >= 2 then
                        Just (prefixOf card)

                    else
                        Nothing

        anyExplicit =
            List.any (explicitSection >> (/=) Nothing) cards

        ( loose, named ) =
            List.partition (sectionOf >> (==) Nothing) cards

        namedSections =
            named
                |> List.Extra.gatherEqualsBy sectionOf
                |> List.map
                    (\( first, rest ) ->
                        { name = sectionOf first, cards = first :: rest }
                    )
    in
    case ( loose, namedSections ) of
        ( [], [ _ ] ) ->
            if anyExplicit then
                namedSections

            else
                [ { name = Nothing, cards = cards } ]

        ( _, [] ) ->
            [ { name = Nothing, cards = cards } ]

        _ ->
            if List.isEmpty loose then
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


{-| The part of a pipeline name before its first `-` or `_`, when there is a
non-empty part on both sides of it.
-}
namePrefix : String -> Maybe String
namePrefix name =
    let
        separatorAt =
            [ "-", "_" ]
                |> List.filterMap (\sep -> String.indexes sep name |> List.head)
                |> List.minimum
    in
    case separatorAt of
        Just idx ->
            if idx > 0 && idx < String.length name - 1 then
                Just (String.left idx name)

            else
                Nothing

        Nothing ->
            Nothing


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
