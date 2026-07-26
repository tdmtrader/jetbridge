<!--
Environment facts collected from both machines while triaging the red
`k8s-runtime-tests` job, plus the small set of live observations made inside the
CI image. Raw notes, lightly formatted.
-->

# Environment comparison — CI vs. developer laptop

## The two environments

| | CI task container | Developer laptop |
|---|---|---|
| where | Concourse task, `rootfs_uri: docker:///golang:1.25-bookworm` | bare metal |
| platform | linux / amd64 | darwin / arm64 |
| base OS | Debian GNU/Linux 12 (bookworm) | macOS 15.6 |
| Go | go1.25 linux/amd64 (image toolchain) | go1.25.6 darwin/arm64 |
| `ls -l /bin/sh` | `/bin/sh -> dash` | `/bin/sh` (a real file, not a symlink) |
| `sh --version` \| head -1 | *(no `--version`; prints usage to stderr)* | `GNU bash, version 3.2.57(1)-release (arm64-apple-darwin24)` |
| userland | GNU coreutils / util-linux | BSD userland (`ps`, `sed`, `cut` are the BSD variants) |
| `/proc` mounted | yes | no (no procfs on macOS) |
| container runtime | containerd / runc, default namespaces | n/a |
| pid 1 in the task container | the task's `sh -exc` | n/a |
| ginkgo | serial, one process (`go test` with no `-p`) | same |
| module cache | task cache mount, warm | warm |

`sh` resolves through `PATH` in both environments and lands on `/bin/sh` in
both; nothing shadows it earlier in `PATH`.

## Live observations inside the CI image

Reproduced by running the same suite in a `golang:1.25-bookworm` container by
hand (`docker run -v $PWD:/src -w /src golang:1.25-bookworm go test ...`), which
fails identically to the pipeline:

1. The terminal-end kill script **exits 0** and writes nothing to stdout or
   stderr. `Expect(err).ToNot(HaveOccurred(), "kill script failed: %s", out)` at
   `supervisor_script_test.go:146` passes, with `out` empty.

2. The supervised runner is untouched. Ten seconds after the "successful" kill
   it is still in `ps`. Left alone past the spec, the supervised `sleep 60`
   **runs to completion normally** and the supervisor records its exit code in
   the state dir's `exit` file — i.e. the terminal-end kill had *no effect at
   all*, rather than being slow or partially effective.

3. `cat /proc/<runner-pid>/stat` inside the container returns a normal line, and
   the field the script extracts for the process-group id is a plausible,
   non-empty number matching what the kernel reports for that process. On the
   laptop there is no `/proc`, so the script's `ps -o pgid=` fallback is the
   branch that runs there; both branches were checked by hand and both yield the
   right pgid on their own platform.

4. Nothing in the container is missing: `cat`, `sed`, `cut`, `sleep`, `tail`,
   `mv`, `ps`, `kill` all resolve and behave.

## Things checked and ruled out

- **Not a build-freshness problem.** The task compiles from the `repo` input at
  the commit under test; `go test` recompiles from source every run
  (`-count=1`), and the failure survives a cleared `gocache`.
- **Not a timing/slow-machine problem.** See observation 2: the process is not
  slow to die, it never dies. Raising the timeout to 60s locally in the container
  changes nothing except how long the failure takes.
- **Not parallelism.** The suite runs serially in both places; `--procs=1` on the
  laptop is still green, and the container is still red.
- **Not the isolated-session setup.** The spec's `Setsid: true` succeeds in the
  container (verified: the supervisor `sh` is its own session and group leader,
  and its group id differs from the test runner's).
- **Not a permissions problem.** The task container runs as root; the runner
  process is owned by the same uid as the process running the kill script.
