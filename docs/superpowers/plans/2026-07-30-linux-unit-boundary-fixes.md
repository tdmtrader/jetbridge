# Linux Unit Boundary Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the two failures from Linux build `unit-tests/905` pass while
preserving fail-closed PR-record ownership and DirectGit credential-file
privacy.

**Architecture:** `forge-pr` will retain a read-only descriptor for its
published record through protocol output, use that live identity for final
verification and rollback, and close it with the rest of destination
ownership. The DirectGit test script will use a GNU-first, BSD-fallback
permission probe.

**Tech Stack:** Go 1.25.6, `os.Root`, `os.File`, Unix file identity,
Concourse resource protocol, POSIX shell.

## Global Constraints

- Work only in the clean isolated worktree based on
  `fabf9b83e56cba10f97e743875920b538b3a1a2c`; do not touch the dirty primary
  `jetbridge` worktree.
- Preserve the caller-owned destination mount; never rename over or replace it.
- Successful `forge-pr` output membership remains exactly `record.json`,
  `source-repository`, and `target-repository`.
- Rollback may remove only filesystem objects whose retained identity proves
  this invocation created them.
- `record.json` remains mode `0600`, contains no credential, and is published
  only after both repositories are materialized and checked.
- The DirectGit bearer credential must remain absent from argv, the process
  environment, returned stdout/stderr, and persistent temporary files.
- Do not weaken, skip, or add timing retries to either failing regression test.
- Database-backed Go packages, if any broader verification reaches them, run
  serially rather than as parallel PostgreSQL package processes.

---

### Task 1: Retain `forge-pr` record identity across protocol output

**Files:**
- Modify: `agent/pullrequest/resource/in.go`
- Test: `agent/pullrequest/resource/in_test.go`

**Interfaces:**
- Consumes: the existing `ownedFile`, `destinationOwnership`,
  `publishRecord`, `verifyCompletedDestination`, and `cleanupOwnedFile`
  lifecycle.
- Produces: `ownedFile.guard *os.File`, a read-only descriptor that pins the
  created record until `destinationOwnership.close`.

- [ ] **Step 1: Re-run the existing regression test at the failing revision**

Run:

```bash
go test ./agent/pullrequest/resource -run '^TestForgePRInRechecksPublishedRecordAfterProtocolWrite$' -count=1
```

Expected on the Linux CI filesystem at the starting revision: FAIL with
`record replacement across the protocol boundary was accepted`. APFS may pass
because it does not promptly reuse the unlinked inode; the recorded Linux
failure is the required red evidence.

- [ ] **Step 2: Extend owned-file state with a retained descriptor**

Change `ownedFile` to carry the live identity:

```go
type ownedFile struct {
	name    string
	info    os.FileInfo
	guard   *os.File
	present bool
}
```

After the private writer has been synced and closed, open the private record
for reading through the retained root and verify it before publishing:

```go
guard, err := root.Open(privateRecordName)
if err != nil {
	return fmt.Errorf("forge-pr: retain private record")
}
private.guard = guard
retainedInfo, err := guard.Stat()
if err != nil || private.info == nil || !retainedInfo.Mode().IsRegular() ||
	!os.SameFile(private.info, retainedInfo) {
	return fmt.Errorf("forge-pr: private record identity changed")
}
```

Create the public ownership entry after linking. Remove the private link while
the private ownership entry still holds the descriptor, then transfer the
descriptor to the public entry:

```go
record := &ownedFile{name: "record.json", info: private.info, present: true}
ownership.files = append(ownership.files, record)
retainedInfo, retainedErr := private.guard.Stat()
recordInfo, recordErr := root.Lstat(record.name)
if retainedErr != nil || recordErr != nil ||
	!retainedInfo.Mode().IsRegular() || !recordInfo.Mode().IsRegular() ||
	!os.SameFile(record.info, retainedInfo) ||
	!os.SameFile(retainedInfo, recordInfo) {
	return fmt.Errorf("forge-pr: record identity changed")
}
if err := cleanupOwnedFile(root, private); err != nil {
	return fmt.Errorf("forge-pr: remove private record")
}
record.guard = private.guard
private.guard = nil
```

This ordering ensures every error path has either the private link or the
public link plus an open descriptor pinning the created inode.

- [ ] **Step 3: Make final verification use the retained descriptor**

In `verifyCompletedDestination`, require `record.guard` and compare captured,
retained, and path metadata:

```go
if record == nil || record.info == nil || record.guard == nil {
	return fmt.Errorf("forge-pr: record identity is unavailable")
}
retainedInfo, retainedErr := record.guard.Stat()
recordInfo, recordErr := root.Lstat(record.name)
if retainedErr != nil || recordErr != nil ||
	!retainedInfo.Mode().IsRegular() || !recordInfo.Mode().IsRegular() ||
	!os.SameFile(record.info, retainedInfo) ||
	!os.SameFile(retainedInfo, recordInfo) {
	return fmt.Errorf("forge-pr: record identity changed")
}
```

- [ ] **Step 4: Make rollback use and close the retained descriptor**

In `cleanupOwnedFile`, prefer `guard.Stat()` as the expected live identity
when a guard exists, while still corroborating it with the creation metadata:

```go
expected := file.info
if file.guard != nil {
	retained, guardErr := file.guard.Stat()
	if guardErr != nil || file.info == nil ||
		!retained.Mode().IsRegular() ||
		!os.SameFile(file.info, retained) {
		return fmt.Errorf("forge-pr: refused to remove changed owned file")
	}
	expected = retained
}
```

