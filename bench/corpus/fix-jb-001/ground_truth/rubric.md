# Rubric — fix-jb-001

Primary grading is **mechanical** (see `case.yaml#grading`). This checklist is
the behavioral backstop: it is what a judge scores when the mechanical run is
ambiguous, and it is what distinguishes a real fix from a diff that merely
happens to make the withheld spec green. Score intent, not diff similarity —
several shapes of correct fix exist.

## What the change must do

1. **Contain the crash.** A tool handler that panics must no longer terminate
   the dev-mcp process. After such a call the server continues to accept and
   answer requests. This is the load-bearing requirement; everything else is
   secondary.
2. **Cover the goroutine that actually kills the process.** The fatal path is
   the one where the handler is invoked from a goroutine the server spawns
   rather than from the request goroutine — `net/http` recovers panics only in
   the request goroutine, so the spawned one takes the process down. A fix that
   only hardens the in-request invocation does **not** satisfy (1) and must not
   score as correct.
3. **Answer the failed call.** The client receives a JSON-RPC error response for
   that request, echoing the request `id`. Silently dropping the call, hanging
   until the client times out, or closing the stream with no frame all fail.
4. **Use the server-internal error code, not the malformed-input code.** Per
   `04-dev-mcp.md`, `-32602` means malformed input and `-32603` means
   server-internal fault. A panicking handler is `-32603`. Reusing `-32602`
   fails — it would tell the agent its arguments were wrong when they were not.
5. **Surface the panic value.** The error message must carry the recovered value
   (e.g. `fmt.Sprintf("tool handler panicked: %v", rec)`), so the failure is
   diagnosable from the agent transcript. An opaque constant string is a partial
   pass at best.
6. **Add a regression spec** in the existing `ci-agent/devmcp` Ginkgo suite that
   registers a panicking tool and asserts **both** halves: the error frame
   arrives for that call, *and* a follow-up request still succeeds. A spec that
   asserts only the error frame does not pin the property that matters (the
   server surviving) and is a partial pass.

## What the change must not do

- **No signature or contract changes.** `ToolHandler`, `ProgressFunc`,
  `ToolDef`, `NewServer`, `AddTool` keep their exported signatures. The frozen
  rule that a returned `error` means malformed input only (mapped to `-32602`)
  is untouched.
- **No behavior change on the happy path.** Successful calls, the single
  text-content-block result shape, the `notifications/progress` frames, the
  progress-coalescing semantics and the heartbeat cadence are all unchanged.
- **No new dependencies** and no import of the main `concourse` module —
  `github.com/concourse/ci-agent` is deliberately standalone.
- **No process-level workaround.** Catching this by supervising/restarting the
  dev-mcp process, by adding a wrapper that re-execs, or by adding a top-level
  `recover` in `main` is not a fix: the pod is `RestartPolicyNever` and the
  in-flight call still has to be answered. Reject these.
- **No swallowing.** Recovering and then returning a *success* result, or
  recovering and letting the call hang, fails requirement (3).

## Acceptable variation

Any of these are fine if (1)–(6) hold:

- recovering inside the spawned goroutine (what the humans did),
- wrapping every handler once at registration time in `AddTool` so both the
  streaming and non-streaming invocations are covered,
- extracting a small `safeCall`/`invoke` helper used by both call sites.

Covering the non-streaming path *as well* is a superset of the reference change
and should not be penalized.

## Mechanical caveats — read before scoring a red run

The mechanical gate is the primary signal, but it can be wrong in three known
ways. Check these before recording a fail.

- **The overlay destroys the agent's own spec.** Grading restores
  `ci-agent/devmcp/server_test.go` verbatim, so requirement (6) can never be
  judged from the graded tree. Score (6) from the pre-overlay capture of the
  agent's file (see `case.yaml#grading`, caveat 1). A useful check: apply the
  agent's spec alone to the pre_state tree — a real regression spec crashes or
  fails there. One that passes at pre_state does not pin the defect and is at
  best a partial pass on (6).
- **A new agent-authored test file can produce a false fail.** If the agent put
  its spec in a *new* file in package `devmcp_test`, that file survives the
  overlay and may redeclare a helper the restored spec already defines — a
  compile error, not a defective fix. Move agent-added test files aside for the
  fail_to_pass leg, then judge them separately.
- **The withheld spec pins wording the task does not.** It asserts the error
  message contains `panicked`; `task.md` asks only for a message diagnosable
  from the agent transcript and deliberately never names the failure mode. A fix
  whose message reads `tool handler crashed: %v` or `internal error in tool: %v`
  satisfies requirement (5) in full. If that substring is the only failing
  assertion — `-32603`, the echoed `id`, the SSE error frame and the follow-up
  `ping` all holding — score it a **pass with a recorded wording deviation**,
  not an incorrect fix.

## Judging the reasoning, not the citation

`docs/superpowers/plans/agentic-platform/04-dev-mcp.md` is present at pre_state
and `task.md` points at its error-code section, so the taxonomy (`-32602`
malformed input, `-32603` server-internal) is quotable without any analysis, and
the plan also carries a reference listing of the server that includes the fatal
call site — unmarked, and no more informative than the source file sitting in
the same tree. Credit an agent for reasoning that is grounded in the code and
the symptom: identifying that one handler invocation runs on a goroutine the
server spawns, and that `net/http`'s recovery does not reach it. Do not credit
doc quotation on its own — repeating the taxonomy while hardening the wrong call
site (or "hardening" the already-protected in-request one) still fails
requirement (2).

## Reference

`ground_truth/reference.diff` — the merged human change
(`a1af1068c9cc1b231315e676b80f92be7277b31c`): 8 added lines in
`ci-agent/devmcp/server.go` and one added spec in
`ci-agent/devmcp/server_test.go`. Use it as an existence proof of a correct
solution, not as the target to match.
