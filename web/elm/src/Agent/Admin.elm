module Agent.Admin exposing (AdminData, view)

import Agent.Shared as Shared
import Colors
import Concourse.Agent as CredAgent
import Html exposing (Html)
import Html.Attributes exposing (checked, class, disabled, id, placeholder, style, type_, value)
import Html.Events exposing (onClick, onInput)
import Message.Message exposing (Message(..))
import Set exposing (Set)
import Time
import UserState exposing (UserState)


type alias AdminData =
    { credentials : Maybe (List CredAgent.CredentialStatus)
    , credentialsError : Maybe String
    , platformCredentials : Maybe (List CredAgent.CredentialStatus)
    , principals : Maybe (List CredAgent.Principal)
    , principalsError : Maybe String
    , revokeError : Maybe String
    , showEphemeralPrincipals : Bool
    , mintName : String
    , mintDescription : String
    , mintScopes : Set String
    , mintExpiresDays : String
    , mintedToken : Maybe String
    , mintError : Maybe String
    , minting : Bool
    }


{-| The credentials + principals surface is admin-only. The server is the real
boundary (the credential/principal endpoints 403 for non-admins), but a
client-side gate turns an otherwise blank/denied page into an explicit notice.
-}
view : UserState -> Time.Zone -> AdminData -> Html Message
view userState zone data =
    if UserState.isAdmin userState then
        adminBody zone data

    else
        Html.div [ id "agent-admin-denied" ]
            [ Shared.errorLine "not authorized — the agent admin surface (credentials + principals) is admin-only" ]


adminBody : Time.Zone -> AdminData -> Html Message
adminBody zone data =
    Html.div []
        [ credentialsSection zone data
        , principalsSection zone data
        ]



-- CREDENTIALS SECTION (read-only status)


credentialsSection : Time.Zone -> AdminData -> Html Message
credentialsSection zone data =
    -- Two distinct slots, each behind its own labelled sub-header (U17): the
    -- vaulted PLATFORM credential dispatched runs authenticate with, and the
    -- viewer's OWN interactive credential. Keeping them apart stops an empty
    -- personal slot from reading as "the platform auth is missing".
    Shared.sectionBlock "agent-credentials" "Credentials" <|
        platformCredentialsBlock zone data.platformCredentials
            ++ personalCredentialsBlock zone data


{-| A bold, muted sub-header naming one of the two credential slots so the
platform slot and the personal slot can never be mistaken for one another.
-}
credentialSlotLabel : String -> Html Message
credentialSlotLabel labelText =
    Html.div
        [ style "color" Shared.mutedColor
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "font-weight" "700"
        , style "margin" "10px 0 2px 0"
        ]
        [ Html.text labelText ]


{-| The vaulted platform credential dispatched runs actually authenticate
with. Fetched with `?user=platform` (admin-only; a 403 hides the block), so
the section no longer claims "no credentials stored" while dispatch works.
Rendered under its own "Platform credential" header.
-}
platformCredentialsBlock : Time.Zone -> Maybe (List CredAgent.CredentialStatus) -> List (Html Message)
platformCredentialsBlock zone maybeCreds =
    case maybeCreds of
        Just (cred :: rest) ->
            [ credentialSlotLabel "Platform credential (used by dispatched runs)"
            , Html.div
                [ class "agent-platform-credential"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                , style "color" Colors.text
                , style "margin" "0 0 8px 0"
                ]
                (List.map
                    (\c ->
                        Html.div []
                            [ Html.text
                                (c.kind
                                    ++ " (expires "
                                    ++ Shared.formatPosix zone c.expiresAt
                                    ++ ") — active"
                                )
                            ]
                    )
                    (cred :: rest)
                )
            ]

        _ ->
            []


{-| The viewer's own credential, set via `fly agent auth`. Kept under its own
header so an empty personal slot reads as "you have no personal credential",
not "the platform auth is missing" (U17).
-}
personalCredentialsBlock : Time.Zone -> AdminData -> List (Html Message)
personalCredentialsBlock zone data =
    credentialSlotLabel "Your credential (interactive fly login)"
        :: Shared.mutedLine "set or rotate with: fly agent auth"
        :: (case data.credentials of
                Nothing ->
                    case data.credentialsError of
                        Just message ->
                            [ Shared.errorLine message ]

                        Nothing ->
                            [ Shared.mutedLine "loading…" ]

                Just [] ->
                    Shared.staleDataWarning data.credentialsError
                        ++ [ Shared.mutedLine "no personal credential stored — run: fly agent auth" ]

                Just creds ->
                    Shared.staleDataWarning data.credentialsError
                        ++ [ credentialsTable zone creds ]
           )


