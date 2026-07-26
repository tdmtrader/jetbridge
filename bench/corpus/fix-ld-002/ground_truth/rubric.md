# fix-ld-002 — behavioral rubric

Score intent, not diff similarity. The reference change is
`ground_truth/reference.diff`; a materially different implementation that satisfies the
required items below is a **pass**.

Score the *reasoning from the evidence in front of the agent* — the pasted 100-character
capture and the code at pre_state. Do not require, or reward, matching the wording of any
in-tree document or of this rubric: the pre_state tree contains design docs and plans
(`docs/superpowers/plans/`, `forge/tracks/`), and quoting one is not diagnosis. A write-up
that reaches the console-side conclusion in its own terms, from the string and the
transport code, scores full credit; one that asserts the conclusion without a causal chain
does not, however closely its vocabulary matches.

## Root cause (what a correct answer understands)

Eos truncates the `/eos/out/active/chan` display echo at a fixed width (~100 chars) when
the selection is long, cutting the final token and leaving a dangling fragment
(`...,80-85,10..`). The 100-character capture in the report is the console's cut length.
`parseChannelList` treated the fragment as a malformed token and returned an error, which
`ActiveChannels` propagated, so `read_stage` returned nothing on exactly the crowded looks
the operator most wants to inspect.

The load-bearing inference: **the missing data does not exist on our side.** The SLIP
transport reassembles arbitrarily large frames and nothing in `internal/osc` /
`internal/eos` slices the string, so there is no buffer to enlarge and no re-read that
recovers the tail. The only correct response is graceful degradation.

## Required (all must hold)

1. **Does not fail the whole read.** A truncated active-channel string no longer produces
   an error out of `eos.Client.ActiveChannels` (and therefore out of the `read_stage` MCP
   tool). The complete channels ahead of the cut are returned.
2. **Salvages, rather than discards.** For the field capture, the 38 complete channels
   (2 … 80–85) survive; the answer is not "return nil on any parse trouble".
3. **No phantom channels.** The truncated fragment (`10..`) is not coerced into a channel
   number — nothing outside the salvageable set appears in the result.
4. **Incompleteness is surfaced.** The caller can tell the result is not the whole live
   set. In the reference this is the existing `partial` bool: `ActiveChannels` seeds
   `partial := truncated`, and `ReadStageOutput.Partial` carries it to the operator. Any
   equivalent signal that reaches the tool output qualifies; a silently short list does
   **not** — that is the failure mode the report explicitly calls out.
5. **Real malformation is still an error.** A bad token in the *middle* of an otherwise
   complete selection (`"1 bogus 3 [0]"`) still returns an error. Leniency is scoped to
   the trailing fragment; "skip anything that doesn't parse" is a fail.
6. **Well-formed strings are unchanged.** Blackout still yields an empty, non-partial
   result; complete selections still parse to the same channel set and are **not** flagged
   partial.
7. **Hermetic test added.** At least one new test drives the truncated string through the
   in-repo fake / mock transport and fails without the fix. No hardware dependency.

## Expected but not required (credit, don't gate)

- **Dangling-range form.** A final token that is a range with a missing end or start
  (`"1-3,7,101-"`) is also treated as truncation rather than an error. The reference
  handles this defensively; it was not in the field capture, so an agent that only
  handles the ellipsis form has not contradicted the evidence.
- **Operator-facing wording.** The reference changes `read_stage`'s `detail` to
  `"N channel(s) live (list may be incomplete — busy look)."` and updates the `partial`
  jsonschema description. Any wording that tells the operator the list may be incomplete
  is equivalent; exact phrasing must not be graded.
- **Reasoning recorded.** The write-up names the truncation as console-side and says why
  a bigger buffer / retry cannot help. This is the diagnostic step the case is really
  testing; an agent that "fixes" it without articulating why the data is unrecoverable
  got there by luck.

## Mechanical caveat (where the judge overrules the count, not the intent)

The mechanical anchor asserts **exactly 38** salvaged channels and `partial == true` out of
`eos.Client.ActiveChannels`. That count is the plain reading of required item 2, but it is
stricter than the report: a fix that salvages conservatively — dropping the last *complete*
group as well as the fragment (32 channels, 2 … 76–78) — is wrong about scope, not about
the bug. Score such a run as a **near-miss on salvage scope**: root cause found, items 1,
3, 4, 5, 6 satisfiable, item 2 partially met. Record it as a mechanical fail with that
qualification rather than as a missed diagnosis. The reverse — salvaging *more* than the
complete channels ahead of the cut, i.e. manufacturing a channel out of the fragment — is
required item 3 and is a hard fail.

The task deliberately does not say where the truncation flag should live, and the graded
seam is the pre-existing exported `ActiveChannels` signature precisely so that stays open
(see non-requirements below). Do not treat the reference's internal shape as the target.

## Explicit non-requirements (do not penalize)

- The signature of the unexported `parseChannelList`. The reference changed it to
  `(s string) ([]int, bool, error)`, but the truncation flag can equally ride a sentinel
  error, a struct, or be derived in `ActiveChannels`. Mechanical grading is anchored on
  the exported `ActiveChannels` seam precisely so this stays free.
- Where the salvage happens (parser vs. caller).

## Automatic fail

- Widening a buffer, adding a retry, or re-querying the console as the *primary* fix
  (there is no more data to fetch — this cannot work, and any test that appears to prove
  it works is testing the fake, not the console).
- Suppressing the error and returning an empty/nil channel set (turns a loud failure into
  the silent-wrong-answer the report asks to avoid).
- Loosening `parseChannelList` to skip every unparseable token.
- Changing the console-facing wire protocol (a different select command, chunked
  selection) — unverifiable without hardware, and out of scope for this report.
