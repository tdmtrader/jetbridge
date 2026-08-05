# Small repository fix

You are implementing one small, well-specified change to a source repository
and delivering it as a reviewable patch.

## Inputs

Directories in your working directory:

- `repository/` — the source tree to change, at the exact revision the change
  must apply to. It is a writable per-run copy, and editing it cannot affect the
  sealed snapshot or any other run.

  **This node still must not edit it, for a specific reason.** The `change`
  record names `repository` as its base subject, and the seal gate re-reads that
  mount, re-hashes it, and refuses the write if it no longer matches the digest
  it was given. So for this node the input has to stay byte-identical until the
  record is written — not because writing is forbidden, but because your own
  output is what breaks. Work in a copy.
- `work-item/work-item.json` — the request. Its `body` field is the brief.
  Read it first and do exactly what it asks, including any constraint it
  states about what you must not change.

## Set up a writable working copy

Do this first, before reading much code:

```sh
cp -a repository work
chmod -R u+w work
cd work
git status --porcelain          # must be empty
BASE_SHA="$(git rev-parse HEAD)"
echo "$BASE_SHA"
```

Every command below runs inside `work/`. Never edit `repository/`, and never
run `git commit`, `git checkout <other-rev>`, `git fetch`, or anything that
moves HEAD — the change is expressed as a patch against `$BASE_SHA`, and the
platform re-applies it to a pristine copy of the base to verify it.

## Output — the `change` record

Produce the declared `change` output as a strictly validated
`repository-change/v1` record.

**Write a candidate change EARLY when the `EARLY_CHANGE` environment variable
is `true` (the default).** A run that ends with no written record is a total
loss no matter how correct the diagnosis was, and investigation always expands
to fill the turns available. With `EARLY_CHANGE=true`:

1. Read the brief, find the defect, make the smallest edit you believe fixes it.
2. **Write the record immediately**, before verifying anything. This is your
   safety net.
3. Then verify and improve, rewriting the record after each real improvement.
   Writing is idempotent — the last successful write wins.

With `EARLY_CHANGE=false`, verify first and write once at the end.

### How to build the record

The payload is a patch, and every field is derivable from `work/` with git. Run
exactly this, from inside `work/`, after your edits are on disk:

```sh
OUT="$AGENT_OUTPUT_CHANGE"

git add -A
# --no-ext-diff: a configured external or textconv diff driver would emit a
# patch that does not reconstruct the staged tree, and the mismatch only
# surfaces later as a result_tree rejection.
git diff --cached --binary --no-ext-diff > "$OUT/change.patch"

OBJECT_FORMAT="$(git rev-parse --show-object-format=storage)"
REPOSITORY_ID="sha256:$( { printf 'concourse.repository/v1\n%s\n' "$OBJECT_FORMAT"; \
    git rev-list --max-parents=0 HEAD | LC_ALL=C sort; } | sha256sum | cut -d' ' -f1 )"
BASE_SHA="$(git rev-parse HEAD)"
RESULT_TREE="$(git write-tree)"
PAYLOAD_DIGEST="sha256:$(sha256sum "$OUT/change.patch" | cut -d' ' -f1)"
```

`REPOSITORY_ID` is the platform's repository identity: the SHA-256 of
`concourse.repository/v1\n`, the repository's own object format, then every
root commit reachable from HEAD in ascending bytewise order, each followed by a
newline. The command above produces it exactly; do not paraphrase it, do not
assume the object format is `sha1`, and do not drop `LC_ALL=C` — the platform
sorts bytewise, and a locale-aware `sort` is not guaranteed to agree.

If `change.patch` comes out empty you have written no change at all. An empty
patch is not a valid no-op record: the seal gate runs `git apply --check` on
it, and git rejects it. Make the edit first.

Then write the record with the output builder. The platform tells you which
mechanism is active in the sections it prepends above this prompt; prefer them
in this order:

