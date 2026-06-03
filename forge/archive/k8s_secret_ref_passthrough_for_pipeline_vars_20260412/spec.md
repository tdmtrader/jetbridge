# Spec: K8s Secret Ref Passthrough for Pipeline Vars

**Track ID:** `k8s_secret_ref_passthrough_for_pipeline_vars_20260412`
**Type:** feature

## Overview

When Concourse resolves `((vars))` from a Kubernetes Secrets credential manager, the secret values are currently embedded as plain-text environment variables in the Pod spec. Anyone with `kubectl get pod -o yaml` access can read them. This feature changes the K8s runtime to emit `ValueFrom.SecretKeyRef` references instead of literal values for vars sourced from K8s Secrets, so the kubelet fetches the secret at pod start and the plain text never appears in the pod spec.

## Why

- **Security**: Plain-text secrets in pod specs are visible to any user with pod read RBAC, broadening the blast radius of a credential leak.
- **Compliance**: Many security policies require that secrets are never stored in plain text in API objects.
- **K8s-native**: Users who already manage secrets via K8s Secrets (or GCP Secret Manager synced to K8s Secrets) expect pod specs to reference secrets by name, not embed them.

## Requirements

1. When the active credential manager is the Kubernetes Secrets backend (`atc/creds/kubernetes`), resolved `((vars))` that appear as task `params` must be injected into the pod spec as `ValueFrom.SecretKeyRef` references instead of literal `Value` entries.
2. A new optional interface `SecretRefProvider` allows credential managers to advertise that they can provide native K8s Secret coordinates (namespace, name, key) for a given variable path.
3. The `CredVarsTracker` must record K8s Secret reference metadata alongside the existing interpolated credential values, so the pod builder can look up the reference for any resolved var.
4. The `runtime.ContainerSpec` must carry secret reference metadata so the pod builder can distinguish between literal env vars and secret-backed env vars.
5. Non-K8s credential managers (Vault, AWS SSM, Conjur, CredHub, etc.) are unaffected — they do not implement `SecretRefProvider` and their vars continue as literal `Value` entries.
6. Log redaction continues to work — values are still resolved and tracked for redaction purposes, even when the pod spec uses `SecretKeyRef`.
7. Pipeline config syntax is unchanged — `((vars))` work exactly as before from the user's perspective.
8. Sidecar containers must also use `SecretKeyRef` for any secret-backed env vars.

## Technical Approach

### New Interface: `SecretRefProvider`

In `atc/creds/`, define:

```go
type K8sSecretRef struct {
    Namespace string
    Name      string
    Key       string
}

type SecretRefProvider interface {
    GetSecretRef(path string) (*K8sSecretRef, bool)
}
```

The K8s Secrets manager (`atc/creds/kubernetes/secrets.go`) implements this by parsing the `namespace/name` path it already computes during `Get()`.

### Tracker Extension

`vars.CredVarsTracker` gains a parallel `secretRefs` map that records `varPath -> K8sSecretRef` for vars resolved from a `SecretRefProvider` backend. The existing `interpolatedCreds` map is unchanged.

### ContainerSpec Extension

`runtime.ContainerSpec` gains a `SecretEnv` field (`map[string]creds.K8sSecretRef`) mapping env var names to their K8s Secret coordinates. The task step populates this by cross-referencing `config.Params` against the tracker's secret refs.

### Pod Builder Change

`jetbridge/container.go` `createPod()` checks `SecretEnv` when building the env var list. For entries with a matching secret ref, it emits `corev1.EnvVar{Name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ...}}` instead of `corev1.EnvVar{Name, Value}`.

### Secret Lookup Path Resolution

The `VariableLookupFromSecrets.get()` method tries multiple lookup paths (pipeline-scoped, team-scoped, root-scoped). The secret ref must record the **actual path that matched**, not just the var name. This is done by extending the lookup loop to also call `SecretRefProvider.GetSecretRef()` on the successful path.

## Acceptance Criteria

- [ ] A task step using `((var))` params with the K8s Secrets credential manager produces a pod spec with `ValueFrom.SecretKeyRef` — no plain-text secret values in the pod spec.
- [ ] The same pipeline with a non-K8s credential manager (e.g., Vault) continues to produce literal `Value` entries unchanged.
- [ ] Secret values are still tracked for log redaction.
- [ ] Sidecar env vars that reference K8s secrets also use `SecretKeyRef`.
- [ ] Unit tests cover: SecretRefProvider implementation, tracker extension, ContainerSpec population, pod builder branching.
- [ ] Integration test: a task step with K8s-secret-backed params produces the expected pod spec.

## Out of Scope

- CSI Secret Store Driver integration
- Vault Agent sidecar injection
- Ephemeral K8s Secret creation/cleanup
- Changes to pipeline config syntax
- Secret rotation or TTL handling
- Non-env-var secret injection (e.g., volume mounts)
