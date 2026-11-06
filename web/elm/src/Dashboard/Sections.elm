module Dashboard.Sections exposing
    ( TeamSection
    , collapseKey
    , namePrefix
    , sectionsWithinTeam
    , toggle
    )

{-| A team's dashboard cards split into sections.

A card belongs to the section named by the prefix of its pipeline name, up
to the first `-` or `_` (`landing-compose` and `landing-land` are in
section `landing`). A pipeline whose whole name is that prefix belongs to it
too. A section forms only when at least two cards share the prefix; every
other card stays with the team, in the team's own, unnamed section.

Splitting a team only happens when it separates something: a team whose
cards would all land in one section, with nothing left over, is shown
unsectioned.

The sections keep the team's pipeline ordering (`fly order-pipelines`): the
team's own cards come first, then each named section in the position of its
first card, and cards keep their order inside a section.

-}

import Concourse
import Dashboard.Group.Models exposing (Card, cardName)
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
        keyOf card =
            namePrefix (cardName card)
                |> Maybe.withDefault (cardName card)

        counts =
            List.foldl
                (\card -> Dict.update (keyOf card) (Maybe.withDefault 0 >> (+) 1 >> Just))
                Dict.empty
                cards

        sectionOf card =
            let
                key =
                    keyOf card
            in
            if (Dict.get key counts |> Maybe.withDefault 0) >= 2 then
                Just key

            else
                Nothing

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
            [ { name = Nothing, cards = cards } ]

        ( _, [] ) ->
            [ { name = Nothing, cards = cards } ]

        _ ->
            if List.isEmpty loose then
                namedSections

            else
                { name = Nothing, cards = loose } :: namedSections


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
