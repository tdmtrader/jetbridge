# S-5 Web Loop Closure Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

**Goal:** Let a human create, queue-with-workflow, and dispatch an agent ticket entirely from the web UI, then watch the resulting attempt live — closing the loop that today only `fly` can drive.

**Architecture:** This is a **pure front-end (Elm) track**. Every server endpoint already exists and is already authorized for the cookie-authenticated main-team web user: `POST /api/v1/agent/tickets` (create), `PUT /api/v1/agent/tickets/:id/state` (queue = `draft`→`queued`), `POST /api/v1/agent/tickets/:id/dispatch` (dispatch), and `GET /api/v1/agent/workflows` (picker). We add a new-ticket form to the ticket-queue page (`AgentTickets.elm`), reuse the existing money-spending two-step dispatch confirm on the ticket-detail page (`AgentTicket.elm`) as-is, and add a lightweight "active attempt" live strip to the detail page. No Go, no migration, no new route.

**Tech Stack:** Elm 0.19.1, `elm-test` (tests in `web/elm/tests/`), the shared `Api`/`Endpoints` request helpers, the `Polling` module (already used by both pages), and `hack/build-web.sh` to regenerate the embedded `web/public/elm.min.js` bundle.

---

## Why no server, no migration, no route

Grounded facts (read during planning):

- `atc/routes.go:319` — `{Path: "/api/v1/agent/tickets", Method: "POST", Name: CreateAgentTicket}` already exists.
- `atc/routes.go:322` — `{Path: "/api/v1/agent/tickets/:ticket_id/state", Method: "PUT", Name: TransitionAgentTicket}` (the queue edge).
- `atc/routes.go:326` — `{Path: "/api/v1/agent/tickets/:ticket_id/dispatch", Method: "POST", Name: DispatchAgentTicket}`.
- `atc/routes.go:332` — `{Path: "/api/v1/agent/workflows", Method: "GET", Name: ListAgentWorkflows}` (the picker source).
- `atc/wrappa/api_auth_wrappa.go:241-246` — `CreateAgentTicket` and `TransitionAgentTicket` route through `AgentPrincipalOrMainTeamHandler`; an authorized main-team member (the web user) is accepted. `DispatchAgentTicket` (`:227`) and `ListAgentWorkflows` (`:216`) route through `CheckAgentAuthorizationHandler` (main-team viewer). All four are reachable from a logged-in browser session with a CSRF token.
- `agent/api/tickets/handler.go:85-155` — `CreateTicket` reads `CreateRequest`, defaults `origin` to `"web"` when omitted, requires `Title` and `Repo`, rejects negative `budget_usd`, and on success writes the **bare created `Ticket` JSON** with `201 Created` (`writeJSON(w, http.StatusCreated, created)`, where `created` is `*Ticket` from `store.Get`). So the Elm client decodes the create response with the existing `Concourse.AgentTicket.decodeTicket`.
- `agent/api/tickets/types.go:251-261` — `CreateRequest{ Title, Body, Origin, Repo, TargetBranch, WorkflowName, WorkflowVersion *int, BudgetUSD *float64, ExternalRef }` with `omitempty` on every optional field.

Because the write path is already exercised by `fly agent tickets create` / `queue` / `dispatch` (`fly/commands/agent_tickets.go`), this track only wires the browser to the same endpoints. **The migration ledger in `docs/superpowers/plans/agentic-platform/remainders/README.md` is untouched by this track.**

---

## File Structure

| File | Create/Modify | One responsibility |
|------|---------------|--------------------|
| `web/elm/src/Concourse/AgentTicket.elm` | Modify | Add `CreateParams` type + `encodeCreate` encoder (the create request body); nothing else changes. |
| `web/elm/src/Message/Message.elm` | Modify | Add the new-ticket-form messages (open/cancel, field-changed, queue toggle, submit). |
| `web/elm/src/Message/Callback.elm` | Modify | Add `AgentTicketCreated (Fetched Concourse.AgentTicket.Ticket)`. |
| `web/elm/src/Message/Effects.elm` | Modify | Add the `CreateAgentTicket CreateParams` effect and its `runEffect` arm (POST + decode). |
| `web/elm/src/AgentTickets/AgentTickets.elm` | Modify | Own the form model, lazy workflow fetch, form view, submit → create → optional queue → navigate. |
| `web/elm/src/AgentTickets/AgentTicket.elm` | Modify | Add the "active attempt" live strip shown while a run is in flight (reuses existing metrics + polling). |
| `web/elm/tests/AgentTicketTests.elm` | Modify | Unit-test `encodeCreate` field mapping/omission. |
| `web/elm/tests/AgentTicketsPageTests.elm` | Modify | Test the form: open button, workflow options, submit emits `CreateAgentTicket`, created→queue+navigate. |
| `web/elm/tests/AgentTicketPageTests.elm` | Modify | Test the detail-page active-attempt strip renders while running. |
| `web/public/elm.min.js` | Modify (generated) | Rebuilt bundle — the served UI; stale otherwise. |

---

## Task 1 — `encodeCreate` + `CreateParams` in `Concourse.AgentTicket`

The create request body. Kept in `Concourse.AgentTicket` (not `Effects`) so it is exposed and unit-testable, mirroring `decodeDispatchResult` living here. Optional fields are omitted when empty/`Nothing` so the server's `omitempty` defaults (e.g. `origin` → `"web"`) apply.

**Files:**
- Modify: `web/elm/src/Concourse/AgentTicket.elm` (module exposing list; add `CreateParams`, `encodeCreate`)
- Test: `web/elm/tests/AgentTicketTests.elm`

**Steps:**

- [ ] Write the failing test. Append to `web/elm/tests/AgentTicketTests.elm` (add `import Json.Encode` and `import Json.Decode` at the top if not already present, and add the new `describe` block into the top-level `all` list):

