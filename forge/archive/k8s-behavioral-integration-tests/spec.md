# Spec: K8s Behavioral Integration Test Suite

## Overview

A comprehensive, mock-free behavioral integration test suite that validates Concourse CI's K8s-native runtime (JetBridge) from a user's perspective. Tests spin up a real Kubernetes environment (KinD), deploy Concourse, create pipelines via `fly`, and validate outcomes through `fly` CLI, Kubernetes APIs (`kubectl`), and the Concourse HTTP API.

The suite is organized by user-facing behavior — not by internal module — and includes assertions at the Kubernetes infrastructure layer (pod counts, resource allocations, container composition, cleanup) to catch resource leaks and excessive pod creation that could degrade a user's cluster.

## Requirements

1. **Real K8s environment** — All tests run against a KinD cluster with a deployed Concourse. No mocked Kubernetes clients. No fake API servers.
2. **User-facing verification** — Every test validates behavior observable through `fly` CLI, the Concourse API, or `kubectl`. Internal Go interfaces are never tested directly.
3. **Pipeline-driven** — Tests deploy real pipeline YAML via `fly set-pipeline`, trigger jobs via `fly trigger-job`, and observe results via `fly watch`, `fly builds`, and the events API.
4. **K8s infrastructure assertions** — Each test that creates pods also verifies: correct pod count, expected container composition (no extra sidecars on check pods, correct artifact-helper usage), resource allocations matching `container_limits`, proper labels/annotations, and full cleanup after build completion.
5. **Custom resource type depth** — Thorough coverage of type chains, image resolution paths (direct `image:` vs check/get cycle), operator overrides, cross-step image passing, and cross-job version flow — the area with the most observed pain points.
6. **No mocks** — No `counterfeiter` fakes, no in-process API servers, no simulated K8s. The only test doubles are lightweight resources (e.g., `mock` resource type) used as pipeline components.

## Acceptance Criteria

- All tests are runnable with a single command against a KinD cluster.
- Tests are organized into independent suites that can run in parallel at the suite level.
- Each test cleans up after itself (pipelines destroyed, pods verified gone).
- Test failures produce clear diagnostics: fly output, pod descriptions, event logs.
- K8s pod/container assertions are standard fixtures, not one-off checks — every test that creates a build verifies pod hygiene.

## Out of Scope

- Unit tests for individual Go packages (those exist separately).
- Web UI / Elm frontend testing.
- Agent-first workflow features (planned, not yet implemented).
- Multi-cluster worker support.
- Performance/load testing (this suite is correctness-focused).
- Credential manager integration tests beyond Kubernetes secrets (Vault, AWS SSM, etc. require external infrastructure).