Compare `expected` with the current rooted path before removal. Extend
`destinationOwnership.close` to close each non-nil file guard after cleanup,
in addition to closing retained directory roots.

- [ ] **Step 5: Run focused and package tests**

Run:

```bash
go test ./agent/pullrequest/resource -run '^TestForgePRInRechecksPublishedRecordAfterProtocolWrite$' -count=100
go test ./agent/pullrequest/resource -count=1
```

Expected: PASS. The replacement remains untouched, the two owned repository
directories are rolled back, successful output has no private record, and the
package suite remains green.

- [ ] **Step 6: Commit the production fix**

```bash
git add agent/pullrequest/resource/in.go agent/pullrequest/resource/in_test.go
git commit -m "fix: retain forge pr record identity"
```

Do not modify `in_test.go` unless the existing regression needs an assertion
clarification; the Linux failure already supplies the red test.

---

### Task 2: Make the DirectGit permission assertion portable

**Files:**
- Modify: `agent/publisher/directgit/runner_test.go`

**Interfaces:**
- Consumes: the shell fixture in
  `TestCommandRunnerUsesExplicitPrivateBearerHeaderWithoutTokenInference`.
- Produces: a single octal permission string from GNU `stat` on Linux or BSD
  `stat` on macOS.

- [ ] **Step 1: Preserve the recorded Linux red evidence**

The starting Linux failure reports the mode as the first line of GNU
filesystem-statistics output instead of `600`. The production runner's
creation and `chmod(0600)` behavior already succeeds and must not change.

- [ ] **Step 2: Reverse the capability probes**

Replace the BSD-first command:

```sh
mode=$(stat -f '%Lp' "$GIT_CONFIG_GLOBAL" 2>/dev/null || stat -c '%a' "$GIT_CONFIG_GLOBAL")
```

with:

```sh
mode=$(stat -c '%a' "$GIT_CONFIG_GLOBAL" 2>/dev/null || stat -f '%Lp' "$GIT_CONFIG_GLOBAL")
```

GNU/Linux now emits only the file mode, while macOS falls back after rejecting
the unsupported `-c` form.

- [ ] **Step 3: Run focused and package tests**

Run:

```bash
go test ./agent/publisher/directgit -run '^TestCommandRunnerUsesExplicitPrivateBearerHeaderWithoutTokenInference$' -count=20
go test ./agent/publisher/directgit -count=1
```

Expected: PASS, including the existing assertions that the bearer token never
appears in argv, the environment, unsanitized command output, or surviving
temporary files.

- [ ] **Step 4: Commit the portability fix**

```bash
git add agent/publisher/directgit/runner_test.go
git commit -m "test: make direct git mode check portable"
```

---

### Task 3: Verify and publish the repaired branch

**Files:**
- Modify: `docs/superpowers/specs/2026-07-30-linux-unit-boundary-fixes-design.md`
- Modify: `docs/superpowers/plans/2026-07-30-linux-unit-boundary-fixes.md`

**Interfaces:**
- Consumes: the two reviewed task commits.
- Produces: a reviewed, tested `origin/jetbridge` commit with Linux CI evidence.

- [ ] **Step 1: Run formatting and focused verification**

Run:

```bash
gofmt -w agent/pullrequest/resource/in.go agent/pullrequest/resource/in_test.go agent/publisher/directgit/runner_test.go
go test ./agent/pullrequest/resource ./agent/publisher/directgit -count=1
```

Expected: no formatting diff beyond the intended files and both packages PASS.

- [ ] **Step 2: Run the repository unit-test target**

Run the package selection encoded by `deploy/concourse-pipeline.yml`:

```bash
PACKAGES=$(go list ./... \
  | grep -v /integration \
  | grep -v /testflight \
  | grep -v /topgun \
  | grep -v /fly/integration \
  | grep -v /atc/db \
  | grep -v /atc/gc \
  | grep -v /atc/postgresrunner \
  | grep -v /atc/scheduler/algorithm \
  | grep -v '/atc/worker$' \
  | grep -v /skymarshal/dexserver \
  | grep -v /cmd)
go test -count=1 -timeout 10m $PACKAGES
```

Expected: PASS. This target excludes the database-backed packages and thus
does not start parallel PostgreSQL package processes.

- [ ] **Step 3: Run a whole-branch review**

Review every change from
`fabf9b83e56cba10f97e743875920b538b3a1a2c` through `HEAD`. Confirm the
descriptor cannot be lost or double-closed on any `publishRecord` error path,
rollback cannot delete a replacement, exact output membership is unchanged,
and the shell probe returns exactly one octal mode.

- [ ] **Step 4: Commit the design and verification record**

```bash
git add docs/superpowers/specs/2026-07-30-linux-unit-boundary-fixes-design.md docs/superpowers/plans/2026-07-30-linux-unit-boundary-fixes.md
git commit -m "docs: record linux unit boundary fixes"
```

- [ ] **Step 5: Reconcile and push**

Fetch `origin/jetbridge`, integrate only if the remote advanced, re-run the
affected tests after any integration, and push the repaired head without
force. Verify that `origin/jetbridge` resolves to the pushed commit.

- [ ] **Step 6: Validate Linux CI**

Inspect the resulting `jetbridge` unit-test build on `concourse.home`.
Expected: both task attempts, if a retry occurs, pass the repaired tests and
the unit-test job finishes green.