```elm
encodeCreateTests : Test
encodeCreateTests =
    describe "encodeCreate"
        [ test "includes required title and repo" <|
            \_ ->
                let
                    json =
                        Json.Encode.encode 0
                            (AgentTicket.encodeCreate
                                { title = "ship it"
                                , body = "do the thing"
                                , repo = "tdmtrader/concourse"
                                , targetBranch = ""
                                , workflowName = ""
                                , workflowVersion = Nothing
                                , budgetUsd = Nothing
                                }
                            )
                in
                Expect.all
                    [ \s -> Expect.equal True (String.contains "\"title\":\"ship it\"" s)
                    , \s -> Expect.equal True (String.contains "\"repo\":\"tdmtrader/concourse\"" s)
                    , \s -> Expect.equal True (String.contains "\"body\":\"do the thing\"" s)
                    ]
                    json
        , test "omits empty optional fields" <|
            \_ ->
                let
                    json =
                        Json.Encode.encode 0
                            (AgentTicket.encodeCreate
                                { title = "t"
                                , body = ""
                                , repo = "o/n"
                                , targetBranch = ""
                                , workflowName = ""
                                , workflowVersion = Nothing
                                , budgetUsd = Nothing
                                }
                            )
                in
                Expect.all
                    [ \s -> Expect.equal False (String.contains "target_branch" s)
                    , \s -> Expect.equal False (String.contains "workflow_name" s)
                    , \s -> Expect.equal False (String.contains "workflow_version" s)
                    , \s -> Expect.equal False (String.contains "budget_usd" s)
                    ]
                    json
        , test "includes optional fields when set" <|
            \_ ->
                let
                    json =
                        Json.Encode.encode 0
                            (AgentTicket.encodeCreate
                                { title = "t"
                                , body = ""
                                , repo = "o/n"
                                , targetBranch = "main"
                                , workflowName = "develop"
                                , workflowVersion = Nothing
                                , budgetUsd = Just 5.0
                                }
                            )
                in
                Expect.all
                    [ \s -> Expect.equal True (String.contains "\"target_branch\":\"main\"" s)
                    , \s -> Expect.equal True (String.contains "\"workflow_name\":\"develop\"" s)
                    , \s -> Expect.equal True (String.contains "\"budget_usd\":5" s)
                    ]
                    json
        ]
```

Ensure `encodeCreateTests` is referenced in the `all : Test` / `describe` aggregate at the bottom of the file (add it to the list).

- [ ] Run it, expect FAIL:

```
cd web/elm && npx elm-test tests/AgentTicketTests.elm
```

Expected: compile failure — `AgentTicket.encodeCreate` and `CreateParams` do not exist (`Naming error` / `I cannot find a AgentTicket.encodeCreate variable`).

- [ ] Minimal implementation. In `web/elm/src/Concourse/AgentTicket.elm`, add `CreateParams` and `encodeCreate` to the exposing list (insert after `compareUrl` in the `exposing (...)` block):

```elm
module Concourse.AgentTicket exposing
    ( CreateParams
    , Detail
    , DispatchResult
    , Spec
    , Task
    , Ticket
    , compareUrl
    , decodeDetail
    , decodeDispatchResult
    , decodeSpec
    , decodeTask
    , decodeTicket
    , encodeCreate
    , repoWebUrl
    )
```

Add `import Json.Encode` next to the existing `import Json.Decode`. Then add the type and encoder (e.g. just below the `DispatchResult` alias):

```elm
type alias CreateParams =
    { title : String
    , body : String
    , repo : String
    , targetBranch : String
    , workflowName : String
    , workflowVersion : Maybe Int
    , budgetUsd : Maybe Float
    }


{-| The create-ticket request body (POST /api/v1/agent/tickets). Required
fields (title, repo) are always sent; every optional field is omitted when
empty/Nothing so the server's omitempty defaults apply — notably `origin`,
which the server fills as "web" for browser-created tickets.
-}
encodeCreate : CreateParams -> Json.Encode.Value
encodeCreate params =
    Json.Encode.object
        (List.concat
            [ [ ( "title", Json.Encode.string params.title )
              , ( "body", Json.Encode.string params.body )
              , ( "repo", Json.Encode.string params.repo )
              ]
            , if params.targetBranch == "" then
                []

              else
                [ ( "target_branch", Json.Encode.string params.targetBranch ) ]
            , if params.workflowName == "" then
                []

              else
                [ ( "workflow_name", Json.Encode.string params.workflowName ) ]
            , case params.workflowVersion of
                Just v ->
                    [ ( "workflow_version", Json.Encode.int v ) ]

                Nothing ->
                    []
            , case params.budgetUsd of
                Just b ->
                    [ ( "budget_usd", Json.Encode.float b ) ]

                Nothing ->
                    []
            ]
        )
```

- [ ] Run it, expect PASS:

```
cd web/elm && npx elm-test tests/AgentTicketTests.elm
```

Expected: `TEST RUN PASSED`.

- [ ] Commit:

```
git add web/elm/src/Concourse/AgentTicket.elm web/elm/tests/AgentTicketTests.elm
git commit -m "feat(web): CreateParams + encodeCreate for agent-ticket create"
```

---

## Task 2 — Effect + Callback + Message plumbing

Wire a `CreateAgentTicket` effect that POSTs the body and decodes the created ticket, a `AgentTicketCreated` callback, and the form messages. This task is compile-and-plumb; behavior is exercised in Tasks 4–5.

**Files:**
- Modify: `web/elm/src/Message/Message.elm`
- Modify: `web/elm/src/Message/Callback.elm`
- Modify: `web/elm/src/Message/Effects.elm`
- Test: `web/elm/tests/AgentTicketsPageTests.elm`

**Steps:**

- [ ] Write the failing test. Append to `web/elm/tests/AgentTicketsPageTests.elm` a test that the effect and callback constructors exist and round-trip through `update`. Add this test into the top-level `all` describe list:

