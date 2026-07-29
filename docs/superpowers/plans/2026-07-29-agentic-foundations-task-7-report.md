# Task 7 — Exact current validation gate

## Scope and result

The governed schema-v3 consumers now name a validation artifact in source and
receive a consumer-specific, private rendered requirement. The private
requirement deep-clones the existing `DevValidationAuthority`, including its
frozen profile, protected configuration, pinned image, toolchain, definition
identity, version, candidate input, and base-input declarations.

Typed-flow checks require the candidate and validation to be guaranteed,
unambiguous authoritative outputs. They reject a validation produced for an
earlier candidate or earlier base binding. Runtime reopens the exact named
candidate, bases, and validation through the build repository and authorized
team metadata; it bounds and verifies the validation archive, decodes the
canonical record, requires revision 3 and `passed`, and compares every
attestation field to the private authority before selecting a review worker,
creating/sealing a merge-approval question, or calling the publisher executor.

The three v3 seeds now validate after their final mutation/rebase and before
their governed consumer. Existing merge-preflight behavior remains separate.

## Review round 1 blocking fixes

- Small-fix and version-upgrade now expose the already validated `candidate`
  as their public/disposition `change`; the former post-validation
  `enforce-approval` repository-change producer was removed. Seed coverage
  proves there is no later repository-change producer.
- Runtime validation now validates a private consumer requirement before it
  reads base declarations, so a direct malformed plan with a nil authority
  fails closed instead of panicking the controller.

The bounded round-2 review confirmed both blockers are closed and found no new
Critical, High, or acceptance-blocking issue. Task 7 is **Accepted**.

## Verification

- `go test ./agent/workflow ./atc/exec ./atc/engine -count=1` — passed after
  the round-1 fixes.
- `go test ./atc/db ./atc/builds -run '^$' -count=1` — passed
  (compile-only).
- `git diff --check` — passed.

The focused selected-build DB specification ran zero specs: the sandboxed
attempt could not allocate PostgreSQL shared memory, and the one authorized
host retry found port 5434 already occupied before its BeforeSuite. Per the
bounded-infrastructure policy it was not repeated. The query path was reviewed
directly and both affected DB packages compiled.

## Review

- Round 1 found one High issue: small-fix and version-upgrade could publish a
  repository-change produced after the validated candidate. It also confirmed
  the controller's independently found malformed-private-plan nil-authority
  panic.
- The correction commit makes the public change the exact validated candidate,
  removes the later repository-change producer, and validates the private
  requirement before traversing its bases.
- Round 2 passed with no Critical, High, or acceptance-blocking finding.

## Deferred

No nonblocking Task 7 items were recorded.
