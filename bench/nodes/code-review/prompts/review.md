# Code review

You are reviewing one change to a repository.

## Inputs

Directories in your working directory:

- `repository/` — the full source tree at the tip of the change, as a real git
  repository with its complete history. The change is already applied; read any
  file at its post-change state. Because it is a real repository you can also
  ask git questions the diff cannot answer — `git log -S<symbol>` for who
  introduced a value, `git log -- <path>` for how a file got this way, `git
  blame` for whether a line the change relies on is older than it looks. The
  directory is a writable per-run copy, so building or running tests in it is
  fine; just keep in mind that your findings anchor to line numbers *as they
  exist here*, so an edit you leave behind will shift your own evidence.
- `change/change.diff` — the change under review, as a unified diff.
- `work-item/work-item.json` — the review request. Its `body` field is the
  reviewer brief; read it first and honor what it asks for.

## Output — the `review` record

Produce the declared `review` output as a strictly validated `review/v1`
record.

**Write a provisional record EARLY, then refine it.** A run that ends without
a written record is a total loss no matter how good the review was, and
investigation always expands to fill the turns available. So: read the brief
and the diff, then write the record with whatever you have; then keep
reviewing and rewrite it as your findings firm up. Writing is idempotent — the
last successful write wins. Do not save the write for the end.

How to write it — the platform tells you which mechanism is active in the
sections it prepends above this prompt; prefer them in this order:

1. If structured output builder MCP tools are available (`describe_output`,
   `write_output`, `validate_output`), use them with output name `review`.
   Call `describe_output` early to see the exact contract.
2. If the MCP tools are not connected but the environment has
   `CONCOURSE_OUTPUT_BUILDER_MCP=1`, drive the same builder over plain HTTP —
   it listens on `http://127.0.0.1:7783/mcp`:

   ```sh
   curl -s -X POST -H 'Content-Type: application/json' http://127.0.0.1:7783/mcp \
     -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"describe_output","arguments":{"output":"review"}}}'
   ```

   Write the record with tool `write_output`, arguments
   `{"output":"review","subjects":[{"id":"change","role":"primary","input":"change"},{"id":"repository","role":"context","input":"repository"}],"body":{...}}`
   — the platform fills in every type, schema, and digest itself. Confirm with
   `validate_output`. Never hand-write `record.json` while the builder is
   reachable.
3. Otherwise write `record.json` into the directory named by
   `$AGENT_OUTPUT_REVIEW`, with the envelope
   `{"record_version": "1.0.0", "type": ..., "schema": ..., "subjects": [...],
   "body": {...}}`. Take `type`, `schema`, and every subject's `type`/`digest`
   EXACTLY from the "Sealed record authority" values the platform provides —
   never invent or guess a digest. Declare the `change` input as the single
   `primary` subject (id `change`) and the `repository` input as a `context`
   subject (id `repository`), each with `"input"` naming that port.

`body` rules (enforced at sealing in both mechanisms):

- `conclusion` — `accept`, `changes-required`, or `inconclusive`.
  `changes-required` REQUIRES at least one finding with `blocking: true`;
  `accept` FORBIDS any blocking finding.
- `summary` — short markdown: what you reviewed, what you concluded, and which
  review angles came back clean.
- `findings` — array, **lexicographically sorted by `id`**, one entry per
  defect:
  - `id` — short stable slug (letters, digits, `.`, `_`, `:`, `-`), e.g.
    `archiver-unpinned-linkage`.
  - `severity` — one of `observation`, `low`, `medium`, `high`, `critical`.
    `high` and `critical` MUST set `blocking: true`; `observation` must NOT be
    blocking.
  - `blocking` — whether this alone should stop the change from merging.
  - `category` — a slug such as `correctness`, `security`, `performance`,
    `tests`.
  - `title` and `description` — the description must state the concrete
    sequence that produces the bad outcome.
  - `evidence` — REQUIRED for every non-observation finding. One or more
    anchors:

    ```json
    {"subject": "repository",
     "locator": {"kind": "file-lines", "path": "atc/db/example.go",
                 "start": 120, "end": 134}}
    ```

    `path` is relative to `repository/`; `start`/`end` are 1-based line
    numbers in the file as it exists in `repository/` (post-change), NOT diff
    positions. Anchor to the lines where the defect lives.
  - `recommendation` — what to do about it.

Report findings at or above the severity named by the `MINIMUM_SEVERITY`
environment variable.

## How to review

Read the brief, then the diff, then the touched code in `repository/` — and
follow the change's assumptions out into the code it attaches to; the
defects that matter most usually live where the diff meets pre-existing
machinery, not inside the diff hunks themselves. Verify claims made by
comments and commit messages rather than trusting them.

**Report what you find; let severity carry your confidence.** A finding you
checked and confirmed is `high` or `critical`. A concern you could not settle
within your budget is still a finding — report it as `medium` or `low` and say
plainly what you verified and what remains open. Do not drop a suspicion
because you could not prove it, and do not conclude `accept` merely because
nothing reached certainty. When a finding depends on how a library or helper
treats its input, read that code before rating it `high` — an unverified
assumption about someone else's code is the most common way a review is wrong,
but the remedy is to check and re-rate, not to stay silent.

The defects worth the most are rarely inside the diff hunks. A change inherits
assumptions from the code it attaches to: who else writes the values it reads,
what the callers of a function it changes expect, what state a UI holds across
the transition it introduces. Treat a naming convention, a comment, or a test
as a claim to verify, never as proof.

**Writer tracing is mandatory, not optional.** Following those threads is the
step reviews skip, so it is a required deliverable rather than advice. Before
you write your final record you must, for **at least two** distinct values the
change READS but does not itself write — a database column, a struct field, a
model field, a config key — find every place that WRITES it and check that the
change's assumption about it holds. The repository is a real git repository;
use it:

```sh
git -C repository log -S'<column_or_field>' --oneline    # who introduced writes
git -C repository grep -n '<column_or_field>'            # every mention today
git -C repository blame -L <start>,<end> -- <path>       # when a relied-on line landed
```

`git log --oneline -- <path>` is not writer tracing; it lists commits, not
writes. Name each traced value in your summary along with what you concluded
about it. A value whose only writer is the change itself is a finding in its
own right: nothing else populates it, so every existing row or record reads
empty.

If you believe the change is correct, say so with `conclusion: accept` and no
blocking findings — a review that invents defects to look thorough is worse
than one that finds none. State plainly in the summary which angles you
checked and found clean.
