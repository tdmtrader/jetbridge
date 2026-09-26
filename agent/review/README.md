# Review worker

A human, Codex, Claude, or another MCP client can submit the same detached,
inspection-only review. The CLI and stdio MCP use one shared client and the
platform's durable Run, input and result contracts. No additional job store is
involved. The client is the workload-neutral `agent/detached`; `agent/review/client`
binds it to review's bundle, `change` input, `findings` result and report.

## Capture and submit

Build `jb` with `go build -o /tmp/jb ./cmd/jb`, then use a saved `fly` login:

```sh
jb review capture --repo /path/to/repo --base origin/main --head HEAD \
  --plan /path/to/plan.md --output /path/outside/repo/change
jb review submit --target YOUR_TARGET --team YOUR_TEAM --template review \
  --input /path/outside/repo/change --receipt /path/outside/repo/request.json \
  --auth-file /owner/selected/auth.json
```

The plan is optional. The receipt must be outside the input bundle. It retains
only destination, input digest and invocation/Run identifiers, never credentials
or upload bearers. Retry with the **same receipt** after interruption. A changed
input or destination requires a new receipt. Losing a response does not create a
second Run. A claimed credential delivery is never retried or reseeded.

Only `ready: true` confirms that the detached worker accepted the session's
credentials and checked its installed Codex version. It does not confirm provider
login or review success. An admitted but unready response still includes the Run
handle. Once ready, the caller can close the CLI or MCP and check the Run later.
Each session receives its own memory-backed credential home; refreshes stay there
and are discarded with it. Nothing is written back to the owner's auth file.

## Operator template

The [review template](../../deploy/review-template.yml) contains one inline task,
input `change` and result `findings`. Install it with fixed operator choices:

```sh
fly -t YOUR_TARGET set-pipeline --team YOUR_TEAM -p review -c deploy/review-template.yml \
  -v review_worker_image=YOUR_REGISTRY/review-worker@sha256:YOUR_DIGEST \
  -v review_model=YOUR_CODEX_MODEL
fly -t YOUR_TARGET unpause-pipeline --team YOUR_TEAM -p review
```

Build that image from `deploy/Dockerfile.review-worker`. Its default criteria
are embedded from `review-profile.md`; image digest and model are selected at
installation, not supplied by review requests.

The template alone does not fix the image: any team member able to set the
template can name another one, and the next Run would receive the owner's
credentials in it. So web delivers credentials only into an image the operator
pins by digest, with `--run-credential-worker-image=YOUR_REGISTRY/review-worker@sha256:YOUR_DIGEST`
(chart value `web.runCredentialWorkerImages`, repeatable). The Run's snapshotted
result producer must declare exactly `rootfs_uri: docker:///<pin>`, with no
`image:` artifact, `image_resource` or sidecar. Tag-only references are refused.
**With no pin configured, every credential handoff is refused**; submission then
reports an unacknowledged delivery and the Run receives no credentials. The pin
covers the image, not the task script: a member who can set the template can
still change what that image runs, so restrict who holds that role on the
review team. The pin is per image, not per template: the
[implement template](../implement/README.md) runs the same image and is admitted
by the same pin ([ADR-0006](../../docs/adr/0006-credential-pin-per-image.md)).
The ATC fills `run_id` per Run.
The worker stages a complete report below the output mount, then the task moves
its two validated files into the named result before successful completion.

Intake additionally requires `web.runInputSigningKeySecret`, naming a separate
web-only Secret with `input.key` containing exactly 32 random raw bytes. This
service key signs input grants; it is unrelated to Codex credentials and must
not be shared with node keys. Both Hangar facets and `hangarOutput.webEnabled`
must be configured: a review template declares results, so its Runs are
admitted only while the web node's Hangar output epoch is enabled. Run
admission itself is on at every deploy (`web.pipelineRunActivationEpoch`). Do
not activate this feature until the remaining acceptance checks pass.

## Checking a Run

After logging in with `fly`, any new local process can read a Run:

```sh
jb review status --target YOUR_TARGET --team YOUR_TEAM --template review --run 1
```

The JSON response includes its durable identity and status. Authorized team members also receive ordered capture progress, including selection, sealing and disposition, after payload cleanup. An authorized caller
also receives the immutable `terminal` observation once published, including the
result references and version. Pending Runs have no terminal observation. This
continues working after disposable build data is reclaimed. It does not download
the report files. Status uses platform credentials only; it needs no Codex auth.

