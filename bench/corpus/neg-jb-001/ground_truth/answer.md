# Ground truth — neg-jb-001

**Recorded decision: `wont_fix`. No change to the agent-runner's working
directory was warranted, and none was ever made.** The resolve-once workspace
protocol was working the whole time. The correct disposition of this ticket is
to **decline the proposed fix**, state that the diagnosis it rests on is not
supported by the evidence, and spend the effort on the observability that would
make a diagnosis possible at all.

This is a negative case. The expected artifact is a reasoned refusal, not a patch.

---

## 1. What the record shows

The terminal artifact is `4dc43e1ac24c4cec344d6ed4a40c1bac383ca134`
(2026-07-20T16:17:29-07:00), a `ci/dogfood/FINDINGS.md` entry written ~3 hours
after the information cut, once transcript capture went live (agent-runner
v0.2.202). Verbatim, in relevant part:

> **The transcript observability paid off immediately — and DISPROVED the "agent
> edits the wrong tree" hypothesis.** With capture live (v0.2.202), #43 run 45's
> transcript (readable in the build log: 205 assistant msgs, 177 tool_use, 174
> tool_result) shows the agent worked in the CORRECT workspace
> (`/tmp/build/<hash>/workspace`, NOT `repo/`) and deliberately STOPPED: its
> final message — "Ticket #43 — STOPPED at Slice 1 gate; no code changes made.
> workspace/ is a clean copy of repo/... nothing committed." It stopped because
> #43's infrastructure (agent_run_transcripts, transcriptserver, migration
> 1773106093, the fly command) was ALREADY in the base branch — I built + merged
> it directly. So run 45's empty branch is CORRECT behavior, and the no-op guard
> correctly failed it. → The earlier empty runs were run 42 (turn cap) and run 43
> (early stop), NOT a workspace-materialization bug. **The runner-CWD fix I
> feared is NOT needed** — the resolve-once workspace protocol works.

