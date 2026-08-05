# Repository snapshot duplication — audit

**Date:** 2026-08-05
**Scope:** what it costs to materialize and seal a repository, found while investigating why `forge-pr` fetches the same remote twice.
**Status:** findings only. No remediation proposed here — the fix touches a sealed contract and deserves its own design.

## Live evidence (theborg, 2026-08-05)

Verified against the running deployment rather than inferred:

```
snapshot 22  repository/v1  258,360,320 B  claims: binding->2026-08-11 | pin subject:... | pin system:resource-capture (NULL expiry)
snapshot 21  repository/v1    2,567,680 B  claims: binding->2026-08-10 | pin subject:... | pin system:resource-capture (NULL expiry)
snapshot 32  repository/v1  226,557,440 B  4,635 files, downstream consumers: 3
snapshot 26  repository/v1  223,054,336 B
snapshot 23  repository/v1  221,725,696 B
```

Two things this settles:

1. **The permanent pin is live, not hypothetical.** `system:resource-capture` — the finalizer's actor — holds NULL-expiry pins on real snapshots today, reached through `fly agent snapshots capture-resource`, which needs no boot flag. An earlier draft of this audit said "nothing is accumulating today"; that was wrong. What remains pre-enablement is only the 4x PR-observation multiplier.
2. **The whole-tree cost is real in production.** Four `repository/v1` snapshots at 221-258 MB each, from ordinary agentic workflows. Total store: 0.96 GB across 37 snapshots, 0.87 GB of it eight `repository/v1` blobs.

Absolute volume is small today and nothing needs reclaiming — the three largest are user-pinned and carry downstream consumers. The concern is the growth rate once anything runs at scale.

## Why the PR half is not urgent, and why it still matters

**No PR observations exist.** The PR monitor has never run: `--agent-publisher-pull-requests-enabled` fails boot validation (`atc/atccmd/agent_publisher.go:20`, `:83`, `:156`) and no production code constructs the monitor coordinator. There are zero pinned `pull-request/v1` observations on any cluster. Everything below is what happens *on enablement*.

It matters because the numbers decide whether the PR monitor is affordable at all, and because the root cause is not specific to pull requests — it is how `repository/v1` represents a repository.

## The measurement

One admit build materializes two directories, `source-repository` (PR head) and `target-repository` (PR base). Both are fetched separately from the **same** remote (`agent/pullrequest/resource/in.go`, the checkout loop passes `RemoteURL: request.Source.RepositoryURL` for both).

Measured on this repo, `main`, fresh `git init` + single-ref fetch reproducing `dependencies.go:74-90`:

| shape | git objects | pack | dir size | entries | fetch CPU |
|---|---:|---:|---:|---:|---:|
| head-only | 225,812 | 176.17 MiB | 256,724 KiB | 5,388 | 10.3 s |
| base-only | 225,765 | 176.16 MiB | 256,704 KiB | 5,384 | 11.6 s |
| **both refs, one directory** | **225,812** | **176.17 MiB** | **257,128 KiB** | **5,389** | **11.1 s** |

The base's object set is a 99.98% subset of the head's. Adding the second ref to a repository that already holds the head costs **0 additional git objects, 404 KiB, and 1 filesystem entry**. So **50.1% of transferred bytes and 49.4% of fetch CPU are pure duplication**, and removing it is nearly free at the git layer.

## Storage duplication is 4×, not 2×

Three blobs carry repository bytes per observation, not two:

- **Blob A — `pull-request/v1`.** The capture task is a literal `cp -a <source>/. <output>/` (`atc/resource_capture.go:97-99`) over the whole get destination, which `in` asserts contains exactly `{record.json, source-repository, target-repository}`. So the observation snapshot **contains both complete repositories**.
- **Blobs B and C — two `repository/v1`.** `pr-monitor-v3`'s `materialize-pr` re-emits each repository as its own typed output (`workflow.yaml:66-80`), via `prmonitor.Materialize` — a byte-for-byte `WalkDir` copy with a post-copy metadata-equality assertion (`materialize.go:249-330`). No transformation, no `.git` pruning.

Four full copies of one commit history.

| blob | uncompressed | Hangar (zstd-3) | retention |
|---|---:|---:|---|
| A `pull-request/v1` (both repos) | 487.7 MiB | **394.9 MiB** | **permanent pin** |
| B `repository/v1` source | 243.8 MiB | 197.4 MiB | binding, 7d |
| C `repository/v1` target | 243.8 MiB | 197.4 MiB | binding, 7d |
| **total** | **975.3 MiB** | **789.8 MiB** | |

