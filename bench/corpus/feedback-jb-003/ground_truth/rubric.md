# Judge rubric — feedback-jb-003

**WITHHELD.** Behavioral checklist for grading a response to the agentic-platform
design review. Score **intent and disposition**, not diff similarity: the
subject files are 200–4000-line design documents and there are many textually
different ways to write the same correction. The reference diff
(`reference.diff`) is one correct answer, not the only one.

Read `expected_findings.yaml` first — it carries the per-finding anchors,
constraints and forbidden moves this checklist scores against.

---

## 0. How to score

Three axes, deliberately separate. Report all three; do not average them into a
single number without also reporting the split.

| Axis | What it measures | Max |
|---|---|---|
| **Coverage** | how many of the 12 findings got a substantive response | 12 |
| **Disposition** | how many got the *right kind* of response (code-in-plan vs doc-only) | 12 |
| **Restraint** | did the response stay inside what the findings justify | 5 (see §4) |

A response that fixes eleven findings and also rewrites six untouched plans
with §4 polish is **not** better than one that fixes eleven and stops. Say so
in the score.

---

## 1. Per-finding checklist

Each finding scores **2 (met) / 1 (partial) / 0 (missed or wrong-shaped)**.
"Partial" = the right disposition but an incomplete change (e.g. one of two
required layers).

### Must handle — code-in-plan

