# Learnings

### 2026-02-27 [performance]

Root cause of integration test slowness identified: ATC scheduler polling interval.

The Concourse ATC has three relevant polling intervals:
1. `--component-runner-interval` (CLI flag, default 10s) - how often the runner checks if components should run
2. `--build-tracker-interval` (CLI flag, default 10s) - how often the tracker picks up started builds
3. Scheduler component interval (HARD-CODED at 10s in atc/atccmd/command.go:1192) - how often the scheduler picks up pending builds

The scheduler interval is NOT exposed as a CLI flag - it's stored in the DB `components` table and must be updated via SQL after deployment.

Measured impact: With all three at 2s, 5-test suite went from 57.6s to 21.5s (63% reduction). Per-test averages dropped from 11.5s to 4.3s.

Other findings:
- Fly CLI overhead: 25-130ms per call, ~300ms total per test - negligible
- PVC provisioning: one-time ~3s cost, amortized after first test
- Image preloading via crictl: saves ~4s per test (must use `crictl pull` inside KinD node, NOT `kind load docker-image` which fails with Docker Desktop multi-arch images)
