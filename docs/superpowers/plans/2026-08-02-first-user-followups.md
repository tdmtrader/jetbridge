# First-user follow-ups

Work identified during the first-user dogfood and its remediation that was
deliberately **not** done at the time, either because it needed a design
decision or because it sat outside the scope of the change in hand. Each item
states what is actually true on `jetbridge` as of 2026-08-02, verified against
the code rather than carried over from when it was first noticed.

Two remediation efforts ran in parallel on 2026-08-01 and converged: one landed
the `PublicValidationFailure` disclosure mechanism, the checksummed agent-runner
CLI pin, and the reusable node packages; the other landed `git` in the shipped
web image, node-run cancellation, `--json` on node import/release, the
`show-run` failure reason, and the hermetic-egress warning. Where an item below
was resolved by that convergence it is marked closed, with what closed it.

---

## 1. Three runtime images still ship without `git` — OPEN, highest value

`deploy/concourse-pipeline.yml`'s `build-image` job installs a pinned
`git=1:2.34.1-1ubuntu1.17`, and `deploy/runtime_image_parity_test.go` guards it
against `Dockerfile.build`. Three more copies of the same runtime image do not,
and the guard does not read them:

- `Dockerfile.local` — builds `concourse-local:latest`, the default
  `CONCOURSE_IMAGE` for `make test-k8s-integration` and `make test-k8s-behavioral`
- `deploy/k8s-e2e-pipeline.yml` (two build sites)

Verified 2026-08-02: none contains `git=1:2.34.1`.

Why it matters more than a missing package: `repository/v1` validation and the
ATC direct-Git publisher both exec `git` inside ATC, so **no k8s test tier can
exercise either**. That is precisely the blind spot that let the original
defect ship — every unit test passed on dev hosts where git happens to exist.
`deploy/test-pipeline.yml` is a fourth copy but builds a web-only image; judge
whether it needs the same treatment.

Do: add the pinned package to those images; widen the parity guard to a table
of (file, extraction) pairs so it covers every copy; and, if feasible, add a
k8s-tier assertion that actually seals a `repository/v1` snapshot, so the
capability is proven on a shipped image rather than inferred from a package
list. Note the exact apt pin will eventually vanish from the Ubuntu 22.04
archive and fail every build — decide deliberately whether to keep pinning.

## 2. Record *body* validation failures are still opaque — OPEN

`PublicValidationFailure` (`agent/snapshot/validator.go`) now carries a closed
enum of disclosable reasons, and `writeSnapshotError` returns them as
`{error, message, reason}`. `agent/snapshot/contracts/repository.go` uses it
throughout, so a bad `repository/v1` upload now explains itself.

Two layers still do not, verified 2026-08-02:

- **Record bodies.** `agent/snapshot/contracts/review.go` and its siblings have
  zero public failures. Every rule the node-authoring guide tells prompt
  authors to encode — the severity vocabulary, blocking coupling,
  accept/changes-required, id sorting — fails with a bare `validation_failed`
  and no reason. This is the failure a hand-authoring user is most likely to
  hit, because the guide actively instructs them to satisfy those rules.
- **Archive rejections.** `writeSnapshotError`'s `ErrInvalidArchive` branch is
  still a plain `writeError`. A tar with a `./` entry, a trailing separator, a
  duplicate canonical path, or an escaping symlink comes back as
  `snapshot archive is invalid` with nothing else — the original F5/F6 class.

Do: extend the enum to cover both. Keep the discipline that makes the existing
mechanism safe — the disclosed value is a fixed constant from a closed set, and
the detailed cause stays in the server log. For archive rejections the
offending entry name is caller-submitted and would be genuinely useful in the
message; if you want it, that is a deliberate widening of the contract, not a
free addition, and it needs a length bound because `MaxSnapshotPathBytes` is
4096.

## 3. `fly agent workflows import` has no `--json` — OPEN

`fly/commands/agent_workflows.go` prints the allocated revision as prose only
(verified: no `Json` field on `WorkflowsImportCommand`). A script that wants
`workflows set-live NAME VERSION` must scrape it, or use `--set-live` and give
up the inspect-before-promote step the operations guide recommends.

This is the same problem already fixed for `nodes import`/`release` in
`691886649b` — read that commit for the pattern. It hit a trap worth
anticipating: the release response type carried no name or version, so
marshalling the raw server type would have omitted the field the flag exists
for, and a small wrapper struct was needed. Check what the workflow import
endpoint actually decodes into before assuming, and prove it with a real
`jq -r .version` against captured stdout.

While there, check whether `workflows set-live` and the other workflow
subcommands lack `--json` too, and report rather than fixing them all.

## 4. Hermetic egress failures are still unexplained at runtime — OPEN

Hermetic pods run under a deny-all egress NetworkPolicy the chart emits
unconditionally. With `networkPolicy.hermeticEgressTo` empty, an agent's first
model call hangs about five minutes and dies with a bare client timeout. That
cost a live debugging session.

`b1cf374cd7` added two warnings — a NOTES block and an annotation on the
hermetic NetworkPolicy that survives `helm template` on the ArgoCD path — but
both are *install-time*. Neither reaches the user during the failure.

