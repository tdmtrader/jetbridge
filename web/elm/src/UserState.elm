module UserState exposing (UserState(..), isAdmin, isAnonymous, isMember)

import Concourse
import Dict


type UserState
    = UserStateLoggedIn Concourse.User
    | UserStateLoggedOut
    | UserStateUnknown


isAnonymous : UserState -> Bool
isAnonymous userState =
    case userState of
        UserStateLoggedIn _ ->
            False

        _ ->
            True


isAdmin : UserState -> Bool
isAdmin userState =
    case userState of
        UserStateLoggedIn user ->
            user.isAdmin

        _ ->
            False


isMember : { a | teamName : String, userState : UserState } -> Bool
isMember { teamName, userState } =
    case userState of
        UserStateLoggedIn user ->
            if user.isAdmin then
                True

            else
                case Dict.get teamName user.teams of
                    Just roles ->
                        List.member "pipeline-operator" roles
                            || List.member "member" roles
                            || List.member "owner" roles

                    Nothing ->
                        False

        _ ->
            False
