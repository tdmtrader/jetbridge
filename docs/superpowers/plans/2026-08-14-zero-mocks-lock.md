# Mock-Free Database Locks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `FakeLockDB` with a real PostgreSQL concurrency contract and remove the test-only lock database abstraction.

**Architecture:** The public `LockFactory` boundary remains unchanged. Internally, `lockFactory` and `lock` own the concrete `*lockDB`; concurrency is verified by racing two callers through the production factory against the suite's real PostgreSQL advisory lock.

**Tech Stack:** Go 1.25, Ginkgo v2/Gomega, PostgreSQL advisory locks, `sync` channels.

**Spec:** `docs/superpowers/specs/2026-08-14-zero-mocks-design.md`

## Global Constraints

- Execute every multi-command shell block with fail-fast semantics; stop on the first non-zero status even when a snippet does not repeat `set -e`.
- Zero means no generated mocks and no project-owned substitutes configured per method or used primarily for call assertions.
- Assert lock ownership and release behavior, not method calls.
- Run this database-backed package with `ginkgo ./atc/db/lock`, never plain `go test` alongside another database suite.
- Retain the public `LockFactory` and `Lock` boundaries; remove only `LockDB` and `NewTestLockFactory`, which have no production alternatives.
- Do not modify the two untracked review documents.

---

### Task 1: Characterize Concurrent Acquisition with PostgreSQL

**Files:**
- Modify: `atc/db/lock/lock_test.go:1-260`
- Delete later: `atc/db/lock/lockfakes/fake_lock_db.go`

**Interfaces:**
- Consumes: `lock.LockFactory.Acquire(lager.Logger, lock.LockID) (lock.Lock, bool, error)` from production and two independently owned arrays of PostgreSQL singleton connections.
- Produces: a race-safe behavioral spec proving that exactly one of two database sessions owns a lock and can release it.

- [ ] **Step 1: Record the package baseline**

Run: `/usr/bin/time -p ginkgo ./atc/db/lock`

Expected: PASS; record the `real` time with the plan-set baseline evidence.

- [ ] **Step 2: Replace the fake-backed context with a real concurrent acquisition spec**

Delete the `lockfakes` import and the entire nested context beginning `when two locks are being acquired at the same time`. Add this spec under `locks in general`:

```go
It("allows exactly one concurrent caller to acquire a lock", func() {
	type acquisition struct {
		lock     lock.Lock
		acquired bool
		err      error
	}

	var secondConns [lock.FactoryCount]*sql.DB
	for i := range lock.FactoryCount {
		conn := postgresRunner.OpenSingleton()
		secondConns[i] = conn
		DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	}
	secondFactory := lock.NewLockFactory(secondConns, fakeLogFunc, fakeLogFunc)

	start := make(chan struct{})
	results := make(chan acquisition, 2)

	for _, factory := range []lock.LockFactory{lockFactory, secondFactory} {
		go func(factory lock.LockFactory) {
			<-start
			acquiredLock, acquired, err := factory.Acquire(logger, lock.LockID{57})
			results <- acquisition{lock: acquiredLock, acquired: acquired, err: err}
		}(factory)
	}

	close(start)
	first := <-results
	second := <-results

	Expect(first.err).NotTo(HaveOccurred())
	Expect(second.err).NotTo(HaveOccurred())
	Expect([]bool{first.acquired, second.acquired}).To(ConsistOf(true, false))

	if first.acquired {
		Expect(first.lock.Release()).To(Succeed())
	} else {
		Expect(second.lock.Release()).To(Succeed())
	}
})
```

Keep the winning lock held until both buffered results have arrived, so the losing session necessarily observes the database advisory lock rather than a release race. This intentionally removes the old “different factories both acquire” case: two real PostgreSQL sessions cannot both own the same advisory lock. Do not race two calls through one factory: its local mutex/repository would serialize the calls before the loser reaches PostgreSQL.

- [ ] **Step 3: Format and run the characterization**

Run:

```bash
gofmt -w atc/db/lock/lock_test.go
ginkgo ./atc/db/lock
```

Expected: PASS with no data race or dependence on which goroutine wins.

- [ ] **Step 4: Commit the behavioral replacement**

```bash
git add atc/db/lock/lock_test.go
git commit -m "test(lock): cover real concurrent acquisition"
```

Expected: the commit contains only the test migration.

### Task 2: Remove the Test-Only Lock Database Seam

**Files:**
- Modify: `atc/db/lock/lock.go:90-240`
- Delete: `atc/db/lock/lockfakes/fake_lock_db.go`

**Interfaces:**
- Consumes: the green real-PostgreSQL spec from Task 1.
- Produces: `lockFactory.db *lockDB` and `lock.db *lockDB`; removes `LockDB` and `NewTestLockFactory` without changing `NewLockFactory` or `LockFactory`.

- [ ] **Step 1: Make the structural search demonstrate the seam still exists**

Run:

```bash
git grep -n -E 'NewTestLockFactory|type LockDB interface|counterfeiter:generate . LockDB|lockfakes' -- atc/db/lock
```

Expected: matches in `lock.go`, `lock_test.go` only if Task 1 was not completed, and the generated fake. Do not proceed until the test file has no match.

- [ ] **Step 2: Replace interface-typed internal fields with the concrete database adapter**

In `atc/db/lock/lock.go`, make these exact declarations:

```go
type lockFactory struct {
	db           *lockDB
	locks        lockRepo
	acquireMutex *sync.Mutex

	acquireFunc LogFunc
	releaseFunc LogFunc
}
```

```go
type lock struct {
	id LockID

	logger       lager.Logger
	db           *lockDB
	locks        lockRepo
	acquireMutex *sync.Mutex

	acquired LogFunc
	released LogFunc
}
```

Delete `NewTestLockFactory`, the `LockDB` interface, its Counterfeiter directive, and the package-level `go:generate` line once no other directive remains in the file. Leave `NoopLock` intact: it is a no-operation production value for conditional acquisition, not an interaction mock.

- [ ] **Step 3: Delete the generated fake**

Delete `atc/db/lock/lockfakes/fake_lock_db.go`. Remove the empty `lockfakes` directory if Git no longer tracks anything in it.

- [ ] **Step 4: Format and verify the package**

Run:

```bash
set -e
gofmt -w atc/db/lock/lock.go
ginkgo ./atc/db/lock
if git grep -n -E 'NewTestLockFactory|type LockDB interface|FakeLockDB|lockfakes|counterfeiter:generate . LockDB' -- '*.go'; then false; else test $? -eq 1; fi
```

Expected: the Ginkgo suite passes and the final search exits with no matches.

- [ ] **Step 5: Re-measure runtime**

Run: `/usr/bin/time -p ginkgo ./atc/db/lock`

Expected: no unexplained material increase over Task 1.

- [ ] **Step 6: Commit the completed lock migration**

```bash
git add atc/db/lock/lock.go atc/db/lock/lock_test.go
git add -u atc/db/lock/lockfakes
git commit -m "test(lock): replace fake database with real concurrency"
```

Expected: `git status --short` shows no lock-related changes after the commit.
