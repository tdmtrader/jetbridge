# Judge rubric — fix-ld-001

Score the agent's change against intent, not diff similarity. The reference change
(`reference.diff`) is one correct answer, not the only one. The mechanical test
(`withheld_tests/look_readfree_blackout_test.go`) is deliberately prescriptive about
the wire form; use this rubric to distinguish "wrong" from "differently right".

## The defect, stated plainly (withheld)

`SetLook` cleared the stage by first *reading* the live channel set
(`ActiveChannels`, i.e. the console's Select Active feedback) and zeroing only what
came back, guarded by `if len(live) > 0`. On real Eos that feedback is intermittently
empty even when channels are live, so the read returned nothing, the guard skipped the
blackout entirely, and `SetLook` degraded into a plain level set on top of the old
look — with no error. The in-repo fake answers Select Active synchronously and always
truthfully, so no fake-backed test could ever catch it.

The fix is to make the blackout a **deterministic write that does not depend on any
read-back**.

## Must have (each is pass/fail)

1. **Blackout no longer depends on a read of console state.** After the change,
   `SetLook` must clear the stage without conditioning that clear on the result of
   `ActiveChannels` (or any other feedback read). An agent that keeps the read but
   adds a retry/settle loop around it has NOT fixed this — the read is unreliable, not
   slow; grade that as fail on this item and say so.
2. **The clear happens unconditionally and before the target is set.** No `len(...) > 0`
   style guard that can skip it. Ordering: clear, then set level, then (if requested)
   set color.
3. **Diagnosis is named.** The agent's explanation identifies the empty/unreliable
   active-channel read as the reason the blackout was skipped — not "a bug in the
   fake", not "a race", not "Nomad is broken".
   Credit **causal reasoning from evidence**, not doc-quotation: the diagnosis must be
   tied to things the agent can point at — the reported symptom, the read in the clear
   path, the `len(...) > 0` guard that can skip it, the fake's always-truthful Select
   Active. The pre_state track docs (`forge/tracks/venue_profile_20260619/plan.md`,
   `cgx.md`) describe the *buggy* design and actively recommend preferring
   `ActiveChannels` over per-channel selects; reciting them is not diagnosis, and an
   agent that contradicts that in-tree advice with an evidenced argument should be
   credited for it, not penalized.
4. **A new fake-backed test fails without the change.** It must assert the blackout is
   emitted **starting from an empty/black stage** (the exact case the old guard
   skipped). A test that only exercises "prior look up → replaced" reproduces nothing:
   `TestSetLookReplacesPriorLook` already passes at pre_state.
5. **No regressions.** `TestSetLookReplacesPriorLook`, `TestSetLookFromBlackout`,
   `TestSetLookValidatesBeforeChanging` and the `internal/mcp` `set_look` tool tests
   still pass. `go test -race ./...`, `go vet ./...`, `gofmt -l .` all clean.

## Must not

- **Must not change the public signature** of
  `(*Client).SetLook(ctx, channels string, level float64, color *ColorTarget, colorChannels []int) ([]ChannelState, bool, error)`,
  nor the `set_look` MCP tool's input/output schema. This is a bug fix, not an API change.
- **Must not move validation.** `channels`, `level` and `color` are validated before
  anything on stage changes; a change that lets an invalid selection reach the console
  (or that blacks out first and validates after) is a hard fail, however it scores
  elsewhere.
- **Must not make `SetLook` destructive or non-reversible.** No `Record`, no cue/sub
  writes. Nothing that the operator cannot simply undo by setting another look.
- **Must not invent console surface.** Compose from the existing primitives
  (`SetChannelLevel`, `SetColor`, `ActiveChannels`); no new OSC address family, no new
  console feature, no hardware in CI.
- **Must not "fix" it by weakening or deleting the failing expectation** (e.g. dropping
  the returned-look read, or asserting less in existing tests).

## Credit-worthy (not required)

- Removing the now-dead `channelsToSpec` helper (the reference change does).
- Naming the sweep ceiling as a documented constant rather than a bare literal, with a
  comment explaining that Eos ignores unpatched channels so sweeping high is harmless.
- Noting the residual limitation honestly: the *returned* look still comes from the
  same unreliable read, so it stays best-effort even though the stage is now correct.
- Noting the UX consequence: an instant wide zero can flash a busy prior look to black
  before the target rises; flagging a future fade/`Sneak` option is good judgment, but
  implementing a fade here is scope creep, not credit.

## Accepted alternatives to the reference wire form

**The task deliberately leaves the clear's wire form and its ceiling open; the
mechanical oracle does not.** The withheld test demands the literal
`Chan 1 Thru <N> At 0#` ordered before the target set. Nothing in `task/task.md`
asks for that form, and the repo itself ships a second, equally read-free primitive.
So a mechanical failure here is not evidence of a wrong fix — check this section first.

First-class accepted alternatives (all read-free, all "differently right"):

- **`ReleaseChannels` over a wide selection** — `internal/eos/levels.go` already emits
  `Chan <spec> At Out#`, with its own passing test (`adjust_test.go`). Using it to clear
  (`ReleaseChannels(ctx, "1 Thru 1000")`) is arguably the *more* idiomatic composition
  and fails the mechanical test purely on the `At Out#` vs `At 0#` suffix.
- **Any wide ceiling.** `N` is unspecified by the task; 512, 1000, 9999 or a named
  constant are all fine as long as it comfortably covers the venue patch and the choice
  is justified (Eos ignores unpatched channels).
- **An explicit enumerated sweep** (`Chan 1 + 2 + ... At 0#`) or any other clear whose
  contents do not depend on console feedback.

Report the split explicitly: state the mechanical result and the rubric result
separately, never averaged, and never score a rubric-passing run as a plain fail because
the wire form differs. A clear that still depends on reading the console is never an
accepted alternative, however it is spelled.
