# CGX: DaemonSet Artifact Security Hardening

## Session Notes

_Record friction, surprises, and workflow observations here during implementation._

### 2026-06-06 — "cluster verification" surfaced an incomplete data plane

Picked this up as a verification task; it became a fix+verify task.

**Surprise 1 — the plan over-reported completeness.** Several Phase 2–5 tasks were
marked `[x]` but the artifacts didn't exist: the ATC `DaemonClient`/peer/init-container
mTLS *unit tests*, and the Phase 5 Helm TLS template tests. Only the daemon-*server*
TLS tests (`cmd/artifact-daemon/tls_test.go`) were real.

**Surprise 2 (the big one) — mTLS data plane was broken.** Enabling
`artifactDaemon.tls.enabled=true` would have broken artifact passing. The daemon
server, ATC `DaemonClient`, and init-container wget were converted to TLS, but the
ATC **data plane** still hardcoded `http://` + plain `http.Client` on protected
paths: `registerDaemonAlias` (storage_daemonset.go), reaper DELETE (reaper.go),
and stream-in / StreamOut / peer-fetch URLs (volume_daemonset.go). All would have
hit 401 / scheme-mismatch. This is why TLS was never enabled in the suite or prod.

**Fix:** shared `newDaemonHTTPClient` + `daemonURLScheme` (`daemon_tls.go`); routed
all 6 call sites through them; added `daemon_tls_test.go` (the regression net);
gated suite TLS behind `ARTIFACT_DAEMON_TLS=true` + extended the security test with
in-cluster mTLS assertions. Full jetbridge + daemon suites green.

**Verification surprises:**
- The daemon server cert SANs include `127.0.0.1` + `localhost` — deliberately, so
  port-forward-based HTTPS probing works from the test host.
- `http.DefaultTransport.Clone()` carries a non-nil (empty) `TLSClientConfig`; assert
  on `len(Certificates)`, not nil-ness.
- Non-root securityContext (Phase 6) was **reversed** on 2026-04-12 (hostPath is
  root-owned; `fsGroup` doesn't apply) — runs as root + `CAP_DAC_OVERRIDE`. The plan
  still listed "verify non-root" as pending; reconciled.

**Env blocker:** Colima was stopped (no Docker), and local Colima runs of the
testcontainers K3s suite are recorded as flaky (namespace errors). The TLS suite run
(`ARTIFACT_DAEMON_TLS=true`) is staged but still needs an actual run — locally if
Colima cooperates, else via the `k8s-e2e` CI pipeline.

### 2026-06-06 (cont.) — CI run (k8s-e2e #191) found two real TLS bugs

Ran on the live `k8s-e2e` pipeline (`ARTIFACT_DAEMON_TLS=true`). Plain-HTTP suite
passed 128/0; the mTLS run failed 5/10 — and the failures were **real bugs the unit
tests couldn't see**, all rooted in one fact: **the daemon is addressed by IP (pod IP
for ATC Go clients, node IP for init containers), but the server cert only covers the
service DNS + 127.0.0.1/localhost.**

1. **Init-container pod invalid** — `volumeMounts[N].name: Not found:
   "artifact-daemon-tls-ca"`. Phase 4 added the CA *mount* to the init container but
   never added the *Volume* to the pod → every task pod was rejected by the API
   server. Fix: the init container can't verify a node-IP host anyway, so it now uses
   `wget --no-check-certificate` (still TLS-encrypted; `/resolve` is exempt + same-node
   + NetworkPolicy-protected, data flows via hostPath). Removed the CA mount entirely.
2. **mTLS by pod IP failed verification** — `x509: certificate is valid for 127.0.0.1,
   not 172.17.0.3`. Fix: set `tls.Config.ServerName` to `<service>.<ns>.svc` (a cert
   SAN) in both `newDaemonHTTPClient` and `NewDaemonClient`, so by-IP dials verify
   against the cert's service-DNS name. Confirmed the chart's `--kubernetes-artifact-
   daemon-service` flag and the cert SAN are the identical string.

My security/enforcement spec (no-cert→401, cert→201, /healthz→200) **passed** —
port-forward uses 127.0.0.1, which is a cert SAN — so the daemon mTLS server + client
cert handling were already correct. Lesson: unit tests that assert on a *container's*
volumeMounts don't catch a missing *pod* Volume; only an in-cluster run does.
