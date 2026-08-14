# Zero Generated and Interaction Mocks

**Status:** Approved design
**Date:** 2026-08-14
**Scope:** The first of two sequential test-foundation projects. This project
removes mocks. A later design will establish and raise product coverage to the
approved global 80% statement target after this refactor has stabilized the
baseline.

## Purpose

JetBridge should be protected by observable behavior rather than tests that
describe its current implementation through call counts, captured arguments,
or injected collaborator failures. The repository has already removed most of
its generated mocks. This project removes the remaining estate and prevents it
from returning without replacing deterministic, protocol-level fixtures with
slow or unreliable infrastructure.

## Definition of zero mocks

The end state has no generated mocks and no project-owned interaction mocks.
A mock is a substitute whose behavior is configured per method or whose main
purpose is to record internal calls for assertions. Examples include
Counterfeiter outputs, `CallCount`, `ArgsForCall`, `Returns`, method stubs, and
tests whose result is only that one collaborator called another.

The following are not mocks under this design and remain available:

- deterministic in-memory models that implement a real boundary's semantics,
  such as client-go's in-memory Kubernetes API and the runtime test model;
- clocks controlled by a test when elapsed time is part of the public behavior;
- `httptest`, GHTTP, and OCI/resource protocol servers exercised over real wire
  formats;
- Concourse's mock-resource image, which is a resource-protocol fixture;
- small channel-gated functions or scripted steps used to produce lifecycle
  and concurrency states that real dependencies cannot produce cheaply and
  deterministically.

Allowed fixtures must be asserted through outputs, persisted state, protocol
messages, or externally observable timing. They must not grow call-count or
argument-spy APIs. A name containing “fake” is not by itself decisive, and
renaming a mock to “stub” does not make it acceptable.

## Current estate

Eight Counterfeiter directives produce eight files in four packages:

| Area | Generated doubles |
| --- | --- |
| API access | `FakeAccess`, `FakeAccessFactory` |
| Database locks | `FakeLockDB` |
| Engine | `FakeCoreStepFactory` |
| Scheduler | `FakeAlgorithm`, `FakeBuildPlanner`, `FakeBuildStarter`, `FakeBuildScheduler` |

The generated files total 2,157 lines. The repository also contains
Counterfeiter tooling and documentation, an orphaned generation entry point,
and `atc/.ignore`, whose `fake_*.go` rule hides every generated fake from an
ordinary ripgrep scan.

The broader fixture estate—client-go fake clientsets, controlled clocks,
in-memory runtime state, and HTTP protocol servers—will be audited for
interaction-style assertions but will not be removed merely because its type
or package uses the word “fake.” Replacing these with K3s, wall-clock sleeps,
or external registries would make the suite slower and less deterministic;
K3s is also not a viable local tier on macOS in this repository.

## Design principles

1. Assert public contracts and persisted outcomes, not collaborator calls.
2. Use real in-process production components and PostgreSQL where the suite
   already provides them.
3. Keep only the smallest deterministic seam for otherwise unproducible
   lifecycle or concurrency states.
4. Do not preserve improbable injected failures solely to retain line
   coverage. Preserve likely failures that users or operators can encounter.
5. Remove an interface only when it exists for test substitution rather than a
   meaningful production boundary.
6. Map behavior to an existing or replacement scenario before deleting an
   interaction test. Deleting redundant implementation assertions is expected;
   silently deleting the only behavioral protection is not.

## Subsystem migrations

### Database locks

Replace the useful fake-backed concurrency spec with two synchronized
goroutines using the real lock factory and PostgreSQL advisory locks. Exactly
one acquisition must succeed; the test must not depend on which goroutine
wins. Remove the spec claiming that separate factories can both acquire the
same lock, because a real database cannot provide that behavior and the spec
only verifies test-local bookkeeping.

`LockDB` has no meaningful alternate production implementation. Use the
concrete database implementation internally and remove `NewTestLockFactory`,
the interface, directive, and generated fake.

### Engine

The builder suite currently mirrors dispatch through constructor call counts,
call order, and captured plans. Retain unsupported-schema, unknown-plan,
metadata, attempt derivation, and nested-retry behavior that has independent
meaning. Before removing each composition assertion, map the behavior to an
existing exec or K8s behavioral scenario; add a behavioral scenario only when
a real coverage gap exists.

Engine lifecycle tests must continue to cover success, a false result,
cancellation, abort, retryable failure, panic recovery, and blocked/released
execution. Supply their existing scripted step through a small
`StepperFactoryFunc` rather than a generated core-factory mock. This function
may capture a plan only where the plan itself is part of the Engine contract;
it must not become a general spy.

