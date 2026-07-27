# S-4 In-app Diff Review Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. This diff-review proposal targeted per-ticket diffs; diff review now operates over workflow-run outcomes — see the delivery-outcomes design.

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

**Goal:** Render the ticket's `base_sha..pushed_sha` unified diff directly on the agent-ticket detail page as colored hunks — the primary review surface — and demote the stale-prone GitHub compare link to a secondary "compare on GitHub" affordance.

**Architecture:** The server endpoint `GET /api/v1/agent/tickets/:ticket_id/diff` already exists (landed #36 — route const, team-less auth, `DiffHandler`, and cache wiring are all in place) and returns a file-windowed `DiffPage` JSON (`files[].patch` are raw per-file unified-diff text). This track adds the read-only client plumbing that #36 skipped — a go-concourse `GetAgentTicketDiff` method (+counterfeiter fake regen), an optional `fly agent tickets diff` command — and the Elm surface: a small decoder module, an Endpoints entry, Effects/Callback wiring, a static unified-diff renderer, and the `AgentTicket.elm` wiring that fetches the diff on init and on the 5s self-heal poll while the ticket is non-terminal. No migration, no server handler change, no `render.go` touch.

**Tech Stack:** Go 1.x (go-concourse client, `ghttp` tests, counterfeiter fakes, `jessevdk/go-flags` for fly), Elm 0.19.1 (elm-test, `Json.Decode.Extra.andMap`), the embedded `web/public/elm.min.js` bundle rebuilt via `hack/build-web.sh`.

---

## File Structure

| File | Create/Modify | Responsibility |
|------|---------------|----------------|
| `go-concourse/concourse/client.go` | Modify | Add `GetAgentTicketDiff(id, offset, limit int) (gitcheck.DiffPage, bool, error)` to the `Client` interface. |
| `go-concourse/concourse/agent_tickets.go` | Modify | Implement `GetAgentTicketDiff` on `*client` — GET with `offset`/`limit` query, 404 → `(zero, false, nil)`. |
| `go-concourse/concourse/agent_tickets_test.go` | Modify | `ghttp` tests: happy path decodes a `DiffPage`; 404 returns `found == false`. |
| `go-concourse/concourse/concoursefakes/fake_client.go` | Regenerate | Counterfeiter fake for the new interface method (via `go generate`, not hand-edited). |
| `fly/commands/agent_tickets.go` | Modify | Add `AgentTicketsDiffCommand` (`command:"diff"`) printing each file's patch; register on `AgentTicketsCommand`. |
| `web/elm/src/Concourse/AgentDiff.elm` | Create | `DiffPage`/`DiffFile` types, JSON decoder, and a pure `classifyLine` line-kind classifier (unit-testable). |
| `web/elm/src/Views/AgentDiff.elm` | Create | `view : DiffPage -> Html msg` — colored unified-diff hunks, per-file headers, truncation/has_more notices. |
| `web/elm/src/Api/Endpoints.elm` | Modify | Add `AgentTicketDiff Int` endpoint mapping to `["agent","tickets", id, "diff"]`. |
| `web/elm/src/Message/Effects.elm` | Modify | Add `FetchAgentTicketDiff Int` effect that GETs the endpoint and decodes `AgentDiff.decodeDiffPage`. |
| `web/elm/src/Message/Callback.elm` | Modify | Add `AgentTicketDiffFetched (Fetched Concourse.AgentDiff.DiffPage)`. |
| `web/elm/src/AgentTickets/AgentTicket.elm` | Modify | Model fields `diff`/`diffLoadError`, init + poll fetch, callback handling, `diffSection` render, GitHub-link demotion. |
| `web/elm/tests/AgentDiffTests.elm` | Create | elm-test for `classifyLine` and `decodeDiffPage`. |
| `web/elm/tests/AgentTicketPageTests.elm` | Modify | Assert the in-app diff renders when `AgentTicketDiffFetched` arrives. |
| `web/public/elm.min.js` | Rebuild | Regenerated bundle — the served UI (stale-bundle trap). |

---

## Grounding facts (verified against the repo before writing this plan)

- `atc/routes.go:158,330` — `GetAgentTicketDiff` const + `{Path: "/api/v1/agent/tickets/:ticket_id/diff", Method: "GET"}` already exist.
- `atc/wrappa/api_auth_wrappa.go:232-237` — `GetAgentTicketDiff` is already in the **team-less** `CheckAgentAuthorizationHandler` block. **No wrappa change is needed** for this track.
- `atc/api/handler.go:176,357` — `outcomeDiffServer := outcomesapi.NewDiffHandler(...)` and `atc.GetAgentTicketDiff: http.HandlerFunc(outcomeDiffServer.GetDiff)` are already registered. **No handler change is needed.**
- `agent/api/outcomes/diff_handler.go` — the handler returns `writeJSON(w, 200, page)` where `page` is a `gitcheck.DiffPage`. 404s when the API is disabled, no outcome row exists, or `BaseSha`/`PushedSha` is empty. Accepts `offset` (default 0) and `limit` (default 50, capped 200) query params.
- `agent/gitcheck/detect.go:93-107` — the wire shape:
  ```go
  type DiffFile struct { Path string `json:"path"`; Patch string `json:"patch"`; Truncated bool `json:"truncated,omitempty"` }
  type DiffPage struct { Files []DiffFile `json:"files"`; Offset int `json:"offset"`; Limit int `json:"limit"`; TotalFiles int `json:"total_files"`; HasMore bool `json:"has_more"` }
  ```
  `gitcheck` is a leaf package (imports nothing from `go-concourse`), so importing it into the client creates no cycle.
- `go-concourse/concourse/agent_tickets.go:95-111` — `GetAgentTicketOutcome` is the exact template for the new 404-aware getter (`switch err.(type)` on `internal.ResourceNotFoundError`).
- `web/elm/src/AgentTickets/AgentTicket.elm:572-617` — `provenanceLine` renders the primary compare link with `class "agent-ticket-compare-link"` from `AgentTicket.compareUrl`. Existing tests (`AgentTicketPageTests.elm:166-185`) assert this class + href are present with a branch and absent without one — the demotion **must keep the class and href** so those tests still pass.
- `web/elm/src/AgentTickets/AgentTicket.elm:118` — init effects; `287-304` — the 5s `polls`; `455-538` — `view`/`content` composition.

---

## Task 1 — go-concourse `GetAgentTicketDiff` client method

**Files:**
- Modify `go-concourse/concourse/client.go` (Client interface, ~line 56)
- Modify `go-concourse/concourse/agent_tickets.go`
- Test `go-concourse/concourse/agent_tickets_test.go`
- Regenerate `go-concourse/concourse/concoursefakes/fake_client.go`

### Steps

- [ ] Write the failing test. Append to `go-concourse/concourse/agent_tickets_test.go` a new `Describe` block (add the `gitcheck` import `"github.com/concourse/concourse/agent/gitcheck"` at the top of the file):

```go
	Describe("GetAgentTicketDiff", func() {
		Context("when the diff exists", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/12/diff", "offset=0&limit=50"),
						ghttp.RespondWithJSONEncoded(http.StatusOK, gitcheck.DiffPage{
							Files: []gitcheck.DiffFile{
								{Path: "atc/foo.go", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
							},
							Offset: 0, Limit: 50, TotalFiles: 1, HasMore: false,
						}),
					),
				)
			})

			It("returns the decoded diff page and found=true", func() {
				page, found, err := client.GetAgentTicketDiff(12, 0, 50)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(page.TotalFiles).To(Equal(1))
				Expect(page.Files).To(HaveLen(1))
				Expect(page.Files[0].Path).To(Equal("atc/foo.go"))
				Expect(page.Files[0].Patch).To(ContainSubstring("+new"))
			})
		})

		Context("when there is no diff for the ticket", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99/diff", "offset=0&limit=50"),
						ghttp.RespondWith(http.StatusNotFound, ""),
					),
				)
			})

			It("returns found=false without an error", func() {
				_, found, err := client.GetAgentTicketDiff(99, 0, 50)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})
```

- [ ] Run it, expect FAIL (method does not exist / interface unsatisfied):
  ```
  cd /Users/tdmtrader/concourse/concourse && go test ./go-concourse/concourse/ -run TestGoConcourse 2>&1 | head -30
  ```
  Expected: a compile error like `client.GetAgentTicketDiff undefined (type Client has no field or method GetAgentTicketDiff)`.

- [ ] Add the interface method to `go-concourse/concourse/client.go`. Insert immediately after the `GetAgentTicketOutcome(...)` line (~line 56), and add the import `"github.com/concourse/concourse/agent/gitcheck"` to the import block:
```go
	GetAgentTicketDiff(id int, offset, limit int) (gitcheck.DiffPage, bool, error)
```

- [ ] Implement it in `go-concourse/concourse/agent_tickets.go`. Add `"github.com/concourse/concourse/agent/gitcheck"` to the imports, then append after `GetAgentTicketOutcome`:
```go
func (client *client) GetAgentTicketDiff(id int, offset, limit int) (gitcheck.DiffPage, bool, error) {
	query := url.Values{}
	query.Set("offset", strconv.Itoa(offset))
	query.Set("limit", strconv.Itoa(limit))

	var page gitcheck.DiffPage
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentTicketDiff,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
		Query:       query,
	}, &internal.Response{
		Result: &page,
	})
	switch err.(type) {
	case nil:
		return page, true, nil
	case internal.ResourceNotFoundError:
		return page, false, nil
	default:
		return page, false, err
	}
}
```

- [ ] Regenerate the counterfeiter fake (the fake is generated, never hand-edited):
  ```
  cd /Users/tdmtrader/concourse/concourse/go-concourse/concourse && go generate ./... 2>&1 | tail -5
  ```
  Expected: no error; `git status` shows `concoursefakes/fake_client.go` modified with a new `GetAgentTicketDiff` block.

- [ ] Run the test, expect PASS:
  ```
  cd /Users/tdmtrader/concourse/concourse && go test ./go-concourse/concourse/ -run TestGoConcourse 2>&1 | tail -15
  ```
  Expected: `ok  github.com/concourse/concourse/go-concourse/concourse` with the two new specs green.

- [ ] Commit:
  ```
  git add go-concourse/concourse/client.go go-concourse/concourse/agent_tickets.go go-concourse/concourse/agent_tickets_test.go go-concourse/concourse/concoursefakes/fake_client.go
  git commit -m "feat(go-concourse): GetAgentTicketDiff client method for in-app diff review"
  ```

---

## Task 2 — `fly agent tickets diff` command

**Files:**
- Modify `fly/commands/agent_tickets.go`

This is touchpoint 6 (optional but included for CLI parity with the web surface). No new test file — `fly/integration` builds and exercises the CLI; a targeted integration spec is out of scope for this focused wave (noted in Self-Review). The command is a thin printer over the Task-1 client method.

### Steps

- [ ] Add the subcommand field to `AgentTicketsCommand` in `fly/commands/agent_tickets.go` (after the `Dispose` line, ~line 27):
```go
	Diff       AgentTicketsDiffCommand       `command:"diff" description:"Show the base..pushed unified diff for a ticket's harvest branch"`
```

- [ ] Append the command type + `Execute` to the same file (mirrors `AgentTicketsShowCommand`'s target-resolution boilerplate; confirm the exact `Fly` global and `rc.LoadTarget` signature by reading the top of the file and `AgentTicketsShowCommand.Execute` before writing):
```go
type AgentTicketsDiffCommand struct {
	ID     int `long:"id" required:"true" description:"Ticket id"`
	Offset int `long:"offset" default:"0" description:"File window offset"`
	Limit  int `long:"limit" default:"50" description:"Max files in the window (server caps at 200)"`
}

func (command *AgentTicketsDiffCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	page, found, err := target.Client().GetAgentTicketDiff(command.ID, command.Offset, command.Limit)
	if err != nil {
		return err
	}
	if !found {
		fmt.Printf("no diff available for ticket %d\n", command.ID)
		return nil
	}

	for _, f := range page.Files {
		fmt.Printf("=== %s%s ===\n", f.Path, truncatedTag(f.Truncated))
		fmt.Println(f.Patch)
	}
	if page.HasMore {
		fmt.Printf("... %d of %d files shown; re-run with --offset %d for more\n",
			command.Offset+len(page.Files), page.TotalFiles, command.Offset+command.Limit)
	}
	return nil
}

func truncatedTag(t bool) string {
	if t {
		return " [truncated]"
	}
	return ""
}
```

- [ ] Verify the correct target-resolution idiom. Read `fly/commands/agent_tickets.go:242-291` (`AgentTicketsShowCommand.Execute`) FIRST and copy its exact `target, err := ...` / `target.Client()` lines verbatim — do not assume `rc.LoadTarget`/`Fly.Target` if that file uses a different accessor.

- [ ] Build the CLI, expect SUCCESS:
  ```
  cd /Users/tdmtrader/concourse/concourse && go build ./fly/... 2>&1 | head -20
  ```
  Expected: no output (clean build). If `fmt` is not yet imported in the file, add it.

- [ ] Manual verification of the command wiring (no live server needed):
  ```
  cd /Users/tdmtrader/concourse/concourse && go run ./fly agent tickets diff --help 2>&1 | head -20
  ```
  Expected: help text listing `--id`, `--offset`, `--limit`.

- [ ] Commit:
  ```
  git add fly/commands/agent_tickets.go
  git commit -m "feat(fly): agent tickets diff command"
  ```

---

## Task 3 — Elm `Concourse.AgentDiff` decoder + line classifier

**Files:**
- Create `web/elm/src/Concourse/AgentDiff.elm`
- Create `web/elm/tests/AgentDiffTests.elm`

### Steps

- [ ] Create `web/elm/src/Concourse/AgentDiff.elm`:
```elm
module Concourse.AgentDiff exposing
    ( DiffFile
    , DiffPage
    , LineKind(..)
    , classifyLine
    , decodeDiffPage
    )

{-| Wire types + decoder for GET /api/v1/agent/tickets/:id/diff (the §1.11.1
file-windowed diff). Each `DiffFile.patch` is raw unified-diff text; the render
layer splits it into lines and asks `classifyLine` how to color each one.
-}

import Json.Decode
import Json.Decode.Extra exposing (andMap)


type alias DiffFile =
    { path : String
    , patch : String
    , truncated : Bool
    }


type alias DiffPage =
    { files : List DiffFile
    , offset : Int
    , limit : Int
    , totalFiles : Int
    , hasMore : Bool
    }


{-| The kind of one unified-diff line, used purely to pick a color.

  - `Addition` — a `+` line (but not the `+++` file header).
  - `Deletion` — a `-` line (but not the `---` file header).
  - `HunkHeader` — an `@@ ... @@` locator.
  - `Meta` — `diff `/`index `/`+++ `/`--- `/`new file`/`deleted file` framing.
  - `Context` — everything else (unchanged lines, blank lines).

-}
type LineKind
    = Addition
    | Deletion
    | HunkHeader
    | Meta
    | Context


classifyLine : String -> LineKind
classifyLine line =
    if String.startsWith "@@" line then
        HunkHeader

    else if String.startsWith "+++" line || String.startsWith "---" line then
        Meta

    else if
        String.startsWith "diff " line
            || String.startsWith "index " line
            || String.startsWith "new file" line
            || String.startsWith "deleted file" line
            || String.startsWith "similarity index" line
            || String.startsWith "rename " line
    then
        Meta

    else if String.startsWith "+" line then
        Addition

    else if String.startsWith "-" line then
        Deletion

    else
        Context


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo fallback decoder =
    Json.Decode.oneOf [ decoder, Json.Decode.succeed fallback ]


decodeDiffFile : Json.Decode.Decoder DiffFile
decodeDiffFile =
    Json.Decode.succeed DiffFile
        |> andMap (defaultTo "" <| Json.Decode.field "path" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "patch" Json.Decode.string)
        |> andMap (defaultTo False <| Json.Decode.field "truncated" Json.Decode.bool)


decodeDiffPage : Json.Decode.Decoder DiffPage
decodeDiffPage =
    Json.Decode.succeed DiffPage
        |> andMap (defaultTo [] <| Json.Decode.field "files" (Json.Decode.list decodeDiffFile))
        |> andMap (defaultTo 0 <| Json.Decode.field "offset" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "limit" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "total_files" Json.Decode.int)
        |> andMap (defaultTo False <| Json.Decode.field "has_more" Json.Decode.bool)
```

- [ ] Create `web/elm/tests/AgentDiffTests.elm`:
```elm
module AgentDiffTests exposing (all)

import Concourse.AgentDiff as AgentDiff exposing (LineKind(..))
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "Concourse.AgentDiff"
        [ describe "classifyLine"
            [ test "classifies an addition" <|
                \_ -> AgentDiff.classifyLine "+new code" |> Expect.equal Addition
            , test "classifies a deletion" <|
                \_ -> AgentDiff.classifyLine "-old code" |> Expect.equal Deletion
            , test "treats +++ as meta, not addition" <|
                \_ -> AgentDiff.classifyLine "+++ b/foo.go" |> Expect.equal Meta
            , test "treats --- as meta, not deletion" <|
                \_ -> AgentDiff.classifyLine "--- a/foo.go" |> Expect.equal Meta
            , test "classifies a hunk header" <|
                \_ -> AgentDiff.classifyLine "@@ -1,3 +1,4 @@ func x()" |> Expect.equal HunkHeader
            , test "classifies a context line" <|
                \_ -> AgentDiff.classifyLine " unchanged" |> Expect.equal Context
            ]
        , describe "decodeDiffPage"
            [ test "decodes the DiffPage wire shape" <|
                \_ ->
                    let
                        json =
                            """
                            { "files": [ { "path": "atc/foo.go", "patch": "@@ -1 +1 @@\\n-old\\n+new\\n", "truncated": true } ]
                            , "offset": 0, "limit": 50, "total_files": 1, "has_more": false }
                            """
                    in
                    Json.Decode.decodeString AgentDiff.decodeDiffPage json
                        |> Result.map (\p -> ( List.length p.files, p.totalFiles, List.head p.files |> Maybe.map .truncated ))
                        |> Expect.equal (Ok ( 1, 1, Just True ))
            , test "tolerates a missing files field" <|
                \_ ->
                    Json.Decode.decodeString AgentDiff.decodeDiffPage "{}"
                        |> Result.map (\p -> List.length p.files)
                        |> Expect.equal (Ok 0)
            ]
        ]
```

- [ ] Run it, expect PASS (the module compiles and both decoders/classifier behave):
  ```
  cd /Users/tdmtrader/concourse/concourse/web/elm && elm-test tests/AgentDiffTests.elm 2>&1 | tail -15
  ```
  Expected: `TEST RUN PASSED` with 8 passing tests. (If elm-test insists on running the whole suite, run `elm-test` and confirm no failures.)

- [ ] Commit:
  ```
  git add web/elm/src/Concourse/AgentDiff.elm web/elm/tests/AgentDiffTests.elm
  git commit -m "feat(web): Concourse.AgentDiff wire types, decoder, line classifier"
  ```

---

## Task 4 — Elm `Views.AgentDiff` unified-diff renderer

**Files:**
- Create `web/elm/src/Views/AgentDiff.elm`

The renderer is a pure `DiffPage -> Html msg` (message-free — v1 has no interactions). Its correctness is exercised through the page test in Task 7; a standalone elm-test isn't added because the output is styled `Html` whose value is the visible text, which Task 7 asserts against.

### Steps

- [ ] Create `web/elm/src/Views/AgentDiff.elm`:
```elm
module Views.AgentDiff exposing (view)

{-| Renders a §1.11.1 DiffPage as colored unified-diff hunks. Static (no
interaction) in v1 — one `<div>` per file with a monospace body whose lines are
colored by `Concourse.AgentDiff.classifyLine`. This is the PRIMARY review
surface on the ticket page; the GitHub compare link is demoted to a secondary
affordance in `AgentTicket.provenanceLine`.
-}

import Concourse.AgentDiff as AgentDiff exposing (DiffFile, DiffPage, LineKind(..))
import Html exposing (Html)
import Html.Attributes exposing (class, style)


view : DiffPage -> Html msg
view page =
    Html.div
        [ class "agent-ticket-diff"
        , style "margin" "12px 0"
        , style "border" "1px solid #2b2b2b"
        , style "border-radius" "4px"
        , style "overflow" "hidden"
        ]
        (List.map fileBlock page.files
            ++ (if page.hasMore then
                    [ moreNotice page ]

                else
                    []
               )
        )


fileBlock : DiffFile -> Html msg
fileBlock file =
    Html.div
        [ style "border-top" "1px solid #2b2b2b" ]
        [ Html.div
            [ class "agent-ticket-diff-file-header"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "padding" "6px 10px"
            , style "background" "#1b1b1b"
            , style "color" "#c8d0c8"
            , style "display" "flex"
            , style "justify-content" "space-between"
            ]
            [ Html.span [] [ Html.text file.path ]
            , if file.truncated then
                Html.span [ style "color" "#c0a060" ] [ Html.text "truncated (64 KiB cap)" ]

              else
                Html.text ""
            ]
        , Html.div
            [ style "font-family" "monospace"
            , style "font-size" "12px"
            , style "line-height" "1.45"
            , style "overflow-x" "auto"
            , style "background" "#0f0f0f"
            ]
            (file.patch
                |> String.split "\n"
                |> List.map lineRow
            )
        ]


lineRow : String -> Html msg
lineRow line =
    let
        ( bg, fg ) =
            case AgentDiff.classifyLine line of
                Addition ->
                    ( "#122a12", "#a7d7a7" )

                Deletion ->
                    ( "#2a1212", "#d7a7a7" )

                HunkHeader ->
                    ( "#12203a", "#7a9ac0" )

                Meta ->
                    ( "#0f0f0f", "#7f7f7f" )

                Context ->
                    ( "#0f0f0f", "#b8b8b8" )
    in
    Html.div
        [ style "background" bg
        , style "color" fg
        , style "padding" "0 10px"
        , style "white-space" "pre"
        ]
        [ Html.text
            (if line == "" then
                " "

             else
                line
            )
        ]


moreNotice : DiffPage -> Html msg
moreNotice page =
    Html.div
        [ style "font-family" "monospace"
        , style "font-size" "12px"
        , style "padding" "6px 10px"
        , style "background" "#1b1b1b"
        , style "color" "#9aa39b"
        , style "border-top" "1px solid #2b2b2b"
        ]
        [ Html.text
            (String.fromInt (List.length page.files)
                ++ " of "
                ++ String.fromInt page.totalFiles
                ++ " files shown — open the full diff on GitHub for the rest."
            )
        ]
```

- [ ] Compile-check the module (elm-test compiles the whole src tree; a passing suite proves it builds):
  ```
  cd /Users/tdmtrader/concourse/concourse/web/elm && elm-test tests/AgentDiffTests.elm 2>&1 | tail -8
  ```
  Expected: still `TEST RUN PASSED` (no compile error introduced by the new module).

- [ ] Commit:
  ```
  git add web/elm/src/Views/AgentDiff.elm
  git commit -m "feat(web): Views.AgentDiff unified-diff hunk renderer"
  ```

---

## Task 5 — Endpoints + Effects + Callback wiring

**Files:**
- Modify `web/elm/src/Api/Endpoints.elm`
- Modify `web/elm/src/Message/Effects.elm`
- Modify `web/elm/src/Message/Callback.elm`

### Steps

- [ ] Add the endpoint constructor to `web/elm/src/Api/Endpoints.elm`. In the `AgentEndpoint` custom type (~line 46-51), after `AgentTicketMetrics Int`:
```elm
    | AgentTicketDiff Int
```
  and in the `agentEndpoint` case mapping (after the `AgentTicketMetrics ticketId ->` branch, ~line 260-261):
```elm
        AgentTicketDiff ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "diff" ]
```

- [ ] Add the callback constructor to `web/elm/src/Message/Callback.elm`. Add the import at the top (near the other `Concourse.*` imports):
```elm
import Concourse.AgentDiff
```
  and after `AgentTicketMetricsFetched Int (Fetched (List Concourse.Agent.RunMetric))` (~line 89):
```elm
    | AgentTicketDiffFetched (Fetched Concourse.AgentDiff.DiffPage)
```

- [ ] Add the effect constructor + handler to `web/elm/src/Message/Effects.elm`. Add the import near the other `Concourse.*` imports:
```elm
import Concourse.AgentDiff
```
  add the effect variant after `FetchAgentTicketMetrics Int` (~line 249):
```elm
    | FetchAgentTicketDiff Int
```
  and add the `runEffect` branch after the `FetchAgentTicketMetrics ticketId ->` branch (~line 934-938):
```elm
        FetchAgentTicketDiff ticketId ->
            Api.get (Endpoints.AgentTicketDiff ticketId)
                |> Api.expectJson Concourse.AgentDiff.decodeDiffPage
                |> Api.request
                |> Task.attempt AgentTicketDiffFetched
```

- [ ] Confirm the exact four-stage idiom. Read the existing `FetchAgentTicketMetrics ticketId ->` branch (`web/elm/src/Message/Effects.elm:934-938`), which is `Api.get |> Api.expectJson |> Api.request |> Task.attempt (AgentTicketMetricsFetched ticketId)`. Note the `|> Api.request` stage is mandatory: `Api.expectJson` returns a **request builder**, not a `Task`; `Api.request` runs it into the `Task x (Result ...)` that `Task.attempt` maps to a `Fetched`. Omitting `Api.request` gives `Task.attempt` a request builder and will not typecheck. The only difference from the metrics branch is the constructor arity — `AgentTicketMetricsFetched` takes the id then the `Fetched`, whereas `AgentTicketDiffFetched` takes only the `Fetched`, so it is passed bare: `|> Task.attempt AgentTicketDiffFetched`. There is no `Callback.wrap` helper in `src/Message/Callback.elm`; the metrics callback is applied bare, and this branch matches it.

- [ ] Compile-check via elm-test (the whole tree must still compile):
  ```
  cd /Users/tdmtrader/concourse/concourse/web/elm && elm-test tests/AgentDiffTests.elm 2>&1 | tail -8
  ```
  Expected: `TEST RUN PASSED` — a green run proves Endpoints/Effects/Callback compile together. If the `Task.attempt` wrapper is wrong you get a type mismatch here; fix per the metrics-branch shape.

- [ ] Commit:
  ```
  git add web/elm/src/Api/Endpoints.elm web/elm/src/Message/Effects.elm web/elm/src/Message/Callback.elm
  git commit -m "feat(web): wire AgentTicketDiff endpoint, effect, and callback"
  ```

---

## Task 6 — AgentTicket page: fetch, store, render, demote compare link

**Files:**
- Modify `web/elm/src/AgentTickets/AgentTicket.elm`

### Steps

- [ ] Add the imports (near the existing `import Concourse.AgentTicket as AgentTicket`):
```elm
import Concourse.AgentDiff
import Views.AgentDiff
```

- [ ] Add the model fields. In the `type alias Model` record (~line 60-88), after `actionError : Maybe String`:
```elm
        , diff : Maybe Concourse.AgentDiff.DiffPage
        , diffLoadError : Bool
```

- [ ] Seed them in `init` (~line 93-117), after `actionError = Nothing`:
```elm
      , diff = Nothing
      , diffLoadError = False
```
  and add the fetch to the init effects list (~line 118):
```elm
    , [ FetchAgentTicket id, FetchAgentTicketMetrics id, FetchAgentTicketDiff id ]
```

- [ ] Handle the callback. In `handleCallback` (~line 132), add two branches immediately after the `AgentTicketMetricsFetched _ (Err _) ->` branch (~line 230-231):
```elm
        AgentTicketDiffFetched (Ok fresh) ->
            let
                -- Reference-preserve so the lazy views below aren't defeated by
                -- an equal-but-fresh record installed on every 5s self-heal.
                page =
                    case model.diff of
                        Just old ->
                            if old == fresh then
                                old

                            else
                                fresh

                        Nothing ->
                            fresh
            in
            ( { model | diff = Just page, diffLoadError = False }, effects )

        AgentTicketDiffFetched (Err _) ->
            -- No diff yet (404 before harvest pushes base/pushed shas) or a
            -- transient error: keep it off-screen and leave the GitHub compare
            -- link as the fallback. Don't surface a red banner for the common
            -- "diff not ready" case.
            ( { model | diff = Nothing, diffLoadError = True }, effects )
```

- [ ] Re-fetch the diff on the self-heal poll. In `polls` (~line 287-304), change the non-terminal branch's effect list to also fetch the diff:
```elm
                if settled then
                    []

                else
                    [ FetchAgentTicket model.ticketId
                    , FetchAgentTicketMetrics model.ticketId
                    , FetchAgentTicketDiff model.ticketId
                    ]
```
  Also fetch after a fresh dispatch so the diff appears once the run harvests. In the `AgentTicketDispatched _ (Ok _) ->` branch (~line 251-254) append `FetchAgentTicketDiff model.ticketId` to the effects list:
```elm
        AgentTicketDispatched _ (Ok _) ->
            ( { model | actionError = Nothing }
            , effects ++ [ FetchAgentTicket model.ticketId, FetchAgentTicketMetrics model.ticketId, FetchAgentTicketDiff model.ticketId ]
            )
```

- [ ] Add the `diffSection` render helper. Add this function near `provenanceLine` (~after line 617):
```elm
{-| The in-app diff — the PRIMARY review surface (S-4). Renders only when the
server returned a windowed diff with at least one file; otherwise nothing shows
and the demoted GitHub compare link in `provenanceLine` remains the fallback.
-}
diffSection : Model -> Html Message
diffSection model =
    case model.diff of
        Just page ->
            if List.isEmpty page.files then
                Html.text ""

            else
                Html.div [ id "ticket-diff" ]
                    [ Html.div
                        [ style "font-size" "13px"
                        , style "color" "#c8d0c8"
                        , style "margin" "10px 0 2px 0"
                        ]
                        [ Html.text "Diff vs base" ]
                    , Views.AgentDiff.view page
                    ]

        Nothing ->
            Html.text ""
```

- [ ] Place `diffSection` in the page composition. In `content` (~line 492-538), add it to `top` so it renders high on the page for every state that has a diff. Change the `top` binding (~line 500-506) to append the diff after the provenance block:
```elm
                    top =
                        [ header model ticket
                        , provenanceLine ticket
                        , provenanceTimestamps session.timeZone ticket
                        , errorNotice ticket
                        , actionErrorBanner model
                        , diffSection model
                        ]
```

- [ ] Demote the GitHub compare link in `provenanceLine` (~line 594-608). Keep the `class "agent-ticket-compare-link"` and the same `href url` (the existing tests assert both), but relabel and mute it so the in-app diff reads as primary. Replace the `Just url ->` case body:
```elm
                    case AgentTicket.compareUrl ticket of
                        Just url ->
                            [ Html.text (" · branch " ++ ticket.branch ++ " — ")
                            , Html.a
                                (class "agent-ticket-compare-link"
                                    :: href url
                                    :: style "font-size" "11px"
                                    :: linkStyle
                                )
                                [ Html.text "compare on GitHub ↗" ]
                            ]

                        Nothing ->
                            [ Html.text (" · branch " ++ ticket.branch) ]
```

- [ ] Verify no compile break via a focused elm-test run before adding the page test:
  ```
  cd /Users/tdmtrader/concourse/concourse/web/elm && elm-test tests/AgentDiffTests.elm 2>&1 | tail -8
  ```
  Expected: `TEST RUN PASSED`.

- [ ] Commit:
  ```
  git add web/elm/src/AgentTickets/AgentTicket.elm
  git commit -m "feat(web): render in-app ticket diff, demote GitHub compare link"
  ```

---

## Task 7 — Page test for the rendered diff

**Files:**
- Modify `web/elm/tests/AgentTicketPageTests.elm`

### Steps

- [ ] Read the top of `web/elm/tests/AgentTicketPageTests.elm` to reuse its existing helpers (`withDetail`, `renderWith`, `sampleDetailJson`, and how it dispatches a `Callback` through `Application.handleCallback`). Then add a test that feeds a `AgentTicketDiffFetched` callback and asserts the diff text renders. Add inside the top-level `describe`:
```elm
        , test "renders the in-app unified diff when the diff endpoint returns one" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketDiffFetched
                                    (Ok
                                        { files =
                                            [ { path = "atc/foo.go"
                                              , patch = "@@ -1 +1 @@\n-old line\n+new line\n"
                                              , truncated = False
                                              }
                                            ]
                                        , offset = 0
                                        , limit = 50
                                        , totalFiles = 1
                                        , hasMore = False
                                        }
                                    )
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.find [ id "ticket-diff" ]
                            |> Query.has
                                [ containing [ text "atc/foo.go" ]
                                , containing [ text "+new line" ]
                                , containing [ text "-old line" ]
                                ]
                    )
```

- [ ] Confirm the helper names. If `Common.queryView` / `Common.init` are not the exact names in this test module, read the imports (lines 1-20) and the other tests (e.g. the "run history rows link to their build" test at ~line 186 uses `Common.init` + `Application.handleCallback` + `Tuple.first`) and match their idiom precisely. The `AgentTicketDiffFetched (Ok {...})` record literal must match `Concourse.AgentDiff.DiffPage` field-for-field (`files`, `offset`, `limit`, `totalFiles`, `hasMore`).

- [ ] Run the page test suite, expect PASS:
  ```
  cd /Users/tdmtrader/concourse/concourse/web/elm && elm-test tests/AgentTicketPageTests.elm 2>&1 | tail -15
  ```
  Expected: `TEST RUN PASSED`, including the two pre-existing `agent-ticket-compare-link` tests (still green because the class + href were preserved) and the new diff test.

- [ ] Run the full Elm suite to catch any cross-module break:
  ```
  cd /Users/tdmtrader/concourse/concourse/web/elm && elm-test 2>&1 | tail -15
  ```
  Expected: `TEST RUN PASSED`.

- [ ] Commit:
  ```
  git add web/elm/tests/AgentTicketPageTests.elm
  git commit -m "test(web): assert in-app ticket diff renders"
  ```

---

## Task 8 — Rebuild the embedded Elm bundle

**Files:**
- Rebuild `web/public/elm.min.js`

The served UI is `web/public/elm.min.js`. Every Elm source change above is invisible in the deployed web until this bundle is rebuilt and committed — the known stale-bundle trap. There is **no local elm-build gate today** (WF-2 adds one), so this step is manual and mandatory.

### Steps

- [ ] Rebuild the bundle:
  ```
  cd /Users/tdmtrader/concourse/concourse && ./hack/build-web.sh 2>&1 | tail -5
  ```
  Expected: `built web/public/elm.min.js (<N> bytes)` with no `elm make` compile error. (Requires `elm` 0.19.1 and `uglifyjs` on PATH — install `npm i -g uglify-js` if missing.)

- [ ] Confirm the bundle changed:
  ```
  cd /Users/tdmtrader/concourse/concourse && git status --short web/public/elm.min.js
  ```
  Expected: ` M web/public/elm.min.js`.

- [ ] Commit:
  ```
  git add web/public/elm.min.js
  git commit -m "build(web): rebuild elm.min.js for in-app diff review"
  ```

---

## Self-Review

**Spec coverage:**
- "Render the diff ON the ticket page as the PRIMARY review surface — colored unified-diff hunks" → `Views.AgentDiff.view` + `diffSection` in `AgentTicket.content.top` (Tasks 4, 6).
- "Demote the GitHub compare to a secondary link" → `provenanceLine` relabel to "compare on GitHub ↗", muted 11px, class/href preserved (Task 6).
- "FIRST read its handler to learn the exact response shape" → done; shape is `gitcheck.DiffPage` with `files[].patch` raw unified text (grounding facts).
- "If the go-concourse client method or fly plumbing is missing, include adding it (six-touchpoint, read-only)" → Task 1 (client + fake) and Task 2 (fly). Routes/wrappa/handler/registration already exist (verified) so those four touchpoints need **no change** — explicitly stated.
- "Elm: a unified-diff renderer component" → `Views.AgentDiff` (Task 4) + `Concourse.AgentDiff.classifyLine` (Task 3).
- "note the elm.min.js rebuild" → Task 8.

**Placeholder scan:** No `TODO`/`TBD`/"handle edge cases"/"similar to Task N". Every code step contains real code. Task 5 ships the exact four-stage effect body (`Api.get |> Api.expectJson |> Api.request |> Task.attempt AgentTicketDiffFetched`) pinned verbatim against `Effects.elm:934-938` — no wrapper is left open. The spots that say "confirm the exact idiom" (fly target-resolution in Task 2; test-helper names in Task 7) are verification steps against named source lines, not placeholders — each ships concrete code that the reader then reconciles with the cited existing pattern, because the fly `rc.LoadTarget` accessor and the test-helper names could not be 100% pinned from grep alone.

**Type consistency:** `gitcheck.DiffPage` (Go) ⇄ JSON (`files/offset/limit/total_files/has_more`, `path/patch/truncated`) ⇄ `Concourse.AgentDiff.DiffPage` (Elm: `files/offset/limit/totalFiles/hasMore`). Client returns `(gitcheck.DiffPage, bool, error)` matching the `GetAgentTicketOutcome` 404 convention. Elm effect decodes with `decodeDiffPage`; callback carries `Fetched Concourse.AgentDiff.DiffPage`; the page-test record literal matches the alias field-for-field.

**Coordination:** No `render.go` edit. No migration. No wrappa edit (diff auth already team-less). The counterfeiter fake is regenerated, not hand-written. Wire change is additive/back-compat (new endpoint already served; new client/fly/Elm are all read-only consumers).

---

## Open Decisions

1. **Paging / large diffs (has_more).** v1 fetches only the default 50-file window and shows a "N of M files shown — open on GitHub for the rest" notice; it does not implement in-app "load more". **Recommendation:** ship the single-window v1 (covers the overwhelming majority of agent PRs, which are small), and defer client-side paging to a follow-up only if real tickets routinely exceed 50 changed files. The `offset`/`limit` params are already plumbed through the client and fly command, so a future "load more" button is additive.

2. **Diff-fetch cadence vs. git cost.** The plan re-fetches the diff on the same 5s self-heal poll as ticket detail while the ticket is non-terminal; each fetch runs `git diff` in the ATC-side mirror. **Recommendation:** accept the 5s cadence for parity with the existing detail/metrics polling (the mirror is local and the window is capped), but if load becomes a concern, gate the diff poll on `ticket.state == "needs_review"` (the only state where a reviewer is actually looking) — a one-line change in `polls`. Flagged for a human owner because it trades freshness against ATC git load.

3. **Syntax highlighting.** v1 colors only by diff line-kind (add/del/hunk/meta/context), not by language syntax. **Recommendation:** keep it kind-only — language-aware highlighting is a large dependency and a separate effort; the add/del coloring is what reviewers need to read a patch.
