# JetBridge action surface audit

Audited 2026-09-12 against working tree based on `b41dfe01c1`. This is a source
audit, not a live deployment or protocol-conformance test. Existing unrelated
working-tree changes were left untouched.

## Current position

JetBridge is primarily API-driven: `fly` and the Elm UI call the HTTP API. It
does **not currently expose a public JetBridge MCP endpoint**. There is also no
single operation catalog enforcing CLI/API/MCP coverage.

The central API registry contains **100 active method/path entries**
(`atc/routes.go:130`); `FlyCommand` declares **65 named commands**
(`fly/commands/fly.go:7`). These are registry counts, not a parity percentage:
some routes are infrastructure endpoints, some commands are local utilities,
and commands such as `execute` combine several API operations.

| Surface | Current implementation | Evidence |
| --- | --- | --- |
| HTTP API | Named routes, scoped handlers, per-action role checks, policy checks and audit hooks | `atc/routes.go:130`, `atc/api/handler.go:121`, `atc/atccmd/command.go:2354` |
| Go client | Typed `Client` and `Team` interfaces consumed by fly; broad coverage but no completeness guarantee | `go-concourse/concourse/client.go:12`, `go-concourse/concourse/team.go:10` |
| Fly CLI | Typed human-facing commands plus authenticated `fly curl` for arbitrary API requests | `fly/commands/fly.go:7`, `fly/commands/curl.go:21` |
| Web UI | Independently maintained Elm endpoint types and effects calling HTTP | `web/elm/src/Api/Endpoints.elm:18`, `web/elm/src/Api.elm:36`, `web/elm/src/Message/Effects.elm:236` |
| Public MCP | Unregistered transport scaffold; no production tools or route | `atc/api/mcpserver/server.go:23`, `architecture_test.go:60`, `architecture_test.go:263` |
| Review tooling | Separate local `jb review capture/render/schema` CLI; private worker MCP exposes only bundle `list/read/search` | `cmd/jb/main.go:28`, `agent/review/reader.go:194` |

### The prior MCP implementation matters

The public MCP route was deliberately removed. The release notes record that it
authenticated requests but trusted the team supplied in the JSON body; an
authenticated user could mutate other teams and abort builds by ID
(`docs/releases/jb-0.3.1.md:134`). The present source confirms the removal:
there is no MCP route in `atc.Routes`, no MCP construction in `api.NewHandler`,
and all remaining `AddTool` calls are transport tests.

`architecture_test.go:263` pins the residual MCP package to **zero internal
Concourse imports**, explicitly instructing future tools to compose with core
handlers instead of calling the database. Preserve that boundary.

The retained transport advertises protocol `2024-11-05`, supports POST JSON-RPC,
and defines only tool name/description/input schema plus text results
(`atc/api/mcpserver/server.go:11`, `atc/api/mcpserver/server.go:42`,
`atc/api/mcpserver/protocol.go:49`). Its existence is not evidence of current
Streamable HTTP or OAuth interoperability.

The review worker's separate MCP is a private stdio input reader, not a public
JetBridge control plane. Remote review submission/status/results and public MCP
are explicitly future work (`agent/review/README.md:3`,
`agent/review/reader.go:200`). The workspace `.mcp.json` configures an external
development tool; it does not publish JetBridge operations.

## Representative parity and verified gaps

This table samples representative capabilities. Registry inspections are complete;
the table is not an exhaustive behavior-level audit of every route. Every public
MCP entry is absent today. “Via curl” means API reachability through the CLI,
not a dedicated typed command.

| Capability | API | Dedicated CLI | UI | Evidence |
| --- | --- | --- | --- | --- |
| Trigger/rerun/abort builds | Yes | Yes | Yes | `atc/routes.go:141`, `atc/routes.go:151`, `fly/commands/fly.go:84`, `web/elm/src/Message/Effects.elm:463` |
| Pause/unpause jobs and pipelines | Yes | Yes | Yes | `atc/routes.go:155`, `atc/routes.go:170`, `fly/commands/fly.go:45`, `web/elm/src/Api/Endpoints.elm:39` |
| Create/update pipeline configuration | `SaveConfig` | `set-pipeline` | No configuration-editor endpoint/effect found | `atc/routes.go:131`, `fly/commands/fly.go:53`, `web/elm/src/Api/Endpoints.elm:39` |
| Create/list numbered pipeline runs | Yes | `run-pipeline`, `runs` | Yes | `atc/routes.go:164`, `fly/commands/fly.go:64`, `web/elm/src/Message/Effects.elm:236` |
| Fetch one numbered run | Yes; typed Go client too | No dedicated detail command; via curl | Yes | `atc/routes.go:166`, `go-concourse/concourse/pipeline_runs.go:60`, `fly/commands/runs.go:26`, `web/elm/src/Message/Effects.elm:239` |
| Set a build comment | Yes; typed Go client too | No registered comment command or production fly caller; via curl | Yes | `atc/routes.go:144`, `go-concourse/concourse/builds.go:77`, `fly/commands/fly.go:7`, `web/elm/src/Message/Effects.elm:471` |
| Inspect resource-version upstream/downstream causality | Yes | No dedicated command; via curl | Yes | `atc/routes.go:207`, `fly/commands/fly.go:67`, `web/elm/src/Message/Effects.elm:421` |
| Change server log level | Yes | No dedicated command; via curl | No endpoint/effect found | `atc/routes.go:218`, `fly/commands/fly.go:7`, `web/elm/src/Api/Endpoints.elm:18` |
| Validate/format pipeline without saving | No standalone API route | Local commands | No endpoint/effect found | `fly/commands/validate_pipeline.go:34`, `fly/commands/format_pipeline.go:18`, `atc/routes.go:130` |
| Capture/render a review bundle and retrieve its schema | No public API | Local `jb review` commands | No endpoint/effect found | `cmd/jb/main.go:28`, `agent/review/README.md:3` |

