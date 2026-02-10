module AgentFeedback.ChatPanel exposing (Model, Msg(..), init, update, view, getConversation)

import Html exposing (..)
import Html.Attributes exposing (..)
import Html.Events exposing (onInput, onClick, onSubmit)
import AgentFeedback.Types exposing (ConversationMessage)


-- MODEL


type alias Model =
    { messages : List ConversationMessage
    , inputText : String
    }


init : List ConversationMessage -> Model
init initialMessages =
    { messages = initialMessages
    , inputText = ""
    }


getConversation : Model -> List ConversationMessage
getConversation model =
    model.messages


-- UPDATE


type Msg
    = UpdateInput String
    | SendMessage
    | AddSystemMessage String


update : Msg -> Model -> Model
update msg model =
    case msg of
        UpdateInput text ->
            { model | inputText = text }

        SendMessage ->
            if String.trim model.inputText == "" then
                model
            else
                let
                    newMessage =
                        { role = "human"
                        , content = model.inputText
                        }
                in
                { model
                    | messages = model.messages ++ [ newMessage ]
                    , inputText = ""
                }

        AddSystemMessage content ->
            let
                sysMessage =
                    { role = "system"
                    , content = content
                    }
            in
            { model | messages = model.messages ++ [ sysMessage ] }


-- VIEW


view : Model -> Html Msg
view model =
    div [ class "chat-panel" ]
        [ div [ class "chat-messages" ]
            (List.map viewMessage model.messages)
        , Html.form [ class "chat-input-form", onSubmit SendMessage ]
            [ input
                [ type_ "text"
                , class "chat-input"
                , placeholder "Type your response..."
                , value model.inputText
                , onInput UpdateInput
                ]
                []
            , button
                [ type_ "submit"
                , class "chat-send-btn"
                ]
                [ text "Send" ]
            ]
        ]


viewMessage : ConversationMessage -> Html Msg
viewMessage msg =
    div [ class ("chat-message chat-message-" ++ msg.role) ]
        [ span [ class "chat-role" ] [ text (roleLabel msg.role) ]
        , span [ class "chat-content" ] [ text msg.content ]
        ]


roleLabel : String -> String
roleLabel role =
    case role of
        "human" -> "You"
        "system" -> "Agent"
        _ -> role