Use the concrete core step factory in production composition and remove the
`CoreStepFactory` directive, interface, and generated fake. Its only alternate
implementation is the generated test double, so it is not a production
boundary.

### Scheduler

Use the real algorithm with the existing versions database, seed versions as
needed, and assert resolved inputs, next-input mappings, pending builds, trace
links, and build state. Use the real planner and assert the persisted plan,
including manual-build behavior. Use the concrete build starter and assert its
effects rather than whether it was called.

Drop injected ordinary database, planner, starter, and panic failures that are
both unlikely and observable only through fake configuration. Retain genuine
unplannable configuration and other failures a user can reasonably produce.

Runner duplicate suppression and the in-flight scheduling gauge require a
schedule operation to remain blocked at a precise point. Replace
`BuildScheduler` with a small `ScheduleFunc` and use a channel-gated function
only for those concurrency contracts. Normal scan, advisory-lock, timestamp,
retry, and tracing behavior must use the real Scheduler and be asserted through
database state and metrics.

Remove the four generated scheduler fakes and any interfaces whose only
remaining purpose was substitution.

### API access

Replace the shared fake access factory used across endpoint suites with the
real access factory and existing token, verifier, team, and PostgreSQL
fixtures. Establish reusable request profiles for anonymous, authenticated
team member, admin, and system access. Exercise routes with real bearer tokens
and role membership, then assert status codes, response bodies, mutations, and
team-scoped visibility.

Replace callback-driven per-team authorization with real teams carrying
different memberships. Remove the two remaining spy-only assertions—whether
`IsAdmin` was called and the exact argument sent to `IsAuthorized`—once the
same security contract is protected through the response and visible data.

Make `AccessFactory` concrete because it has no production alternative. Retain
the `Access` interface as the request-context boundary used by handlers, but do
not provide a test implementation; the objective is to remove interaction
mocks, not interfaces by reflex.

## Repository guard and tooling cleanup

After the last consumer is migrated:

- delete all generated fake files and generation directives;
- remove Counterfeiter from `tools.go`, `go.mod`, `go.sum`, contributor
  documentation, and generation entry points;
- remove `atc/.ignore`, whose only rule hides generated fake files;
- remove stale comments that describe generated fakes as the testing pattern.

Extend the root architecture tests with a mock-free invariant. The test will
enumerate module Go files through `go list` or a direct filesystem walk rather
than an ignore-aware text search. It will fail on Counterfeiter directives,
generated headers, fake packages, mocking-framework imports, and generated-spy
API patterns. It will assert that it inspected a substantial non-zero set of
packages and Go files, without pinning an exact count. This prevents both the
`atc/.ignore` false-zero failure and silent success when enumeration breaks.

The structural check is a reintroduction guard, not a substitute for the final
semantic audit. Completion also requires reviewing project-owned test helpers
and client-go usage for call inspection, reactors used only to inject unlikely
errors, and hand-written configurable collaborators.

## Migration order and checkpoints

Migrate in this order so the smallest and least connected boundaries prove the
pattern before broad suites are changed:

1. database locks;
2. engine;
3. scheduler;
4. API access;
5. repository guard and tooling cleanup.

After each area, run its Ginkgo suite and perform a tracked-file search for the
removed fake and interface. Database-backed packages must run through Ginkgo,
never concurrent plain `go test ./...`. After all areas, run the root
architecture tests, the complete unit tier through its Ginkgo-based runner,
and the relevant K8s behavioral gates in CI.

Record clean before-and-after wall times for affected suites. Prefer deleting
redundant interaction matrices and reusing existing fixtures over adding new
setup. Any material runtime increase must be explained and investigated, but
meaningful behavioral protection is not removed merely to recover seconds.

## Completion criteria

This project is complete only when all of the following are proven from the
current worktree:

- no Counterfeiter output, directive, dependency, tool entry, or documentation
  remains;
- no project-owned interaction mock or call-spy assertion remains;
- retained deterministic models and protocol fixtures are used through
  observable outcomes rather than internal-call assertions;
- the mock-free architecture guard scans real source files and passes;
- lock, engine, scheduler, API, root architecture, and complete unit suites
  pass under their correct runners;
- required K8s behavioral checks pass in CI;
- before-and-after runtime evidence shows no unexplained material regression;
- existing untracked audit documents and unrelated worktree changes remain
  untouched.

The subsequent coverage project will measure the stabilized product closure,
collect subprocess coverage, and add higher-level scenarios until global
statement coverage is at least 80%.
