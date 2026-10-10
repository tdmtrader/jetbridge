---
status: accepted
date: 2026-10-04
---

# The chart offers choices, not flags

The Helm chart's values are a closed, checked interface. Every value is one
of four kinds:

- a **per-cluster fact**: something only the cluster knows, such as the image,
  the external URL, resources, scheduling, a storage size or class, or the
  ingress;
- a **real tunable**: a value that deployments sensibly set differently, typed
  and range-checked;
- a **mode**: an enum that picks one of several supported shapes, such as a
  store backend;
- a **required reference**: the name of a Secret, ConfigMap or ServiceAccount
  that the operator provides.

Three kinds are not allowed. A **feature flag** turns code on or off. A
**one-answer setting** has only one sensible value, so it belongs in the
binary's defaults or a template. A **pass-through** (`extraArgs`, `env`, or a
map of arbitrary flags) hands the binary whatever the operator types, past
every check. A boolean stays only when it reflects something the cluster has
or lacks, such as an ingress controller or the Prometheus operator.
`extraVolumes` and `extraVolumeMounts` stay because they only add to the pod.

The alternative was to keep accepting any key and rely on review. The chart
grew a value group with each feature that way, and Helm rendered keys nobody
read: a misspelt or retired value was silently ignored, and concourse.home
still set one (`networkPolicy.hermeticEgressTo`) after the chart dropped it.

## Where the rules live

- `deploy/chart/values.schema.json` holds the shape. It is hand-written and
  closed: every object sets `additionalProperties: false`. Helm checks it
  before any template runs, and so does Argo's `helm template`, so an unknown
  key, a wrong type, an enum miss or an out-of-range port never renders.
- `deploy/chart/templates/_validate.tpl` holds every rule that relates several
  values, or that needs a message naming the fix. Schema messages are generic;
  a template message names the value and what to set. `web-deployment.yaml`
  includes it on its first line. Hangar's rules stay with Hangar
  (`_hangar-*.tpl`, `hangar-*.yaml`, and the `artifactDaemon.hangar` rules in
  `artifact-daemon-daemonset.yaml`) until its values are reshaped.
- A removed key stays in the schema as an open type for one release, and
  the removed-keys table in `_validate.tpl` fails it with
  "<key> has been removed; <action>". The release after, both entries go.
- `deploy/chart/tests/shape_test.go` holds the budget as constants at its
  top, and the guards that enforce it in `unit-tests`:
  - the number of values stays within `maxValues`;
  - every boolean is on `allowedSwitches`;
  - no key has a pass-through name (`bannedKeys`) unless it is on
    `grandfathered`;
  - every key has a typed schema entry;
  - every schema object is closed;
  - every removed key fails the render naming its removal;
  - a render with no values fails naming what is missing.

## Exceptions

Two lists name what the schema leaves open:

- `openMaps` are the free-form Kubernetes maps and lists: annotations,
  labels, node selectors, affinity, tolerations, resources, strategy, probes,
  security contexts, OTLP headers, extra volumes and mounts, env, ingress TLS
  and NetworkPolicy peers. Each counts as one value, and nothing inside one
  is checked.
- `deferredGroups` are the Hangar groups (`artifactDaemon.hangar`,
  `hangarBootstrap`, `hangarOutput`, `hangarStorage`), which later tracks
  reshape. Each is closed at its top level only. The list only shrinks.

## The raise rule

The constants only move down. Each change that removes values lowers
`maxValues` in the same commit. A raise of `maxValues`, or a new entry on
`allowedSwitches`, `openMaps` or `grandfathered`, is allowed only in the
commit that removes the pass-through or step value it replaces. The reason
is written beside the constant. A raise is a one-line diff a reviewer sees.

## Consequences

- A new chart value needs a schema entry, a kind from the list above, and
  room under `maxValues`. A new rule goes in `_validate.tpl` with a message
  naming the fix.
- A deployment that sets a key the chart does not read fails to render
  (below a deferred group's own keys, only once its track closes it), so
  a stale or misspelt key surfaces at the next sync instead of being
  ignored. A deployment's values file has to change in step with a chart
  that removes a key.
- `web.env` remains, grandfathered, until its settings become typed values
  or binary defaults. `web.extraArgs` went first (2026-10-05): its settings
  became `kubernetes.podSchedulingTimeout`, `kubernetes.defaultTaskCPURequest`
  and `kubernetes.preferredStepNode`, web derives `--cookie-secure` from an
  `https` external URL, and the OTLP flags use the existing `tracing.*` and
  `otelMetrics.*` values.
- ADR-0002's durable-tier consequences no longer apply to the chart. The
  chart does not configure the durable tier: `artifactDaemon.durable` is a
  removed key, the daemon is handed no `--durable-*` flag, and Hangar names
  its own store rather than inheriting the tier's. The tier's fail-open
  semantics, its retention classes and its separation from Hangar still hold
  in the daemon, which keeps its durable-tier code and flags; only the
  chart's way of setting them is gone. ADR-0002 is unedited, and this is its
  amendment.

Amended by C4 (daemon small cuts): resolve capability expiry is derived from
pod timeouts, retries and one margin. `artifactDaemon.resolveCapability.ttl`
leaves the values budget; its open schema tombstone and actionable rejection
remain for one release, as above. See `deploy/chart/MIGRATING.md` for rollout.

Amended by C5: ATC OTel metrics instruments and their OTLP/GCP exporters are
retired. The earlier `otelMetrics.*` guidance no longer applies: remove that
value group and use `metrics.enabled` for Prometheus metrics. `tracing.*`
continues to configure traces. The three metrics values leave the budget;
`otelMetrics` retains only an open schema tombstone and removal error for one
release. OTel-only measurements are retired, with no promise of emitter parity.

Amended 2026-10-09 — Amended by C2 (durable resource-cache tier removal):
The binary resource-cache tier is removed as well. `hangarOutput.cacheBucket` is a rejected open-schema tombstone for one release, and the chart value budget drops by one. The existing `artifactDaemon.durable` tombstone remains.

Amended 2026-10-09 — Amended by C7: the Run epoch and two result-read
settings leave the values budget. Each keeps an open schema tombstone and
removed-key diagnostic for one release. Result reads use concurrency 2 and a
fixed 1Gi disk scratch volume; private spool setup and cleanup remain.

Amended 2026-10-09 — Amended by C6: deliberately reduce the binary to local
user authentication, Kubernetes Secrets (with dummy, noop and ID-token support
retained), and Prometheus or Lager metrics. Remove the five vendor credential
managers, nine external auth connectors, and three vendor metric emitters.
`web.env` remains grandfathered: it previously reached these flags, and vendor
pipeline var_sources bypassed chart configuration entirely. This is a binary
capability reduction, not a consequence of the chart's closed values. See
`deploy/chart/MIGRATING.md` for persisted-data checks and rollout verification.
The branch's merges since 2026-02 are internal; recurring upstream rebases are
not a rationale for this decision. These backends had recent fork fixes, so
removal is based on the chosen capability boundary, not presumed inactivity.