To expose `review_submit`, `review_status` and `review_result` to an MCP client, configure a stdio server with command
`jb` and arguments `review mcp --target YOUR_TARGET --team YOUR_TEAM --template
review --auth-file /owner/selected/auth.json`. The client must start it under the user with that saved platform login.
For clients accepting the common JSON server configuration:

```json
{
  "mcpServers": {
    "jetbridge-review": {
      "command": "jb",
      "args": ["review", "mcp", "--target", "YOUR_TARGET", "--team", "YOUR_TEAM", "--template", "review", "--auth-file", "/owner/selected/auth.json"]
    }
  }
}
```

Call `review_status` with `{"run": 1}`. Its output schema describes a compact Run
observation and the same terminal result bindings. Closing the local MCP process
does not change the Run. The server selects its target, team and template at
startup; tool arguments cannot replace its platform credentials. Only the local auth file path is part of startup configuration; its contents never enter tool arguments. Omit `--auth-file` to expose status/result access without allowing submission. Call `review_result` with `{"run": 1}` to retrieve the
schema-validated report, including typed findings and provenance. Call `review_submit` with `{"input":"/path/to/change","receipt":"/path/to/request.json"}`. The same stdio configuration works for Codex, Claude and other MCP clients.

`jb mcp --target YOUR_TARGET --team YOUR_TEAM --review-template review
--implement-template implement --auth-file /owner/selected/auth.json` is one
server that serves these tools unchanged beside the implement workload's
`implement_*` tools (see the [implement README](../implement/README.md#local-mcp)).
`review mcp` stays the same server with only the review tools, so existing
configurations keep working.

Retrieve the same report from a fresh human CLI process:

```sh
jb review result --target YOUR_TARGET --team YOUR_TEAM --run 1
jb review result --target YOUR_TARGET --team YOUR_TEAM --run 1 --format markdown
```

The default result name is `findings`; `--result NAME` (or the MCP `result`
argument) selects another named result containing `review.json`. The client
verifies the canonical archive against the immutable Run binding, validates the
report schema and checks its Run identity before returning anything. Pending,
failed, unauthorized or missing results are explicit errors.

Operators enable downloads by setting `hangarOutput.readControlURL` to an HTTPS
web API address reachable from output daemons. The server certificate must be
trusted by the system or output-daemon TLS CA. The node uses signed read-lease
callbacks; it holds bucket access, while the web API serves only verified copies.
Current readers serve their configured activation epoch; reading older epochs
after an epoch rotation is not yet wired.

Build the local command:

```sh
go build -o /tmp/jb ./cmd/jb
/tmp/jb review capture --repo /path/to/repo --base origin/main --head HEAD \
  --plan /path/to/plan.md --output /path/outside/repo/change
```

The plan is optional. The repository must be clean, including untracked files;
ignored files are not captured. Base and head resolve once to immutable commits.
The capture contains `manifest.json`, `change.diff`, both complete trees under
`base/` and `head/`, and optional `plan.md`. It reads raw Git objects, so export
attributes, executable bits, filters and hooks cannot alter or execute the input.
Symlinks are stored as inert target text; the manifest preserves their Git mode.
Submodules and LFS pointers are explicitly unsupported. Limits are 16 MiB per
source/plan file and 256 MiB for the input tree including the diff. Oversized input
fails capture rather than being silently truncated.

Submission packages this capture under `bundle/` in the Run input. The template
reads `source/bundle`, keeping Hangar's materialization receipt outside the sealed
review inventory. Local capture and worker paths remain unchanged.

## Running one review

[`codex-version`](../session/codex-version) and
[`codex-checksums.txt`](../session/codex-checksums.txt) pin the supported Codex
release for every worker mode; the provider session in `agent/session` checks the
pin on each run. The Dockerfile verifies the release archive and uses immutable base-image digests.
The worker requires an explicit model; it does not choose billing or another model.

```sh
docker build -f deploy/Dockerfile.review-worker -t jb-review-worker:dev .
mkdir -p /path/to/review-results
docker run --rm -i --read-only \
  --user "$(id -u):$(id -g)" \
  --tmpfs /dev/shm:rw,nosuid,nodev,noexec,size=256m \
  --mount type=bind,src=/path/outside/repo/change,dst=/input,readonly \
  --mount type=bind,src=/path/to/review-results,dst=/output \
  jb-review-worker:dev \
  --input /input --output /output/review --model YOUR_CODEX_MODEL \
  --auth-stdin --timeout 30m < /owner/selected/auth.json
```

The private-fixture subscription smoke passes with Codex 0.153.1 and `gpt-6-astra`.
The owner accepted this deployment's account-auth risks on 2026-09-16, resolving
**AUTH-1** for reviewing JetBridge on controlled private workers. This is an owner
deployment decision, not provider confirmation: the
[advanced account-auth CI guide](https://learn.chatgpt.com/docs/auth/ci-cd-auth)
excludes public/open-source repositories from its persistent-auth workflow. The
general [headless authentication guide](https://learn.chatgpt.com/docs/auth)
documents cache copying. This implementation instead discards all session auth,
including refreshes, when the run ends; it provides no durable auth renewal.
The deterministic test suite uses synthetic credentials and makes no real model
calls. A successful test with those fixtures does not establish live account
eligibility or detached subscription behavior.

Credentials travel on stdin separately from the input. Each invocation creates a
private mode-0700 home on tmpfs, with a mode-0600 auth file. The worker forces
ChatGPT auth, rejects API-key auth files, and gives the child a clean environment.
It does not inherit local Codex configuration, repository hooks, MCP connections,
API keys, or the caller's home. Refreshed files stay in that same temporary home.
The source file on the owner's machine is never modified.

Success, error, cancellation and timeout all remove the runtime before publication.
The timeout includes an abandoned credential stream. A disposable container is
required so abrupt process/container loss also destroys the tmpfs; **a bare process
using a shared host `/dev/shm` does not provide that guarantee**. Remote handoff
installs a bounded Pod deadline before sending credentials. Live Brine checks
verify deadline enforcement and credential removal after worker and node runtime
loss, using synthetic credentials. Do not bind a
host credential directory or persist this runtime in a Secret, volume or artifact.
There is no credential recovery, cross-run reuse or write-back. If a later local
copy is stale, reauthenticate and submit a new review.

The foreground worker exits with its container; use `jb review submit` for detached platform execution. Its result survives in `/path/to/review-results/review`.

For integration with an already-started worker, `--auth-socket /runtime/auth.sock
--run-id RUN_ID` replaces `--auth-stdin`. `/runtime` must be that container's
private mode-0700 tmpfs runtime directory, also supplied as `--runtime-dir`.
The in-container helper `jb-review-worker auth-handoff --socket
/runtime/auth.sock --run-id RUN_ID` consumes auth on stdin and returns a small
`ready` response only after the worker has staged it and verified its installed
Codex version. The helper may then disconnect. This acknowledges staging, not
successful provider authentication or a completed review.

The socket accepts one connection and disappears when that connection is
accepted. Wrong Run identity or an interrupted handoff consumes the attempt;
there is no reseeding. `--handoff-timeout` defaults to two minutes and the worker's
overall timeout still bounds the session. The transport does not authorize a
remote caller: the platform endpoint verifies the original invocation owner,
current roles, Run and signed execution/Pod before claiming a single handoff.

## Inspection and findings

The default operator profile is `review-profile.md`; `--profile` can select another
operator-controlled criteria file. Codex runs in an empty control directory with
shell/unified execution, web access, apps, plugins and subagents disabled. Its
private stdio MCP server exposes only `list`, `read` and literal `search` over the
verified input inventory. Submitted paths never grant access to the credential
home. The event checker rejects command execution, edits, other MCP servers and
unknown tool kinds; raw provider stdout/stderr is not published or logged.

The output schema is derived from `$defs.assessment` in `review.schema.json`, the
single production declaration. The model supplies only that assessment. The
harness stamps provenance, derives the verdict and replaces provisional IDs once
with `f-001`, `f-002`, etc. A finding must name an inspected changed file, an
existing base/head side and valid text line bounds. Deleted files can point to the
base. Findings are not proof that code ran; the report always states non-execution.

Both `review.json` and `review.md` are published together after validation. Existing
reports cannot be overwritten. No output, schema failure, policy violation or
provider error yields an authoritative report. Partial or missing declared file
coverage yields `incomplete`; otherwise the verdict is `findings` or `no_findings`.
Findings do not make the worker fail. `run_id` is null for local reviews; the installed template supplies its durable identity using `--run-id`.

```sh
/tmp/jb review render --input /path/outside/repo/change \
  --report /path/to/review-results/review/review.json
/tmp/jb review schema
```

The input digest binds the complete canonical manifest and its file/diff/plan
digests. It detects changes against the captured input; it does not independently
authenticate the submitter. Durable ownership and authorization belong to the
Run admission service.

## Validation

Behavioral coverage lives in the existing JetBridge Brine adapter. Build that
adapter before running it, using the Brine CLI/engine revision pinned by its
module and CI image. From `atc/worker/jetbridge/brine`:

```sh
go build -o .build/brine-adapter-jetbridge ./cmd/brine-adapter-jetbridge
brine run features/review-artifacts.feature --mode sync
brine run features/review-worker.feature --mode sync
```

Worker scenarios require Linux tmpfs. On macOS, run the artifact feature only;
it includes the portable stdio scenarios. The CI Brine job discovers both files.
The scenarios use compiled production commands, real Git repositories, real
stdio pipes and real credential cleanup. Only the model process is substituted,
with deterministic assessment/event output and synthetic auth. Run, database
and HTTP operations use their actual implementations. API-only scenarios supply
Pod status explicitly; the separate live tier observes real kubelet status.

Low-level schema, containment and protocol checks remain in Go:

```sh
go test ./agent/review ./cmd/jb ./cmd/jb-review-worker
go test -count=1 -run TestAgentic .
go vet ./agent/review ./cmd/jb ./cmd/jb-review-worker
```

The real-model smoke is opt-in: `features/subscription/review-live.feature` is
outside the ordinary Brine glob. `hack/test-review-subscription` creates a private
two-commit fixture with a first-byte regression and invokes that feature in a
fresh disposable container. It checks a located finding, schema/provenance,
unchanged input, matching Markdown and credential cleanup. It makes a real model
call and consumes subscription usage; never run it as an ordinary CI test.

Prepare a directory with a host `jb`, Linux `jb-review-worker`, and the pinned
Linux `codex` and companion `codex-code-mode-host` binaries verified against
`agent/session/codex-checksums.txt`. The review instructions restrict Code Mode to calls to the
input reader and prohibit evaluating repository code. Shell tools remain disabled,
and the worker validates the same closed event stream.
Build the Linux Brine
adapter from its own module, matching the container architecture. Supply a
trusted image containing Brine, `/bin/sh` and CA certificates:

```sh
hack/test-review-subscription \
  --auth-file /owner/selected/auth.json --model OWNER_SELECTED_MODEL \
  --brine-image YOUR_BRINE_IMAGE --binaries /docker/shared/review-bin \
  --adapter atc/worker/jetbridge/brine/.build/brine-adapter-linux \
  --output /docker/shared/new-review-evidence
```

All bind-mount paths must be shared with Docker. Auth travels only on stdin into
tmpfs and is unlinked before the worker starts; the original file is checked for
changes. The container is removed even on failure. This private-fixture worker
check complements the installed-template cases; it does not establish provider
eligibility for public repositories or test a live remote Run with real auth.

The explicit Linux acceptance features live under
`atc/worker/jetbridge/brine/features/kubelet/`. The disposable CI task in
`deploy/review-kubelet-task.yml` runs them with a real K3s node, BusyBox, SPDY
transport and container memory. It requires a committed `repo` input and a
`brine-module` input containing the pinned private module's Go download-cache
files, with no credentials. Run it as a privileged CI task; never substitute a
developer or production cluster. Node runtime loss runs separately, after the
other scenarios. Missing live infrastructure fails rather than skipping tests.

Named input routing is declared on a template task as `run_inputs: [{name: change, input: source}]` beside its stable `task_id`. Routes require inline task inputs and literal relative mount paths, with no overlapping writable mounts or `input_mapping` for that slot. Validation, durable binding and worker delivery are implemented. Authenticated upload admission publishes through the node and issues a short-lived source grant only after acquiring a temporary claim. Expiry releases that claim; admitted Runs retain their own claims. Shared submission uses this upload and admission path; public v2 activation remains held.

The input API is `POST /api/v1/teams/:team/pipelines/:template/run-inputs/:name`,
with a tar body and the caller's platform login. It returns a typed source grant;
the bearer is transient and the Run retains only the immutable source identity.
It requires the operator creation switch, the database activation gate, and the
web-only `--run-input-signing-key` file (exactly 32 raw bytes). This service key
must differ from all node capability/materialization keys. It is unrelated to
the user's Codex credentials. Missing setup refuses intake. The route checks
the upload-specific team role before reading and at each publication boundary.

Versioned admission is `POST /api/v2/teams/:team/pipelines/:template/runs` with
`invocation_key`, optional `vars`, and named `inputs`. The shared client provides
`UploadInput` and `CreateRun`. Keep the same invocation key after an uncertain
response: new admission returns 201, exact replay returns 200, and changed
intent returns 409. An admitted source may be replayed without its bearer. The
server keeps authorization and activation checks on both admission and replay.
Submission follows admission with `POST /api/v2/teams/:team/pipelines/:template/runs/:number/credentials/findings`. The corresponding GET returns only delivery state; claimed/ready replay cannot resend credentials.
