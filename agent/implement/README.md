# Implement worker

A developer submits a repository snapshot and a brief, closes the laptop, and
later retrieves a patch against the snapshot's base, a summary of what the
agent did, and a validation the template produced by running the operator's
command against the patch. The agent is edit-only: Codex changes a writable copy of the
snapshot through its file-edit tool, with shell, exec, network and web search
disabled, and never runs repository code or tests. The patch is applied locally
as one commit on a new branch, whose `base..branch` range is exactly what
`jb review capture` takes.

The submission path is the workload-neutral `agent/detached` client, shared with
review; `agent/implement/client` binds it to the snapshot, the `snapshot` input
and the `change` result. It uses the platform's durable Run, input and result
contracts, and no other job store.

## Capture and submit

Build `jb` with `go build -o /tmp/jb ./cmd/jb`, then use a saved `fly` login:

```sh
jb implement capture --repo /path/to/repo --base origin/main \
  --brief /path/to/brief.md --output /path/outside/repo/snapshot
jb implement submit --target YOUR_TARGET --team YOUR_TEAM --template implement \
  --input /path/outside/repo/snapshot --receipt /path/outside/repo/implement-request.json \
  --auth-file /owner/selected/auth.json
```

`--base` defaults to `HEAD`. The repository must be clean, including untracked
files. The snapshot holds `manifest.json`, `brief.md` and the full base tree
under `base/`, read from raw Git objects with the same rules and limits as a
review capture (no submodules or LFS pointers, 16 MiB per file, 256 MiB in
total, a 256 KiB brief), plus `prior.patch` and `findings.json` when it
carries a prior change or review findings (see
[Iterating](#iterating-a-prior-change-and-review-findings)). Submission
packages it under `snapshot/` in the Run input; the template reads
`source/snapshot`.

The submission receipt behaves as it does for review: it must be outside the
snapshot, it keeps only the destination, the input digest, the workload name
and the invocation and Run identifiers, never credentials or upload bearers,
and a retry must reuse the **same receipt**. A receipt written by one workload
is refused by another, so a review receipt cannot resume an implementation or
the other way round. Only `ready: true` means the worker accepted the session's
credentials and checked its Codex version; after that the caller can disconnect.

## Checking a Run and retrieving the change

```sh
jb implement status --target YOUR_TARGET --team YOUR_TEAM --run 1
jb implement result --target YOUR_TARGET --team YOUR_TEAM --run 1
jb implement result --target YOUR_TARGET --team YOUR_TEAM --run 1 --format markdown
jb implement result --target YOUR_TARGET --team YOUR_TEAM --run 1 --output /path/to/change
```

`result` verifies the downloaded archive against the Run's immutable result
binding, then checks that `summary.json` is schema-valid `implement/v1`, that
`change.patch` matches its `patch_digest` and touches exactly its
`changed_files`, and that its `run_id` is the Run's. The JSON output is
`{"summary": …, "patch": "…"}`. `--output` also writes both files, byte for
byte as published, into a new directory. Pending, failed, unauthorized and
missing results are explicit errors.

```sh
jb implement result --target YOUR_TARGET --team YOUR_TEAM --run 1 --result validation
```

`--result validation` returns the `validation` result, `validation.json`
(`implement-validation/v1`, [schema](validation.schema.json)): the command as
installed, whether the patch applied, the command's exit code, an outcome of
`passed`, `failed` or `not_applied`, and the last 64 KiB of its output. It is
read together with the Run's change and refused unless it names the same Run,
the same snapshot (`input_digest`) and exactly the change's patch
(`patch_digest`). `--format markdown`, for either result, shows the change
with its validation before the patch. A failing command is a result, not a
failed Run: the patch still downloads and applies.

## Applying the change

```sh
jb implement apply --repo /path/to/repo --target YOUR_TARGET --team YOUR_TEAM --run 1
jb implement apply --repo /path/to/repo --result-dir /path/to/change
```

`--run` fetches and verifies the change as `result` does, then applies it;
`--result-dir` applies one already on disk (from `result --output`, or from a
local worker run). Apply refuses a dirty worktree and a base commit the
repository does not have. It builds the commit in a private index with hooks
and global Git configuration disabled, so nothing in the patch runs, then
creates and switches to the branch. The branch defaults to `impl/run-<Run ID>`
(the `run_id` in the summary, which is also the commit's `JetBridge-Run`
trailer); `--branch` chooses another. The commit's parent is the base, so

```sh
jb review capture --repo /path/to/repo --base BASE_COMMIT --head impl/run-41 \
  --output /path/outside/repo/change
```

reviews exactly what the agent wrote.

## Iterating: a prior change and review findings

A Run is never reopened. To iterate, capture a new snapshot of the same base
that carries what came before:

```sh
jb implement capture --repo /path/to/repo --base BASE_COMMIT --brief /path/to/brief.md \
  --output /path/outside/repo/snapshot-2 \
  --target YOUR_TARGET --team YOUR_TEAM --template implement --prior-run 1 \
  --review-template review --findings-run 3
```

`--prior-run` retrieves an implement Run's change exactly as `jb implement
result` does, and `--findings-run` a review Run's report exactly as `jb review
result` does: each archive is verified against its Run's immutable binding and
its `run_id` against the Run before anything is sealed. `--prior-dir` takes a
change already on disk instead (from `result --output`, or a local worker
run), read as `apply --result-dir` reads it. Capture refuses a prior change
whose base is not `--base` or whose patch does not apply exactly to it, and
findings from a review whose range neither starts nor ends at `--base`.

The snapshot then also holds `prior.patch` (the change, byte for byte) and
`findings.json` (the review's `assessment.findings`, in the published
`review/v1` encoding; this workload reads nothing else of the review). Their
digests, and the IDs of the Runs they were verified against (`prior_run`,
`findings_run`), are in the manifest, so they are part of the input digest. A
prior change read from disk records no Run. A snapshot carrying neither has
exactly the manifest, and so the digest, it had before these fields existed.

The session starts from the base with the prior change already applied, and
the workspace tools serve both files read-only as `/input/prior.patch` and
`/input/findings.json`, outside the workspace, where no edit can reach them.
The published patch is still against the base: it carries the prior change
forward, and applies and validates on its own. The summary's provenance
records `prior_run` and `findings_run` from the manifest, and a summary whose
Runs differ from its snapshot's is refused.

## Local MCP

One stdio MCP server serves both detached workloads to a local agent. Configure
command `jb` with arguments:

```json
{
  "mcpServers": {
    "jetbridge": {
      "command": "jb",
      "args": ["mcp", "--target", "YOUR_TARGET", "--team", "YOUR_TEAM",
               "--review-template", "review", "--implement-template", "implement",
               "--auth-file", "/owner/selected/auth.json"]
    }
  }
}
```

It serves the `review_*` tools exactly as `jb review mcp` does, and:

| Tool | Arguments | Returns |
|---|---|---|
| `implement_submit` | `{"input": "/path/to/snapshot", "receipt": "/path/to/implement-request.json"}` | the submission, as `jb implement submit` prints it |
| `implement_status` | `{"run": 1}` | the Run observation `review_status` returns |
| `implement_result` | `{"run": 1}` or `{"run": 1, "result": "validation"}` | `{"result": "change", "summary": …, "patch": "…"}` or `{"result": "validation", "validation": …}` |

The target, team, both templates and the auth file are fixed when the server
starts; tool arguments cannot change them, and the auth file's contents never
enter a tool call. Without `--auth-file` the submit tools refuse and the rest
still work. Results are verified exactly as `jb implement result` verifies
them, and a validation only against its Run's change; a failed validation is a
result, not a tool error. The output schema is built from the published
[summary](implement.schema.json) and [validation](validation.schema.json)
schemas. There is no apply tool: apply a Run's change with
`jb implement apply --run`.

## Operator template

The [implement template](../../deploy/implement-template.yml) has two inline
tasks. `author` takes input `snapshot` and publishes result `change`; it runs
the same worker image as review, in its `implement` mode. `validate` routes the
same `snapshot` input, reads the author's `change` output, and publishes result
`validation`. Install it with fixed operator choices:

```sh
fly -t YOUR_TARGET set-pipeline --team YOUR_TEAM -p implement -c deploy/implement-template.yml \
  -v review_worker_image=YOUR_REGISTRY/review-worker@sha256:YOUR_DIGEST \
  -v implement_model=YOUR_CODEX_MODEL \
  -v validate_image=YOUR_REGISTRY/toolchain@sha256:YOUR_DIGEST \
  -v validate_command='go test ./...'
fly -t YOUR_TARGET unpause-pipeline --team YOUR_TEAM -p implement
```

The image digests, model and validation command are fixed at installation; a
submission chooses none of them. The default criteria are embedded from
`implement-profile.md`.

`validate` copies the snapshot's base tree, applies `change.patch` with
`git apply` (an empty patch is validated against the unchanged base), and runs
`validate_command` with `/bin/sh -c` in that copy, at most 30 minutes when the
image has `timeout`. Its script is inline in the template, takes no `file:`
and no `vars:`, and reads nothing from the change except the patch it
applies, so nothing the agent writes decides what runs. A Run publishes its
results all or nothing, so the script always records the outcome and exits 0
once it has written `validation.json`; a failing or unappliable change is
recorded, never lost. `validate_image` must provide `/bin/sh`, `git`,
`sha256sum`, `cp`, `tail`, `tr` and `sed`, and run as root, since Hangar
reserves task outputs as root-owned directories. A Run input does not carry Git
modes: the script restores executable bits from the manifest, but symlinks are
plain files holding their target, as in every capture. `validate` never
receives the owner's credentials: the platform delivers one handoff per Run, to
the `change` producer, and its image is not the credential pin.

The validation attests what the operator's command reported, not that the
change is benign. The command necessarily executes code the agent wrote, in the
same container that writes `validation.json`; a hostile change can leave a
process behind that rewrites the file after the script finishes, with digests
that still cross-check. Treat `passed` as evidence against honest mistakes,
and review the patch before trusting it.

**The credential pin is per image** ([ADR-0006](../../docs/adr/0006-credential-pin-per-image.md)).
Web delivers credentials only into a result producer whose `rootfs_uri` is
exactly `docker:///<pin>` for a configured `--run-credential-worker-image`
(chart value `web.runCredentialWorkerImages`). The review and implement
templates run the same image, so the pin that admits review admits implement
too; there is nothing more to configure. The pin covers the image, not the
task script: anyone able to set a template on the team can change what the
pinned image is asked to do. Operator-installed templates are the trust
boundary, so restrict who holds that role on the team.

The deployment switches are the review's: Run admission, both Hangar facets
with `hangarOutput.webEnabled` (the template declares a result), the web-only
`web.runInputSigningKeySecret`, and at least one credential pin. With no pin
configured every credential handoff is refused.

## Running one implementation locally

The worker runs outside a Run too, with credentials on stdin:

```sh
docker run --rm -i --read-only \
  --user "$(id -u):$(id -g)" \
  --tmpfs /dev/shm:rw,nosuid,nodev,noexec,size=256m \
  --mount type=bind,src=/path/outside/repo/snapshot,dst=/input,readonly \
  --mount type=bind,src=/path/to/results,dst=/output \
  jb-review-worker:dev implement \
  --input /input --output /output/change --model YOUR_CODEX_MODEL \
  --auth-stdin --timeout 30m < /owner/selected/auth.json
jb implement apply --repo /path/to/repo --result-dir /path/to/results/change
```

Credential handling is the review worker's: a private tmpfs home per session,
ChatGPT auth only, nothing written back, everything destroyed on every exit
path. See the [review worker](../review/README.md) for the details, which apply
unchanged.

## Validation

```sh
go test ./agent/implement/... ./agent/detached
go test -count=1 -run TestAgentic .
```

Brine covers the worker (`features/implement-worker.feature`) and the detached
path (`features/implement-submit.feature`, including the validate task as the
Run's second result producer and the `jb mcp` rows); both need Linux tmpfs and
run in CI. The
template's validate script itself runs in `go test ./agent/implement/client`
wherever `git` and `sha256sum` exist.
