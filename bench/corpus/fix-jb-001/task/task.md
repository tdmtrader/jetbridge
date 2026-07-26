# dev-mcp sidecar dies outright when a tool misbehaves

**Type:** bug / small fix
**Component:** `ci-agent` — dev-mcp sidecar
**Reported:** 2026-07-11

## Symptom

If a registered dev-mcp tool's handler blows up mid-call, we don't lose that one
call — we lose the whole sidecar. The `dev-mcp` process exits, and every
subsequent MCP request from the agent gets connection-refused for the rest of
the pod's life.

That is the wrong blast radius by a long way. dev-mcp is a long-lived sidecar
serving an agent that is in the middle of a step; agent pods are
`RestartPolicyNever`, so there is no "it comes back". One bad tool invocation
takes the step with it, and what the agent sees is not "that tool failed" but a
dead transport with no explanation — which it then has no way to report or work
around.

## Expected behavior

A tool handler that fails catastrophically should be contained to its own call:

1. The client gets a well-formed JSON-RPC error response for **that request**,
   carrying the request's id, and enough of the failure in the message to be
   diagnosable from the agent transcript.
2. The error code follows the taxonomy already fixed for this server in
   `docs/superpowers/plans/agentic-platform/04-dev-mcp.md` (§ "JSON-RPC error
   codes") — in particular, do not reuse the malformed-input code for something
   that is not malformed input.
3. The server stays up. A plain `ping` immediately afterwards must still be
   answered normally, and later tool calls must still work.

## Constraints

- Do not change the exported `ToolHandler` signature, and do not weaken the
  frozen contract that a **returned error means malformed input only** — run
  outcomes, including tooling breakage, stay in the returned payload's `status`
  field. This is about a handler that never returns at all.
- Containment must not disturb the happy path: successful tool calls, the
  progress-notification stream and its heartbeat cadence all keep behaving
  exactly as they do today.
- No new dependencies. This module (`github.com/concourse/ci-agent`) is
  deliberately standalone and cannot import the main module.
- Add a regression test in the existing `devmcp` Ginkgo suite that would have
  caught this. It has to pin both halves of the expectation: the error response
  for the failed call, **and** that the server is still serving afterwards.

## Notes

The five contract tools we ship today are all well-behaved as far as we know —
this came out of hardening review of the new server, not from a specific tool
misfiring in production. Treat it as a robustness gap in the server itself, not
as a bug in any one tool: the server is the thing that must survive its
handlers.
