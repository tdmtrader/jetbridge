# Agentic

The surface through which agents drive JetBridge: MCP operations, the grants
that authorize them, and composition, where one build asks for a child
pipeline run. It is a layer over [Core](../CONTEXT.md), not part of it. It
reaches core only through the run admission port and the wrapped API, and
core never reaches it; the web composition root is the one place the two are
wired together. A test pins every package here to the core packages it may
import.

Member packages: `atc/mcp`, `atc/api/mcpserver`, `atc/agent/composition`,
`internal/mcpclient`, `cmd/jb-mcp-client`, and `skymarshal/mcpauth`.

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
id and plan id and deduplicated by that identity alone.
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