```elm
plumbingTest : Test
plumbingTest =
    test "CreateAgentTicket effect and AgentTicketCreated callback are wired" <|
        \_ ->
            -- Compile-level guard: these constructors must exist and typecheck.
            let
                effect =
                    Effects.CreateAgentTicket
                        { title = "t"
                        , body = ""
                        , repo = "o/n"
                        , targetBranch = ""
                        , workflowName = ""
                        , workflowVersion = Nothing
                        , budgetUsd = Nothing
                        }

                callback =
                    Callback.AgentTicketCreated
                        (Err (Http.BadStatus 500))
            in
            Expect.equal effect effect
```

Add `import Http` to the test file's imports if not present.

- [ ] Run it, expect FAIL:

```
cd web/elm && npx elm-test tests/AgentTicketsPageTests.elm
```

Expected: compile failure — `Effects.CreateAgentTicket` and `Callback.AgentTicketCreated` do not exist.

- [ ] Implement. In `web/elm/src/Message/Callback.elm`, add after `AgentTicketDispatched` (line ~87):

```elm
    | AgentTicketCreated (Fetched Concourse.AgentTicket.Ticket)
```

- [ ] In `web/elm/src/Message/Message.elm`, add after `AgentTicketsSortToggled` (line ~88):

```elm
    | ClickNewAgentTicket
    | CancelNewAgentTicket
    | NewAgentTicketTitleChanged String
    | NewAgentTicketBodyChanged String
    | NewAgentTicketRepoChanged String
    | NewAgentTicketBranchChanged String
    | NewAgentTicketBudgetChanged String
    | NewAgentTicketWorkflowChanged String
    | NewAgentTicketQueueToggled
    | SubmitNewAgentTicket
```

- [ ] In `web/elm/src/Message/Effects.elm`, add the effect constructor after `DispatchAgentTicket Int` (line ~247):

```elm
    | CreateAgentTicket Concourse.AgentTicket.CreateParams
```

Then add the `runEffect` arm next to `DispatchAgentTicket` (after line ~926):

```elm
        CreateAgentTicket params ->
            Api.post Endpoints.AgentTicketsList csrfToken
                |> Api.withJsonBody (Concourse.AgentTicket.encodeCreate params)
                |> Api.expectJson Concourse.AgentTicket.decodeTicket
                |> Api.request
                |> Task.attempt AgentTicketCreated
```

(`Endpoints.AgentTicketsList` already maps to `["agent","tickets"]`; POST to it is the create route.)

- [ ] Run it, expect PASS:

```
cd web/elm && npx elm-test tests/AgentTicketsPageTests.elm
```

Expected: `TEST RUN PASSED` (existing tests still green; new `plumbingTest` passes).

- [ ] Commit:

```
git add web/elm/src/Message/Message.elm web/elm/src/Message/Callback.elm web/elm/src/Message/Effects.elm web/elm/tests/AgentTicketsPageTests.elm
git commit -m "feat(web): CreateAgentTicket effect + AgentTicketCreated callback + form messages"
```

---

## Task 3 — Form model, lazy workflow fetch, and form view on the queue page

Add the form's model fields, fetch workflows lazily when the form opens, and render the form (title, spec body, repo, target branch, budget, workflow `<select>`, "queue immediately" checkbox). The "New ticket" button toggles it.

**Files:**
- Modify: `web/elm/src/AgentTickets/AgentTickets.elm`
- Test: `web/elm/tests/AgentTicketsPageTests.elm`

**Steps:**

- [ ] Write the failing test. Append to `web/elm/tests/AgentTicketsPageTests.elm`:

```elm
openFormTest : Test
openFormTest =
    describe "new-ticket form"
        [ test "New ticket button is present" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.view
                    |> Query.fromHtml
                    |> Query.find [ class "agent-new-ticket-open" ]
                    |> Query.has [ text "New ticket" ]
        , test "clicking New ticket reveals the form and fetches workflows" <|
            \_ ->
                let
                    ( _, effects ) =
                        Common.init "/agent-tickets"
                            |> Application.update
                                (Msgs.Update Message.ClickNewAgentTicket)
                in
                Expect.equal True (List.member Effects.FetchAgentWorkflows effects)
        ]
```

Note: the queue page's controls dispatch bare `Message` values via plain `onClick ClickNewAgentTicket` (not the `Click`/`Hover` hover-state messages — the page has no `HoverState`). So the "clicking New ticket" test above drives the message directly through `Msgs.Update Message.ClickNewAgentTicket`, the same pattern the existing `AgentTicketsFilterChanged`/`AgentTicketsSortToggled` tests use. (There is no `Message.Click`/`Message.NewAgentTicketButton` constructor — do not wire one.)

Add `openFormTest` to the `all` list.

- [ ] Run it, expect FAIL:

```
cd web/elm && npx elm-test tests/AgentTicketsPageTests.elm
```

Expected: FAIL — no `agent-new-ticket-open` element; `ClickNewAgentTicket` not handled so no `FetchAgentWorkflows` effect emitted.

- [ ] Implement. In `web/elm/src/AgentTickets/AgentTickets.elm`:

Add imports (top of file, alphabetized with the existing block):

```elm
import Concourse.Agent
```

Extend `Model` (inside the `Login.Model { ... }` record) with the form fields:

```elm
        , showNewForm : Bool
        , newTitle : String
        , newBody : String
        , newRepo : String
        , newBranch : String
        , newBudget : String
        , newWorkflow : String
        , newQueue : Bool
        , workflows : List Concourse.Agent.WorkflowSummary
        , creating : Bool
        , createError : Maybe String
```

Extend `init` record literal with the defaults (add after `sortByWait = False`):

```elm
      , showNewForm = False
      , newTitle = ""
      , newBody = ""
      , newRepo = ""
      , newBranch = ""
      , newBudget = ""
      , newWorkflow = ""
      , newQueue = False
      , workflows = []
      , creating = False
      , createError = Nothing
```

In `handleCallback`, add an arm to capture the workflow list (place before the final `_ ->`):

```elm
        AgentWorkflowsFetched (Ok workflows) ->
            ( { model | workflows = workflows }, effects )

        AgentWorkflowsFetched (Err _) ->
            ( model, effects )
```

In `update`, add the field/toggle handlers (before the final `_ ->`):

