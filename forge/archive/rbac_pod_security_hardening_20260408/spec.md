# Spec: RBAC & Pod Security Hardening

## Overview

Concourse's K8s runtime has solid security defaults for the web container and PostgreSQL, but several gaps remain: task pods mount service account tokens they don't need, seccomp profiles are missing, the artifact daemon DaemonSet has no SecurityContext, and NetworkPolicy templates are incomplete.

This track closes these gaps with low-risk, opt-out-safe hardening. No behavioral changes for existing installs.

## Current State

### Already Hardened
- **Web container**: Non-root (UID 65534), read-only filesystem, all capabilities dropped, no privilege escalation
- **PostgreSQL**: Non-root (UID 999), capabilities restricted
- **K8s RBAC**: Minimal scoped roles — `concourse-web` ClusterRole (secrets/get, nodes/get), `concourse-pod-manager` Role (pods/*, PVCs, deployments, endpointslices)
- **Task containers**: `AllowPrivilegeEscalation=false` for non-privileged tasks
- **Concourse-level RBAC**: Team/role auth (owner/member/operator/viewer) functioning

### Gaps
- Task pods: `automountServiceAccountToken` not disabled — tasks get K8s API access they don't need
- Seccomp profiles: Not configured on any pod (required for CIS benchmarks and PSS restricted)
- Artifact daemon: No SecurityContext (no RunAsNonRoot, no capability drop, no privilege escalation prevention)
- NetworkPolicy: Missing artifact daemon ingress policy and task pod egress policy templates
- RBAC scope: `concourse-pod-manager` can create/modify any pod in namespace (comment in `rbac.yaml:44-45` notes future tightening)

## Requirements

1. Set `automountServiceAccountToken: false` on task and check pods
2. Add seccomp `RuntimeDefault` profile to all pod specs (web, task, check, daemon, postgres) — hardcoded, no override
3. Add artifact daemon NetworkPolicy template (ingress from Concourse pods only)
4. Add task pod egress policy template (DNS + artifact daemon + configurable external + GKE metadata server)
5. Keep `networkPolicy.enabled` default as `false` — operators opt in
6. Document recommended Pod Security Admission namespace labels
7. Document RBAC permissions with justification for each rule

## Technical Approach

### Key Files

| File | Role |
|------|------|
| `atc/worker/jetbridge/container.go` | Task/check pod spec construction — SecurityContext (lines 776-798) |
| `atc/worker/jetbridge/live_security_test.go` | Security context test coverage |
| `atc/worker/jetbridge/container_test.go` | Pod spec tests |
| `deploy/chart/templates/web-deployment.yaml` | Web pod spec |
| `deploy/chart/templates/artifact-daemon-daemonset.yaml` | Daemon pod spec |
| `deploy/chart/templates/networkpolicy.yaml` | Network policies |
| `deploy/chart/templates/rbac.yaml` | RBAC roles and bindings |
| `deploy/chart/values.yaml` | Helm defaults |

### Changes

**automountServiceAccountToken:**
- In `container.go` pod spec construction, set `AutomountServiceAccountToken: boolPtr(false)` on PodSpec
- Add Helm value `taskPods.automountServiceAccountToken` (default: `false`) for opt-out

**Seccomp profiles:**
- Hardcode `SeccompProfile{Type: RuntimeDefault}` on all pod-level SecurityContexts
- Web deployment, task/check pods (in container.go), daemon DaemonSet, PostgreSQL statefulset
- No Helm override — RuntimeDefault is always correct

**NetworkPolicy templates (opt-in via `networkPolicy.enabled`):**
- Add artifact daemon ingress policy: allow port 7780 from Concourse web + task pods only
- Add task pod egress policy: DNS (port 53) + daemon (port 7780) + GKE metadata (169.254.169.254:80,988) + configurable `taskEgressTo`
- Keep `networkPolicy.enabled: false` default — no breaking changes on upgrade

**Documentation:**
- Add recommended PSA labels for Concourse namespace
- Document RBAC audit results (permissions already well-commented in rbac.yaml)

## Workload Identity Interaction

- `automountServiceAccountToken: false` is safe — Workload Identity uses the GKE metadata server, not the SA token mount
- Task egress NetworkPolicy MUST include 169.254.169.254:80 and :988 for Workload Identity token refresh
- Artifact daemon SA (`artifact-daemon-rbac.yaml`) is separate from web SA — doesn't have annotation support for Workload Identity yet (out of scope, noted for future)

## Acceptance Criteria

- [ ] Task pods do not mount service account tokens by default
- [ ] All pods have seccomp `RuntimeDefault` profile
- [ ] Artifact daemon NetworkPolicy template exists (active when `networkPolicy.enabled: true`)
- [ ] Task pod egress policy template exists with GKE metadata server allowlist
- [ ] Existing deployments can upgrade without breakage (`networkPolicy.enabled` stays `false`)
- [ ] RBAC permissions documented with justification for each rule
- [ ] Pod Security Admission recommendations documented

## Out of Scope

- Flipping `networkPolicy.enabled` default to `true` (operator decision)
- `taskPods.securityContext` Helm plumbing (RunAsNonRoot opt-in) — most resource type images run as root
- Seccomp profile Helm override values — RuntimeDefault is always correct
- Pod Security Admission enforcement via admission controller (operator responsibility)
- Namespace separation for multi-tenant teams
- RBAC tightening via resourceNames or admission webhooks (future work noted in rbac.yaml)
- OPA/Gatekeeper policy definitions
- Artifact daemon auth/TLS (covered in separate daemon security track)
- Artifact daemon SA Workload Identity annotation support
