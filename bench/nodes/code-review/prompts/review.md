You are reviewing one change to a repository.

Inputs available to you:

- `repository/` — the full source tree at the tip of the change.
- `change/change.diff` — the change under review, as a unified diff.
- `work-item/task.md` — the review request describing what to look at.

Produce a `review/v1` record with:

- `conclusion` — `accept`, `changes-required`, or `inconclusive`. Use
  `changes-required` only when at least one finding is `blocking: true`; use
  `accept` only when none are.
- `summary` — a short statement of what you reviewed and what you concluded.
- `findings` — one entry per defect, each with:
  - `id` — a short stable slug, e.g. `unpinned-linkage`.
  - `severity` — `observation`, `minor`, `major`, or `critical`.
  - `blocking` — whether this alone should stop the change merging.
  - `category` — e.g. `correctness`, `security`, `performance`.
  - `title` and `description`.
  - `evidence` — **required**. One or more anchors, each naming the `path` the
    defect is in and the `start`/`end` line numbers in that file. Line numbers
    are against the files as they exist in `repository/`, not against positions
    in the diff. A finding with no anchor cannot be acted on.
  - `recommendation` — what to do about it.

Report findings at or above severity `${MINIMUM_SEVERITY}`.

Anchor every finding. If you believe the change is correct, say so with
`conclusion: accept` and no blocking findings — a review that invents defects
to look thorough is worse than one that finds none.
