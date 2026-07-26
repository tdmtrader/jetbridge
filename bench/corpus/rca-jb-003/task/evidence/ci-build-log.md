<!--
Build output for the failing job and, for comparison, the same command run on the
developer's laptop. Both trimmed: the per-spec dot lines and the structured
lager JSON that the fake-runtime specs emit have been cut, nothing else.
-->

# `k8s-runtime-tests` — build output

## CI (failing)

Pipeline `jetbridge`, job `k8s-runtime-tests`, resource `repo` at
`44fdad0f64b079979b9573de8095ead6bbafff2b`.

Task config (from `deploy/borg-pipeline.yml`):

```yaml
- task: k8s-runtime-integration-tests
  config:
    platform: linux
    rootfs_uri: docker:///golang:1.25-bookworm
    inputs:
    - name: repo
    caches:
    - path: gopath/pkg/mod
    - path: gocache
    run:
      path: sh
      args:
      - -exc
      - |
        export GOPATH="$(pwd)/gopath"
        export GOCACHE="$(pwd)/gocache"
        cd repo

        echo "=== Running jetbridge full test suite ==="
        go test -v -count=1 -timeout 5m ./atc/worker/jetbridge/...
        echo "=== All jetbridge tests passed ==="
```

Output:

```
+ export GOPATH=/tmp/build/e55deab7/gopath
+ export GOCACHE=/tmp/build/e55deab7/gocache
+ cd repo
+ echo === Running jetbridge full test suite ===
=== Running jetbridge full test suite ===
+ go test -v -count=1 -timeout 5m ./atc/worker/jetbridge/...
=== RUN   TestJetbridge
Running Suite: Jetbridge Suite - /tmp/build/e55deab7/repo/atc/worker/jetbridge
==============================================================================
Random Seed: 1784972304

Will run 373 of 373 specs

... (371 specs elided) ...

------------------------------
Task exec supervisor script execution
  terminal-end kill tears down the still-running supervised process tree
  /tmp/build/e55deab7/repo/atc/worker/jetbridge/supervisor_script_test.go:110
  [FAILED] in [It] - /tmp/build/e55deab7/repo/atc/worker/jetbridge/supervisor_script_test.go:152 @ 07/16/26 23:41:30.733
• [FAILED] [10.119 seconds]
Task exec supervisor script execution [It] terminal-end kill tears down the still-running supervised process tree
/tmp/build/e55deab7/repo/atc/worker/jetbridge/supervisor_script_test.go:110

  [FAILED] Timed out after 10.001s.
  The matcher passed to Eventually returned the following error:
      <*errors.errorString | 0xc000a3c620>:
      Expected an error, got nil
      {
          s: "Expected an error, got nil",
      }
  In [It] at: /tmp/build/e55deab7/repo/atc/worker/jetbridge/supervisor_script_test.go:152 @ 07/16/26 23:41:30.733
------------------------------

Summarizing 1 Failure:
  [FAIL] Task exec supervisor script execution [It] terminal-end kill tears down the still-running supervised process tree
  /tmp/build/e55deab7/repo/atc/worker/jetbridge/supervisor_script_test.go:152

Ran 373 of 373 Specs in 37.120 seconds
FAIL! -- 372 Passed | 1 Failed | 0 Pending | 0 Skipped
--- FAIL: TestJetbridge (37.13s)
FAIL
FAIL	github.com/concourse/concourse/atc/worker/jetbridge	67.689s
FAIL
```

Notes on the failing spec, for anyone reading the log without the source open:

- `supervisor_script_test.go:146` runs the generated terminal-end kill script
  through the local shell and asserts it did not error. **That assertion is not
  the one that fails** — the script exits 0 and its combined output is empty.
- `supervisor_script_test.go:150-152` is the failing one. It polls
  `syscall.Kill(runnerPid, 0)` for 10 seconds expecting `ESRCH` (process gone).
  It gets `nil` every time: ten seconds after the terminal-end kill "succeeded",
  the supervised runner is still alive.
- The spec's supervised command is `sleep 60`, started under the supervisor with
  `Setsid: true` so its process group is isolated from the test runner.

## Developer laptop (passing)

Same commit, same package, same command, run by hand:

```
$ go test -v -count=1 -timeout 5m ./atc/worker/jetbridge/...
=== RUN   TestJetbridge
Running Suite: Jetbridge Suite - /Users/tm/src/concourse/atc/worker/jetbridge
=============================================================================
Random Seed: 1784971876

Will run 373 of 373 specs

... (373 specs elided) ...

Ran 373 of 373 Specs in 36.882 seconds
SUCCESS! -- 373 Passed | 0 Failed | 0 Pending | 0 Skipped
--- PASS: TestJetbridge (36.89s)
PASS
ok  	github.com/concourse/concourse/atc/worker/jetbridge	58.704s
```

Re-run with `-count=1` five times; green five times. Also green with
`--procs=1`, and green when the terminal-end specs are run in isolation
(`-ginkgo.focus='terminal-end kill'`).

## Related job status

| Job | Build | Result |
|---|---|---|
| `build-and-vet` | #612 | succeeded |
| `unit-tests` | #609 | succeeded |
| `k8s-runtime-tests` | #598 | **failed** (this log) |
| `k8s-runtime-tests` | #597 | failed, same spec, same assertion |
| `k8s-runtime-tests` | #596 | failed, same spec, same assertion |
