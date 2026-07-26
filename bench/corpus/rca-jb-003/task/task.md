# `k8s-runtime-tests` is red in CI and green on every dev machine we own

**Pipeline:** `jetbridge` (`deploy/borg-pipeline.yml`)
**Job:** `k8s-runtime-tests` — `go test -v -count=1 -timeout 5m ./atc/worker/jetbridge/...`
**Commit under test:** `44fdad0f64` (`fix(agent-step+web): pure-CI gateway token +
missing bad/error badge styles`)

## Symptom

Exactly one spec fails, every run, deterministically:

```
[FAIL] Task exec supervisor script execution [It] terminal-end kill tears down
the still-running supervised process tree
  repo/atc/worker/jetbridge/supervisor_script_test.go:152
```

The full job output is in [`evidence/ci-build-log.md`](evidence/ci-build-log.md).

The identical command on the same commit is green on the developer's laptop:
`373 of 373 Specs`, `ok github.com/concourse/concourse/atc/worker/jetbridge`.
Both runs are attached.

## What is already known

- **It is not a flake and not a recent regression.** It fails on every CI run,
  at the same assertion, with the same 10s `Eventually` timeout. The commit under
  test does not touch `atc/worker/jetbridge/` at all — the failing spec and the
  production code it exercises both landed together on 2026-07-12 in
  `3b81488ef1` (*"Timed-out/aborted agent step: claude keeps running under the
  supervisor"*), and this job has been red on it ever since.

- **Nothing reports an error.** The spec runs the terminal-end kill at
  `supervisor_script_test.go:146` and asserts it succeeded:

  ```go
  kill := supervisorKillCommand(stateID, spec, 5)
  out, err := exec.Command(kill[0], kill[1], kill[2]).CombinedOutput()
  Expect(err).ToNot(HaveOccurred(), "kill script failed: %s", out)
  ```

  That assertion **passes** in CI. The kill exits 0 and prints nothing. The
  failure is six lines later, at `:150-152`, waiting for the supervised runner
  process to disappear — it never does, so the supervised `sleep 60` is still
  running when the spec gives up.

- **The unit specs that cover the same code are green in CI.**
  `atc/worker/jetbridge/supervisor_test.go` and `process_test.go` both assert the
  content of the generated kill script and both pass, in CI and locally.

- Environment facts gathered from both machines are in
  [`evidence/environment.md`](evidence/environment.md).

## What is being asked

1. **Root cause.** Explain why the terminal-end kill tears the supervised process
   tree down on the developer's machine and does nothing in CI, and name the
   decisive evidence. If the conclusion is that the production code is fine and
   the fault is elsewhere, say so and argue it — a negative result is acceptable,
   an unargued one is not.
2. **Fix it**, as small a change as can actually be effective end to end.
3. **State how the fix will be confirmed** on the next CI run: what will appear,
   or stop appearing, that did not this time.

## Constraints

- The terminal-end kill must keep doing its job: signal the supervised process
  group, wait out the grace period, escalate if the group is still alive. It
  exists because a timed-out or aborted agent step that keeps running burns real
  API dollars, invisibly, until the pod reaper eventually deletes the pod.
- **Do not weaken the failing spec.** Lengthening the `Eventually` timeout,
  skipping it on some platforms, deleting the assertion, or otherwise making the
  red go away without changing behaviour is not a fix.
- The supervisor scripts run inside arbitrary user task images. Their existing
  contract, stated in the code's own doc comments, is *"needs only POSIX sh
  built-ins plus cat/sed/cut/sleep (busybox-compatible)"*. A fix that requires a
  particular interpreter or a non-POSIX utility to be present in the task image
  breaks that contract and is not acceptable.
- Do not change the exported Go API of `atc/worker/jetbridge`, and do not touch
  the resume/park behaviour of the supervisor itself (the web-restart takeover
  path is separately specced and must stay green).

## Deliverable

A short written diagnosis — root cause, the decisive evidence, the mechanism,
the fix, and the verification plan — plus the change itself. If more than one
hypothesis survives the evidence, rank them and name the single observation that
would separate them.

Write the diagnosis to `DIAGNOSIS.md` at the repository root so it travels with
the change; a summary in the reply is fine too, but the file is what gets read.