credentialsTable : Time.Zone -> List CredAgent.CredentialStatus -> Html Message
credentialsTable zone creds =
    Html.table
        [ class "agent-credentials-table"
        , style "border-collapse" "collapse"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (Html.tr []
            [ Shared.tableHeaderCell "left" "kind"
            , Shared.tableHeaderCell "left" "expires"
            , Shared.tableHeaderCell "left" "last verified"
            ]
            :: List.map (credentialRow zone) creds
        )


credentialRow : Time.Zone -> CredAgent.CredentialStatus -> Html Message
credentialRow zone c =
    Html.tr [ class "agent-credential-row" ]
        [ Shared.tableCell "left" c.kind
        , Shared.tableCell "left" (Shared.formatPosix zone c.expiresAt)
        , Shared.tableCell "left" (Shared.formatPosix zone c.lastVerifiedAt)
        ]



-- PRINCIPALS SECTION (admin: mint / list / revoke)


principalsSection : Time.Zone -> AdminData -> Html Message
principalsSection zone data =
    Shared.sectionBlock "agent-principals"
        "Principals"
        (mintFence data
            :: revokeErrorLine data
            :: principalsBody zone data
        )


{-| Fence the privileged mint form inside its own bordered, labelled box so it
is unmistakably a credential-issuing control and does not read as just another
read-only panel sitting under the spend tables (U15).
-}
mintFence : AdminData -> Html Message
mintFence data =
    Html.div
        [ class "agent-mint-fence"
        , style "border" ("1px solid " ++ Shared.amberColor)
        , style "border-radius" "4px"
        , style "padding" "10px 12px"
        , style "margin" "4px 0 12px 0"
        ]
        (Html.div
            [ style "color" Shared.amberColor
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "font-weight" "700"
            , style "margin-bottom" "8px"
            ]
            [ Html.text "Mint a principal — issues a privileged API credential" ]
            :: mintForm data
            :: mintedTokenBox data
        )


{-| Revoke failures render here, in the principals section next to the table —
not inside the mint form, where a revoke error would be far from the row that
triggered it and would only be cleared by a successful mint.
-}
revokeErrorLine : AdminData -> Html Message
revokeErrorLine data =
    case data.revokeError of
        Just message ->
            Html.div [ class "agent-revoke-error" ] [ Shared.errorLine message ]

        Nothing ->
            Html.text ""


mintForm : AdminData -> Html Message
mintForm data =
    Html.div
        [ class "agent-mint-form"
        , style "margin" "0 0 12px 0"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        [ Html.div [ style "margin-bottom" "6px" ]
            [ mintTextField "name" data.mintName AgentMintNameChanged
            , mintTextField "description (optional)" data.mintDescription AgentMintDescriptionChanged
            ]
        , Html.div
            [ class "agent-mint-scopes"
            , style "display" "flex"
            , style "flex-wrap" "wrap"
            , style "gap" "12px"
            , style "margin-bottom" "6px"
            ]
            (List.map (scopeCheckbox data) Shared.mintScopeVocabulary)
        , Html.div
            [ style "display" "flex"
            , style "align-items" "center"
            , style "gap" "10px"
            ]
            [ expiresField data.mintExpiresDays
            , mintButton data
            ]
        , mintErrorLine data
        ]


mintTextField : String -> String -> (String -> Message) -> Html Message
mintTextField ph val toMsg =
    Html.input
        [ type_ "text"
        , placeholder ph
        , value val
        , onInput toMsg
        , style "margin-right" "8px"
        , style "padding" "3px 6px"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "background" Colors.background
        , style "color" Colors.text
        , style "border" ("1px solid " ++ Shared.subtleColor)
        ]
        []


scopeCheckbox : AdminData -> String -> Html Message
scopeCheckbox data scope =
    Html.label
        [ class "agent-mint-scope"
        , style "display" "inline-flex"
        , style "align-items" "center"
        , style "gap" "4px"
        , style "cursor" "pointer"
        ]
        [ Html.input
            [ type_ "checkbox"
            , checked (Set.member scope data.mintScopes)
            , onClick (AgentMintScopeToggled scope)
            ]
            []
        , Html.text scope
        ]


expiresField : String -> Html Message
expiresField val =
    Html.label
        [ style "display" "inline-flex"
        , style "align-items" "center"
        , style "gap" "4px"
        , style "color" Shared.mutedColor
        ]
        ([ Html.text "expires in"
         , Html.input
            [ type_ "text"
            , placeholder "N"
            , value val
            , onInput AgentMintExpiresChanged
            , style "width" "48px"
            , style "padding" "3px 6px"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "background" Colors.background
            , style "color" Colors.text
            , style "border"
                ("1px solid "
                    ++ (if Shared.expiresIsValid val then
                            Shared.subtleColor

                        else
                            Shared.amberColor
                       )
                )
            ]
            []
         , Html.text "days (optional)"
         ]
            ++ expiresHint val
        )


{-| Inline hint shown only when the expires field has non-blank input that does
not parse to a positive integer. Without this the field would silently mean
"never expires", which is a surprising, easy-to-miss failure.
-}
expiresHint : String -> List (Html Message)
expiresHint val =
    if Shared.expiresIsValid val then
        []

    else
        [ Html.span
            [ class "agent-mint-expires-hint"
            , style "color" Shared.amberColor
            , style "font-size" "11px"
            ]
            [ Html.text "must be a positive number of days; leave blank for no expiry" ]
        ]


mintButton : AdminData -> Html Message
mintButton data =
    let
        enabled =
            Shared.canMint
                { name = data.mintName
                , scopes = data.mintScopes
                , expiresDays = data.mintExpiresDays
                }
                && not data.minting

        label =
            if data.minting then
                "minting…"

            else
                "mint"
    in
    Html.button
        [ class "agent-mint-button"
        , onClick AgentMintSubmitted
        , disabled (not enabled)
        , style "padding" "4px 12px"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "font-weight" "700"
        , style "border" "none"
        , style "border-radius" "3px"
        , style "cursor"
            (if enabled then
                "pointer"

             else
                "not-allowed"
            )
        , style "background"
            (if enabled then
                "#2e4f2e"

             else
                Colors.background
            )
        , style "color"
            (if enabled then
                "#9fdf9f"

             else
                Shared.subtleColor
            )
        ]
        [ Html.text label ]


mintErrorLine : AdminData -> Html Message
mintErrorLine data =
    case data.mintError of
        Just message ->
            Shared.errorLine message

        Nothing ->
            Html.text ""


mintedTokenBox : AdminData -> List (Html Message)
mintedTokenBox data =
    case data.mintedToken of
        Nothing ->
            []

        Just token ->
            [ Html.div
                [ class "agent-minted-token"
                , style "margin" "0 0 12px 0"
                , style "padding" "8px 12px"
                , style "border" ("1px solid " ++ Shared.amberColor)
                , style "border-radius" "3px"
                , style "background" "#3a3320"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                , style "color" Colors.text
                ]
                [ Html.div
                    [ style "color" Shared.amberColor
                    , style "font-weight" "700"
                    , style "margin-bottom" "4px"
                    ]
                    [ Html.text "token (shown once — copy it now):" ]
                , Html.div
                    [ class "agent-minted-token-value"
                    , style "word-break" "break-all"
                    ]
                    [ Html.text token ]
                , Html.button
                    [ class "agent-minted-token-dismiss"
                    , onClick AgentMintedTokenDismissed
                    , style "margin-top" "6px"
                    , style "padding" "2px 8px"
                    , style "font-family" "monospace"
                    , style "font-size" "11px"
                    , style "cursor" "pointer"
                    , style "background" Colors.background
                    , style "color" Shared.mutedColor
                    , style "border" ("1px solid " ++ Shared.subtleColor)
                    , style "border-radius" "3px"
                    ]
                    [ Html.text "dismiss" ]
                ]
            ]


principalsBody : Time.Zone -> AdminData -> List (Html Message)
principalsBody zone data =
    case data.principals of
        Nothing ->
            case data.principalsError of
                Just message ->
                    [ Shared.errorLine message ]

                Nothing ->
                    [ Shared.mutedLine "loading…" ]

        Just [] ->
            Shared.staleDataWarning data.principalsError
                ++ [ Shared.mutedLine "no principals yet — mint one above" ]

        Just principals ->
            let
                ( ephemeral, durable ) =
                    List.partition isEphemeralPrincipal principals
            in
            Shared.staleDataWarning data.principalsError
                ++ (if List.isEmpty durable then
                        [ Shared.mutedLine "no durable principals — mint one above" ]

                    else
                        [ principalsTable zone durable ]
                   )
                ++ ephemeralPrincipals zone data ephemeral


{-| Per-run agent tokens are named `run-<id>` and are minted/revoked
automatically for every dispatch, so they flood the principals table. Fold them
behind a collapsed toggle, leaving the durable (human/service) principals in the
main table.
-}
isEphemeralPrincipal : CredAgent.Principal -> Bool
isEphemeralPrincipal p =
    case String.split "-" p.name of
        [ "run", n ] ->
            String.toInt n /= Nothing

        _ ->
            False


ephemeralPrincipals : Time.Zone -> AdminData -> List CredAgent.Principal -> List (Html Message)
ephemeralPrincipals zone data ephemeral =
    if List.isEmpty ephemeral then
        []

    else
        Html.button
            [ class "agent-ephemeral-toggle"
            , onClick AgentPrincipalsShowEphemeralToggled
            , style "background" "transparent"
            , style "border" "none"
            , style "color" Shared.subtleColor
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "cursor" "pointer"
            , style "padding" "8px 0"
            , style "text-align" "left"
            ]
            [ Html.text
                ((if data.showEphemeralPrincipals then
                    "▾ hide "

                  else
                    "▸ show "
                 )
                    ++ String.fromInt (List.length ephemeral)
                    ++ " ephemeral run "
                    ++ (if List.length ephemeral == 1 then
                            "principal"

                        else
                            "principals"
                       )
                )
            ]
            :: (if data.showEphemeralPrincipals then
                    [ principalsTable zone ephemeral ]

                else
                    []
               )


principalsTable : Time.Zone -> List CredAgent.Principal -> Html Message
principalsTable zone principals =
    Html.table
        [ class "agent-principals-table"
        , style "border-collapse" "collapse"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (Html.tr []
            [ Shared.tableHeaderCell "left" "name"
            , Shared.tableHeaderCell "left" "scopes"
            , Shared.tableHeaderCell "left" "team"
            , Shared.tableHeaderCell "left" "created"
            , Shared.tableHeaderCell "left" "expires"
            , Shared.tableHeaderCell "left" "last used"
            , Shared.tableHeaderCell "left" ""
            ]
            :: List.map (principalRow zone) principals
        )


principalRow : Time.Zone -> CredAgent.Principal -> Html Message
principalRow zone p =
    let
        dim =
            case p.revokedAt of
                Just _ ->
                    [ style "opacity" "0.5" ]

                Nothing ->
                    []

        action =
            case p.revokedAt of
                Just revokedAt ->
                    Html.span
                        [ class "agent-principal-revoked"
                        , style "color" Shared.subtleColor
                        ]
                        [ Html.text ("revoked " ++ Shared.formatPosix zone (Just revokedAt)) ]

                Nothing ->
                    Html.button
                        [ class "agent-principal-revoke"
                        , onClick (AgentPrincipalRevokeClicked p.id)
                        , style "padding" "2px 8px"
                        , style "font-family" "monospace"
                        , style "font-size" "11px"
                        , style "cursor" "pointer"
                        , style "background" "#5c2626"
                        , style "color" "#f0a0a0"
                        , style "border" "none"
                        , style "border-radius" "3px"
                        ]
                        [ Html.text "revoke" ]
    in
    Html.tr (class "agent-principal-row" :: dim)
        [ Shared.tableCell "left" p.name
        , Shared.tableCell "left" (String.join ", " p.scopes)
        , Shared.tableCell "left" p.teamName
        , Shared.tableCell "left" (Shared.formatPosix zone (Just p.createdAt))
        , Shared.tableCell "left" (Shared.formatPosix zone p.expiresAt)
        , Shared.tableCell "left" (Shared.formatPosix zone p.lastUsedAt)
        , Html.td
            [ style "padding" "4px 16px 4px 0"
            , style "border-bottom" Shared.rowBorder
            ]
            [ action ]
        ]