```elm
        ClickNewAgentTicket ->
            -- Open the form and fetch the workflow list lazily (only when a
            -- user actually opens the form, not on every queue-page load).
            ( { model | showNewForm = True, createError = Nothing }
            , effects ++ [ FetchAgentWorkflows ]
            )

        CancelNewAgentTicket ->
            ( { model | showNewForm = False, createError = Nothing }, effects )

        NewAgentTicketTitleChanged v ->
            ( { model | newTitle = v }, effects )

        NewAgentTicketBodyChanged v ->
            ( { model | newBody = v }, effects )

        NewAgentTicketRepoChanged v ->
            ( { model | newRepo = v }, effects )

        NewAgentTicketBranchChanged v ->
            ( { model | newBranch = v }, effects )

        NewAgentTicketBudgetChanged v ->
            ( { model | newBudget = v }, effects )

        NewAgentTicketWorkflowChanged v ->
            ( { model | newWorkflow = v }, effects )

        NewAgentTicketQueueToggled ->
            ( { model | newQueue = not model.newQueue }, effects )
```

(`SubmitNewAgentTicket` and `AgentTicketCreated` are handled in Task 4.)

Render the open button + form. In `content`, change the `controlsBar model :: body ...` list so the form and button appear above the sections. Replace the final `Html.div [] (...)` expression in `content` with:

```elm
        Html.div []
            (newTicketControls model
                :: controlsBar model
                :: body
                ++ unattributedFooter model.costByTicket
            )
```

Add the new view functions at the end of the view section (before the `formatUsd` helper):

```elm
{-| The "New ticket" open button and, when open, the create form. Kept above
the filter/sort controls so the primary write action is the first thing on
the queue page.
-}
newTicketControls : Model -> Html Message
newTicketControls model =
    if model.showNewForm then
        newTicketForm model

    else
        Html.div [ style "margin" "8px 0 0 0" ]
            [ Html.button
                [ class "agent-new-ticket-open"
                , type_ "button"
                , onClick ClickNewAgentTicket
                , style "background" "#2e4f2e"
                , style "color" "#cfe8cf"
                , style "border" "1px solid #3d3c3c"
                , style "padding" "6px 14px"
                , style "cursor" "pointer"
                , style "font-size" "13px"
                ]
                [ Html.text "New ticket" ]
            ]


newTicketForm : Model -> Html Message
newTicketForm model =
    Html.div
        [ class "agent-new-ticket-form"
        , style "border" "1px solid #3d3c3c"
        , style "background" "#1e1d1d"
        , style "padding" "12px"
        , style "margin" "8px 0"
        ]
        [ newFieldLabel "title"
        , Html.input
            (class "agent-new-ticket-title" :: value model.newTitle :: onInput NewAgentTicketTitleChanged :: newInputStyles)
            []
        , newFieldLabel "repo (owner/name — required)"
        , Html.input
            (class "agent-new-ticket-repo" :: value model.newRepo :: placeholder "tdmtrader/concourse" :: onInput NewAgentTicketRepoChanged :: newInputStyles)
            []
        , newFieldLabel "spec (markdown body)"
        , Html.textarea
            (class "agent-new-ticket-body" :: value model.newBody :: onInput NewAgentTicketBodyChanged :: style "min-height" "120px" :: newInputStyles)
            []
        , newFieldLabel "target branch (optional)"
        , Html.input
            (class "agent-new-ticket-branch" :: value model.newBranch :: placeholder "main" :: onInput NewAgentTicketBranchChanged :: newInputStyles)
            []
        , newFieldLabel "budget USD (optional)"
        , Html.input
            (class "agent-new-ticket-budget" :: value model.newBudget :: placeholder "e.g. 5.00" :: onInput NewAgentTicketBudgetChanged :: newInputStyles)
            []
        , newFieldLabel "workflow"
        , workflowPicker model
        , Html.label
            [ style "display" "flex", style "align-items" "center", style "gap" "6px", style "margin" "10px 0 0", style "color" "#b0b0b0", style "font-size" "13px" ]
            [ Html.input
                [ class "agent-new-ticket-queue"
                , type_ "checkbox"
                , Html.Attributes.checked model.newQueue
                , onClick NewAgentTicketQueueToggled
                ]
                []
            , Html.text "queue immediately after creating"
            ]
        , case model.createError of
            Just err ->
                Html.p [ style "color" "#f0a0a0", style "margin" "8px 0 0" ] [ Html.text err ]

            Nothing ->
                Html.text ""
        , Html.div
            [ style "display" "flex", style "gap" "8px", style "margin-top" "10px" ]
            [ Html.button
                [ class "agent-new-ticket-submit"
                , type_ "button"
                , onClick SubmitNewAgentTicket
                , Html.Attributes.disabled model.creating
                , style "background" "#2e4f2e"
                , style "color" "#cfe8cf"
                , style "border" "1px solid #3d3c3c"
                , style "padding" "5px 12px"
                , style "cursor" "pointer"
                , style "font-size" "13px"
                ]
                [ Html.text
                    (if model.creating then
                        "Creating…"

                     else
                        "Create ticket"
                    )
                ]
            , Html.button
                [ type_ "button"
                , onClick CancelNewAgentTicket
                , style "background" "#2a2929"
                , style "color" "#d0d0d0"
                , style "border" "1px solid #3d3c3c"
                , style "padding" "5px 12px"
                , style "cursor" "pointer"
                , style "font-size" "13px"
                ]
                [ Html.text "Cancel" ]
            ]
        ]


{-| Workflow `<select>` populated from the lazily-fetched workflow list. The
empty option leaves `workflow_name` unset so dispatch resolves the live
version later (the ticket freezes a version at dispatch, not at create).
-}
workflowPicker : Model -> Html Message
workflowPicker model =
    Html.select
        [ class "agent-new-ticket-workflow"
        , Html.Events.onInput NewAgentTicketWorkflowChanged
        , style "width" "100%"
        , style "background" "#141313"
        , style "color" "#e0e0e0"
        , style "border" "1px solid #3d3c3c"
        , style "padding" "6px 8px"
        , style "box-sizing" "border-box"
        ]
        (Html.option
            [ value "", Html.Attributes.selected (model.newWorkflow == "") ]
            [ Html.text "(decide at dispatch)" ]
            :: List.map
                (\w ->
                    Html.option
                        [ value w.name, Html.Attributes.selected (model.newWorkflow == w.name) ]
                        [ Html.text w.name ]
                )
                model.workflows
        )


newFieldLabel : String -> Html Message
newFieldLabel txt =
    Html.div
        [ style "font-size" "11px", style "text-transform" "uppercase", style "letter-spacing" "0.08em", style "color" "#9aa39b", style "margin" "8px 0 4px" ]
        [ Html.text txt ]


newInputStyles : List (Html.Attribute Message)
newInputStyles =
    [ style "width" "100%"
    , style "background" "#141313"
    , style "color" "#e0e0e0"
    , style "border" "1px solid #3d3c3c"
    , style "padding" "6px 8px"
    , style "box-sizing" "border-box"
    ]
```

