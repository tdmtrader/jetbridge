# Task 6 — reusable node HTTP and Fly surfaces

## Delivered

- Added node-run create, exact-version list, and kind-scoped detail routes.
  Creation accepts only `inputs`, `params`, and `idempotency_key`, enforces
  the existing strict JSON/media-type/64 KiB body rules, and calls the trusted
  binder with `DefinitionKindNode`, the exact path version, and node
  parameters. It does not expose function or implementation selectors.
- Added a shared workflow-run presenter so node reads reuse the established
  quoted-decimal IDs, redacted status/detail, binding, and authorized output
  manifest projection. Node reads call `GetKind`/`ListKind` with node scope;
  the collection additionally binds its path version.
- Registered the routes in production composition with main-team roles:
  Member for create and Viewer for list/detail. Authentication and archived
  pipeline wrappers pass the routes through the same agent-route boundary.
- Added `fly agent nodes run`, `runs`, and `show-run`, reusing agent request,
  input parsing, idempotency, and deterministic run output helpers.

## TDD evidence

- RED: `go test ./agent/api/noderuns -run 'Test(Create|ListAndGet)' -count=1`
  failed because the noderuns package had no production files.
- GREEN: the create request proves exact node version, node definition kind,
  quoted snapshot inputs, parameter forwarding, and no function/implementation
  fields. The list/detail test proves node-kind stores are used.
- RED: `TestNodeRunRoutesRegisteredExactlyOnce` failed with missing route
  constants before route registration.
- GREEN: route registration passed after the three routes were added.
- RED: `TestCreateAndGetRejectCollectionFilters` showed filters were accepted
  outside the collection endpoint.
- GREEN: create/detail now reject all collection filters; list accepts only
  implemented `status`, `limit`, and `cursor` filters.
- RED: `TestAgentNodesRunUsesExactVersionAndOnlyPublicBodyFields` failed with
  a missing Fly request helper.
- GREEN: Fly submits only inputs, params, and idempotency key to the exact
  node-version path.

## Verification

Passed on 2026-07-29:

`go test ./agent/api/noderuns ./agent/api/workflowruns ./atc/api/accessor ./atc/wrappa ./atc/api ./atc/atccmd ./fly/commands -count=1`

`git diff --check`

## Core handoff

This surface consumes Task 6 core's kind-aware binder request and database
store methods: `DefinitionKind`, `NodeParameters`, `GetKind`, and `ListKind`
with `WorkflowVersion` filtering. No binder, database, or migration logic was
modified by this API/Fly half.
