# Brine test performance

The changes reduce repeated fixture startup and guard compilation work while
retaining serial execution, all scenarios and assertions, real dependencies,
resource disposal, and existing failure deadlines.

## Changes

- Probe real daemon readiness after 1 ms initially, doubling up to the existing
  100 ms interval. Keep the same HTTP/TLS readiness check and 20-second deadline;
  observe process exit during the wait.
- Memoize immutable step-registry lookups by exact sentence within each guard
  invocation. Still read every feature freshly and check every occurrence,
  definition, ambiguity, and diagnostic.
- Compile a fresh guard test binary once per adapter protocol suite, then execute
  it at every launcher invocation. No guard result or feature read is cached.
  Direct execution retains Go's original ten-minute guard timeout. Ordinary
  launcher invocations continue to use `go test`.

## Measured results

Measurements used Linux amd64, Go 1.25.6, GOMAXPROCS=1, one Ginkgo process, and
Brine at pinned revision `1e9345da594d`. The benchmark baseline was `core` at
`d01185fbc`, plus the [unit optimizations](serial-go-tests.md). These are
historical measurements; this branch applies the same optimizations to the
newer `core` revision `08e8be2aff81a34a09e6583b666885bc7efa486b`.

| Selection | Baseline | Optimized | Saving |
|---|---:|---:|---:|
| Full local Brine, first order | 648.563 s | 544.789 s | 103.774 s (16.00%) |
| Full local Brine, reverse order | 643.998 s | 558.228 s | 85.770 s (13.32%) |
| Four native Brine packages, protocol optimization | 222.439 s | 113.501 s | 108.938 s (48.97%) |
| Native confirmation with explicit guard deadline | 203.424 s | 108.114 s | 95.310 s (46.85%) |

All four local behavioral runs passed the same 1,182 scenarios and 6,870 steps,
with completed resource drains. Native comparisons passed the same 255 records,
including subtests. Malformed-corpus checks preserved refusal of repeated
undefined sentences, an empty corpus, and unused definitions. Protocol checks
also confirmed that edits to a feature after compilation are read and rejected.
No scenario, assertion, retry policy, or failure deadline was weakened.

Local manifests deliberately select only the original local features. Running
from the module root also discovers nested live manifests, so these local
numbers must not be described as timings of the full coverage gate.

## Live scope and limitations

The full local-plus-live comparison passed 1,316 scenarios and 7,548 steps on
both sides, but took 2,007.348 versus 2,044.725 seconds (1.86% slower).
Live scenario time alone was 1,280.633 versus 1,420.029 seconds: about 21–24
minutes. This comparison included an isolated gzip experiment that is not part
of this branch, so it is not a timing measurement of this exact patch.

Both runs passed the unchanged 50% production-statement coverage gate. Recorded
coverage was 2,999/3,707 (80.901%) versus 2,996/3,707 (80.820%); exact statement
hits differed. Both completed all resource drains and left no owned Brine
namespaces. Eight kubelet scenarios and one real-subscription scenario were
outside this comparison. There is no full-live speedup claim or claim that the
additional 10% target across the remaining test estate has been achieved.

## Evidence

Historical logs and audits are in `/tmp/jetbridge-remainder-performance/`, with
cluster evidence in its `cluster/` directory. The full local comparisons use
`compare-brine.py` and `repeat-brine.py`; `comparison-audit.json`,
`repeat-audit.json`, and `completed-drain-audit.json` record inventory, outcome,
and cleanup checks. `cluster/native-tagged-evidence.tar.gz` preserves the
native confirmation and guard mutation audit; `cluster/completed-brine-evidence.tar.gz`
preserves both full coverage-gate reports. These paths are local measurement
artifacts, not repository dependencies.

## Validation after updating the branch

On top of `core` at `08e8be2af`, all four native Brine packages passed the same
255 records with zero skips. The full local selection passed all 1,190 scenarios
and 6,915 steps across 96 features, including the additional scenarios from
the newer core revision. Every scenario completed its resource drain with no
partial drain; the independent process check found no surviving executables
from the test checkout or its task-owned temporary directory. All eight changed
source/test files retained their measured hashes throughout validation.

Validation used the pinned toolchain and dependencies above, fresh adapter and
artifact-daemon builds, an isolated local-only manifest derived from `.brine`,
and one Ginkgo process. Native tests ran with the real private-network launcher:

```sh
BRINE_PROTOCOL_LAUNCHER="$PWD/scripts/run-private-network" \
  GOMAXPROCS=1 GOTOOLCHAIN=go1.25.6 ginkgo -r \
  --procs=1 --compilers=2 --flake-attempts=1 --seed=20260925 -- -test.v=true
```

Run this from the Brine module after `sh scripts/build-private-network`, with
the real BusyBox, envtest, and telemetry prerequisites configured. The local
behavioral command is `brine run --no-engine --mode sync --format jsonl` from
the isolated manifest directory. The local check took 560.884 seconds; it has
no matching updated-baseline timing and establishes correctness, not a new
percentage improvement. Commands, logs, source hashes, and the completed audit
are under `/tmp/jetbridge-test-performance-push-checks/`.
