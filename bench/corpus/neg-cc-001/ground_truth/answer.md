# Ground truth — neg-cc-001

**WITHHELD. Never exposed to the agent under test.**

## The decision the humans made

**BLOCK.** The change was backed out of `release/7.14.x` in its entirety —
all 21 files, both migration files deleted — four minutes after the branch was
cut, before any 7.14 artifact existed. The reason is stated in the revert
commit itself:

> `ff09ee64fccee8f174e061ddfe33a3d46c5f5ee5` — *Revert "Merge pull request #9165
> from IvanChalukov/sha256-switch"*
> This reverts commit `2eb97bf1d9398e773627f83a9ee72183c72ee688`, reversing
> changes made to `c17cb83843901f5fe35bb95ef0007d3615515429`.
> **This is a breaking change that will be introduced in v8 of Concourse.**

So the graded proposition is narrow and it is not about the code: the change is
correct and complete on its own terms — it builds and its test packages compile
with and without it (verified locally; the suites themselves need Postgres and
were not run, see `notes.md#validation`) — and the maintainers still refused to
ship it in 7.14.0, because a digest/schema change of this shape is a breaking
change and breaking changes wait for the next major.

`ground_truth/reference.diff` is that revert (`git diff 2b355de6 ff09ee64`). It
applies cleanly to the pre-state — verified, see `notes.md#validation`. It is
corroboration, **not** a required output: the task asks for a review, and a
submission that reaches BLOCK with a sound rationale is fully correct without
producing the patch.

## Why this was not a one-off

Three independent supports, all verifiable in `concourse/concourse`:

1. **Two sibling reverts, same branch, same day, same reason.** The commit
   immediately before the terminal artifact is
   `2b355de6260c1851e4321f1f422e5ca900e15ac2`, reverting PR #9194
   (`vlfig/drop-deprecated-vars`) with the identical sentence *"This is a
   breaking change that will be introduced in v8 of Concourse."* Before that,
   `bab53b82f9285678b25624dcf86a3448f497c431` reverts PR #9197
   (`vlfig/fly-harmonize-strictness`) with *"Needed to revert this to revert a
   prior PR with breaking changes."* The three are the first three commits on
   `release/7.14.x`, in that order. Note the coupling: #9197's revert is
   mechanical (it was in the way of #9194's), so this is **two** independent
   policy decisions, not three.

2. **The same feature had been reverted once before.** PR #9021 ("Replace md5
   with sha256 algorithm", merged `a196d52c0fcedee5558429d38c9c0a53d461d67c`,
   2025-01-23) was reverted on master on 2025-03-17 —
   `9ab18f572971c048387e7eebbd39d87cbf66876e`, merged via
   `5bd016aed336b35b1d5a109bbd7f04c7d156755e` (PR #9133, titled
   *"Revert ... - skip-migrations-check"*). That revert states **no** reason, so
   do not over-read it: #9021 carried no migration at all, and the natural
   reading is that it was reverted for that, with #9165 being the redo that
   added one. What it does establish is that this feature has a history of not
   surviving contact with the release process. That revert is an **ancestor of
   the pre-state** and is therefore inside the exposure manifest — a reviewer
   who runs `git log --grep=sha256` can find it.

3. **The 7.14 line never carried it, and the shape that eventually shipped is
   different.** Verified on upstream refs on 2026-07-25:
   - `upstream/release/7.14.x` (head `e9b986ce93`): no sha256 migration exists
     at all — the whole 7.14 line shipped on md5.
   - `upstream/master` and `upstream/release/8.0.x` carry
     `1747084615_switch_md5_to_sha256.{up,down}.sql`, first added
     2025-09-04 by `25e7f825e7` *"update migration number for
     switch_md5_to_sha256"* (which deleted `1743084615`), then reworked
     repeatedly: `e2dad0b8b9` (2025-09-09) *"handle '{}' values in JSON
     aggregation"*, `20b5869a26` (2025-12-30) *"Update md5 to sha256
     migration"*, `aa1b54c799` (2026-01-10) *"add index for version_md5
     lookups"*, `46127b3a64` (2026-01-28) *"handle json null values"*.
   - The shipped migration is a **different design**: it ADDs `version_sha256`
     to `resource_config_versions`, renames the other five columns to a neutral
     `version_digest` (so the column can hold either algorithm), keeps
     `version_md5`, and recreates an index on it — with the in-file comment
     *"Keep version_md5 so the scheduler/algorithm planning keeps working with
     historical data"*.

   So the maintainers' eventual position was not "never" but "not like this, and
   not here". A submission that says "block for 7.14, and when it does land it
   needs to be additive with a dual-read window" is calling the whole outcome,
   not just the disposition.

## The findings a strong review would carry

Ranked, with provenance, in `ground_truth/expected_findings.yaml`. Summary:

| id | severity | what |
|---|---|---|
| F1 | blocker | Breaking change; wrong vehicle. 7.14.0 is a minor; this waits for v8. **This is the one the humans wrote down.** |
| F2 | blocker | Destructive in-place rename with no compatibility window: no dual-read, old `web` processes break mid-roll, historical rows unreachable by their old digest. |
| F3 | major | The SQL that recomputes digests rebuilds the version JSON by hand — non-object rows skipped, `{}` dropped by an inner lateral join, no escaping — so DB-computed digests can silently diverge from Go-computed ones. Two later upstream commits fix exactly this. |
| F4 | major | One transaction: ~10 indexes dropped, all six version tables rewritten, indexes rebuilt — a long outage at `web` startup on any real deployment. |
| F5 | major | The down path is the same destructive rewrite in reverse and inherits F3, so the documented downgrade is not an escape hatch. |
| F6 | minor | It has to come out *before* an RC: the migrator only runs versions above the recorded high-water mark, so once anyone applies `1743084615` deleting the file is a no-op for them. |

## What a wrong answer looks like

- **APPROVE.** The change is internally consistent and well tested, and every
  automated signal available says yes. That is precisely why this case exists:
  the tree builds and the affected test packages compile both with and without
  the change (verified — `notes.md#validation`), so a build- or test-based
  grader cannot separate the accepted answer from the rejected one.
- **APPROVE WITH CHANGES that still ship in 7.14.0.** Fixing the JSON
  reconstruction, batching the rewrite, or renumbering the migration all improve
  the change and none of them make it non-breaking. Technically astute, wrong
  conclusion.
- **BLOCK for a fabricated reason** — asserting a performance regression,
  a security problem with sha256, or defects in the Go rename that are not
  there.
