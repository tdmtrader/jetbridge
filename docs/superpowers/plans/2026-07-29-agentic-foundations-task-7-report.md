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

## Verification

- `go test ./agent/workflow ./atc/exec ./atc/engine -count=1` — passed.
- `go test ./atc/db -run '^$' -count=1` — passed (compile-only).
- `git diff --check` — passed.

The full `./atc/db` suite could not start because another local PostgreSQL
runner already held port 5434; its BeforeSuite failed before any spec ran.
This is an environment conflict, not a Task 7 assertion failure.

## Deferred

No nonblocking Task 7 items were recorded.
