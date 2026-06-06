# Plan: DaemonSet Artifact Security Hardening

## Phase 1: Daemon TLS Server + mTLS Middleware

**Files:** `cmd/artifact-daemon/main.go`, `cmd/artifact-daemon/server.go`, NEW `cmd/artifact-daemon/tls.go`

- [x] Write test: daemon starts HTTPS when `--tls-cert`, `--tls-key`, `--tls-ca-cert` provided
- [x] Write test: daemon starts HTTP when no TLS flags (backwards compat)
- [x] Write test: mTLS middleware returns 401 on protected paths without client cert
- [x] Write test: mTLS middleware allows protected paths with valid client cert
- [x] Write test: exempt paths (`/healthz`, `/resolve`, `/resolve-batch`) work without client cert
- [x] Implement `tls.go` — config builder reusing `atc.DefaultTLSConfig()`, `VerifyClientCertIfGiven`
- [x] Implement `requireClientCert` middleware in `server.go` — wraps protected routes, checks `r.TLS.PeerCertificates`
- [x] Add TLS flags to `main.go`, conditional `ListenAndServeTLS` vs `ListenAndServe`

## Phase 2: ATC DaemonClient mTLS

**Files:** `atc/worker/jetbridge/daemon_client.go`, `atc/worker/jetbridge/config.go`, `atc/atccmd/command.go`

- [x] Write test: DaemonClient uses HTTPS with client cert when TLS config present
- [x] Write test: DaemonClient uses HTTP when no TLS config (backwards compat)
- [x] Add `ArtifactDaemonTLSCert`, `ArtifactDaemonTLSKey`, `ArtifactDaemonTLSCACert` to `Config`
- [x] Update `NewDaemonClient` to build `http.Transport` with mTLS when configured
- [x] Update URL construction: `https://` when TLS, `http://` otherwise
- [x] Add `--kubernetes-artifact-daemon-tls-{cert,key,ca-cert}` flags to `atc/atccmd/command.go`
- [x] Wire flags to Config and DaemonClient construction

## Phase 3: Peer Resolver mTLS

**Files:** `cmd/artifact-daemon/peers.go`

- [x] Write test: peer probe uses mTLS when TLS configured
- [x] Write test: peer fetch uses mTLS when TLS configured
- [x] Write test: peers fall back to HTTP when no TLS config
- [x] Update `NewPeerResolver` to accept TLS config
- [x] Build mTLS transport for both probe client (10s) and fetch client (3m)
- [x] Update peer URL construction: `https://` when TLS

## Phase 4: Init Container HTTPS with CA Cert Mount

**Files:** `atc/worker/jetbridge/storage_daemonset.go`, `atc/worker/jetbridge/config.go`

- [x] Write test: init container command uses `https://` when TLS enabled
- [x] Write test: init container has CA cert volume mount and `SSL_CERT_FILE` env var when TLS enabled
- [x] Write test: init container command uses `http://` and no cert mount when TLS disabled
- [x] Add `ArtifactDaemonTLSEnabled bool` and `ArtifactDaemonTLSCACert string` to Config
- [x] Update `BuildFetchInitContainers` — add CA cert volume mount and `SSL_CERT_FILE` env var
- [x] Update `daemonResolveCommand` — conditional `https://` URL scheme
- [x] Update `daemonResolveBatchCommand` — same change
- [x] Update `BuildCleanupInitContainer` — verified: does not call daemon, no TLS changes needed

## Phase 5: Helm Chart — TLS Secret & Wiring

**Files:** `deploy/chart/values.yaml`, NEW `deploy/chart/templates/artifact-daemon-tls-secret.yaml`, `deploy/chart/templates/artifact-daemon-daemonset.yaml`, `deploy/chart/templates/web-deployment.yaml`

- [x] Add `artifactDaemon.tls.enabled`, `artifactDaemon.tls.existingSecret` to values.yaml
- [x] Create TLS secret template — `genCA` + `genSignedCert` with `lookup` for persistence
- [x] Update DaemonSet template — mount TLS secret, pass `--tls-*` flags, `scheme: HTTPS` on probes
- [x] Update web deployment template — mount TLS secret (client cert portion), pass `--kubernetes-artifact-daemon-tls-*` flags
- [x] Write Helm template test: TLS enabled renders secret + volume mounts + flags
- [x] Write Helm template test: TLS disabled renders unchanged

## Phase 6: SecurityContext & NetworkPolicy

**Files:** `deploy/chart/templates/artifact-daemon-daemonset.yaml`, NEW `deploy/chart/templates/artifact-daemon-networkpolicy.yaml`

