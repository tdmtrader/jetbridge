# Implementation Plan: K8s Secret Ref Passthrough for Pipeline Vars

## Phase 1: Core Types and Interfaces [checkpoint: dbb66baabf]

- [x] Write tests for `SecretRefProvider` interface and `K8sSecretRef` type f26ee5ce37
- [x] Implement `SecretRefProvider` interface in `atc/creds/secrets_factory.go` and `K8sSecretRef` type f26ee5ce37
- [x] Write tests for K8s Secrets manager `GetSecretRef()` implementation f26ee5ce37
- [x] Implement `GetSecretRef()` on `kubernetes.Secrets` in `atc/creds/kubernetes/secrets.go` dbb66baabf
- [x] Phase 1 Manual Verification dbb66baabf

---

## Phase 2: Tracker and Lookup Extension [checkpoint: a1642585c2]

- [x] Write tests for `CredVarsTracker` secret ref tracking 844e41a8f1
- [x] Extend `vars/tracker.go` to track `secretRefs` map alongside `interpolatedCreds` 844e41a8f1
- [x] Write tests for `VariableLookupFromSecrets` secret ref propagation a1642585c2
- [x] Extend `atc/creds/secret_var_lookup.go` to call `SecretRefProvider.GetSecretRef()` on matched paths and propagate refs through the tracker a1642585c2
- [x] Phase 2 Manual Verification a1642585c2

---

## Phase 3: ContainerSpec and Task Step Wiring [checkpoint: c493a81dbc]

- [x] Write tests for `ContainerSpec.SecretEnv` population from tracker c493a81dbc
- [x] Add `SecretEnv map[string]K8sSecretRef` field to `runtime.ContainerSpec` in `atc/runtime/types.go` c493a81dbc
- [x] Write tests for task step `containerSpec()` cross-referencing params against tracker secret refs c493a81dbc
- [x] Implement secret ref population in `atc/exec/task_step.go` `containerSpec()` — for each param env var, check if the tracker has a K8s secret ref for that var path and add to `SecretEnv` c493a81dbc
- [x] Phase 3 Manual Verification c493a81dbc

---

## Phase 4: Pod Builder — SecretKeyRef Emission [checkpoint: a56342b557]

- [x] Write tests for `createPod()` emitting `ValueFrom.SecretKeyRef` for secret-backed env vars a56342b557
- [x] Modify `envVars()` or `createPod()` in `atc/worker/jetbridge/container.go` to check `SecretEnv` and emit `ValueFrom.SecretKeyRef` instead of literal `Value` for matching entries a56342b557
- [x] Sidecar env vars — N/A: sidecars use explicit SidecarEnvVar, not ((vars)) interpolation a56342b557
- [x] Sidecar env var construction — N/A: not routed through credential manager a56342b557
- [x] Phase 4 Manual Verification a56342b557

---

## Phase 5: Integration and Fallback Testing [checkpoint: 9ac23f3d4f]

- [x] Write integration test: K8s Secrets credential manager produces pod spec with `SecretKeyRef` 9ac23f3d4f
- [x] Write integration test: non-K8s credential manager produces pod spec with literal `Value` (no regression) 9ac23f3d4f
- [x] Write test: log redaction still works for secret-ref-backed vars 9ac23f3d4f
- [x] Write test: mixed credential managers (some vars from K8s, some from other backends) produce correct pod spec 9ac23f3d4f
- [x] Phase 5 Manual Verification 9ac23f3d4f

---
