# GitOps Writeback Lock Hygiene Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent a stale Git lock inherited from a cached `home-infra` input from making the native Git-resource put fail after a successful GitOps writeback task.

**Architecture:** Every GitOps writer first creates a distinct output repository through one shared, fail-closed preparation script. The script copies the input repository, verifies that the copied `.git` control directory is a real directory, and removes only regular `*.lock` files beneath that fresh control directory before any writer or resource put uses it. The source input is immutable and untouched; the output remains a complete Git repository. All four pipeline writeback paths use the same helper so runner, web promotion, live-test attestation, and final release cannot drift.

**Tech Stack:** POSIX shell, Go pipeline-contract tests, Concourse native Git resource.

## Global Constraints

- Never mutate or clean the `home-infra` input artifact.
- Clean only regular `*.lock` files inside the freshly copied output's real `.git` directory; do not follow a `.git` symlink and do not delete arbitrary repository files.
- Preserve the full Git repository and its existing history/configuration for the native `put`.
- Keep the four GitOps writers on distinct `home-infra-updated` outputs and retain `rebase: true`, timeouts, and current ordering.
- The helper must reject missing arguments, a missing/non-directory source `.git`, a pre-existing non-empty destination, and a copied `.git` that is not a directory.
- Tests must reproduce the exact native-put boundary: a source `.git/config.lock` survives an ordinary copy, while the prepared output permits `git remote add` or equivalent Git configuration.

---

### Task 1: Prepare lock-free GitOps output repositories

**Files:**
- Create: `deploy/prepare-home-infra-writeback.sh`
- Create: `deploy/prepare_home_infra_writeback_test.go`
- Modify: `deploy/concourse-pipeline.yml`
- Modify: `deploy/write_agent_runner_home_infra_test.go`
- Modify: `deploy/concourse_pipeline_release_test.go`

**Interface:**

```sh
sh deploy/prepare-home-infra-writeback.sh SOURCE_REPOSITORY OUTPUT_REPOSITORY
```

The command copies `SOURCE_REPOSITORY/.` into the distinct, empty Concourse output directory, verifies `OUTPUT_REPOSITORY/.git` is a directory, removes regular files matching `*.lock` below that copied control directory, and returns success without changing the source.

- [ ] **Step 1: Add an executable stale-lock regression**

Create a temporary source repository with one committed file, an empty output directory, and stale regular lock files such as `.git/config.lock` and `.git/refs/heads/main.lock`. Run the not-yet-existing helper and assert:

- the initial focused test is RED because the helper is absent;
- the source locks remain present;
- the output locks are absent;
- a non-lock sentinel below `.git` remains;
- source and output `HEAD` are identical; and
- `git -C OUTPUT remote add push-target https://example.invalid/home-infra.git` succeeds, exercising the exact operation that failed in build `653507`.

Add negative cases for missing source `.git`, a `.git` symlink, and a non-empty destination. Use task-specific temporary paths and clean only those paths.

- [ ] **Step 2: Implement the minimal preparation helper**

Use `set -eu`, require exactly two arguments, canonicalize neither broad path nor environment variable, and reject equal source/output arguments. Require the source `.git` to be a physical directory and not a symbolic link. Require the destination directory to exist and be empty before copying. Copy with `cp -a "$source/." "$output/"`, repeat the physical-directory check on the copied `.git`, then remove only regular matching locks without following symlinks:

```sh
find "$output/.git" -type f -name '*.lock' -exec rm -f -- {} +
```

Do not run Git against the source and do not broaden cleanup outside the fresh output's `.git` directory.

- [ ] **Step 3: Route all four writeback tasks through the helper**

Replace each inline `cp -a` plus `.git` existence check with the helper, using the path appropriate to that task's working directory:

- runner image writeback near `update-home-infra-agent-runner-image`;
- self-upgrade web-image writeback near `resolve-and-write-home-infra-web-image`;
- live-test attestation writeback; and
- final release web-image writeback.

Keep each writer invocation immediately after the helper and preserve all metadata, privilege, input/output, put, rebase, and timeout contracts.

- [ ] **Step 4: Strengthen pipeline-contract coverage**

Update existing literal ordering assertions to require the shared helper before the relevant writer. Add a table over all four task names that proves each script calls the helper exactly once and contains neither a raw `cp -a ...home-infra` nor an inline broad lock deletion. Assert the helper script is present in the `repo` input path appropriate to each working directory.

- [ ] **Step 5: Verify GREEN**

Run:

```sh
go test ./deploy -run 'TestPrepareHomeInfraWriteback|TestAgentRunner|TestConcoursePipelineRelease' -count=1
go test ./deploy -count=1
git diff --check
```

Expected: all PASS. Record that live acceptance still requires a new `set-self` build followed by a new self-upgrade; retrying build `653507` cannot acquire the new task definition.

- [ ] **Step 6: Commit Task 1**

```sh
git add deploy/prepare-home-infra-writeback.sh deploy/prepare_home_infra_writeback_test.go deploy/concourse-pipeline.yml deploy/write_agent_runner_home_infra_test.go deploy/concourse_pipeline_release_test.go
git commit -m "fix(deploy): discard stale Git writeback locks"
```