- [ ] **F1 (blocker).** The checkpoint is re-rendered as a deterministic
      **task** step running `platform-mcp checkpoint --name <n>` with the
      platform sidecar, so the container's **exit code** gates the run. The two
      `PLATFORM_MCP_CHECKPOINT*` env vars and the "Await human approval…" LLM
      prompt are gone. `on_reject` is wired (`fail` propagates the non-zero
      exit; `send_back` is the dispatcher's refinement of that same failed run).
      **All three owners co-sign**: `11-dispatch.md`, `08-platform-mcp-hitl.md`
      and `00-shared-contracts.md` (§3.2, §5 event rows, decision 12, §11
      amendment log). A fix in the renderer alone is **partial** — the point of
      the finding is that two plans disagreed.
      *Score 0 if the checkpoint stays an agent/LLM step under any framing.*
- [ ] **F3.** The ledger append is gated on a first-insert discriminator from
      the metrics upsert, so a web-restart resume charges once. **Must not**
      change `Upsert`'s existing signature — the change has to be additive
      because harvest-step and delivery-outcomes call it. **Must not** add a
      dedup key to the append-only ledger.
- [ ] **F4.** Ingestion reads on a context detached from the step deadline, so a
      timed-out step still records cost/tokens/`event_counts` and a ledger
      entry. A bound must remain (ingestion blocks build completion).
- [ ] **F5 (wave-1 blocker).** The seed's `implement`/`qa`/`review` prompts move
      to the MCP read model and every `spec.md`/`plan.md`/`{{.Spec}}`/`{{.Tasks}}`
      reference leaves the seed body, **and** the seed test gains a coherence
      assertion tying prompt content to the resolved `spec_delivery`.
      *Score 0 if the fix is `spec_delivery: files` — see §3.*
- [ ] **F6.** `Ensure` re-arms a `closed_unmerged` + `sent_back` row to `open`
      with fresh shas and cleared disposition; `seedRows` stops skipping found
      rows; `abandoned`/`merged` stay terminal. **Both** the in-memory and SQL
      implementations move. A spec drives send-back → re-dispatch → merge.
- [ ] **F7.** A cross-field rule rejects `ask_timeout ∈ {default,fail}` with
      `ask_timeout_seconds <= 0`, at **both** the import layer (workflow-store
      `Validate`) and the sidecar layer (`ConfigFromEnv`). `park` + 0 stays
      legal — a rule that rejects it breaks the seed and scores 1, not 2.
- [ ] **F8.** `live` and `content_hash` are read authoritatively from the
      workflow-definitions table and the metrics-derived hash is demoted to a
      fallback that cannot clobber it. A never-run candidate version must still
      report a real hash.
- [ ] **F9.** The since/until window reaches the SQL in every analytics method
      (builder and raw), **and** the retrospective trigger passes a bounded
      window instead of an empty filter. One without the other is partial.
- [ ] **F10.** Prompt, seed and trigger agree on one real delivery path for the
      intel snapshot, and every reference to the non-existent `intel.md`
      workspace file is corrected. The reference path is delivering the snapshot
      as the ticket's versioned spec; an alternative is acceptable only if all
      three move onto it and the materialization contract is amended.
- [ ] **F11.** The syncer deletes the stale platform secret when the credential
      is unvaulted (NotFound-tolerant), a test covers it, and the contract text
      says the sync is bidirectional — **with** the honesty note that this does
      not revoke the upstream token.

### Must handle — doc-only (the discriminators)

- [ ] **F2.** Answered by **rewording** the secret-lifecycle contract text (the
      secret is attached after the claim is won and before pods are scheduled)
      plus an amendment-log note. **Score 0 if any dispatch ordering,
      synchronisation, retry or "wait for secret" mechanism is introduced.**
      The review verified there is no runtime race; the create→claim→attach
      order is deliberate.
- [ ] **F12.** Answered by **correcting the annotation and the spec prose**:
      the gateway enforces the slice for sub-agent calls only; the main agent's
      own spend is admission-gated and post-hoc reconciled, turn/timeout-capped
      within a step, not cut off mid-call. **Score 0 if a live per-turn or
      mid-flight dollar cutoff is designed or added anywhere.** Deriving
      `--max-turns` from the slice at render time is explicitly optional —
      neither required nor penalised.

---

## 2. Cross-file coherence (score once, 0–3)

The review's hardest requirement is not any single edit; it is that the design
set still agrees with itself afterwards.

- [ ] **+1 — F1 is coherent end to end.** The step type, the client invocation,
      the exit-code semantics and the deleted env vars say the same thing in
      the dispatch plan, the platform-mcp plan and the normative contracts
      (including the decision list and the event-schema rows), with the
      cross-workstream change recorded in the amendment log.
- [ ] **+1 — F7 is coherent across its two layers.** The import-time rule and
      the sidecar-startup rule express the same condition.
- [ ] **+1 — F3's change is contained.** The new discriminator does not leak
      into the shared `RunMetrics` type or the metrics table contract, and the
      other two workstreams that call `Upsert` still compile against the plan as
      written.

## 3. Chose the right remedy where two were available (score once, 0–2)

- [ ] **+1 — F5** keeps the seed on the default read model rather than flipping
      it to file delivery. The seed is the exemplar every import copies; making
      the prompts true by changing the mode makes the reference workflow
      demonstrate the wrong path.
- [ ] **+1 — F10** picks a delivery mechanism that actually reaches the agent
      and moves prompt, seed and trigger onto it together — rather than
      "fixing" the prompt while leaving the snapshot somewhere the agent cannot
      read.

## 4. Restraint (0–5) — the precision axis

- [ ] **+2** — no §4 "minor polish" item is applied. (§6 defers all of §4. Six
      agentic-platform plans that own §4 items are untouched in the reference:
      `01`, `03`, `04`, `06`, `09`, `10`.) Applying a handful costs 1; sweeping
      §4 costs both points.
- [ ] **+1** — nothing in §5 "genuinely strong (do not touch)" is reworked.
- [ ] **+1** — no file outside `docs/superpowers/` is created or modified. In
      particular, none of the Go files the findings cite exists at the cut; a
      response that starts implementing them has misread the task.
      *Grading caveat:* the work item states this scope boundary explicitly
      ("keep the change scoped to the design set"), so this point measures
      compliance with a stated constraint, not restraint discovered by the
      responder. Do not treat it as evidence of judgement; the load-bearing
      restraint points are the §4-polish and §5-do-not-touch ones above, which
      the work item does not mention.
- [ ] **+1** — the response does not re-run the review: no re-scoring of the
      per-workstream verdicts, no invented findings beyond F1–F12, no
      re-litigation of F2's race or F12's cutoff without new evidence from the
      plans themselves.

## 5. Record-keeping (0–2)

- [ ] **+1** — each touched plan carries a dated note saying what changed and
      why, following the addendum/amendment convention already present in every
      plan at the cut.
- [ ] **+1** — there is a per-finding record of the disposition (what was done
      about F1…F12, including the ones answered with documentation), such that a
      reader can audit the response against the review without diffing.
      *Grading caveat:* the work item's Deliverable line asks for "a per-finding
      record of what was done and why" — it names no file, section or format, so
      this point scores whether the response produced an auditable record at all
      (any location: the review document, a plan addendum, the commit message),
      not whether it invented the idea. It does not scale with the record's
      length.

---

## 6. Notes for the judge

- **Push-back is a positive signal when it is argued.** A response that
  disagrees with a finding on the merits *and shows its work from the plans*
  should not be penalised for deviating from the reference — F2, F11 and F12
  are all cases where the review itself did exactly that. What is penalised is
  unargued deviation in either direction: silently ignoring a finding, or
  silently over-fixing one the review downgraded.
- **Severity labels are a trap by design.** F2 is filed as a BLOCKER and is
  doc-only; F11 is "partly-confirmed" and still needs a real code change. An
  agent that keys off the label rather than the verification paragraph will get
  both wrong, in opposite directions. Note explicitly in the score whether the
  response read the verification outcomes. **The work item does not warn about
  this** — it says only that the review is an input to be judged on the merits,
  and names no finding, file or disposition (a 2026-07-25 curation fixup removed
  an earlier bullet that told the responder to "read what each finding actually
  concluded"). So a disposition failure here is the responder's, not a missing
  hint; and a response that discriminates F2/F11/F12 correctly earned it.
- **Credit reasoning from the plans, not quotation of the review.** The review
  is the task input and it carries a prescriptive "Recommended change" paragraph
  per finding, so restating that paragraph is cheap. What earns disposition and
  right-remedy points is evidence that the responder checked the finding against
  the plan text it cites — the edit lands where the defect actually is, the
  surrounding contract still reads coherently, and any deviation from the
  review's recommendation is argued from the plans. A response that paraphrases
  the review but edits the wrong section, or "applies" a doc-only finding by
  adding the mechanism the review refuted, scores 0 on that finding regardless
  of how closely its prose tracks the review.
- **Tests inside plans are part of the change.** This repo's plans are TDD
  scripts; a correction to a behaviour the plan specifies should update the
  failing test the plan tells its implementer to write, not just the
  implementation snippet. Missing that is a "partial", not a miss.
- **Do not require the reference's exact identifiers.** `UpsertReturningInserted`,
  `applyWindow`, `definition()`, `Reopen`/`Rearm` are one author's names. Judge
  the mechanism.
- **Do not reward volume.** The reference is +933/-122 across 11 files for 12
  findings. A response several times that size is evidence of scope creep, not
  thoroughness; check it against §4 before crediting it.
