# Root cause — rca-jb-003

## The named root cause

`supervisorKillScriptTemplate` in `atc/worker/jetbridge/supervisor.go` signals the
supervised process group with an end-of-options separator before the negative
pid:

```sh
kill -TERM -- "-$pgid" 2>/dev/null
...
while [ "$n" -lt __GRACE_SECONDS__ ] && kill -0 -- "-$pgid" 2>/dev/null; do
...
kill -0 -- "-$pgid" 2>/dev/null && kill -KILL -- "-$pgid" 2>/dev/null
```

The `kill` **built-ins** of `dash` and `busybox ash` do not accept `--` before a
negative pid. dash reports

```
kill: Illegal number: -
```

and exits 2 without signalling anything. `bash`'s built-in does accept it.

So on any image whose `/bin/sh` is dash or busybox — which is every Debian-based
image, including the `golang:1.25-bookworm` rootfs the `k8s-runtime-tests` job
runs in — all three group operations fail as argument-parse errors:

1. the group `SIGTERM` never reaches the tree;
2. the grace-loop liveness probe `kill -0 -- "-$pgid"` fails, so the loop exits
   immediately and the script *believes* the group is already gone;
3. the escalation guard `kill -0 -- "-$pgid" && kill -KILL -- "-$pgid"` fails at
   the guard, so no `SIGKILL` is sent either.

Every one of those failures is swallowed by `2>/dev/null`, and the script's last
statement is an unconditional `exit 0`. The terminal-end kill therefore reports
success while killing nothing at all — a silent no-op. That is precisely the
outcome the terminal-end kill exists to prevent: an abandoned agent step keeps
running (burning unmetered API spend) until the pod reaper deletes the pod.

macOS `/bin/sh` **is** bash (3.2.57), whose built-in tolerates the separator, so
the identical script tears the tree down correctly on the developer's laptop.
That is the entire works-on-my-machine gap: one shell's built-in `kill` argument
parser.

## The decisive evidence

- The kill script exits 0 and prints nothing, yet the supervised process is
  untouched — not slow to die, but never signalled. A no-op that reports success
  points at a command whose failure is being discarded, not at a race.
- The two environments differ in exactly one thing that can change how the same
  script text behaves: `/bin/sh` is `dash` in CI and `bash` on the laptop.
  Everything else that plausibly differs (`/proc` vs `ps` for the pgid, BSD vs
  GNU userland, arch, parallelism, permissions) was checked and exonerated —
  and note that the pgid-extraction branch differs *per platform*, so it cannot
  explain a failure that follows the shell rather than the platform's procfs.
- Running the two forms by hand settles it:

  ```
  $ dash -c 'kill -0 -- "-999999"; echo exit=$?'
  dash: 1: kill: Illegal number: -
  exit=2
  $ dash -c 'kill -0 "-999999"; echo exit=$?'
  dash: 1: kill: No such process
  exit=1
  ```

  Without the separator dash parses the negative pid and reaches the kernel
  (`ESRCH` for a group that does not exist); with it, dash never gets that far.
  Under `bash` both forms behave identically, which is why no local run could
  ever show this.

## Why the unit specs did not catch it

`supervisor_test.go` and `process_test.go` assert the *text* of the generated
script, and at the pre-state they assert the buggy text:

```go
Expect(kill[2]).To(ContainSubstring(`kill -TERM -- "-$pgid"`))
Expect(kill[2]).To(ContainSubstring(`kill -KILL -- "-$pgid"`))
```

They are green in CI and locally. They pin the defect rather than detecting it —
a string assertion can only ever confirm that the code says what someone
believed it should say. Only `supervisor_script_test.go`, which *executes* the
script through the local `sh`, can see the behaviour, and it is the one that goes
red.

## The fix

Drop the `--` from all three group operations (`atc/worker/jetbridge/supervisor.go`):

```sh
kill -TERM "-$pgid" 2>/dev/null
while [ "$n" -lt __GRACE_SECONDS__ ] && kill -0 "-$pgid" 2>/dev/null; do
kill -0 "-$pgid" 2>/dev/null && kill -KILL "-$pgid" 2>/dev/null
```

`kill -SIG "-$pgid"` — no separator — parses and signals the group under dash,
busybox ash, bash 5 and macOS bash 3.2 alike, so it satisfies the script's
existing "POSIX sh built-ins only, busybox-compatible" contract. The reference
change also records the constraint in a doc comment above the template and
rewrites the two text assertions to pin the portable form *and* explicitly reject
the separator form:

```go
Expect(kill[2]).To(ContainSubstring(`kill -TERM "-$pgid"`))
Expect(kill[2]).To(ContainSubstring(`kill -KILL "-$pgid"`))
Expect(kill[2]).ToNot(ContainSubstring(`kill -TERM -- `))
Expect(kill[2]).ToNot(ContainSubstring(`kill -KILL -- `))
```

The supervisor script proper (`supervisorScriptTemplate`) is untouched: its kills
target a plain positive pid, which every shell parses the same way.

## Verification

Run `./atc/worker/jetbridge/...` in an image whose `/bin/sh` is dash (the CI
rootfs, or any Debian container). Before: `Ran 373 of 373`, one failure, the
terminal-end teardown spec timing out at `supervisor_script_test.go:152`. After:
373/373 green. The macOS run stays green throughout — it cannot distinguish the
two forms, which is exactly why it must not be the only place the suite is run.
