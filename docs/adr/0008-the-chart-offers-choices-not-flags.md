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
  (`_hangar-*.tpl`, `hangar-*.yaml`, and the `artifactDaemon.hangar` and
  `durable` rules in `artifact-daemon-daemonset.yaml`) until its values are
  reshaped.
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
  - a render with no values fails naming what is missing.

## Exceptions

Two lists name what the schema leaves open:

- `openMaps` are the free-form Kubernetes maps and lists: annotations,
  labels, node selectors, affinity, tolerations, resources, strategy, probes,
  security contexts, OTLP headers, extra volumes and mounts, env, ingress TLS
  and NetworkPolicy peers. Each counts as one value, and nothing inside one
  is checked.
- `deferredGroups` are the Hangar and durable groups (`artifactDaemon.hangar`,
  `artifactDaemon.durable`, `hangarBootstrap`, `hangarOutput`,
  `hangarStorage`), which later tracks reshape. Each is closed at its top
  level only. The list only shrinks.

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
- A deployment that sets a key the chart does not read fails to render, so
  a stale or misspelt key surfaces at the next sync instead of being
  ignored. A deployment's values file has to change in step with a chart
  that removes a key.
- `web.extraArgs` and `web.env` remain, grandfathered, until their settings
  become typed values or binary defaults.
- The amendment to ADR-0002 that removes the durable tier from the chart
  lands with that removal.
