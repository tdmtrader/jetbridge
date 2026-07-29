# Task 14 preflight — direct in-ATC publication

This is a read-only semantic-rebase map prepared before Task 14 implementation.
It must be read together with:

- `2026-07-28-agentic-foundations-semantic-rebase-session-context.md`
- `2026-07-28-agentic-foundations-semantic-rebase.md`
- `2026-07-28-agentic-foundations-semantic-rebase-design.md`

Use Terra agents by default. Fix and review only Critical, High, or
acceptance-blocking issues, with at most three review rounds total.

## Preserve the accepted boundary

- `atc/exec/publish_snapshot_step.go` already reopens the exact sealed input,
  enforces the authoritative Task 7 validation before any publisher side
  effect, derives actor and operation identity server-side, derives the
  expected base SHA from sealed change metadata, and requires exact durable
  human approval for merge-mode publication.
- `agent/publisher/git.go` already owns the durable acquire, exact change
  reinspection, provider lookup, current-head comparison, publish, and
  completion flow.
- `atc/db/agent_publications_factory.go` and migrations `1773106110`,
  `1773106115`, and `1773106118` already provide the neutral durable lease,
  reclaim, and idempotent-completion model. Do not remove them with the
  gateway.

## Minimum implementation

Adapt the useful behavior from historical commits rather than mechanically
transplanting their old composition:

- `f242da8049` — policy, credential, and inspector extraction
- `bdcf937675` — direct Git backend
- `8fe849329a` — ATC composition
- `c682e33f71` — gateway transport removal
- `b25a57f4a8` — Helm and runtime image changes
- `1e785a501b`, `c9432728a3` — atomic recovery and scratch/credential hardening

Create or adapt:

- `agent/publisher/{policy,credentials,snapshot_inspector}.go`
- `agent/publisher/directgit/{backend,runner}.go`
- `atc/atccmd/agent_publisher.go`

The direct backend must:

- use a new private `0700` bare Git directory for every invocation;
- disable ambient and repository-local Git configuration, alternates, hooks,
  filters, submodules, and inherited credential environments;
- keep credentials out of argv, URLs, logs, errors, operation records, and
  inherited environments by using private per-invocation askpass files;
- atomically push the destination ref and
  `refs/concourse/publications/<operation-hash>` with leases;
- recover an ambiguous remote success by finding the exact marker after lease
  reclaim, without a second push.

Replace the gateway flags and composition only after the direct backend is
green. Move generic snapshot-inspection coverage to the neutral package, then
delete gateway HTTP/TLS/token/CA transport, service, flags, chart resources,
and transport-only tests.

## Helm and image boundary

- Mount distinct read-only, web-only policy and credential Secrets.
- Map exact credential references to exact Secret keys.
- Reject reuse through extra volumes or environments, migration containers,
  workers, or agent pods.
- Add the controlled Git package to the web runtime image and prove no
  credential material is embedded.
- Remove all `agentPublisherGateway` values, templates, flags, and tests.

## Blocking rebase correction

The current `merge-delivery-v3` seed describes a rebase but invokes
`merge-prepare --method=merge`; `cmd/function-runner` supports merge/squash
only. This violates the approved trunk-based rule.

Do not rebase inside the publisher after validation or human approval. That
would mutate the approved candidate and invalidate both gates. Rebase must
produce the final sealed candidate upstream; Task 7 validation and any human
approval then bind that rebased value. The publisher only performs the atomic
trunk update.

## Exact current-head transplant boundary

Use the final historical forms from `c9432728a3` for the publisher policy,
mounted-file/credential handling, Git runner, and hardened direct backend.
Apply the atomic recovery behavior from `1e785a501b`. Preserve only the
publisher-specific portions of `8fe849329a` and `b25a57f4a8`; current ATC,
Hangar, checkpoint, and Helm composition is authoritative.

The historical direct backend depends on snapshot APIs that current HEAD
deliberately does not expose: `CapturedTree.OpenRoot`,
`Canonicalizer.TempRoot`, and `Canonicalizer.CaptureRoot`. Do not restore the
historical `agent/snapshot/archive.go`; that would collide with the accepted
snapshot and output-builder boundaries. Instead:

- add only a narrow, closed-state-aware `CapturedTree.OpenRoot` accessor backed
  by the tree's retained anchored root;
- canonicalize the direct backend's payload under its verified private scratch
  directory with the current `Canonicalizer.TempDir`; and
- keep the backend's scratch verification before and after subprocesses.

Adapt the current gateway-shaped publisher credential into the historical
opaque resolved authorization: adapter kind, server-owned remote URL, copied
secret bytes, and redacted formatting. Direct Git must use only these resolved
values and must never derive a remote from the authored destination.

Preserve the current `snapshotPublisherComposer` seam and the Task 13
command-scoped snapshot/source composition. Port only publisher-specific Helm
blocks; do not replace the current web deployment template wholesale. Other
publisher types fail closed through an unavailable executor until a deliberate
in-process backend exists.

`ModeMerge` may remain as the existing name for an already approved
direct-to-trunk publication, but it performs no merge or rebase. It atomically
updates the target ref to the precomputed sealed result commit. Rebase remains
an upstream workflow operation before validation and approval.

Implement in this order:

1. restore/adapt policy, mounted credentials, neutral snapshot inspection, and
   the direct authorization/race contract;
2. restore the isolated Git runner and direct backend with the narrow current
   snapshot adapter;
3. compose the direct executor in ATC, then remove gateway transport and
   command wiring;
4. port publisher-only web image and dedicated web-only Secret mounts; and
5. run focused tests and residue searches before broad verification.

## Required focused proof

- Direct-backend unit coverage: marker lookup, atomic-capability refusal,
  stale lease/non-fast-forward, redacted errors, isolated scratch space, and
  exact repository-change reinspection.
- Local bare-remote integration: crash after remote success but before durable
  completion, followed by lease reclaim and marker-based completion; a target
  race changes neither destination nor marker.
- Existing Task 7 publish-step validation and approval tests remain green.
- Helm/render tests prove publisher credentials are absent from all agent
  main/sidecar pods.
- Residue search proves the gateway flags, values, transport, service, and
  chart resources are gone.

Task 14 should not modify Task 13's opaque execution-envelope, source
admission, or recovery APIs.