Two qualifications affect the desired parity rule:

- `fly curl` provides a useful API escape hatch and already makes many otherwise
  missing operations callable from the CLI. It does not supply typed discovery,
  argument validation, stable structured output or convenient operation names.
- Local workflows need explicit semantics when exposed remotely. For example,
  `format-pipeline --write` mutates a local file; a server equivalent would accept
  config text and return formatted text. `fly execute` prepares local artifacts
  and a build plan before calling `CreateBuild` or `CreatePipelineBuild`
  (`fly/commands/execute.go:94`). Remote parity should cover the operation and
  its artifact contract, not implicitly grant access to the caller's filesystem.

There is visible registry drift: Elm still declares an `AgentFeedback` endpoint
at `/api/v1/agent/feedback`, although that backend route was removed
(`web/elm/src/Api/Endpoints.elm:201`, `docs/releases/jb-0.3.1.md:134`). The scan
found no active Elm effect using it, so this is a stale declaration, not proof
of a currently broken user flow.

## Shared execution and authorization boundary

The reliable common execution surface today is the **fully wrapped API**. Many
individual handlers rely on their wrappers for authorization. For example,
`AbortBuild` directly marks the resolved build aborted
(`atc/api/buildserver/abort.go:9`), while its wrapper authenticates the caller,
loads the actual build, and checks its associated teams
(`atc/api/auth/check_build_write_access_handler.go:45`). Calling the bare
handler or database method would skip those protections.

The API composition includes external policy checks, authorization, archived
pipeline restrictions, role-aware identity construction and auditing
(`atc/atccmd/command.go:2354`, `atc/api/accessor/handler.go:47`). An MCP adapter
must preserve those decisions for each underlying operation, including custom
roles. Authenticating once at `/mcp` is insufficient.

Recommended progression:

1. Add MCP as an adapter over the existing API contract using a properly scoped
   authenticated client or the same wrapped request-dispatch boundary. Keep
   transport code free of database/business rules. Do not forward a token to a
   different audience without an explicit authorization design.
2. Define one operation catalog with stable IDs, argument/result schemas,
   authorization requirements and surface mappings. Generate or validate CLI,
   API and MCP registration from it. Keep the operation identity stable when
   MCP tools group several closely related operations under parameters.
3. Extract transport-independent application services incrementally where shared
   workflows warrant it. Services should own authorization and domain behavior;
   HTTP and MCP should adapt the same result/error contracts. Moving only the
   database call into a service would leave the existing drift risk intact.

There is an existing narrow precedent: `atc/runs` exposes run admission with a
verified principal and reuses the API's `accessor` role policy
(`atc/runs/runs.go:34`, `atc/runs/authorization.go:48`). It is not a general
operation bus: it admits runs for in-process composition, and the HTTP run
handler still calls `PipelineRunFactory.CreateRun`
(`atc/api/pipelinerunserver/server.go:95`). Use it as a boundary pattern, not as
evidence that every surface already shares one application service.

## Existing guards and missing guarantees

Existing tests ensure every API route is classified for authentication
(`atc/wrappa/api_auth_wrappa_test.go:117`); the wrapper panics on unknown routes
(`atc/wrappa/api_auth_wrappa.go:167`). Architecture tests constrain agentic
imports, and the composition boundary test permits only `atc/runs` to call the
transactional run-admission seam outside `atc/db`
(`architecture_test.go:263`, `composition_boundary_test.go:81`). Individual
features have route/client/CLI/UI tests, such as pipeline runs.

No cross-surface operation coverage guard or shared CLI/API/MCP operation
registry was found. Add a non-empty catalog check that fails on missing
required adapters and stale mappings; avoid asserting a fixed action count.
For representative reads, writes and destructive operations, test the same
principal, target and arguments through each adapter and compare both allowed
behavior and denial behavior. Include cross-team build IDs, viewer/operator
roles, custom-role overrides, archived pipelines, and configured external
policy checks. These directly target the failure that caused the earlier MCP
surface to be removed.

No tests were run for this documentation-only audit. Authentication/token
renewal details and current MCP design guidance are covered in the companion
research rather than re-derived here.
