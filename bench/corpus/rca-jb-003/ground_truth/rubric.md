# Rubric — rca-jb-003

Two graded surfaces: the **change** (mechanical, decisive) and the **diagnosis**
(judge). Score them separately; a change that passes the gate without a correct
diagnosis is a partial pass, and a correct diagnosis with no working change is
also a partial pass.

## A. The change — mechanical

The gate is `case.yaml#grading.fail_to_pass`: the pre-existing behavioural spec
`Task exec supervisor script execution / terminal-end kill tears down the
still-running supervised process tree` must go from red to green **in an
environment whose `sh` is dash**. It is unchanged between pre-state and
ground truth, so it is a real oracle, not a test written to fit the fix.

`case.yaml#grading.preflight` runs first and must pass. If it does not, the
environment cannot express this case (a bash `sh` makes pre-state and ground
truth indistinguishable) and the run is **errored, not scored** — no partial
credit, no "mechanical: pass".

Must:

- [ ] Change the **group-signalling form** in `supervisorKillScriptTemplate`
      (`atc/worker/jetbridge/supervisor.go`) so that all three group operations —
      the TERM, the grace-loop `kill -0` liveness probe, and the KILL escalation
      guard + escalation — work under a dash/busybox built-in `kill`. The
      reference does this by dropping the `--` separator. Any form that actually
      signals the group on dash, busybox ash and bash is acceptable.
- [ ] Fix **all three** call sites. Fixing only the TERM leaves the grace loop
      exiting immediately and the escalation guarded by a failing probe — the
      spec may still pass by luck of timing, so check the diff, not only the gate.
- [ ] Leave the terminal-end contract intact: group TERM → wait out
      `__GRACE_SECONDS__` → escalate to KILL, idempotent, exit 0 when the command
      never started or already finished.

Must not:

- [ ] Weaken, skip, retag or delete `supervisor_script_test.go`'s terminal-end
      specs, or lengthen their timeouts. `supervisor_script_test.go` must be
      byte-identical to its pre-state content.
- [ ] Introduce a dependency on a specific interpreter (`bash`, `#!/bin/bash`,
      `exec bash -c …`) or on a non-POSIX/non-busybox utility (`pkill`, `kill
      --timeout`, `setsid --`, `timeout`, GNU-only flags). The scripts run inside
      arbitrary user task images; the existing doc comment states the contract.
- [ ] Move the teardown out of the in-pod script into Go-side signalling that
      cannot reach the pod's process tree (e.g. killing the exec session, or
      deleting the pod) — that changes the semantics the spec is guarding.
- [ ] Change the exported API of `atc/worker/jetbridge`, or touch
      `supervisorScriptTemplate`'s resume/park behaviour (the web-restart takeover
      specs must stay green).

Credit, not required:

- [ ] Updates the text assertions in `supervisor_test.go` / `process_test.go`,
      which at pre-state pin the **buggy** string and would otherwise go red
      against a correct fix. A strong answer both updates them and adds a
      negative assertion so the old form cannot come back.
      **Grading note:** these two specs are excluded from
      `case.yaml#grading.pass_to_pass` precisely because they flip against any
      correct fix. Leaving them red is a quality deduction here, never a
      mechanical failure — and grade this item from the agent's own diff, before
      the reference-form discriminator overlays those files.
- [ ] Leaves a comment at the template explaining the portability constraint.

### Alternative-form adjudication

The reference drops the `--`, and the reference test assertions pin that exact
literal (`kill -TERM "-$pgid"`). The rubric does **not**. Any script that
actually signals the process group under dash, busybox ash and bash satisfies the
"Must" item above — for example negating the pgid into its own variable and
signalling `"$target"`, or any other construct that never passes `--` to a
built-in `kill` before a negative pid.

So when `case.yaml#grading.anti_gaming`'s reference-form discriminator fails,
that is a signal to adjudicate, not a verdict. Adjudicate by behaviour:

1. `fail_to_pass` passed under a dash `sh` (the group really is torn down).
2. Read the produced script: all three group operations — TERM, the grace-loop
   `kill -0` probe, and the KILL escalation — use the same portable construct.
3. Run the produced script's kill form under `busybox ash` and under `bash`; it
   must signal the group in both (no built-in parse error, no fallback to
   signalling only the leader pid when the group was intended).

Pass all three and the change gets full credit on the "Must" items regardless of
how it differs from `reference.diff`.

## B. The diagnosis — judge

**Where to read it.** `task.md` asks for `DIAGNOSIS.md` at the repository root.
Judge whatever the agent actually produced — that file, the run's final message,
or a commit message — and do not deduct for placement; deduct only if there is no
written diagnosis at all. If the diagnosis lands in `DIAGNOSIS.md`, that file is
an expected untracked/added artifact, not a stray edit: it must not be counted
against the "small change" reading, and it is irrelevant to every mechanical
command in `case.yaml#grading`.

Full credit requires all of:

- [ ] Names the **shell built-in** as the variable: `/bin/sh` is dash in the CI
      image and bash on macOS, and the two built-in `kill`s parse the argument
      list differently.
- [ ] Names the **specific construct**: the `--` end-of-options separator before
      the negative (process-group) pid, which dash/busybox reject.
- [ ] Explains the **silence**: `2>/dev/null` discards the parse error and the
      script ends in an unconditional `exit 0`, so the failure is invisible and
      the caller sees success. Bonus for noting that this is why the spec's own
      `kill script failed` assertion passes.
- [ ] Explains that **all three** group operations fail, and what each failure
      does — no TERM delivered, grace loop exits at once, escalation guard never
      fires — rather than treating it as a single missed signal.
- [ ] Explains why the local run cannot see it (bash accepts the separator), and
      therefore why "green locally" was never evidence.

Partial credit:

- [ ] Correctly localises to the kill script and the dash/bash difference but
      misattributes the mechanism (e.g. blames quoting of `"-$pgid"`, or claims
      dash cannot signal process groups at all).
- [ ] Reaches the right fix empirically (bisecting the script under dash) without
      articulating the built-in argument-parsing rule.

No credit / actively wrong:

- [ ] Concludes the production code is correct because `supervisor_test.go` and
      `process_test.go` assert the same string the code produces. This is the
      case's main trap: those assertions pin the defect. An answer that cites
      them as evidence of correctness has been captured by it.
- [ ] Blames the `/proc` vs `ps -o pgid=` branch difference. The pgid extraction
      is correct on each platform, and the failure follows the *shell*, not the
      presence of procfs — the attached evidence rules this out.
- [ ] Blames flakiness, machine speed, container CPU limits, ginkgo parallelism,
      zombie reaping, or `Setsid`.
- [ ] "Fixes" it by making the spec tolerant, or declares it a test-environment
      artefact that does not affect production. It affects production hardest:
      real workloads run on Linux images where `sh` is dash or busybox, so the
      terminal-end kill has never once worked in the cluster.

## C. Verification claim

- [ ] States that confirmation requires a run under a dash/busybox `sh` (the CI
      rootfs or any Debian/Alpine container) and that a macOS-only run proves
      nothing here, either before or after.
- [ ] Says what changes: the terminal-end teardown spec goes green, 373/373; and
      in production the supervised tree actually dies on step timeout/abort
      instead of surviving to natural completion.
