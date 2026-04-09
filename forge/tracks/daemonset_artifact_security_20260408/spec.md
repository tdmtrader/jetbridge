# Spec: DaemonSet Artifact Security Hardening

## Overview

The artifact daemon DaemonSet operates with no authentication, no encryption, and no access control. Every endpoint is open to any pod in the cluster over plain HTTP on hostPort 7780. Artifact data (build outputs, resource caches, streaming tar payloads) flows unencrypted between ATC, task pods, and daemon peers.

This track adds mTLS encryption with path-based enforcement, NetworkPolicy isolation, and SecurityContext hardening to the artifact daemon.

## Problem

- **No authentication**: Any pod in the cluster can call `/register`, `/resolve`, `/stream-in`, `/artifacts/*`, `/resource-caches/*` without credentials
- **No encryption**: All artifact data (including potentially sensitive build outputs) transmits as plaintext HTTP
- **No network isolation**: hostPort 7780 is reachable from any pod on any node; no NetworkPolicy restricts access
- **Peer communication is open**: Daemon-to-daemon artifact fetching (`peers.go`) also uses plain HTTP with no auth
- **Risk**: A compromised or malicious pod could exfiltrate artifacts, inject poisoned data, or disrupt builds

## Design: Single HTTPS Port with Path-Based mTLS Enforcement

One HTTPS port (7780) using `tls.VerifyClientCertIfGiven`. Middleware enforces client cert per-route:

| Path | Client Cert Required | Clients | Rationale |
|------|---------------------|---------|-----------|
| `/healthz` | No | K8s kubelet probes | Probes can't do mTLS |
| `/resolve`, `/resolve-batch` | No | Init containers (BusyBox wget) | Same-node control signals; data flows via shared hostPath, not HTTP |
| `/artifacts/*`, `/register`, `/stream-in/*`, `/resource-caches/*` | **Yes** | ATC `DaemonClient`, peer `PeerResolver` | Cross-node data read/write — must be authenticated |

### Why path-based exemptions are acceptable

- **`/resolve` and `/resolve-batch`** are same-node control signals only. They tell the daemon to arrange files on the shared hostPath — the actual artifact data never flows over HTTP for these calls. An attacker would need to pass the NetworkPolicy AND know valid artifact keys AND have hostPath access to read anything.
- **`/healthz`** is a zero-information endpoint returning 200 OK.
- All exempt paths are protected by NetworkPolicy (Concourse-labeled pods only).
- Protected paths (`/artifacts/*`, `/stream-in/*`, `/resource-caches/*`, `/register`) handle cross-node data transfer and alias registration — these require full mTLS authentication.

### How init containers work with HTTPS

Alpine's BusyBox `wget` supports HTTPS (TLS-encrypted via OpenSSL/LibreSSL) but cannot present client certificates. The CA cert from the TLS Secret is mounted into init containers and `SSL_CERT_FILE` is set so wget verifies the daemon's server certificate. The only script change: `http://` → `https://`. Traffic is encrypted and server-authenticated; NetworkPolicy handles access control. Zero image changes, zero binary changes.

### Certificate management

Auto-generated via Helm:
- `genCA` creates a self-signed CA (persisted via `lookup` across upgrades)
- `genSignedCert` creates server cert (daemon) and client cert (ATC + peers)
- All stored in one K8s Secret: `ca.crt`, `tls.crt`, `tls.key`, `client.crt`, `client.key`
- Operator can override with `artifactDaemon.tls.existingSecret`

### Existing infrastructure reused

- `atc.DefaultTLSConfig()` (`atc/config.go:449-473`): Hardened cipher suites, TLS 1.2+
- `RunCommand.tlsConfig()` (`atc/atccmd/command.go:1673-1690`): mTLS server pattern
- `fly/rc/target.go:523-565`: Client-side cert loading pattern

## Requirements

1. Daemon HTTPS with `VerifyClientCertIfGiven` when TLS flags provided
2. Middleware requiring client cert on protected paths (`/artifacts/*`, `/register`, `/stream-in/*`, `/resource-caches/*`)
3. Exempt paths (`/healthz`, `/resolve`, `/resolve-batch`) accessible without client cert
4. ATC `DaemonClient` presents client cert for mTLS
5. Peer `PeerResolver` presents client cert for mTLS
6. Init container wget scripts switch to HTTPS with mounted CA cert for server verification
7. Helm auto-generates CA + certs; operator can provide their own
8. NetworkPolicy restricts daemon ingress to Concourse-labeled pods
9. SecurityContext on DaemonSet (RunAsNonRoot, drop capabilities, no privilege escalation)
10. All features opt-in — existing deployments without TLS flags continue on HTTP

## Technical Approach

### Key Files

| File | Role |
|------|------|
| `cmd/artifact-daemon/main.go` | Daemon startup, HTTP listener (lines 94-108) |
| `cmd/artifact-daemon/server.go` | HTTP handlers and mux (lines 54-68) |
| `cmd/artifact-daemon/peers.go` | Peer discovery and HTTP clients (lines 42-47) |
| `atc/worker/jetbridge/daemon_client.go` | ATC HTTP client to daemon (lines 37-39) |
| `atc/worker/jetbridge/config.go` | K8s runtime config (lines 161-170) |
| `atc/worker/jetbridge/storage_daemonset.go` | Init container wget scripts (lines 170-251) |
| `atc/atccmd/command.go` | ATC flags and wiring (lines 180-182, 1413-1427) |
| `atc/config.go` | `DefaultTLSConfig()` (lines 449-473) |
| `deploy/chart/templates/artifact-daemon-daemonset.yaml` | DaemonSet spec |
| `deploy/chart/templates/artifact-daemon-service.yaml` | Headless service |
| `deploy/chart/values.yaml` | Helm defaults (lines 434-460) |

## Acceptance Criteria

- [ ] Daemon serves HTTPS with mTLS when TLS flags are provided
- [ ] Protected endpoints reject requests without valid client cert (401)
- [ ] Exempt endpoints (`/healthz`, `/resolve`, `/resolve-batch`) work without client cert over HTTPS
- [ ] ATC DaemonClient presents client cert and connects over HTTPS
- [ ] Peer daemons present client certs for cross-node communication
- [ ] Init containers connect over HTTPS with server cert verified against mounted CA
- [ ] Helm auto-generates CA + certs stored in K8s Secret
- [ ] Operator can provide their own certs via `existingSecret`
- [ ] NetworkPolicy restricts daemon ingress to Concourse pods
- [ ] DaemonSet has SecurityContext (RunAsNonRoot, AllowPrivilegeEscalation=false, drop ALL)
- [ ] Daemon starts in HTTP mode when no TLS flags (backwards compatible)
- [ ] Health probes work with `scheme: HTTPS`

## Out of Scope

- Go binary init container for full mTLS on /resolve paths (future track)
- cert-manager integration for automatic rotation
- Encryption at rest for cached artifacts on disk
- Per-team or per-pipeline artifact isolation
- Artifact signing or integrity verification
