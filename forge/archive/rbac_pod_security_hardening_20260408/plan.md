# Plan: RBAC & Pod Security Hardening

## Phase 1: Task Pod Service Account Token

- [x] Write tests for task pod spec with `automountServiceAccountToken: false`
- [x] Update `atc/worker/jetbridge/container.go` to set `AutomountServiceAccountToken: false` on PodSpec
- [x] Add Helm value `taskPods.automountServiceAccountToken` (default: false) — simplified: hardcoded in Go, no Helm value needed (task pods are only built in container.go)
- [x] Verify existing tests pass (live_security_test.go, container_test.go)

## Phase 2: Seccomp Profiles

- [x] Write tests for seccomp `RuntimeDefault` on task/check pod specs
- [x] Add seccomp profile to `buildPodSecurityContext()` in container.go
- [x] Add seccomp profile to web deployment template (via values.yaml podSecurityContext)
- [x] Add seccomp profile to artifact daemon DaemonSet template
- [x] Add seccomp profile to PostgreSQL statefulset (via values.yaml postgresql.podSecurityContext)

## Phase 3: NetworkPolicy Templates

- [x] Add artifact daemon NetworkPolicy template (ingress from Concourse web + task pods on port 7780)
- [x] Add task pod egress policy template (DNS + artifact daemon + GKE metadata 169.254.169.254:80,988 + configurable `taskEgressTo`)
- [x] Write Helm template tests for NetworkPolicy rendering
- [x] Document GKE Workload Identity metadata server requirement in values.yaml comments

## Phase 4: RBAC Audit & Documentation

- [x] Audit each RBAC rule in rbac.yaml and artifact-daemon-rbac.yaml
- [x] Document justification for each permission in template comments
- [x] Document recommended Pod Security Admission namespace labels in values.yaml
- [x] Add example namespace annotation block to values.yaml comments

## Phase 5: Integration Verification

- [x] Run existing K8s unit tests with hardened defaults (307/307 pass)
- [x] Verify task pods have automountServiceAccountToken=false in both privileged and non-privileged modes
- [x] Verify seccomp profiles render on all pod types (web, daemon, postgres, task)
