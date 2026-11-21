---
status: accepted
date: 2026-11-21
---

# A task step's params live in a Secret of its own

A task's `params` are whatever its config interpolated: values a credential
manager resolved, static vars, `load_var` results. JetBridge wrote each one
as a literal `env[].value` of the step pod's main container, and under the
`job` step runtime into the step Job's pod template as well. Anyone who can
read pods in the step namespace read them: the `view` role,
`kubectl describe`, an audit policy that logs pod bodies, and an etcd that
encrypts only Secrets at rest.

There was meant to be a guard. A credential manager could report the
Kubernetes Secret a var came from (`GetSecretRef`), and the pod would read
that Secret through a `secretKeyRef`. It never ran: the retrying wrapper
every manager is wrapped in hid the method, the reference it built dropped its
namespace, and only the Kubernetes manager had one.

## Decision

Every task step with params gets its own Secret, `step-env-<handle>`, and its
main container reads every param from it.

- **All params, every credential manager.** No param is matched against the
  credentials the redactor tracked: a value can contain a credential, be
  JSON-encoded around one, or come from a var the tracker never saw, and the
  rule then names no backend. The step's own env (`BUILD_*`,
  `ATC_EXTERNAL_URL`) stays literal, so check, get and put steps get no
  Secret: their source and params already travel on stdin or in the step
  envelope, never in an object.
- **One `secretKeyRef` per param, under keys of ours** (`p0`, `p1`, ... in
  name order), not `envFrom`. A param name need not be a valid Secret key, a
  key `envFrom` cannot use as a variable name may be skipped with only an event, and
  explicit `env` beats `envFrom`, which would let the step's own `BUILD_ID`
  override a param of that name. The entries sit where the literals did,
  after the step's own env, so precedence is unchanged.
- **Named by the handle, immutable, replaced on every creation.** The reaper
  has only a handle for a container whose pod is gone, so it deletes by name;
  web is granted `create`, `patch` and `delete` on Secrets and never `get`,
  `list` or `watch`.
- **Owned by what it serves.** On `exec` the Secret is made, then the pause
  pod, then the Secret's ownerReference is patched to the pod. On `job` it is
  made owned by the suspended step Job, before the unsuspend. The garbage
  collector takes it with its owner; the reaper deletes, by name, the Secret
  of every task container leaving the database, for one a web left unowned by
  dying in between.
- **Never a literal.** A Secret that cannot be made fails the step, naming
  the missing Role rule or the size over the 1 MiB a Secret holds, and both
  runtimes refuse a pod whose main container would carry a param as a value.
  Web still starts under an old Role; only task steps with params fail.

The backend-reference design was not repaired. A `secretKeyRef` cannot cross
namespaces, so it could only name a Secret in the step namespace, and only
for the Kubernetes manager; every other manager's values would stay literal;
and the pod would read the team's own credential Secret, so web, or the step
pod grant, would need read on it.

## What it protects against, and what it does not

It moves params out of the Pod and the Job into an object clusters already
treat as sensitive: it protects them from readers of pods and Jobs who cannot
read Secrets. It is not isolation between teams. Every team's step pods share
the step namespace, so anyone who can read Secrets there, or create a pod
that mounts one, can read every team's params; a step pod grant whose
ServiceAccount has `get secrets` in that namespace is such an identity, and
the trusted-reader audit already names them.

## Limits

- Under the `job` step runtime the task's `run.path` and `run.args` are
  still in the step Job's pod template and its pod, where admission control
  is meant to see them; on `exec` they are not stored but are in the exec
  request's URL. A credential belongs in `params`.
- `run.dir`, the image reference and a sidecar's `env`, `command` and `args`
  are stored in the pod spec. Sidecar config is not interpolated with
  credentials today.
- A resource's result (the version and metadata it printed) is stored as a
  pod annotation.

## Consequences

- The chart's pod-manager Role grants `secrets: create, patch, delete`, in
  every permutation and with no value. It must be applied with or before an
  image that makes these Secrets.
- A task's params are fixed when its pod starts. A web that reattaches to a
  live pod reads nothing; a replaced pod gets the values its step resolved
  again.