Do: surface it at runtime, on the build page. `atc/exec/agent_step.go` already
carries `step.plan.Hermetic`. When a hermetic step terminates without output on
a timeout-shaped failure, say so. Two constraints: do not claim the cause when
you do not know it — gate on the failure shape (timeout / no output / zero
tokens), because a confidently wrong explanation is worse than none; and the
same reasoning applies to hermetic `task:` steps, not only agent steps. A
wrapped error would also improve `fly agent nodes show-run`, which now reads
the terminating error event.

## 5. The agent-runner writeback pins an image the cluster cannot pull — CLOSED 2026-08-02

`build-agent-runner-image` writes the new digest into home-infra
`apps/concourse.yaml` itself. On 2026-08-01 it wrote
`ghcr.io/tdmtrader/agent-runner@sha256:41601273…` and every ATC-created agent
pod failed `ErrImagePull`.

Diagnosed at the time: the GHCR package is private (anonymous manifest fetch
returns HTTP 403) and the cluster has no credentials — no dockerconfigjson
Secret in `cicd`, no `imagePullSecrets` on the web pod, and none on the default
ServiceAccount that ATC-created agent pods inherit. Note agent pods are built
by ATC's k8s runtime directly, not by the chart, so a chart-level
`imagePullSecrets` value may not reach them; check
`atc/worker/jetbridge` pod construction before assuming it does. The identical
image and digest is also pushed to the in-cluster `registry.home`, which pulls
without credentials.

**A hand override is in place** (home-infra `9157d2f`) repointing that row to
`registry.home`. The file's own comment says the pipeline owns the row, so the
next `build-agent-runner-image` run will reintroduce the GHCR value and break
every agent pod again.

**Fixed 2026-08-02** by making the writeback emit the in-cluster reference.
The job still builds, pushes, digest-verifies, immutable-pulls and
platform-inspects through GHCR exactly as before; it then mirrors the same
commit tag to `registry.home`, **asserts the two registries return the same
digest**, and writes `registry.home/agent-runner@<digest>`. Because the digest
is content-addressed, that equality is the whole proof the pullable reference
is the byte-identical image just verified. GHCR remains the offsite mirror.

The `^ghcr\.io/…` assertion was carried in five places, all updated together:
the writeback task, the unprivileged validator, `write-agent-runner-home-infra.sh`,
`deploy/agent_runner_dockerfile_test.go` /
`deploy/write_agent_runner_home_infra_test.go`, and the runbook and design doc
that gate promotion on it. Leaving any one behind would have failed the job or
silently reintroduced the unpullable value.

The alternative fixes — a GHCR pull secret, or making the package public — were
not taken. A pull secret has to reach pods ATC builds itself rather than the
chart, so it would need to hang off the namespace default ServiceAccount and
would be a credential that must exist and stay valid for a pod to start at all;
pulling from the registry already inside the cluster needs no credential.

## 6. Reviewer finds the right code and misjudges it — OPEN, experiment not fix

Across five graded passes on `review-jb-004`, the code-review node's location
recall held at about 1/2 while its mechanism accuracy stayed near zero until
the final run, which produced one true finding (the oracle's bonus F2b). It has
never once found F1, which requires leaving the diff to discover who *writes*
`agent_tickets.pipeline_run_id`.

Prompt tuning is exhausted: four wordings were tried and the two that raised
precision suppressed reporting entirely (`conclusion: accept`, zero findings, on
a case with two real majors). See `bench/nodes/RERUN-2026-08-01.md` and the
findings record for the measurements.

Candidate next moves, in order: give the review node a capability sidecar with
code intelligence (find-references, call-hierarchy) instead of grep; or
decompose into two nodes — an `impacted-surface` node enumerating writers and
callers of everything the diff touches, feeding a review node required to
anchor into that surface. Either is a node-composition experiment and wants a
graded A/B, which brings up the next item.

## 7. `agent/experiment` cannot target a node — OPEN

`TargetKind` is `workflow` or `function` only, so every prompt or model change
to a reusable node has to be A/B'd by hand against a corpus case. That is how
the precision-versus-recall regression above was found, and it would have been
missed without it. Adding `TargetNode` would make the loop first-class and is
the prerequisite for doing item 6 honestly.

---

## Closed by the 2026-08-01 work

- **Claude CLI pin lacked `--max-budget-usd`.** The pin was
  `@anthropic-ai/claude-code@2.0.1`, which contains no such flag — verified by
  unpacking the published package. Every budgeted agent step failed, and a
  rebuild reinstalled the same version. Closed by the checksummed 2.1.212 pin;
  verified live (`claude --version` in a running agent pod) and by two nodes
  completing with `budget_slice_usd: 5.0` enforced.
- **`repository/v1` validation failures were opaque.** Closed by
  `PublicValidationFailure` covering `repository.go`.
- **Redundant prefixes in the disclosed error.** Moot: the closed-enum
  mechanism returns a fixed `message` plus a machine-readable `reason` rather
  than composing a package-prefixed string.
- **`git` missing from the deployed web image.** Closed for the shipped image
  (`62c5d5ae8b`) and verified by running `git --version` in a pod from
  `registry.home/jetbridge:v0.2.221-rc` → `git version 2.34.1`. Still open for
  the three images in item 1, and the deployment had not yet rolled onto the
  new image at the time of writing.