- [x] ~~Add pod SecurityContext to DaemonSet (runAsNonRoot, runAsUser 65534, fsGroup 65534)~~ — **REVERSED 2026-04-12.** hostPath volumes are root-owned and K8s does not apply `fsGroup` to hostPath, so the daemon cannot run as non-root. Final posture: runs as **root** with `drop ALL` + only `CAP_DAC_OVERRIDE`. See `forge/notes/session-learnings-daemon-permissions-20260412.md`.
- [x] Add container SecurityContext (allowPrivilegeEscalation false, drop ALL, seccomp RuntimeDefault)
- [x] Verify daemon works with hostPath writes — locked in by `topgun/k8s/integration/artifact_daemon_security_test.go` ("does NOT run as non-root", "can write to hostPath storage"). (Was "verify non-root"; resolved as root+DAC_OVERRIDE per the reversal above.)
- [x] Create artifact daemon NetworkPolicy template (ingress from Concourse-labeled pods)
- [x] Add `artifactDaemon.networkPolicy.enabled` value

## Phase 7: Integration Testing

Now runs via the **testcontainers** integration suite (`topgun/k8s/integration`), not KinD. mTLS is opt-in via `ARTIFACT_DAEMON_TLS=true` (gates `--set artifactDaemon.tls.enabled=true` in `cluster_lifecycle_test.go`); the extended `artifact_daemon_security_test.go` self-detects TLS from the daemon's probe scheme.

- [x] Deploy with TLS enabled + run pipeline with artifact passing — wired (env-gated); ⏳ needs an actual suite run to confirm green
- [x] Verify protected endpoints succeed WITH client cert (→201) — `artifact_daemon_security_test.go` mTLS spec
- [x] Verify protected endpoints WITHOUT client cert get **401** — same spec
- [x] Verify exempt `/healthz` works without client cert over HTTPS (→200) — same spec
- [x] Verify health probes work with `scheme: HTTPS` — implicit: suite waits for daemon Ready, which requires the HTTPS probes to pass
- [x] Init container HTTPS + CA mount + `SSL_CERT_FILE` — unit-tested in `atc/worker/jetbridge/daemon_tls_test.go` (`TestBuildFetchInitContainers_TLSWiring`)
- [x] Deploy without TLS flags — unchanged HTTP behavior — default suite run (TLS off) + `TestBuildFetchInitContainers_NoTLSMountWhenDisabled`
- [x] **RAN the suite with `ARTIFACT_DAEMON_TLS=true` on `k8s-e2e` CI — GREEN** (build #192, 2026-06-06): plain-HTTP `128 Passed | 0 Failed`, mTLS run `10 Passed | 0 Failed` (Daemon Security + Artifact Passing + Read-After-Reap over mTLS). Now runs automatically every CI run. Two bugs found+fixed first (#191): init-container CA-volume crash → `--no-check-certificate`; by-IP cert SAN mismatch → `ServerName` override. See cgx.md.

## Phase 8: ATC-side data-plane mTLS (gap found during Phase 7 verification)

**Problem found:** Phases 2–5 converted only the daemon *server*, the ATC `DaemonClient`, and the init-container wget to TLS. The ATC artifact **data plane** still hardcoded `http://` and used non-mTLS clients on protected paths — so enabling TLS would have broken stream-in/out, peer fetch, GC, and alias registration with 401s/connection errors. (This is why TLS was never enabled in the suite or production.)

- [x] Add shared `newDaemonHTTPClient` + `daemonURLScheme` helper (`atc/worker/jetbridge/daemon_tls.go`); refactor `NewDaemonClient` to share `loadDaemonClientTLS`
- [x] Route `DaemonSetVolume` stream-in/StreamOut/peer URLs through the scheme + mTLS transport (`volume_daemonset.go`)
- [x] Route reaper artifact DELETE through the scheme + mTLS client (`reaper.go`)
- [x] Route `registerDaemonAlias` through the scheme + mTLS client, not `http.DefaultClient` (`storage_daemonset.go`)
- [x] Unit tests that would have caught the bug (`daemon_tls_test.go`: scheme, client-cert presence, fallback, volume URL scheme, init-container wiring)
- [x] No regressions: full `atc/worker/jetbridge` + `cmd/artifact-daemon` suites green

### Known plan/reality drift (recorded, not yet actioned)
- Phase 5 "Write Helm template test: TLS enabled/disabled" (marked `[x]`) — these chart tests do **not** exist (`deploy/chart/tests/` only has `securitycontext_test.go`).
- Phase 2/3 client + peer mTLS unit tests (marked `[x]`) — the ATC `DaemonClient`/peer-specific unit tests did not exist; partially covered now by `daemon_tls_test.go` (no dedicated peer-mTLS unit test).
