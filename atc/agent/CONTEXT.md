# Agentic

The surface through which agents drive JetBridge: MCP operations, the grants
that authorize them, and composition, where one build asks for a child
pipeline run. It is a layer over [Core](../CONTEXT.md), not part of it. It
reaches core only through the run admission port and the wrapped API, and
core never reaches it; the web composition root is the one place the two are
wired together. A test pins every package here to the core packages it may
import.

Member packages: `atc/mcp`, `atc/api/mcpserver`, `atc/agent/composition`,
`internal/mcpclient`, `cmd/jb-mcp-client`, and `skymarshal/mcpauth`; and the
local workload side: `agent/capture`, `agent/session` (with its test support
`agent/session/codextest`), `agent/runclient`,
`agent/review`, `agent/implement` (each with its `client`), `cmd/jb` and
`cmd/jb-review-worker`.

The word *agent* is reserved for this context and banned from the other
three.

## Language

### MCP surface

**Operation**:
One application action exposed over MCP: an id, a resource, the API action it
maps to, its scope and its schemas.

**Grouped tool**:
An MCP tool per resource kind that takes an operation name and arguments,
rather than one tool per action. Only kinds with executable operations get a
tool.

**Scope**:
The capability class an MCP grant carries and an operation requires (`read`,
`pipelines:write`, `builds:write`, `hijack`, `admin`). A scope restricts team
permissions and never adds a role.

**MCP grant**:
The user-approved, revocable authorization an MCP client holds, separate
from ordinary login. A user consents; what they hold afterwards is a grant.
_Avoid_: consent (the act, not the thing), grant (alone; Hangar's capability
is a warrant)

**Account eligibility**:
A target-free pre-check proving only that an account could never be
authorized. It is never itself an authorization.

**Disabled operation**:
An operation an operator has withheld from every caller regardless of
any grant.

### Composition

**Composition call**:
One step of one build asking for a child pipeline run, identified by build
id and plan id. Its identity is the contract key core's run admission port
replays on; the call row is a join to the run the port admitted, never a
second dedup.
_Avoid_: node

**Composition iteration**:
One admission of a child run under a composition call, numbered from the
first. A call replays its latest iteration rather than adding one.
_Avoid_: ordinal (the number, not the thing)

**Child run**:
The pipeline run a composition iteration admitted.

**Replayed**:
A composition call that re-attached to the child run it already admitted
rather than admitting a second one.

**Input digest**:
The sealed inputs of a composition call, recorded on first admission and
verified on every later one. Never part of the call identity.

### Workloads

**Workload**:
One kind of work an agent submits to the platform as a pipeline run from an
installed template: *review* or *implement*. A workload fixes its sealed
input, the result whose producer receives credentials, and the typed result
it trusts on read-back.
_Avoid_: detached workload, detached Run (core's *detached build* is a
reclaimed run's orphan build)

**Run client**:
The workload-neutral client in `agent/runclient`: it uploads a workload's
sealed input, admits its Run through the public API, hands off credentials
and verifies a published result before parsing it. Human CLI and MCP tools
share it.
_Avoid_: detached client

**Invocation receipt**:
The local file that binds one submission to its Run, so an interrupted
caller resumes that Run instead of admitting a second one.
_Avoid_: receipt (alone; Hangar has two)

**Provider session**:
One run of the pinned model provider inside a worker, in a private
memory-backed runtime that holds the owner's credential and is destroyed
before anything is published.

**Snapshot**:
The sealed input of an implement Run: a base commit's tree, a brief and,
when iterating, a prior change and review findings.
_Avoid_: bundle (review's input)

**Change**:
What an implement Run publishes: a patch against the snapshot's base
commit and a summary binding it to the Run.
_Avoid_: diff (review's captured input), result (alone)

**Validation**:
A template's record of running its checks against exactly one change. A
failed validation is a result, not a failed Run.