Plus ~975 MiB of *uncompressed* node-local daemon cache, which is never swept on a timer — the daemon's sweeper touches only `steps/` and legacy flat `artifacts/`, never `snapshots/` (`cmd/artifact-daemon/sweeper.go:77-129`).

**At 100 observations of a single PR: ~39.5 GiB permanently pinned.**

## Three reasons dedupe can never fire

Not "does not today" — *cannot*.

1. **Granularity is the whole tar.** The Canonicalizer emits one `canonical.tar` per tree and digests those exact bytes (`agent/snapshot/archive.go:274-448`, `:1357-1380`). Sealer reuse is whole-digest only (`sealer.go:401-405`). Hangar is one GCS object per digest, `hangar/v1/snapshots/sha256/<digest>.tar.zst` (`keys.go:32-41`), and the `Store` interface has no chunk, range or delta operation (`types.go:48-70`).
2. **Git packfiles are not byte-reproducible.** Two fetches of the *identical unchanged ref* produced packs of 178,368,395 vs 178,389,048 bytes, SHA-256 `29cd6f8e…` vs `cb89199e…`; `.git/index` differed too. So re-observing an unchanged PR mints entirely new digests. Content-addressing cannot help.
3. **Compression cannot bridge the copies.** Measured on the best possible case, two byte-identical trees in one stream: 414,087,633 vs 207,029,054 for one — **2.0001×, marginally worse than 2×**. Hangar pins zstd to an 8 MiB window (`gcs.go:32-35`); the copies sit 250 MB apart. Forcing a 128 MiB window recovers 0.9%. And 72.5% of the bytes are already-deflated packfile, compressing at ratio 0.975 — the entire 19% overall saving comes from the smaller working-tree portion.

## Half of it is retained forever

`resourcecapture.Finalizer` converts every capture output's build-scoped retention into a durable **pin** (`finalizer.go:11-14`, `:25-26`, `:60`; `output_store.go:61`). The pin is inserted with **no `expires_at`** (`atc/db/agent_snapshots_factory.go:1396-1401`), and `RetentionClaim.Active` treats a NULL expiry as forever (`agent/snapshot/types.go:622-624`). GC collects only when no claim is active (`lifecycle.go:353`). The sole removal path is an explicit operator unpin (`agent_snapshots_factory.go:1453-1457`) that nothing calls automatically. The component is wired and running (`atc/atccmd/command.go:2163`, `:2467`).

The two `repository/v1` blobs do expire — `binding` claims stamped `now + 168h` (`sealer.go:432`, `:455-465`), plus a `run` claim released on run finalization. They are not declared workflow outputs (`workflow.yaml:22-38`), so they never receive a permanent `workflow` claim.

## Root cause: a snapshot can be named, a revision cannot

`repository/v1`'s gate hardcodes `validateRepository(ctx, root, "HEAD")` (`contracts/repository.go:55`) and its intrinsic metadata carries exactly one `head_sha` (`:23-29`). One directory therefore advertises exactly one head, so two heads require two directories, which require two full clones.

**The capability to do better already exists.** `validateRepository` is revision-parameterized (`repository.go:66`) and is *already* called with a non-HEAD revision by `repository_change.go:385`. It is the `repository/v1` contract that pins HEAD, not the git layer and not the validator.

The general shape: the platform models a *repository* as a value, but a `Subject` can name a snapshot and never a revision within one. So every `(repository, revision)` pair becomes a distinct full copy. Git already models this — one object store, many refs — and the platform declines all three of git's sharing mechanisms (`dependencies.go:114` refuses alternates and gitlinks; the destination check requires `.git` to be a directory). Those refusals are correct in isolation: a sealed tree must be self-contained to be independently valid evidence.

Note the platform already has a delta-shaped type — `repository-change/v1` is base-relative with `patch` / `git-bundle` / `git-tree` representations (`contracts/repository_change.go:21-27`, `:90-120`). The whole-tree cost is specific to `repository/v1`.

## What actually needs two directories

Verified consumer by consumer:

| consumer | needs |
|---|---|
| `respond` agent | one worktree at the PR head; never reads the target (`workflow.yaml:85-90`, asserted `pr_monitor_seed_test.go:75`) |
| `merge-prepare --target` | **a mutable worktree** — `Prepare` runs `checkout -B` / `rebase --onto` in place (`repositorymerge/runner.go:150-166`, `:523`) |
| `merge-prepare --base` | **a revision, not a worktree.** `repositorymerge.Request` has no `BaseRoot` field (`runner.go:332-363`); the rebase base is `document.BaseSHA` from the candidate's record (`:524`). The mount exists only to produce a digest/archive |
| `validate-revision` base input | a digest and a head; the diff comes from the candidate's own result tree (`validation_materialize.go:117`) |
| `prmonitor.Materialize` | two distinct `HeadSHA` values — the binding constraint, and purely a consequence of the contract above |

