# Task 7 report: opt-in Hangar Helm rollout

## TDD evidence

The initial chart RED run failed because the enabled chart rendered none of the
Hangar flags, key, readiness label, or private scratch resources; invalid
prerequisites rendered successfully; and the auto TLS Secret contained no
`hangar.key`:

```text
$ env GOCACHE=/private/tmp/hangar-task7-gocache go test ./deploy/chart/tests -run 'TestHangar' -count=1
--- FAIL: TestHangarEnabledRendersSharedBoundedConfiguration
    enabled render missing "--hangar-enabled"
    enabled render missing "--kubernetes-hangar-enabled"
--- FAIL: TestHangarRejectsInvalidPrerequisitesAtRender
    helm template unexpectedly accepted ...
--- FAIL: TestHangarAutoSecretContainsStrongRawKey
    auto-generated artifact-daemon Secret had no hangar.key
FAIL
```

The binary TTL RED runs failed at compile time because neither runtime exposed
the required configured TTL:

```text
$ go test ./cmd/artifact-daemon -run 'TestHangarConfig' -count=1
unknown field CapabilityTTL in struct literal of type hangarOptions

$ go test ./atc/atccmd -run '^TestSuite$' -testify.m '^TestHangarRuntime' -count=1
enabled.Kubernetes.HangarCapabilityTTL undefined
```

After the smallest flag/validation/template changes, the focused tests passed:

```text
$ env GOCACHE=/private/tmp/hangar-task7-gocache go test ./deploy/chart/tests -run 'TestHangar' -count=1
ok github.com/concourse/concourse/deploy/chart/tests 2.363s

$ env GOCACHE=/private/tmp/hangar-task7-gocache go test ./cmd/artifact-daemon -run 'TestHangarConfig' -count=1
ok github.com/concourse/concourse/cmd/artifact-daemon 0.547s

$ env GOCACHE=/private/tmp/hangar-task7-gocache go test ./atc/atccmd -run '^TestSuite$' -testify.m '^TestHangarRuntime' -count=1
ok github.com/concourse/concourse/atc/atccmd 0.678s
```

## Implementation

- Added a default-off `artifactDaemon.hangar` block with a disjoint private
  `emptyDir` scratch mount, positive tree limits, and a shared default 15m
  capability TTL.
- Helm rejects disabled daemon/TLS, non-GCS store, missing bucket, contained or
  relative scratch, non-positive limits, and TTLs outside `(0, 15m]` before
  emitting manifests.
- Enabled daemons receive strict GCS, key, scratch, limit, and TTL flags. Web
  receives only enablement, key path, and the identical TTL. Both select the
  same `hangar.key`; task Pod generation never receives the Secret or key.
- The auto-generated TLS Secret reuses an existing `hangar.key` through
  `lookup`, or generates 32 cryptographically random bytes on first enablement.
  An operator-managed TLS Secret produces no parallel Secret and must contain
  exactly 32 raw bytes under `hangar.key`.
- Capability TTL is now a real startup-bounded setting in both binaries. The
  web signer and daemon verifier use the configured value and reject zero,
  negative, or values greater than `hangar.MaxGrantTTL`.
- Default rendering contains no Hangar flag, key, scratch resource, or label.
  The legacy artifact-cache label flag and security context are unchanged.
- Added operator documentation for GCS/Workload Identity, exact refs and local
  receipts, private scratch sizing, key/grant visibility, fail-closed versus
  cache fail-open behavior, truthful node-IP TLS identity limits, optional
  CNI-dependent NetworkPolicy, and daemon-first/reverse downgrade order.

## Security-context audit

The canonicalizer and materializer create private daemon-owned scratch/staging
files and use descriptor-relative open, mkdir, rename, fsync, symlink, and
chmod operations. They do not mount filesystems, create devices, change
ownership, trace processes, bind privileged ports, or require network
administration. The anchored strict destination is created by kubelet/daemon as
root and is read-only to task containers. Root plus `DAC_OVERRIDE` is therefore
sufficient for the existing arbitrary-UID hostPath permission bypass; no added
capability is required. The enabled-render test asserts root,
`allowPrivilegeEscalation: false`, `RuntimeDefault`, drop `ALL`, and add only
`DAC_OVERRIDE`.

## Verification

```text
$ env GOCACHE=/private/tmp/hangar-task7-gocache go test ./deploy/chart/tests -count=1
ok github.com/concourse/concourse/deploy/chart/tests 22.825s

$ helm lint deploy/chart
1 chart(s) linted, 0 chart(s) failed

$ env GOCACHE=/private/tmp/hangar-task7-gocache go test ./atc/atccmd -count=1
ok github.com/concourse/concourse/atc/atccmd 0.683s

$ env GOCACHE=/private/tmp/hangar-task7-gocache go test ./cmd/artifact-daemon ./cmd/artifact-daemon/durable -count=1
ok github.com/concourse/concourse/cmd/artifact-daemon 54.198s
ok github.com/concourse/concourse/cmd/artifact-daemon/durable 4.643s
```

Default and enabled `helm template` renders were generated separately. A
non-vacuous scan found no Hangar surface in the default render and found the
daemon/web flags, shared TTL, key selections, private scratch, and readiness
label declaration in the enabled render.

## Local gaps

- Helm cannot reliably inspect an arbitrary live `tls.existingSecret` under
  GitOps/RBAC. Required Secret item selection makes a missing `hangar.key` fail
  at kubelet mount time; both binaries enforce the exact raw length at startup.
- No live GCS bucket, Workload Identity binding, Kubernetes node-label rollout,
  CNI NetworkPolicy, or real BusyBox/K3s Pod was exercised locally. Those
  environment-dependent proofs remain for Task 8/CI.
- The first sandboxed daemon run failed at `httptest` listener creation with
  `bind: operation not permitted`; the identical loopback-approved focused and
  full runs passed.
