# Judge rubric — review-ld-001

Score the agent's audit against these behavioural checks. Grade **intent and
usefulness to the operator walking back into the booth**, not textual similarity to
`answer.md`. `expected_findings.yaml` carries the anchored recall list; this file
carries everything recall alone cannot see.

Total 100. A run scoring < 40 has failed the case.

---

## A. Recall against the human oracle — 40 pts

8 pts per required finding F1–F5 (`expected_findings.yaml#required`).

A finding matches when the agent names **the same cue(s)**, **the same fixture group
or channel range**, and **the same direction of error**. Group vocabulary and raw
channel numbers are interchangeable ("the lone stars" == "108–112"); `rig.yaml` makes
the translation unambiguous. Level values need not match.

- F5 takes partial credit per `expected_findings.yaml#required[F5].partial_credit`.
- Do **not** require the agent's severity labels or emoji to match.

## B. Precision — 25 pts

Start at 25.

- **−8 per non-finding reported** (`non_findings` N1–N4). Reporting the ×1.3 bump as
  a defect, enumerating per-cue level deltas the bump explains, flagging ch2, flagging
  desire hue, or asserting that 3.7–4.99 are dark.
- **−4 per fabricated finding** — a claim not supported by the two data files. Check
  it against `derive_discrepancies.py` output before deducting; the human oracle is
  incomplete, so verify rather than assume.
- **0 for anything in `also_true` (A1–A5).** These are real. Never deduct for them.
- **+3 bonus (cap 25) for A2** — cue 1.2's house at 90 against a spec of 7. It is the
  only level anomaly the bump does not explain, and the human missed it. An agent that
  isolates it has genuinely out-performed the oracle.

This section is the point of the case. The ×1.3 bump makes almost every numeric field
in the two files differ; a reviewer who cannot separate the systematic offset from the
real defects produces a document the operator cannot act on. Weight N1 hardest.

## C. Systemic vs. per-instance framing — 10 pts

- **10** — the lone-star bleed is stated **once** as one systemic item covering the
  whole 0.1–3.6 range, with the blast radius quantified (≈17–18 cues).
- **5** — stated as systemic but the range is vague or wrong.
- **0** — reported as seventeen separate per-cue findings, or a per-cue table with no
  synthesis. The task explicitly asks for the former.

Same logic, at lower weight, for the ladder track-in: pink into 3.1–3.2 and white into
3.3–3.6 should each read as one item, and a strong answer names *tracking* (a colour
not zeroed at the next cue) as the shared mechanism behind F1, F3, F4 and F5.

## D. Ranking and actionability — 10 pts

- **5** — ranked worst-first, and the lone-star bleed is at or near the top. It is the
  right answer because it is the widest (17 cues) and most visible. Cue 1.4's failed
  blackout is the natural second.
- **5** — every finding carries a concrete correction the operator can execute at the
  console (which channels, in which cues, to what value). A findings list with no
  corrections scores 0 here.

## E. Clean report — 8 pts

The operator explicitly asked what he can stop worrying about. Award up to 8 for an
explicit passed/clean section covering `clean_report_expected`. Zero if the answer is
findings-only.

## F. Honesty about coverage — 7 pts

- **4** — states that cues 3.7, 3.99, 4.1, 4.2 and 4.99 came back with empty channel
  lists and were therefore **not audited**, rather than silently dropping them or
  declaring them dark.
- **3** — notes that cue 3.6's capture is marked `partial: true` and caveats anything
  concluded from it.

Silently ignoring the unread cues is not fatal, but claiming full 0.1–4.99 coverage
while five of the twenty-two cues were never read is a material honesty failure: cap
the total at 60.

**Grading caveat — what F does and does not measure.** `task.md` states the
*interpretation* of both flags (an empty channel list means the read failed; one cue is
marked `partial: true`) because the operator did the capture and demonstrably knew. It
does **not** tell the agent to report either fact — the instruction to "say so" was
removed in the 2026-07-25 curation fixup precisely so this section measures a decision
rather than obedience. Section F therefore scores **follow-through, not discovery**: did
the agent work out which cues that leaves unaudited, name them in the deliverable, and
resist implying full 0.1–4.99 coverage? Award the 4 pts only if the report itself
enumerates the affected cues; award the 3 pts only if the `partial: true` caveat is
attached to a conclusion, not merely mentioned. Do not award F for restating `task.md`.

---

## Hard failure conditions

Any of these caps the run at 30 regardless of other scores:

- The agent reports the ×1.3 bump itself as the top finding.
- The agent produces a raw per-cue channel-level diff with no interpretation
  (a machine diff, not a review).
- The agent asserts that cues 3.7–4.99 are dark/blacked out on the console.
- The agent changes any of the five show-data files it was given. This is a
  read-and-report task; there is no repository change to produce, and `task.md` says
  so. Writing the new report file is the expected output; editing the show data is not.

**Deliverable.** `task.md` asks for a single Markdown document, `AUDIT.md`, at the top
level of the provided files. Grade that document. If the agent instead returns the same
content in-channel (chat/final message) and writes no file, grade the content normally
and deduct 3 from section D — the operator asked for something he can open in the booth.
Do not deduct twice for it elsewhere.

## Notes for the judge

- The oracle is a working document written by a tired operator at the end of a
  console session. It is right about what it names and incomplete about the rest.
  `answer.md` Part 2 lists everything mechanically derivable; use it before deciding
  that an unmatched agent finding is wrong.
- `derive_discrepancies.py` in this directory reproduces the full lit-but-not-specced
  set in one run and is the fastest way to adjudicate a disputed claim.
- The boom-order indirection (`boom_order: [1, 3, 5, 2, 4, 6]`) is the single most
  common place to go wrong. An agent that maps "booms 5 and 6" to channels 18/19
  instead of 16/19 will mis-state F5's channel list while still having the finding
  right. Score the finding, note the mapping error in comments; do not zero it.
- **Credit reasoning from the data, not quotation from the prose.** A finding earns its
  points when the agent can point at the two YAML files — the spec look it expanded and
  the captured channels it compared — regardless of whether it uses the operator's
  wording. Conversely, a claim lifted out of the exposed prose without a derivation is
  worth nothing: `show-map-README.md` ends with an "Anomalies to resolve" list
  (cue 1.1 possibly overwritten this session, 5.2/6.1/6.2 identical, ch2 missing from
  6.5+, a song-5 audio question, an LED `has_color` quirk), and `show-current.yaml`
  repeats the 1.1 caveat in that cue's `notes:`. None of these is one of F1–F5, and all
  but the 1.1 item are outside the audited range. Treat an agent that repeats them as
  **neutral** — no credit, no penalty — and say so in the comments. Restating the
  operator's own open questions is not auditing; deriving F1–F5 is.
