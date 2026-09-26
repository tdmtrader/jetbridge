# Implement worker

A developer submits a repository snapshot and a brief, closes the laptop, and
later retrieves a patch against the snapshot's base plus a summary of what the
agent did. The agent is edit-only: Codex changes a writable copy of the
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
total, a 256 KiB brief). Submission packages it under `snapshot/` in the Run
input; the template reads `source/snapshot`.

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

## Operator template

The [implement template](../../deploy/implement-template.yml) has one inline
task, `author`, with input `snapshot` and result `change`. It runs the same
worker image as review, in its `implement` mode. Install it with fixed operator
choices:

```sh
fly -t YOUR_TARGET set-pipeline --team YOUR_TEAM -p implement -c deploy/implement-template.yml \
  -v review_worker_image=YOUR_REGISTRY/review-worker@sha256:YOUR_DIGEST \
  -v implement_model=YOUR_CODEX_MODEL
fly -t YOUR_TARGET unpause-pipeline --team YOUR_TEAM -p implement
```

The image digest and model are fixed at installation; a submission chooses
neither. The default criteria are embedded from `implement-profile.md`.

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
path (`features/implement-submit.feature`); both need Linux tmpfs and run in CI.