- [ ] Run it, expect PASS:

```
cd web/elm && npx elm-test tests/AgentTicketsPageTests.elm
```

Expected: `TEST RUN PASSED`.

- [ ] Commit:

```
git add web/elm/src/AgentTickets/AgentTickets.elm web/elm/tests/AgentTicketsPageTests.elm
git commit -m "feat(web): new-ticket form + workflow picker on the queue page"
```

---

## Task 4 — Submit → create → optional queue → navigate

Handle `SubmitNewAgentTicket` (validate repo/title, build `CreateParams`, emit `CreateAgentTicket`) and `AgentTicketCreated` (`Ok ticket` → optionally queue then navigate to the new ticket detail; `Err` → inline error). Reuses `TransitionAgentTicket` for the queue edge and `NavigateTo` for the redirect.

**Files:**
- Modify: `web/elm/src/AgentTickets/AgentTickets.elm`
- Test: `web/elm/tests/AgentTicketsPageTests.elm`

**Steps:**

- [ ] Write the failing test. Append to `web/elm/tests/AgentTicketsPageTests.elm`:

```elm
submitTest : Test
submitTest =
    describe "submitting the new-ticket form"
        [ test "valid submit emits CreateAgentTicket with the entered fields" <|
            \_ ->
                let
                    ( _, effects ) =
                        Common.init "/agent-tickets"
                            |> Application.update (Msgs.Update Message.ClickNewAgentTicket)
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.NewAgentTicketTitleChanged "ship it"))
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.NewAgentTicketRepoChanged "o/n"))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.SubmitNewAgentTicket)
                in
                Expect.equal True
                    (List.member
                        (Effects.CreateAgentTicket
                            { title = "ship it"
                            , body = ""
                            , repo = "o/n"
                            , targetBranch = ""
                            , workflowName = ""
                            , workflowVersion = Nothing
                            , budgetUsd = Nothing
                            }
                        )
                        effects
                    )
        , test "submit with empty repo does not emit CreateAgentTicket" <|
            \_ ->
                let
                    ( _, effects ) =
                        Common.init "/agent-tickets"
                            |> Application.update (Msgs.Update Message.ClickNewAgentTicket)
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.NewAgentTicketTitleChanged "no repo"))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.SubmitNewAgentTicket)
                in
                Expect.equal False
                    (List.any
                        (\e ->
                            case e of
                                Effects.CreateAgentTicket _ ->
                                    True

                                _ ->
                                    False
                        )
                        effects
                    )
        , test "created ticket navigates to its detail page" <|
            \_ ->
                let
                    created =
                        { id = 99
                        , title = "ship it"
                        , body = ""
                        , state = "draft"
                        , origin = "web"
                        , repo = "o/n"
                        , targetBranch = ""
                        , workflowName = ""
                        , budgetUsd = Nothing
                        , userName = "me"
                        , branch = ""
                        , createdAt = 0
                        , updatedAt = 0
                        , workflowVersion = Nothing
                        , pipelineRunId = Nothing
                        , attemptCount = 0
                        , errorDetail = ""
                        , completedAt = Nothing
                        }

                    ( _, effects ) =
                        Common.init "/agent-tickets"
                            |> Application.handleCallback
                                (Callback.AgentTicketCreated (Ok created))
                in
                Expect.equal True
                    (List.member (Effects.NavigateTo "/agent-tickets/99") effects)
        ]
```

Add `submitTest` to the `all` list.

- [ ] Run it, expect FAIL:

```
cd web/elm && npx elm-test tests/AgentTicketsPageTests.elm
```

Expected: FAIL — `SubmitNewAgentTicket` and `AgentTicketCreated` are not handled, so no `CreateAgentTicket` / `NavigateTo` effect is emitted.

- [ ] Implement. In `web/elm/src/AgentTickets/AgentTickets.elm` `update`, add the submit handler (before the final `_ ->`):

```elm
        SubmitNewAgentTicket ->
            let
                title =
                    String.trim model.newTitle

                repo =
                    String.trim model.newRepo
            in
            if title == "" || repo == "" then
                ( { model | createError = Just "Title and repo are required." }, effects )

            else
                ( { model | creating = True, createError = Nothing }
                , effects
                    ++ [ CreateAgentTicket
                            { title = title
                            , body = model.newBody
                            , repo = repo
                            , targetBranch = String.trim model.newBranch
                            , workflowName = model.newWorkflow
                            , workflowVersion = Nothing
                            , budgetUsd = parseBudget model.newBudget
                            }
                       ]
                )
```

Add the `parseBudget` helper (near the other helpers at the bottom, mirroring `AgentTicket.elm`):

```elm
parseBudget : String -> Maybe Float
parseBudget raw =
    case String.trim raw of
        "" ->
            Nothing

        trimmed ->
            String.toFloat trimmed
```

In `handleCallback`, add the created arms (before the final `_ ->`):

