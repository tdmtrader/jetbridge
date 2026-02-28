# Learnings

### 2026-02-28 [anti-pattern]

- [2026-02-27] k3s via testcontainers-go is NOT viable for Concourse K8s tests on macOS. Containerd sandbox instability (SandboxChanged errors, CrashLoopBackOff) in nested Docker environments. Tested with both 8GB and 16GB Colima — same failures. Root cause: containerd-in-docker architecture problem, not memory. References: k3s-io/k3s#11315, containerd/containerd#10848.

### 2026-02-28 [good-pattern]

- [2026-02-27] KinD Go library (sigs.k8s.io/kind/pkg/cluster v0.31.0) is a drop-in replacement for the kind CLI. Eliminates binary dependency while keeping proven runtime stability. 295/296 tests passed, suite time within 1% of CLI baseline. Only docker, helm, kubectl needed on PATH.