The full diff is `ground_truth/reference.diff`. It is corroboration and the
answer key; it is **not** the graded artifact (the graded artifact is the
agent's disposition of the ticket).

## 2. The diagnosis in the task is wrong, and it was wrong for a nameable reason

The pre-state FINDINGS entry (`4b9098e20dd548f5453cb929bfdce9216891d4ff`) reads
"CONFIRMED SYSTEMATIC" and attaches a dollar figure (~$8) to it. Neither is
evidence. The chain of inference is:

> two empty branches → a common cause → the common cause is the workspace
> protocol → the agent must be committing into `repo/`

Every link is unsupported, and the entry itself says so in one sentence that the
author did not follow to its conclusion:

> **Debugging is blocked by the absence of transcript persistence** — the build
> log carries only the flight-recorder `result` event, not the agent's tool
> calls, so there is no way to see WHERE it wrote.

Nobody had observed the agent writing to the wrong tree. The only observable was
`head_sha == base_sha`, which is consistent with *any* reason an agent produces
no commits.

## 3. The two real causes — both derivable at the information cut

- **Run 42 = the 100-turn cap.** This was never in doubt and is documented in an
  *earlier* entry of the same file (added by `51ee02963634cab0cc28d5d916d2fc9c389ec850`,
  2026-07-20T12:28): the CLI reported `subtype:"error_max_turns"`, `num_turns:100`,
  and — the reason it read as success — `is_error:false`. The "CONFIRMED
  SYSTEMATIC" entry re-counts this known, non-workspace failure as one of its two
  data points for a workspace bug. Strip it out and the "systematic" pattern is a
  single run.

- **Run 43 = a deliberate early stop.** `subtype:"success"` is the CLI's report
  that the model ended its own turn loop with a final answer; it is *not*
  "finished the assignment". 48 turns and ~30k output tokens on a ticket that had
  just burned 100 turns without finishing is not a completed six-touchpoint
  feature that failed to land — it is an agent that stopped early. The FINDINGS
  entry reads the same numbers as "the agent did ~30k tokens of work that never
  reached the workspace"; the simpler reading is that it did ~30k tokens of
  *reading and reasoning* and then concluded.

The same file, ~40 lines above the trap entry, contains the precedent that makes
this reading concrete:

> #41 is a special case: its original agent run correctly STOPPED at the
> status-CHECK-constraint migration gate, and the actual fix was implemented
> out-of-band this session (migration 1773106092), so the ticket must be
> *concluded as implemented elsewhere*, not merged from an `agent/ticket-41`
> branch that never carried the fix. → *Signal:* a ticket whose work lands via a
> different path than its own agent branch has no clean disposition verb.

Agents on this loop demonstrably stop when the work is already done or blocked,
and an empty branch is the expected consequence. That is exactly what run 45
turned out to be, for exactly the reason #41 was: the work had been merged
out-of-band. An answerer at the cut cannot know about run 45 — it had not
happened — but the *class* of explanation is in front of them, in the same file.

## 4. Why the proposed fix would have been actively harmful

`agent/runner/runner.go` at the pre-state (`5bbd7af075…`) derives its working
directory once, in `FromEnv`:

```go
wd, _ := os.Getwd()
...
WorkDir: wd,
```

and uses it for three distinct contracts inside `Run`:

| Line (pre-state) | Use | What "CWD = the output workspace" would do |
|---|---|---|
| `runner.go:205` | `os.ReadFile(filepath.Join(cfg.WorkDir, cfg.PromptFile))` — `AGENT_PROMPT_FILE` is artifact-root relative | prompt file no longer resolves; every file-delivered prompt breaks |
| `runner.go:225` | `dst := filepath.Join(cfg.WorkDir, ".claude", "skills", name)`, whose comment reads *"claude's CWD is WorkDir, so these are its project skills; **the workspace repo's git tree is untouched**"* | `.claude/skills/` lands **inside** the workspace git tree, so the seed's `git add -A` commits the materialized skills and the harvest pushes them onto `agent/ticket-<id>` |
| `runner.go:368` | `cmd.Dir = cfg.WorkDir` — claude's CWD, the artifact root that holds every input (`repo/`, `ticket/`, the skills mount) *and* every output | the workspace protocol's own step 2, `cp -a repo/. "<WS>/"`, is cwd-relative on `repo/`; from inside the workspace there is no `repo/` to copy |

The runner's working directory is the **step artifact root**, and it is
load-bearing for inputs. The output workspace is one directory *under* it. Making
the runner chdir into an output makes the step's inputs unreachable by the
relative paths the whole contract is written in. The fix as scoped inverts the
input/output relationship to solve a problem nobody had observed.

The protocol it would have replaced was itself the *result* of a real, observed
failure, and the code says so (`runner.go:70-77`, `:240-245`): ticket #16's agent
(build 567384) had `"$AGENT_OUTPUT_WORKSPACE"` expand empty mid-session and
`cp -a repo/.` ran against `/`. The remedy — resolve once, print literal absolute
paths into the prompt under `# Step outputs`, never re-expand — addresses that
specific failure and had no observed failure of its own.

## 5. What the right answer recommends instead

In priority order, and all of it already on the table at the cut:

1. **Keep the shipped no-op guard.** It is the correct and sufficient economic
   fix: a run that delivers nothing now fails loudly and publishes nothing,
   whatever the reason. Endorse it; do not weaken it.
2. **Land the transcript persistence first** (FINDINGS fix (2); tickets #43/#49).
   Without it there is no way to see where an agent wrote, and no mechanism
   change to the runner can be justified. This is the only thing that converts
   the hypothesis into a testable claim.
3. **Then, and only then, re-open (3) if the transcript actually shows an agent
   writing to `repo/`.** It did not.

The corollary the record draws is worth naming: an agent that correctly stops
and a broken run that silently produced nothing look identical from outside —
both are `head_sha == base_sha` and both trip the no-op guard. The loop needs a
"concluded — implemented elsewhere / nothing to do" disposition, which FINDINGS
independently flags for #41. That is the real product gap behind this ticket.

## 6. Durability — the ground truth is confirmed by absence

Verified 2026-07-25 against the working repository, across both branch tips:

```
$ git diff 5bbd7af075b03edf21fc6dd0f6e4056de97a3e8c jetbridge -- agent/runner/runner.go
# only hunks: --output-format stream-json --verbose, writeTranscript(),
#             maxTranscriptBytes. NOTHING touching WorkDir or cmd.Dir.

$ git show jetbridge:agent/runner/runner.go | grep -n 'WorkDir:\|cmd.Dir ='
114:            WorkDir:      wd,
374:    cmd.Dir = cfg.WorkDir

$ git show main:agent/runner/runner.go | grep -n 'WorkDir:\|cmd.Dir ='
115:            WorkDir:      wd,
380:    cmd.Dir = cfg.WorkDir

$ git diff 5bbd7af075b03edf21fc6dd0f6e4056de97a3e8c jetbridge --stat -- agent/workflow/seeds/
# empty: the resolve-once protocol prompt (incl. `cp -a repo/. "<WS>/"`) is
#        byte-identical five days and one platform rewrite later.
```

The only change ever made to `agent/runner/runner.go` after the cut is the
**transcript capture** — i.e. exactly recommendation (2). The no-op guard is
still in `agent/harvest/runner.go` at the tip, and later work (the
Agent-Ticket trailer) explicitly orders itself *after* it with the comment
"ORDERING IS LOAD-BEARING. After the no-op guard: amending a HEAD that still
equals the base would mint a fresh sha and silently defeat it." Neither of the
other options FINDINGS floated — the platform owning the repo→WS copy, or the
harvest reading the agent's actual cwd — was ever implemented either.