`cmd/function-runner/main.go:575` forbids `candidate == target` and `candidate == base`, but **does not forbid `base == target`**.

## Latent defect found in passing

`in` validates **each** checkout against `DefaultMaxSnapshotEntries` (100,000) and `DefaultMaxSnapshotContentBytes` (10 GiB) independently (`in.go:155`). The seal-time Canonicalizer applies **those same limits** to the combined observation tree, which is ~2× either repository (`archive.go:21-26`, `:751-753`, `:1085-1088`). A repository with 50k–100k entries, or 5–10 GiB of content, passes both per-directory checks and then fails the seal with `ErrLimitExceeded` — after both full fetches have already been paid for. This repo (5,535 entries, 240 MiB) is far from the cliff; a monorepo is not.

## The cheap fix is not the one I proposed

Five mechanisms were tested empirically against the real validator:

| option | verdict | why |
|---|---|---|
| `git clone --local` from repo A into B | **passes** — the only listed option that does | `git remote remove origin` deletes the whole `[remote "origin"]` section, so `config --get-regexp '^remote\.'` exits 1 and the refusal is skipped |
| second fetch via a local filesystem path | fails | `safeURL` at `dependencies.go:55` requires http/https — and only that |
| one repository, two worktrees | fails | `RevalidateSealed`: *"repository .git must be a real contained directory"* — not the refusal list |
| `--filter=blob:none` partial clone | fails | three gates; `repository.go:300-315` permits only `url`/`fetch` under `remote`, and rejects `remote.<url>.promisor` |
| `--depth` / shallow | fails | four gates. Note `fsck --full --strict` **exits 0** on a shallow repo; only the explicit checks catch it |

**But the best option was not on the list: one repository holding both refs in one directory.** Measured — a single `git init` plus one `git fetch <remote> +head:… +base:…` yields 178,419,256 B transferred, 6.41 s, 5,493 entries, and `RevalidateSealed` **OK**, with `head_sha` at the PR head and the base reachable as a ref. Against the two-directory baseline that is **−50% on network, node disk, sealed content bytes, bounds entries and fsck CPU** — the only option that touches storage at all.

Nothing in git or in the security checks blocks it. Three hardcoded interface facts do: the claimed directory set at `in.go:112` and its completion assertion at `:309-313`, the membership assertion at `materialize.go:445`, and `repository/v1` advertising one `head_sha`.

**`clone --local` should be rejected despite passing.** Hardlinked packs mean the two outputs share inodes, while `verifyOwnedDirectory` (`in.go:286-302`) checks directory identity via `os.SameFile` and never file inodes. A single write through either path would mutate both sealed candidates inside the TOCTOU window between `validateRepositoryEvidence` and stream-out — which cuts directly against the independence rationale that motivates the alternates/gitlink refusals. It buys zero storage; the single-directory reshape buys 50% of everything.

## Blocking defect: `in`'s materialization has never worked

Found while replicating `dependencies.go:74`, and independently reproduced.

`directgit.Command{NoRepository: true}` sets `GIT_DIR=<invocation>/no-repository` (`runner.go:160-168`, `:589-590`), and **`git init <dir>` honours `GIT_DIR` over its directory argument**. The repository is therefore created inside the ephemeral credential scratch that `privateScratch.Close()` destroys, the checkout destination stays empty, and the fetch at `dependencies.go:82` fails with `fatal: not a git repository`.

Production reaches this unconditionally: `cmd/forge-pr-resource/main.go:19` passes `resource.Dependencies{}`, so `in.go` falls through to `defaultGitRunner()`.

No test catches it because `dependencies_internal_test.go:19-30` stubs the runner and *emulates* `init` with `os.MkdirAll(filepath.Join(directory, ".git"), 0700)` — real `git init` semantics are never exercised. `git clone` is unaffected; only `init` is hijacked.

Tracked separately.

## Method

Two parallel investigators over the capture → snapshot → Hangar path and the consumer graph, each conclusion then put to an adversarial verifier. Every claim above is `CONFIRMED`, with one exception recorded honestly: an earlier framing that "nothing requires two directories" was **refuted** — `merge-prepare --target` genuinely needs a mutable worktree. A third probe covering cheaper git mechanisms (`clone --local`, partial clone) errored and is being re-run; its results are not folded in here.