1. If structured output builder MCP tools are available (`describe_output`,
   `write_output`, `validate_output`), use them with output name `change`.
   Call `describe_output` early to see the exact contract.
2. If those tools are not connected but the environment has
   `CONCOURSE_OUTPUT_BUILDER_MCP=1`, drive the same builder over plain HTTP at
   `http://127.0.0.1:7783/mcp`:

   ```sh
   curl -s -X POST -H 'Content-Type: application/json' http://127.0.0.1:7783/mcp \
     -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"describe_output","arguments":{"output":"change"}}}'
   ```

3. Otherwise write `record.json` yourself into `$AGENT_OUTPUT_CHANGE`, taking
   `type`, `schema`, and the subject's `type`/`digest` EXACTLY from the
   "Sealed record authority" values the platform provides. The envelope needs
   `"record_version": "1.0.0"` verbatim — sealing rejects any other value — and
   you must create the directory yourself before copying the patch, because
   nothing does it for you on this path:

   ```sh
   mkdir -p "$AGENT_OUTPUT_CHANGE/content"
   cp "$AGENT_OUTPUT_CHANGE/change.patch" "$AGENT_OUTPUT_CHANGE/content/change.patch"
   ```

The `write_output` arguments are:

```json
{
  "output": "change",
  "subjects": [{"id": "repository", "role": "base", "input": "repository"}],
  "body": {
    "repository_id": "<REPOSITORY_ID>",
    "base_sha": "<BASE_SHA>",
    "representation": "patch",
    "result_tree": "<RESULT_TREE>",
    "payload": {
      "path": "content/change.patch",
      "digest": "<PAYLOAD_DIGEST>",
      "media_type": "text/x-patch"
    }
  },
  "content": [{"source": "change.patch", "destination": "content/change.patch"}]
}
```

Rules the seal gate enforces, in the order they usually bite:

- Exactly **one** subject, role `base`, `input` naming the `repository` port.
- `representation` is `patch`, and `result_commit` must then be **omitted
  entirely** — a patch proves a tree, not a commit. Do not commit anything.
- `content[].source` is relative to `$AGENT_OUTPUT_CHANGE` itself, so the patch
  must be written **into that directory first**. `destination` must start with
  `content/`.
- `payload.digest` must be the SHA-256 of the exact bytes you wrote.
- `result_tree` must equal what the platform gets when it applies your patch to
  a pristine base with `git apply --index` and runs `git write-tree`. Producing
  it with `git add -A && git write-tree` on your own copy gives exactly that,
  as long as your working tree contains nothing you did not intend — check
  `git status --porcelain` before staging and remove build output, binaries,
  editor backups, and scratch files.
- The patch must apply to the pristine base with `git apply --check --index`.
  Regenerate it after every edit rather than hand-editing a patch file.

If `write_output` returns `valid: false`, the errors name the exact failed
field. Fix the cause and write again; do not delete the output directory.

## Verification

The `VERIFY_LEVEL` environment variable says how far to go before you treat the
change as final:

- `none` — reason about correctness; run nothing.
- `build` — the package you touched must compile.
- `test` — the package you touched must have its tests run, and they must pass.

At `build` or `test`, work out the right command from the repository itself
(module layout, existing CI config, the brief). Run it in `work/`. If the
toolchain or a dependency is genuinely unavailable, say so in your final
message and fall back to the next level down — but do not silently skip
verification and do not report a change as verified when it was not.

Remove anything verification created — build artifacts, caches, coverage
files, test binaries — before you regenerate the patch. `git status
--porcelain` is the check.

## How to fix

Make the smallest change that satisfies the brief. Respect every stated
constraint, especially ones about signatures, dependencies, and behavior that
must not change; a change that fixes the symptom by breaking a frozen contract
is a failure, not a tradeoff. If the brief asks for a regression test, add it
to the suite that already covers the code you changed, following that suite's
existing style. If you conclude the requested change should not be made, still
produce a record for the smallest defensible change and say plainly in your
final message why the larger request was declined.
