# Agentic Foundations Semantic Rebase Design

**Status:** Approved for implementation on 2026-07-28.

## Objective

Reconstruct the approved agentic-platform foundation ranks 1–5, 7, 9, and
13 on top of the cleaned Jetbridge v3 architecture at
`296ef5dd86676fc48abd62bd1615ec4958da2ddd`, using
`02188c24b48fae22468b693984b6c2320057967b` as a behavioral reference rather
than rebasing its 133 commits mechanically.

The result must preserve Jetbridge's v3 cleanup while retaining the approved
behavioral decisions:

1. snapshots have one owning team and no grant relation;
2. agent principals do not exist;
3. publication executes inside ATC with narrowly scoped credentials;
4. agents receive a managed typed-output builder while server sealing remains
   authoritative;
5. workflow resource sources use ordinary Concourse version selection once
   and reuse the captured snapshot;
7. repo-local dev capability execution is available interactively, while a
   separate deterministic execution is authoritative for validation;
9. agentic snapshots are durable in Hangar;
13. interrupted agent work resumes only from committed safe checkpoints.

## Integration Method

Implementation proceeds on a new branch rooted at the current Jetbridge head.
The original foundations branch remains unchanged as an executable reference.
Each rank is reconstructed as a vertical, independently reviewed slice.

Pure additive packages may be transplanted after a failing test establishes
their expected behavior. Integration code is adapted to current v3 APIs.
Deleted v1 subsystems are not restored to make old code compile.

The following Jetbridge decisions are authoritative:

- schema-v3 workflows are the only executable workflow definitions;
- reviews and feedback are snapshot-keyed;
- workflow-run reconciliation is the single terminalizer;
- dispatcher mode is stored in `agent_settings`;
- the root `agent/devmcp` client and the v1 ci-agent phase runner remain
  deleted;
- `agent/functions/gates`, `agent/functions/repositoryvalidate`, candidate
  port rules, the epistemic contract layer, and static-selector exposure
  remain deleted;
- workflow manifests use `workflow.yaml`;
- current artifact archive, symlink, signing-key, secret-mount, and chart
  conventions remain in force.

## Trust Boundaries

### Output creation

The managed output builder is an agent-only authoring aid. It receives
server-derived, mount-bound authority describing declared inputs and outputs,
offers a loopback MCP/CLI interface, writes candidates atomically, and performs
early validation. It cannot seal snapshots. The ordinary post-step sealer
reopens and independently validates every output.

### Dev capability and validation

The retained `ci-agent/devmcp` server remains the interactive repo-scoped MCP
surface. Its command resolution and execution are extracted into a shared
core also used by a deterministic CLI.

Interactive MCP results, agent transcripts, and agent-authored
`validation/v1` records never satisfy an authoritative gate. Authoritative
validation is a fresh hermetic task that:

- receives an exact read-only candidate and immutable base inputs;
- uses a promoted static profile, exact configuration bytes, pinned image, and
  fixed tool path;
- receives no model, publisher, Kubernetes, or generic capability
  credentials;
- retains complete per-attempt logs;
- emits `validation/v1` revision 3 with server-checked provenance.

Review and publication gates bind the exact candidate, validation profile,
configuration, image, toolchain, workflow revision, and successful
attestation. Any mutation after validation invalidates the gate.

### Publication

The external publisher gateway is removed only after the in-ATC implementation
is operational. ATC reads narrowly mounted destination policy and credentials,
reinspects the exact sealed change, derives the actor and operation key,
executes in a fresh private Git directory, and records idempotent publication
state. Initial support remains direct-to-trunk publication; pull-request
orchestration remains deferred.

### Durable content and recovery

Hangar stores immutable zstd-compressed objects under canonical SHA-256 keys
using GCS semantics. The artifact daemon remains the node-local cache and
mirror coordinator; Hangar is the durable read-through fallback and the
authority for agentic snapshots.

Checkpoint recovery stores only committed safe-boundary workspace snapshots.
It does not claim to preserve live processes. Recovery creates a fresh
execution attempt, restores the committed workspace through Hangar, preserves
attempt/transcript attribution, and supplies resume guidance stating that
processes and other ephemeral state may need reconstruction. Begun ambiguous
external effects require manual review.

## Data Migration

Jetbridge migrations `1773106128` through `1773106138` are immutable and must
remain byte-for-byte unchanged. The semantic rebase appends:

| Version | Purpose |
| --- | --- |
| `1773106139` | Direct snapshot team ownership |
| `1773106140` | Remove agent principals |
| `1773106141` | Freeze authoritative dev-validation provenance |
| `1773106142` | Create workflow resource-source admissions |
| `1773106143` | Capture build pipeline-config provenance |
| `1773106144` | Create checkpoint objects, generations, effects, and events |
| `1773106145` | Create durable execution attempts |
| `1773106146` | Attribute metrics to execution attempts |
| `1773106147` | Persist per-attempt transcripts |

These migrations are re-authored against the schema at `1773106138`; the
same-numbered files from the foundations branch are not copied. Before final
integration, the migration block is renumbered if Jetbridge has independently
advanced beyond `1773106138`.

Both a fresh database and a database already upgraded to `1773106138` must
reach the new head. The migration preflight pointer and legacy-upgrade
regression must equal the embedded migrator's supported version.

## Dependency Order

1. Direct ownership precedes every snapshot consumer.
2. Principal removal follows direct ownership.
3. Shared dev-capability execution precedes authoritative validation.
4. Authoritative validation and output-builder are separate facilities and
   may be developed independently after snapshot contracts are stable.
5. Hangar precedes standing workflow resource capture and checkpoint
   persistence.
6. Exact validation gates precede replacement of the publisher gateway.
7. Checkpoint recovery lands after Hangar, workflow-run identity, resource
   admissions, and execution-attempt accounting are stable.

## Verification

Every slice uses red-green-refactor tests and receives an independent task
review. The final branch must demonstrate:

- fresh-install and `1773106138` upgrade migration paths;
- no live `agent_snapshot_grants`, `agent_principals`, `cap1`, publisher
  gateway, v1 ci-agent runner, or retired function-runner authority;
- unit tests for every affected Go module;
- retained dev-MCP wire behavior and deterministic CLI parity;
- validation revision-3 attestation and stale-validation rejection;
- output-builder/final-sealer independence and secret non-disclosure;
- Hangar fake-GCS recovery after complete daemon cache loss;
- exact workflow source selection reuse across retry and replay;
- destination-scoped, idempotent in-ATC publication;
- checkpoint generation/effect fencing, fresh-attempt attribution, retention,
  metrics, and environment-gated live recovery;
- Fly, ATC integration, Helm, and focused Kubernetes behavioral gates.

Resource-heavy cluster tests may use Borg. Uploading repository source or
persisting a source-derived image in `registry.home` remains a separate
explicit authorization boundary.
