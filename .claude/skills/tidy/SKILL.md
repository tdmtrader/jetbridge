---
name: tidy
description: Make one small tidying in one category, gate it, and merge or hand it to review. Usage: /tidy <category>
disable-model-invocation: true
---

One category, one instance, one commit. The category card in
`categories/<category>.md` is the contract: it holds the scan, the eligibility
predicate, the exclusions, the extra gate, the merge tier, and the log. Read it
in full before touching the tree. If no card matches `$ARGUMENTS`, list the
cards and stop.

## 1. Branch

```bash
git fetch origin core
git switch -c tidy/<category>/$(date +%F) origin/core
```

## 2. Pick

Run the card's scan. Discard every candidate whose instance key already appears
in the card's `## Log`. Discard every candidate that fails any line of the
card's `Exclude` list. Take the first survivor.

If no candidate survives, append a `skipped` log line with the reason
(`queue empty` or `all candidates excluded`), commit that line alone to the
branch, and go to step 6 with `outcome: skipped`. An empty day is a correct day.

## 3. Apply

Change exactly the one instance. The diff touches nothing the predicate does not
name. Behavior, error identity, serialized names, DB columns, wire tags, and
metric names stay as they were unless the card says otherwise. When you notice
a second instance or an adjacent improvement, leave it for another day.

## 4. Gate

Every category:

```bash
make test-quick
go test .
go test ./hangar/output/ -run 'Architecture|Vocabulary'
```

Then the card's `Gate` lines. A red gate means revert the change, append a
`gate-failed` log line naming the failing test, commit that line alone, and go
to step 6 with `outcome: gate-failed`.

## 5. Commit

Append the log line to the card's `## Log` in the same commit as the tidy:

```
tidy(<category>): <what changed, in the glossary's words>

<one sentence on why this instance was eligible>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## 6. Merge or hand off

Push the branch. Then by the card's `Merge` tier:

- `green`: run `hack/ci-check.sh <sha>`. Green: fast-forward `core` onto the
  branch and push `core`. Red: log `gate-failed` on `core` directly and leave the
  branch.
- `review`: run the reviewer with the other model. When this session is Claude,
  the reviewer is codex:
  ```bash
  codex exec --sandbox read-only --ephemeral -o /tmp/tidy-verdict.md \
    "$(cat .claude/skills/tidy/REVIEW.md)

  BRANCH: tidy/<category>/<date>   CARD: .claude/skills/tidy/categories/<category>.md"
  ```
  When this session is codex, the reviewer is `claude -p "/tidy-review <branch>"`.
  `approve`: proceed as `green`. `reject`: append a `rejected` line carrying the
  reviewer's reason to the card's log on `core`, commit that line alone to
  `core`, push, and leave the branch for the weekly digest.

For `skipped` and `gate-failed` outcomes the log line is already on the branch;
cherry-pick it onto `core` and push so tomorrow's pick sees it.

Done when: `core` carries a log line for today in this card, and the branch is
either merged or left with its reason recorded.