```elm
        AgentTicketCreated (Ok ticket) ->
            -- Created as a draft. If "queue immediately" was checked, fire the
            -- draft→queued transition, then navigate to the new ticket's detail
            -- page — where the existing two-step Dispatch confirm is the money
            -- gate. We deliberately do NOT auto-dispatch from the queue form.
            let
                queueEffect =
                    if model.newQueue then
                        [ TransitionAgentTicket
                            { id = ticket.id, from = "draft", to = "queued" }
                        ]

                    else
                        []
            in
            ( { model
                | showNewForm = False
                , creating = False
                , createError = Nothing
                , newTitle = ""
                , newBody = ""
                , newRepo = ""
                , newBranch = ""
                , newBudget = ""
                , newWorkflow = ""
                , newQueue = False
              }
            , effects
                ++ queueEffect
                ++ [ NavigateTo (Routes.toString (Routes.AgentTicket { id = ticket.id })) ]
            )

        AgentTicketCreated (Err _) ->
            ( { model | creating = False, createError = Just "Couldn't create the ticket." }, effects )
```

- [ ] Run it, expect PASS:

```
cd web/elm && npx elm-test tests/AgentTicketsPageTests.elm
```

Expected: `TEST RUN PASSED`.

- [ ] Commit:

```
git add web/elm/src/AgentTickets/AgentTickets.elm web/elm/tests/AgentTicketsPageTests.elm
git commit -m "feat(web): submit new ticket, optional queue, navigate to detail"
```

---

## Task 5 — "Active attempt" live strip on the ticket detail page

The loop's "live attempt view." The detail page already polls every 5s (`polls`) and renders `runHistory` + the review card, so the data is live. Rather than duplicate the S-2 transcript viewer, add a compact strip at the top of the detail (in the `top` list) that appears while the ticket is `running` (or `queued`, i.e. dispatched-but-not-started), showing the latest build's live status badge and a direct link to the build page. This reuses `AgentBadge` and the already-fetched `runMetrics`; it is the lightest surface that closes the "I dispatched — now what?" gap. (Justification: full transcript rendering is S-2's scope and depends on the flight-events read API in draft #43; this strip needs neither.)

**Files:**
- Modify: `web/elm/src/AgentTickets/AgentTicket.elm`
- Test: `web/elm/tests/AgentTicketPageTests.elm`

**Steps:**

- [ ] Write the failing test. Append to `web/elm/tests/AgentTicketPageTests.elm`, reusing the file's existing helpers: the `withDetail` JSON-decode wrapper, the `runningDetailJson` fixture (already `state: "running"`, ticket `id: 9`), and `Common.queryView`. The agent-ticket detail route carries **no** `/teams/main` prefix — it is `/agent-tickets/<id>` (`Routes.elm:331`, and every existing test in this file uses `/agent-tickets/12`). The `RunMetric` for the metrics callback is written as a full record literal exactly like the "run history rows link to their build" test at ~lines 194-219 (no separate named fixture, no `Debug.todo`, no `Concourse.Agent` import needed — the callback's type fixes the record type). Add `activeAttemptTest` to the `all` list:

```elm
activeAttemptTest : Test
activeAttemptTest =
    test "shows an active-attempt strip while running" <|
        \_ ->
            withDetail runningDetailJson
                (\d ->
                    Common.init "/agent-tickets/9"
                        |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                        |> Tuple.first
                        |> Application.handleCallback
                            (Callback.AgentTicketMetricsFetched 9
                                (Ok
                                    [ { ticketId = Just 9
                                      , pipelineRunId = Just 2
                                      , buildId = 500
                                      , planId = "plan-run"
                                      , stepName = "implement"
                                      , workflowName = "develop"
                                      , workflowVersion = Just 1
                                      , status = "ok"
                                      , buildStatus = "started"
                                      , outcome = ""
                                      , summary = ""
                                      , model = ""
                                      , usage =
                                            { inputTokens = 0
                                            , outputTokens = 0
                                            , cacheReadInputTokens = 0
                                            , cacheCreationInputTokens = 0
                                            }
                                      , turns = 1
                                      , wallTimeSeconds = 1
                                      , costUsd = 0.0
                                      , eventCounts = Dict.empty
                                      , createdAt = 100
                                      }
                                    ]
                                )
                            )
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.has [ class "agent-ticket-active-attempt" ]
                )
```

(`runningDetailJson` is already defined at the top of the file with `id: 9` / `state: "running"`, so reusing it means the fixture cannot rot as the `Ticket` decoder changes shape. Confirm the inline `RunMetric` field names against the sibling record literal at ~lines 194-219 before finalizing — `buildStatus = "started"` marks the attempt as in-flight, and `Dict.empty` matches the `eventCounts : Dict ...` field, `Dict` already being imported by this file.)

- [ ] Run it, expect FAIL:

```
cd web/elm && npx elm-test tests/AgentTicketPageTests.elm
```

Expected: FAIL — no `agent-ticket-active-attempt` element in the rendered page.

- [ ] Implement. In `web/elm/src/AgentTickets/AgentTicket.elm`, add `activeAttempt` to the `top` list in `content` (insert after `errorNotice ticket`):

```elm
                    top =
                        [ header model ticket
                        , provenanceLine ticket
                        , provenanceTimestamps session.timeZone ticket
                        , errorNotice ticket
                        , activeAttempt ticket model.runMetrics
                        , actionErrorBanner model
                        ]
```

Add the view function (near `runHistory`):

