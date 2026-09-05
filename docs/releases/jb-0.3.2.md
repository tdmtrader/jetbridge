# JetBridge 0.3.2 (in progress)

Running notes for the next release; rewritten into full notes at cut time.

## Fixes

- `fly abort-build` works again. The engine's abort watcher checked once and never read the build's `aborted` flag up front (a regression from cdbc18fe95, which removed `conditionNotifier`), so an abort issued while no wake-up was pending — or while no web tracked the build — was silently lost and the build ran to a normal completion. The watcher now reads the flag before the plan starts and re-reads on every notification, through a query that does not rewrite the in-memory build (060ae44993, 4fdb63fa12).
- A supervised task step's pod is torn down when its step context ends. Task steps run under an in-pod supervisor that deliberately survives the exec stream, and the reaper only collects pods that recorded an exit status, so an aborted or timed-out task kept running — including privileged workloads such as a nested `dockerd`. The pod is now deleted with a zero grace period; get, put and check pods, and containers reached through `fly hijack`, are unaffected (2a9355e1e6).
- The Postgres notification listener now emits the disconnect notice the notifications bus reconnect path was written for, so a `NOTIFY` lost across a reconnect triggers a rescan instead of being unrecoverable (e8a9dff094).

## Behaviour changes

- A task step that times out or is aborted no longer leaves its pod behind, so `fly hijack` into it afterwards is not possible; hijack a running step instead. This is what makes the runaway command actually stop.
- Note on `timeout:` with `attempts:`: `timeout` is a task field and `attempts` wraps the task, so each attempt gets the full timeout and a timed-out attempt is retried. This is stock Concourse behaviour and unchanged.

## CI and build

- `build-image` builds the runtime base (`jetbridge-base:jammy-<hash>`) once per change to its Dockerfile instead of running apt on every build (8ecbf14430).
- The `k8s-e2e` pipeline (container-based K8s tier) tracks `core` again; every `dockerd` it starts inside a pod runs with `--mtu=1450`, and its apt steps retry (7275310445, a8d66711b0, d4ddf1d3b6).
- `build-and-vet` compiles the `-tags live` suites so API drift in them is caught immediately (06d3c556b1).
- The `cmd/concourse` Web Command suite gives the web thirty seconds to exit in `AfterEach` (f0bb15c6df).

## Operator notes

- The CI node needs `fs.inotify.max_user_instances` above Ubuntu's default 128 (home-infra pins 1024); at 128 the kubelet fails every follow-logs stream, which drops sidecar logs and breaks testcontainers' readiness waits.