```elm
{-| A compact live strip for a dispatched-but-unfinished ticket: the latest
build's current status badge plus a link into the build page. Only shown while
the ticket is running/queued — terminal and needs_review tickets have the full
run history and review card instead. Live because the page already refetches
metrics on the 5s poll.
-}
activeAttempt : AgentTicket.Ticket -> List Concourse.Agent.RunMetric -> Html Message
activeAttempt ticket metrics =
    if ticket.state /= "running" then
        Html.text ""

    else
        case metrics |> List.map .buildId |> List.maximum of
            Nothing ->
                Html.div
                    [ class "agent-ticket-active-attempt"
                    , style "border" "1px solid #3d3c3c"
                    , style "background" "#1b201b"
                    , style "padding" "8px 12px"
                    , style "margin" "10px 0"
                    , style "color" "#9aa39b"
                    , style "font-size" "13px"
                    ]
                    [ Html.text "Attempt starting…" ]

            Just buildId ->
                let
                    forBuild =
                        metrics |> List.filter (\m -> m.buildId == buildId)

                    runStatus =
                        case List.filter (\m -> m.status == "parked") forBuild of
                            parked :: _ ->
                                parked.status

                            [] ->
                                forBuild
                                    |> List.reverse
                                    |> List.head
                                    |> Maybe.map .status
                                    |> Maybe.withDefault ""

                    buildStatus =
                        forBuild |> List.head |> Maybe.map .buildStatus |> Maybe.withDefault ""

                    hasResult =
                        lastNonEmptySummary forBuild /= ""

                    statusView =
                        case AgentBadge.runOutcome { buildStatus = buildStatus, runStatus = runStatus, hasResult = hasResult } of
                            Just s ->
                                AgentBadge.view s

                            Nothing ->
                                Html.span [ style "color" "#b0b0b0" ] [ Html.text runStatus ]
                in
                Html.a
                    [ class "agent-ticket-active-attempt"
                    , href (Routes.toString (Routes.OneOffBuild { id = buildId, highlight = Routes.HighlightNothing }))
                    , style "display" "flex"
                    , style "align-items" "center"
                    , style "gap" "10px"
                    , style "border" "1px solid #3d5f3d"
                    , style "background" "#1b201b"
                    , style "padding" "8px 12px"
                    , style "margin" "10px 0"
                    , style "color" "inherit"
                    , style "text-decoration" "none"
                    ]
                    [ Html.span [ style "color" "#9aa39b", style "font-size" "12px" ] [ Html.text "active attempt" ]
                    , statusView
                    , Html.span
                        [ style "font-family" "monospace", style "color" "#7aa37a", style "font-size" "12px" ]
                        [ Html.text ("build " ++ String.fromInt buildId ++ " →") ]
                    ]
```

- [ ] Run it, expect PASS:

```
cd web/elm && npx elm-test tests/AgentTicketPageTests.elm
```

Expected: `TEST RUN PASSED`.

- [ ] Commit:

```
git add web/elm/src/AgentTickets/AgentTicket.elm web/elm/tests/AgentTicketPageTests.elm
git commit -m "feat(web): active-attempt live strip on the ticket detail page"
```

---

## Task 6 — Full elm-test sweep + rebuild the embedded bundle

The served UI is `web/public/elm.min.js`. Editing `web/elm/src` without rebuilding leaves the browser on the OLD bundle (the documented stale-bundle trap). **There is no local elm-build gate today** — nothing in CI regenerates or verifies the bundle on merge, so this rebuild-and-commit step is mandatory and manual. (WF-2 is the track that adds an automated elm-build gate; until it lands, forgetting this step ships a stale UI.)

**Files:**
- Modify: `web/public/elm.min.js` (generated)

**Steps:**

- [ ] Run the full Elm test suite (not just the touched files):

```
cd web/elm && npx elm-test
```

Expected: `TEST RUN PASSED` for the whole suite.

- [ ] Rebuild the embedded bundle:

```
cd /Users/tdmtrader/concourse/concourse && ./hack/build-web.sh
```

Expected: `built web/public/elm.min.js (<N> bytes)` with no `elm make` errors. (Requires `elm` 0.19.1 and `uglify-js` on PATH.)

- [ ] Sanity-confirm the bundle changed and mentions the new class hooks:

```
cd /Users/tdmtrader/concourse/concourse && git status --short web/public/elm.min.js && grep -c "agent-new-ticket-open" web/public/elm.min.js
```

Expected: `elm.min.js` shows as modified and the grep count is `>= 1`.

- [ ] Commit the bundle:

```
git add web/public/elm.min.js
git commit -m "build(web): rebuild elm.min.js for S-5 web loop closure"
```

---

## Self-Review

**Spec coverage (S-5 deliverables → tasks):**
- New-ticket form (title, spec markdown body, budget, workflow picker) → Task 3 (`newTicketForm`, `workflowPicker`), with title + body + budget + repo + branch fields.
- Queue-with-workflow → Task 3 (workflow `<select>` feeds `workflow_name`) + Task 4 ("queue immediately" checkbox fires `draft→queued` after create).
- Dispatch button → **reused as-is**: `AgentTicket.elm` already has the two-step `ClickAgentTicketDispatch`/`ConfirmAgentTicketDispatch` money-gate; Task 4 navigates the user there. No dispatch button is duplicated on the queue form (deliberate — see Open Decisions).
- Live attempt view → Task 5 (`activeAttempt` strip, live via the existing 5s poll), justified against reusing S-2's transcript viewer.
- elm.min.js rebuild → Task 6.

**Placeholder scan:** No `TODO`/`TBD`/"handle edge cases"/"similar to Task N" in any implementation step; every code step contains real code. Task 5's metrics fixture is a full `RunMetric` record literal copied from the sibling test at `AgentTicketPageTests.elm` ~lines 194-219 (no `Debug.todo`, no hand-written JSON string), and the ticket is the file's existing `runningDetailJson` — the only conditional guidance is to re-confirm those `RunMetric` field names against the sibling literal before finalizing, a grounding instruction rather than a code placeholder, repeated below as a grounding risk.

**Type consistency:**
- `Effects.CreateAgentTicket : Concourse.AgentTicket.CreateParams -> Effect`; the `runEffect` arm uses `Api.post ... |> Api.withJsonBody (encodeCreate params) |> Api.expectJson decodeTicket |> Api.request |> Task.attempt AgentTicketCreated`. Verified against `Api.elm`: `post` yields `Request ()`, `withJsonBody` preserves the type, `expectJson decodeTicket` returns `Request Ticket`, `Task.attempt AgentTicketCreated` needs `AgentTicketCreated : Fetched Ticket -> Callback` — matches the added constructor.
- `AgentTicketCreated (Ok ticket)` navigates with `NavigateTo (Routes.toString (Routes.AgentTicket { id = ticket.id }))`; `Routes.AgentTicket { id : Int }` confirmed at `Routes.elm:64`, and `NavigateTo String` is an existing effect (`Effects.elm:173`).
- The queue transition reuses the existing `TransitionAgentTicket { id, from, to }` effect (string states `"draft"`/`"queued"`), identical to the shape the detail page already sends.
- `WorkflowSummary` fields (`name`) confirmed at `Concourse/Agent.elm:25`; `AgentWorkflowsFetched (Fetched (List WorkflowSummary))` callback confirmed at `Callback.elm:76`; `FetchAgentWorkflows` effect confirmed at `Effects.elm:223,820`.
- Create response shape confirmed bare `Ticket` (not `Detail`) from `handler.go:151` (`writeJSON(..., created)`), so `decodeTicket` (not `decodeDetail`) is correct.

---

## Open Decisions

1. **Should the queue-page form offer one-click "create & dispatch" (spending money immediately), matching `fly agent tickets create --dispatch`?**
   Recommendation: **No.** Keep create (and optional queue) on the form, and route the user to the detail page where the existing two-step arm/confirm dispatch is the deliberate money gate. `DispatchAgentTicket` is intentionally human-only precisely because the manual trigger is the budget gate (`api_auth_wrappa.go:224-227`); a one-click form dispatch would erode that. Revisit only if budget admission lands (dispatcher-budget track).

2. **Should the workflow picker let the user pin a specific workflow _version_, or always leave it live?**
   Recommendation: **Always live (send only `workflow_name`, `workflow_version` unset).** The ticket freezes the workflow version at dispatch, not at create, and `WorkflowSummary` already carries `liveVersion`. Pinning at create adds a second control for a value that dispatch will re-resolve anyway. Expose version-pinning later only if a concrete need appears (e.g. reproducing an old run).

3. **Should opening the form fetch the workflow list every time, or cache it for the page's lifetime?**
   Recommendation: **Fetch on each open (as planned).** The list is small, changes rarely, and fetching lazily on open keeps the queue page's default load path unchanged (no extra request for users who never create a ticket). If profiling shows this is noticeable, add a `workflowsLoaded : Bool` guard.

4. **Live attempt view depth: the lightweight status strip (this plan) vs. reusing the S-2 transcript viewer inline.**
   Recommendation: **Ship the strip now.** It is self-contained (no dependency on the draft #43 flight-events read API that S-2 needs) and closes the immediate "I dispatched — now what?" gap. When S-2 lands its transcript component, the strip can link to or embed it. Flag: sequence this after S-3's route placement if S-3 relocates the detail page.

---

## Grounding Risks

- **`Concourse.Agent.RunMetric` record fields in the Task 5 fixture** are copied verbatim from the sibling record literal at `AgentTicketPageTests.elm` ~lines 194-219 (`ticketId`, `pipelineRunId`, `buildId`, `planId`, `stepName`, `workflowName`, `workflowVersion`, `status`, `buildStatus`, `outcome`, `summary`, `model`, `usage{…}`, `turns`, `wallTimeSeconds`, `costUsd`, `eventCounts`, `createdAt`). Before finalizing Task 5, diff the fixture against that sibling literal so any field added to `RunMetric` since is picked up; because both are record literals the compiler flags any drift, and no hand-written JSON is involved.
- **`AgentTicketPageTests.elm` init/callback helpers** — confirmed present and reused: `withDetail` (line 72, decodes a JSON string via `AgentTicket.decodeDetail`), the `runningDetailJson` fixture (line 48, `id: 9` / `state: "running"`), `Common.init "/agent-tickets/<id>"`, `Application.handleCallback`, and `Common.queryView`. The detail route is `/agent-tickets/<id>` with no team prefix, matching every existing test in the file.
- **`Application.update` / `Application.handleCallback` return shape** (`( Model, List Effect )` and the `Msgs.Update` wrapper) is taken from the existing `AgentTicketsPageTests.elm` patterns; if the top-level test harness wraps effects differently, adjust the `Tuple.first` chaining accordingly.
- **`Msgs.Update Message.ClickNewAgentTicket`** assumes queue-page buttons dispatch bare `Message` values through `Msgs.Update` (consistent with the existing `AgentTicketsFilterChanged`/`AgentTicketsSortToggled` handlers, which are plain `onInput`/`onClick` messages, not `HoverState` `Click`/`Hover`). Verified against `AgentTickets.elm` `update` — it matches bare messages — but confirm the test harness's `Msgs.Update` constructor name against an existing passing test in the same file.
- **`hack/build-web.sh` toolchain** (`elm` 0.19.1 + `uglify-js`) must be installed locally; the script is otherwise correct (reads/writes `web/public/elm.min.js`). If `uglifyjs` is absent the bundle won't regenerate and the UI ships stale.

---

## Coordination Notes

- **No `agent/dispatch/render.go` edits** — this track never touches the refusal switch or `RenderAgentStep`; it only consumes existing endpoints.
- **No migration** — the migration ledger (`remainders/README.md`, head `1773106066`, next free `1773106067`) is untouched; no slot is claimed.
- **No new route / no six-touchpoint** — every endpoint (create/queue/dispatch/workflows) already exists in `atc/routes.go` and is already authorized for the main-team web user in `api_auth_wrappa.go`. This is why the track is Elm-only.
- **Additive wire only** — `CreateParams`/`encodeCreate` and the new effect/callback/messages are purely additive; no existing decoder, effect, or message changes shape, so it merges cleanly against in-flight web tracks (S-1..S-4).
- **elm.min.js is a merge hotspot** — the generated bundle (Task 6) will conflict with any other web track landing concurrently. Rebuild-and-commit it **last**, and if a conflict occurs, re-run `./hack/build-web.sh` after merging source rather than hand-resolving the minified diff.
