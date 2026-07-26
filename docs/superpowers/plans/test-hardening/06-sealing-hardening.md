# WS6 — Sealing and Digest Hardening

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the content-addressing layer's unstated assumptions into stated, executable ones. Pin what a digest *must* distinguish and what it deliberately does *not*; prove the round trip survives a real filesystem, not just a byte buffer; point the repository's first two fuzz targets at the tar parser and the canonical-JSON serializer that the whole security model rests on; give the "is capture re-run deterministic?" question a test-backed answer instead of an ambiguity; make the one exposure claim that is *about content* actually checked against content; and replace the two faked concurrency stories (a non-blocking digest lease, a counter-simulated operation lock) with two goroutines and a lock that genuinely excludes.

**Architecture:** Six of the seven tasks are tests, one Makefile target, and three doc comments — all inside `agent/snapshot`, `agent/snapshot/contracts`, and `agent/resourcecapture`. Exactly one production change lands (Task 5): a new `snapshot.VerifyExposedPaths` plus its call from `BatchSealer.Seal`, which recomputes an enumerated static-selector exposure's per-path digests against the exposed input's own stored bytes and refuses on disagreement. It is a seal-time gate only; it reads no stored record and can reject no stored bytes.

**Tech Stack:** Go 1.25.6, plain `testing` (no Ginkgo in `agent/`), stdlib `testing.F` native fuzzing, no new third-party dependency.

## Global Constraints

- **Add no third-party dependency.** Fuzzing is stdlib `testing.F`. No `gopter`, no `rapid`, no `testing/quick`, no mutation tool.
- **CROSS-PLAN CONTRACT — CI wiring belongs to plan `01-ci-execution.md`, not here.** This plan *creates* the `test-fuzz` Makefile target and adds it to `test-all`; plan 01 adds the CI step that invokes it. Likewise, plan 01 owns the `-race` lane (`test-agent-race` over `agent/...`); **this plan's obligation is that Tasks 6 and 7 are `-race`-clean**, and each of those tasks verifies that locally with `go test -race` before committing. Do not edit `deploy/concourse-pipeline.yml` or `deploy/dogfood-pipeline.yml` in this plan.
- **Nothing here may reject previously-sealed bytes at read time.** The Task 5 gate is reachable only from `BatchSealer.Seal`. `RevalidateSealed`, `load_snapshot`, and the artifact reader are untouched. See Task 5's two-gate argument.
- **No migration, no `atc/db` change, no `atc/exec` change.** WS6 assigns no migration number. If a task seems to need one, stop — it has drifted out of scope.
- **`agent/schema` is a separate module and is not touched.**
- Test conventions in `agent/`: plain `testing`, table-driven `t.Run` subtests, `t.Fatalf("X = %v, want %q", got, want)` shape, error assertions by `strings.Contains` on a distinctive fragment or `errors.Is` on a sentinel.
- **The red step is not optional, and it takes two shapes here.** Task 5 changes behaviour, so it is ordinary TDD: the test fails because the gate does not exist, then it passes because it does. Tasks 1, 2, 3, 6 and 7 pin behaviour that is *already correct*, and a characterization test that has never been seen red is indistinguishable from a test that asserts nothing. Each of those tasks therefore carries a **named, reversible mutation** — of production code, of a test helper, or of a fake — with the exact failure it must produce. Do not skip it, and do not commit until the mutation has been reverted and the green run repeated.
- Run `gofmt -l` on every changed Go file before each commit; it must print nothing.
- Every task below is independently landable and leaves the tree green. There are no ordering constraints between them.

## Facts established by scouting (do not re-derive)

Measured against this branch's HEAD (`410d9b59f8`) on Go 1.25.6. Trust these; re-verify only if something contradicts them.

1. **`Canonicalizer.Capture(ctx, rawTar) (*CapturedTree, error)`** (`agent/snapshot/archive.go:254`) is the only capture entry point. `CapturedTree` exposes `Root` (the materialized directory — this *is* "the real extraction path"), `ArchivePath`, `Digest`, `ByteSize`, `FileCount`, and `Close()`.
2. **What is identity:** sorted paths, typeflag, size + content, cleaned symlink target, and the executable bit only — regular files normalize to `0644`/`0755` (`archive.go:1059-1062`), directories always to `0755` (`archive.go:963`, `:973`, `:1012`), symlinks always emit mode `0777` (`archive.go:797`). **What is not identity:** every other permission bit, uid/gid/uname/gname, and all three timestamps (all zeroed in `writeCanonicalEntries`, `archive.go:1341-1352`). `tar.FormatGNU` is pinned there with a comment already calling a serializer change an identity migration.
3. **All six anti-collision pairs in Task 1 do differ, and all five identity-boundary pairs are equal.** Verified by running them through the real `Canonicalizer` before this plan was written. No pair errors out.
4. **The true round trip works.** Extracting a canonical archive with `Capture`, re-tarring `tree.Root` with the walker in Task 2, and capturing again reproduces the original digest exactly — including UTF-8 paths, an empty directory, an executable, and two relative symlinks. Flipping one byte (same length) moves the digest.
5. **`agent/workflowwait/materializer.go` is not an exposure materializer.** It builds a `human-answer/v1` document in memory, tars it, and uploads it. It never writes an exposure path to disk and never reads `ExposedPath.Digest`. It is irrelevant to Task 5.
6. **`snapshot.NewStaticSelectorExposure` has ZERO production callers.** `rg` over the whole tree finds it only in tests (`agent/snapshot/exposure_test.go`, `exposure_seal_test.go`, `atc/db/agent_snapshots_factory_test.go`). The single production exposure-recording site is `snapshotInputBindings.recordExposure` (`atc/exec/task_step.go:514-521`, shared by `agent_step.go`), which always records `FullTreeExposure` — mode `full`, tree digest only, **no per-path digests, ever**. Therefore Task 5 lands the *fallback* deliverable named in the spec: a verification helper plus its seal-gate call and tests, not a change to a materialization site that does not exist.
7. **Nothing recomputes `ExposedPath.Digest` against bytes anywhere.** `ExposedPath.Validate` (`exposure.go:104-112`) checks the path shape and that the digest *parses*. `validateDeclaredExposures` (`exposure.go:283`) binds `InputExposure.TreeDigest` to the input's own `SnapshotRef.Digest`. `atc/db/agent_snapshots_factory.go:795` (`verifyExposedPathSet`) compares persisted rows against the declared set — round-tripping the claim through Postgres, never against content. `atc/runtime/snapshot_artifact.go:308-309` verifies the whole-tree digest on read. **The per-path digest is the one exposure field that is a claim about content and is checked by nobody. Its meaning is also undefined anywhere in the codebase** — Task 5 defines it.
8. **`BatchSealer` can read an input's canonical archive**: `sealer.inputOpener(teamID)` (`sealer.go:474-488`) authorizes the exact `SnapshotRef` against `MetadataStore.GetAuthorized` and returns `ContentStore.Open`. That is the seam Task 5 uses.
9. **Canonical archive entries are sorted bytewise by name** (`sortedCaptureEntries`, `archive.go:1324`) and `InputExposure.Paths` is sorted bytewise by path (`sortExposedPaths`, `exposure.go:182`). The two sequences therefore merge in one streaming pass — Task 5 needs no random access into the archive.
10. **The sealer's existing lock fake does not exclude.** `sealerLocks.AcquireMany` (`sealer_test.go:185-194`) hands the same `*sealerLease` to every caller and never blocks. `agent/snapshot/sealer.go` + `store.go` contain **zero goroutines in tests**. `lifecycle_test.go:182-209` (`lifecycleBarrierLocks`) is the closest existing thing — a single global token — and is per-run, not per-digest.
11. **A genuinely exclusive in-memory `lock.LockFactory` already exists in-tree and is unused by `agent/`:** `lock.NewTestLockFactory(db LockDB)` (`atc/db/lock/lock.go:153`) returns the real `*lockFactory`, whose real `lockRepo` + `acquireMutex` exclude in-process (`lock.go:225-252`). It needs only a `LockDB` that behaves like `pg_try_advisory_lock`. Task 7 supplies ~25 lines for that; no new production code.
12. **`agent/resourcecapture/operation_locker_test.go` has zero goroutines.** Contention is simulated by an `attempts` counter that returns `false` on call 1 (`operation_locker_test.go:26-32`). It proves the retry loop retries; it proves nothing about exclusion.
13. **`TestCaptureRetriesTheExactSucceededGenerationWhenItsOutputExpired` (`capture_test.go:213`) never reaches a second manifest**: its `fakeOutputs.finalize` returns `ErrOutputUnavailable` unconditionally and the retry generation is `PipelineRunRunning`, so `result.Snapshot` is nil. There is no digest to compare. Task 4 restructures it so a second generation completes.
14. **Fuzzing throughput is destroyed by the default `-fuzzminimizetime`.** Measured on `FuzzCanonicalCapture`: `-fuzztime=30s` with the default (60s per interesting input) executed **223 inputs**, of which ~220 were in the first three seconds and the remaining 27 seconds were spent minimizing one input. The same target with `-fuzzminimizetime=1s` executed **34,790 inputs** in the same 30 seconds and found 51 new corpus entries. The `test-fuzz` target in Task 3 therefore **must** pass `-fuzzminimizetime`, and the comment explaining why must stay.
15. **Neither fuzz target finds a failure.** `FuzzCanonicalCapture` was run for 30s (34,790 execs) and `FuzzCanonicalJSON` for 25s (8,267,470 execs) against exact copies of the production code before this plan was written. Both passed. If a task's fuzz run goes red, that is a real finding — triage it, do not weaken the property.
16. `agent/snapshot/archive_test.go`, `sealer_test.go`, `store_test.go`, `types_test.go`, `lifecycle_test.go`, and `exposure_seal_test.go` are **internal** (`package snapshot`); `exposure_test.go` and `validator_test.go` are external (`package snapshot_test`). Every new file in this plan that touches unexported helpers is internal.
17. Reusable helpers already in `package snapshot` tests: `makeTar(t, []tarEntry)`, `capture(t, Canonicalizer, io.Reader) *CapturedTree`, `readFile(t, name) []byte`, `readTar(t, data) []*tar.Header`, `assertMode`, `tarBytes(t, name, content)`, `canonicalBody(t, raw)`, `canonicalDigest(t, raw)`, `mustTestDigest(t)`, `mustOtherDigest(t)`, `sealerRequest`, `sealerSource`, `mustNewSealer`, `sealerValidatorFunc`, `sealerRegistry`, `sealerMetadataStore`, `sealerContentStore`, `sealerLocks`, `sealerLease`.
18. In `agent/snapshot/contracts`, the fuzz seed corpus comes straight off the embedded FS: `schemaDocumentSources.ReadDir(schemaDocumentDirectory)` / `.ReadFile(...)` (both package-level, `schema_document_load.go:79`, `:132`), plus the `workedExampleDocument` and `workedExamplePayload` constants (`canonical_json_internal_test.go:21`, `:40`). Six documents are embedded.

---

### Task 1: Pin what a digest must distinguish, and what it deliberately must not

**Files:**
- Create: `agent/snapshot/archive_collision_test.go`
- Modify: `agent/snapshot/archive.go` (doc comment on `Canonicalizer` only)

Zero `digestA != digestB` assertions exist anywhere in the repository today. Prefix-freeness is structurally real but unpinned, and the permission-normalization boundary — only the executable bit survives — is documented by no test at all, so a future mode-preserving change would be a silent identity migration.

- [ ] Create `agent/snapshot/archive_collision_test.go` in `package snapshot` with exactly this content.

```go
package snapshot

import (
	"archive/tar"
	"bytes"
	"testing"
	"time"
)

// TestCanonicalCaptureDistinguishesNearMissTrees is the anti-collision table:
// the statement of what a snapshot digest MUST be able to tell apart. Every
// pair below is a tree an attacker or a bug would like to substitute for its
// neighbour, and every one of them is a shape the length-framed canonical tar
// encoding is supposed to make unconfusable. "Structurally impossible" was the
// argument; this is the assertion.
//
// Its companion, TestCanonicalCaptureIdentityBoundaryIsExecBitOnly, states the
// other half — what deliberately does NOT differ. Read them together; either one
// alone is half a specification.
func TestCanonicalCaptureDistinguishesNearMissTrees(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		why   string
		left  []tarEntry
		right []tarEntry
	}{
		{
			name: "empty file versus absent file",
			why:  "a zero-byte file is a fact about the tree, not the absence of one",
			left: []tarEntry{
				{name: "keep", typeflag: tar.TypeReg, content: "x"},
				{name: "empty", typeflag: tar.TypeReg, content: ""},
			},
			right: []tarEntry{{name: "keep", typeflag: tar.TypeReg, content: "x"}},
		},
		{
			name: "empty directory versus absent directory",
			why:  "an empty directory has no content to hash and must still be identity",
			left: []tarEntry{
				{name: "keep", typeflag: tar.TypeReg, content: "x"},
				{name: "empty", typeflag: tar.TypeDir},
			},
			right: []tarEntry{{name: "keep", typeflag: tar.TypeReg, content: "x"}},
		},
		{
			name:  "same name as file versus as directory",
			why:   "the typeflag is identity: a name is not a value",
			left:  []tarEntry{{name: "foo", typeflag: tar.TypeReg, content: ""}},
			right: []tarEntry{{name: "foo", typeflag: tar.TypeDir}},
		},
		{
			name:  "trailing newline is content",
			why:   "the classic text-file near miss; nothing normalizes line endings",
			left:  []tarEntry{{name: "f", typeflag: tar.TypeReg, content: "a\n"}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, content: "a"}},
		},
		{
			name:  "separator placement is identity",
			why:   "a/bc and ab/c are the same bytes with the separator moved one place",
			left:  []tarEntry{{name: "a/bc", typeflag: tar.TypeReg, content: "same"}},
			right: []tarEntry{{name: "ab/c", typeflag: tar.TypeReg, content: "same"}},
		},
		{
			name:  "executable bit flip",
			why:   "the one permission bit that IS identity; a runnable tree is a different tree",
			left:  []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x"}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0755, content: "x"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, tt.left)))
			defer left.Close()
			right := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, tt.right)))
			defer right.Close()

			if left.Digest == right.Digest {
				t.Fatalf("near-miss trees share digest %s (%s)", left.Digest, tt.why)
			}
			if bytes.Equal(readFile(t, left.ArchivePath), readFile(t, right.ArchivePath)) {
				t.Fatalf("near-miss trees produced byte-identical canonical archives (%s)", tt.why)
			}
		})
	}
}

// TestCanonicalCaptureIdentityBoundaryIsExecBitOnly states what canonicalization
// deliberately discards. Each pair below MUST hash to one digest today.
//
// This test is not a convenience. Making any of these survive canonicalization —
// preserving the full permission mode, or ownership, or mtimes — changes the
// digest of every tree ever sealed. That is an identity migration with the same
// blast radius as changing tar.FormatGNU: every stored digest becomes
// unreproducible from its stored bytes, and the exact-equality revalidation on
// the read paths starts refusing history. If a future change needs
// mode-preserving snapshots, this test must be edited deliberately, in a commit
// that says so and that ships the migration — it must never break by accident.
func TestCanonicalCaptureIdentityBoundaryIsExecBitOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		why   string
		left  []tarEntry
		right []tarEntry
	}{
		{
			name:  "group and other permission bits",
			why:   "0640 and 0644 are one canonical file: only the exec bit survives",
			left:  []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0640, content: "x"}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x"}},
		},
		{
			name:  "any executable spelling",
			why:   "0711 and 0755 both mean executable and normalize to 0755",
			left:  []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0711, content: "x"}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0755, content: "x"}},
		},
		{
			name:  "directory permission bits",
			why:   "directories are always 0755; their source mode is not identity at all",
			left:  []tarEntry{{name: "d", typeflag: tar.TypeDir, mode: 0700}},
			right: []tarEntry{{name: "d", typeflag: tar.TypeDir, mode: 0777}},
		},
		{
			name: "ownership",
			why:  "uid, gid and owner names are producer-environment facts, not content",
			left: []tarEntry{{
				name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x",
				uid: 1000, gid: 1000, uname: "alice", gname: "staff",
			}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x"}},
		},
		{
			name: "modification, access and change times",
			why:  "all three timestamps are zeroed to the epoch; a re-run is not a new value",
			left: []tarEntry{{
				name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x",
				modTime:    time.Unix(1_000_000, 0),
				accessTime: time.Unix(1_000_001, 0),
				changeTime: time.Unix(1_000_002, 0),
			}},
			right: []tarEntry{{
				name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x",
				modTime: time.Unix(1, 0),
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, tt.left)))
			defer left.Close()
			right := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, tt.right)))
			defer right.Close()

			if left.Digest != right.Digest {
				t.Fatalf("identity boundary moved: %s != %s (%s); if this change is intended, it is an identity migration and needs its own commit",
					left.Digest, right.Digest, tt.why)
			}
			if !bytes.Equal(readFile(t, left.ArchivePath), readFile(t, right.ArchivePath)) {
				t.Fatalf("identity boundary moved: canonical bytes differ (%s)", tt.why)
			}
		})
	}
}
```

- [ ] Run `go test ./agent/snapshot/ -run 'TestCanonicalCapture(DistinguishesNearMissTrees|IdentityBoundaryIsExecBitOnly)' -v -count=1` and confirm all eleven subtests pass:

```
=== RUN   TestCanonicalCaptureDistinguishesNearMissTrees
=== RUN   TestCanonicalCaptureDistinguishesNearMissTrees/empty_file_versus_absent_file
...
--- PASS: TestCanonicalCaptureIdentityBoundaryIsExecBitOnly (0.02s)
    --- PASS: TestCanonicalCaptureIdentityBoundaryIsExecBitOnly/modification,_access_and_change_times (0.01s)
PASS
ok  	github.com/concourse/concourse/agent/snapshot	0.31s
```

- [ ] **Prove both tables are load-bearing** with one reversible mutation. In `agent/snapshot/archive.go:1059-1062`, temporarily replace

```go
		mode := os.FileMode(0644)
		if hdr.Mode&0111 != 0 {
			mode = 0755
		}
```

with `mode := os.FileMode(hdr.Mode) & os.ModePerm` (mode-preserving extraction — exactly the future change this boundary exists to catch). Re-run the same command and confirm **the identity table goes red while the collision table stays green**:

```
--- FAIL: TestCanonicalCaptureIdentityBoundaryIsExecBitOnly (0.02s)
    --- FAIL: TestCanonicalCaptureIdentityBoundaryIsExecBitOnly/group_and_other_permission_bits (0.01s)
        archive_collision_test.go:NNN: identity boundary moved: sha256:... != sha256:... (0640 and 0644 are one canonical file: only the exec bit survives); if this change is intended, it is an identity migration and needs its own commit
```

- [ ] Revert that mutation exactly (`git diff agent/snapshot/archive.go` must show only the doc comment once the next step lands) and re-run the command to confirm green again.
- [ ] Extend the `Canonicalizer` doc comment in `agent/snapshot/archive.go` (currently `:170-175`) so the boundary is citable from the package docs. Append these paragraphs after the existing "fail closed at extraction." line, leaving everything above it untouched:

```go
// What the emitted bytes are identity FOR is stated by two tests, which are the
// normative form of this boundary rather than illustrations of it.
// TestCanonicalCaptureDistinguishesNearMissTrees fixes what must produce a
// different digest: an empty file is not an absent file, an empty directory is
// not an absent directory, a name is not a type, a trailing newline is content,
// separator placement is identity, and the executable bit is identity.
// TestCanonicalCaptureIdentityBoundaryIsExecBitOnly fixes what must NOT: every
// permission bit except the executable one, uid/gid and owner names, and all
// three timestamps.
//
// Moving either line changes every digest ever computed, so it is a named
// identity-migration event in exactly the sense the tar.FormatGNU comment in
// writeCanonicalEntries means it. A mode-preserving change is the specific case
// to watch: it looks local and is not.
```

- [ ] Run `gofmt -l agent/snapshot/archive.go agent/snapshot/archive_collision_test.go` and confirm no output.
- [ ] Run `go test ./agent/snapshot/... -count=1` and confirm the whole package is still green.
- [ ] Commit `test(snapshot): pin and document the canonical digest identity boundary`.

---

### Task 2: Round-trip a canonical archive through a real filesystem

**Files:**
- Create: `agent/snapshot/archive_roundtrip_test.go`

`TestCanonicalCaptureRoundTrip` (`archive_test.go:192-237`) re-feeds the canonical **archive bytes** to `Capture`. That proves the serializer has a fixed point, and it never touches a filesystem. The property an agent workspace actually depends on — materialize to disk, let a producer re-tar the directory, seal that, get the same digest — is untested. So is its negative: today's tamper tests assert that an *error* is returned, which is a different property from digest sensitivity.

- [ ] Create `agent/snapshot/archive_roundtrip_test.go` in `package snapshot` with exactly this content.

```go
package snapshot

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestCanonicalCaptureRoundTripsThroughMaterializedDisk is deliberately NOT
// TestCanonicalCaptureRoundTrip, and the difference is the whole point.
//
// That test re-feeds the canonical ARCHIVE BYTES to Capture. It proves the
// serializer has a fixed point and it never touches a filesystem. The property
// an agent workspace actually rests on is this one: a snapshot is materialized
// to disk, a step re-tars that directory the way any producer would, the result
// is sealed, and the digest comes back unchanged. Everything between the two
// digests here is real: real extraction into a real directory, a real
// filepath.WalkDir, real lstat modes and real readlink targets.
//
// The negative half is the same property from the other side, and it is a
// different property from the hostile-archive tests: those assert that Capture
// returns an ERROR. This asserts that content the canonicalizer happily accepts
// still moves the digest. The flipped byte keeps the file the same length, so
// the tree's byte size is identical and only content can be doing the work.
func TestCanonicalCaptureRoundTripsThroughMaterializedDisk(t *testing.T) {
	t.Parallel()

	raw := makeTar(t, []tarEntry{
		{name: "empty", typeflag: tar.TypeDir, mode: 0700},
		{name: "bin", typeflag: tar.TypeDir, mode: 0755},
		{name: "bin/run", typeflag: tar.TypeReg, mode: 0100, content: "#!/bin/sh\n"},
		{name: "données", typeflag: tar.TypeDir, mode: 0755},
		{name: "données/café.txt", typeflag: tar.TypeReg, mode: 0640, content: "bonjour\n"},
		{name: "données/latest", typeflag: tar.TypeSymlink, linkname: "./café.txt"},
		{name: "bin/data", typeflag: tar.TypeSymlink, linkname: "../données/café.txt"},
	})

	original := capture(t, Canonicalizer{}, bytes.NewReader(raw))
	defer original.Close()

	// The real extraction path. Capture materializes the canonical archive into
	// its own private root using the same code a step mount is fed from; there is
	// no second, test-only extractor.
	materialized := capture(t, Canonicalizer{}, bytes.NewReader(readFile(t, original.ArchivePath)))
	defer materialized.Close()

	fromDisk := capture(t, Canonicalizer{}, bytes.NewReader(tarMaterializedDirectory(t, materialized.Root)))
	defer fromDisk.Close()

	if fromDisk.Digest != original.Digest {
		t.Fatalf("re-seal of the materialized tree = %s, want %s", fromDisk.Digest, original.Digest)
	}
	if !bytes.Equal(readFile(t, fromDisk.ArchivePath), readFile(t, original.ArchivePath)) {
		t.Fatal("re-seal of the materialized tree produced different canonical bytes")
	}
	if fromDisk.ByteSize != original.ByteSize || fromDisk.FileCount != original.FileCount {
		t.Fatalf("re-seal identity = %d bytes / %d entries, want %d / %d",
			fromDisk.ByteSize, fromDisk.FileCount, original.ByteSize, original.FileCount)
	}

	// One byte, same length, inside one file, on disk.
	target := filepath.Join(materialized.Root, "données", "café.txt")
	if err := os.WriteFile(target, []byte("bonjouR\n"), 0644); err != nil {
		t.Fatalf("flip one byte on disk: %v", err)
	}
	tampered := capture(t, Canonicalizer{}, bytes.NewReader(tarMaterializedDirectory(t, materialized.Root)))
	defer tampered.Close()

	if tampered.Digest == original.Digest {
		t.Fatalf("a one-byte change on disk left the digest at %s", tampered.Digest)
	}
	if tampered.ByteSize != original.ByteSize || tampered.FileCount != original.FileCount {
		t.Fatalf("the tampered tree changed size (%d bytes / %d entries vs %d / %d); the digest must move on content alone",
			tampered.ByteSize, tampered.FileCount, original.ByteSize, original.FileCount)
	}
}

// tarMaterializedDirectory re-tars an extracted tree the way an ordinary
// producer would: walk it, sort by POSIX path, and emit whatever the filesystem
// reports. It deliberately normalizes nothing of its own — no mode rewriting, no
// time zeroing, no ownership. The round trip is only meaningful if Capture, and
// not this helper, owns identity.
//
// FormatGNU is pinned for the same reason the canonical writer pins it: the
// tree carries a non-ASCII path, and letting archive/tar pick a format would
// make this helper's output depend on the entry set rather than on the tree.
func tarMaterializedDirectory(t *testing.T, root string) []byte {
	t.Helper()

	var names []string
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		names = append(names, filepath.ToSlash(relative))
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk materialized tree %q: %v", root, walkErr)
	}
	sort.Strings(names)

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range names {
		full := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if err != nil {
			t.Fatalf("stat %q: %v", full, err)
		}
		header := &tar.Header{
			Name:   name,
			Mode:   int64(info.Mode().Perm()),
			Format: tar.FormatGNU,
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(full)
			if err != nil {
				t.Fatalf("read symlink %q: %v", full, err)
			}
			header.Typeflag = tar.TypeSymlink
			header.Linkname = link
		case info.IsDir():
			header.Typeflag = tar.TypeDir
		case info.Mode().IsRegular():
			header.Typeflag = tar.TypeReg
			header.Size = info.Size()
		default:
			t.Fatalf("unsupported file mode %v for %q", info.Mode(), name)
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write header %q: %v", name, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		file, err := os.Open(full)
		if err != nil {
			t.Fatalf("open %q: %v", full, err)
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatalf("copy %q: %v", name, copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close %q: %v", name, closeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close re-tar writer: %v", err)
	}
	return buffer.Bytes()
}
```

- [ ] **Prove the assertion reaches through the filesystem before trusting it green.** Temporarily change the helper's `header.Mode` line to `Mode: 0` (so the re-tar reports no executable bit for `bin/run`, which the materialized tree really does carry). Run `go test ./agent/snapshot/ -run 'TestCanonicalCaptureRoundTripsThroughMaterializedDisk' -v -count=1` and confirm it fails on the positive half:

```
--- FAIL: TestCanonicalCaptureRoundTripsThroughMaterializedDisk (0.03s)
    archive_roundtrip_test.go:NN: re-seal of the materialized tree = sha256:..., want sha256:...
FAIL
```

- [ ] Restore `Mode: int64(info.Mode().Perm())` and re-run the same command; confirm:

```
--- PASS: TestCanonicalCaptureRoundTripsThroughMaterializedDisk (0.04s)
PASS
ok  	github.com/concourse/concourse/agent/snapshot	0.29s
```

- [ ] Run `gofmt -l agent/snapshot/archive_roundtrip_test.go` (no output) and `go test ./agent/snapshot/... -count=1`.
- [ ] Commit `test(snapshot): round trip a canonical archive through materialized disk`.

---

### Task 3: The repository's first fuzz targets, and a `test-fuzz` lane

**Files:**
- Create: `agent/snapshot/archive_fuzz_test.go`
- Create: `agent/snapshot/contracts/canonical_json_fuzz_test.go`
- Modify: `agent/snapshot/archive_test.go` (one line: `makeTar`'s parameter type)
- Modify: `Makefile`

Content addressing is this platform's security model, and the repository has zero fuzz targets. Every determinism test in it is a hand-written example. The two most natural targets are the hostile-tar parser and the hand-rolled canonical-JSON serializer — both of which already have good example-based suites whose one structural weakness is that a human chose the examples.

- [ ] Change `makeTar`'s signature in `agent/snapshot/archive_test.go:1373` from `func makeTar(t *testing.T, entries []tarEntry) []byte` to `func makeTar(t testing.TB, entries []tarEntry) []byte`. Change nothing else: `*testing.T` satisfies `testing.TB`, and both `t.Helper()` and `t.Fatalf` are on `testing.TB`, so every existing call site compiles unchanged. The fuzz target needs to build seeds from a `*testing.F`.
- [ ] Run `go build ./agent/snapshot/ && go vet ./agent/snapshot/` and confirm both are silent.
- [ ] Create `agent/snapshot/archive_fuzz_test.go` in `package snapshot` with exactly this content.

```go
package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"testing"
)

// FuzzCanonicalCapture points the fuzzer at the parser this platform's security
// model rests on: arbitrary bytes arriving from a step's output mount, handed
// straight to the canonicalizer. TestExtractRejectsHostileArchives enumerates
// 29 hostile shapes and is excellent; its one structural weakness is that a
// human chose all 29.
//
// Two properties:
//
//  1. Capture never panics, and never returns a tree together with an error.
//     The second half matters because a tree returned alongside an error is a
//     leaked temporary directory and an unowned handle.
//
//  2. Whatever Capture accepts, it accepts as a FIXED POINT: re-capturing its
//     own canonical output yields byte-identical bytes and the same identity.
//     Without this, a stored digest could not be recomputed from stored bytes,
//     which is the one thing content addressing has to guarantee.
//
// Limits are deliberately small so a single iteration is cheap; the limit
// arithmetic itself has its own table tests and is not what is being fuzzed.
func FuzzCanonicalCapture(f *testing.F) {
	f.Add(makeTar(f, []tarEntry{{name: "a.txt", typeflag: tar.TypeReg, content: "hello\n"}}))
	f.Add(makeTar(f, []tarEntry{
		{name: "dir", typeflag: tar.TypeDir},
		{name: "dir/x", typeflag: tar.TypeReg, content: "x"},
	}))
	f.Add(makeTar(f, []tarEntry{
		{name: "a.txt", typeflag: tar.TypeReg, content: "y"},
		{name: "link", typeflag: tar.TypeSymlink, linkname: "a.txt"},
	}))
	f.Add(makeTar(f, []tarEntry{{name: "données/café.txt", typeflag: tar.TypeReg, content: "bonjour\n"}}))
	f.Add(makeTar(f, []tarEntry{{name: "run", typeflag: tar.TypeReg, mode: 0755, content: "#!/bin/sh\n"}}))
	f.Add([]byte(nil))
	f.Add([]byte("not a tar at all"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		canonicalizer := Canonicalizer{MaxEntries: 64, MaxContentBytes: 1 << 16, TempDir: t.TempDir()}

		tree, err := canonicalizer.Capture(context.Background(), bytes.NewReader(raw))
		if err != nil {
			if tree != nil {
				t.Fatalf("Capture() returned a tree together with error %v", err)
			}
			return
		}
		defer tree.Close()

		canonical, err := os.ReadFile(tree.ArchivePath)
		if err != nil {
			t.Fatalf("read canonical archive: %v", err)
		}
		second, err := canonicalizer.Capture(context.Background(), bytes.NewReader(canonical))
		if err != nil {
			t.Fatalf("canonical output was rejected on re-capture: %v", err)
		}
		defer second.Close()

		again, err := os.ReadFile(second.ArchivePath)
		if err != nil {
			t.Fatalf("read re-captured canonical archive: %v", err)
		}
		if !bytes.Equal(canonical, again) {
			t.Fatal("canonicalization is not a fixed point: re-capture changed the emitted bytes")
		}
		if second.Digest != tree.Digest || second.ByteSize != tree.ByteSize || second.FileCount != tree.FileCount {
			t.Fatalf("re-capture identity = %s / %d bytes / %d entries, want %s / %d / %d",
				second.Digest, second.ByteSize, second.FileCount,
				tree.Digest, tree.ByteSize, tree.FileCount)
		}
	})
}
```

- [ ] Create `agent/snapshot/contracts/canonical_json_fuzz_test.go` in `package contracts` (internal — both canonical layers are unexported) with exactly this content.

```go
package contracts

import (
	"bytes"
	"strconv"
	"testing"
)

// FuzzCanonicalJSON fuzzes the hand-rolled canonical serializer that produces
// every schema descriptor from revision 2 onwards. Those bytes have to be
// byte-stable forever, and the existing tests — worked example, fifty shuffled
// key orderings, idempotence, number literals, the escape dialect, ambiguity
// rejection — are all example-based.
//
// Three properties:
//
//  1. Neither layer panics, and a rejected document yields no payload. Rejection
//     is a first-class result here: every rejection in canonical_json.go exists
//     because it is a way for two distinct inputs to reach one canonical form.
//
//  2. Whatever is accepted is accepted as a FIXED POINT: canonicalizing the
//     canonical payload returns exactly the same bytes. Without a fixed point a
//     stored descriptor could not be recomputed, which is what a descriptor is.
//
//  3. The framing is exactly algorithm + "\n" + decimal length + "\n" + payload,
//     with no slack. The framing is what makes the encoding prefix-free, so a
//     drift in it is a concatenation-confusion bug, not a formatting nit.
func FuzzCanonicalJSON(f *testing.F) {
	f.Add([]byte(workedExampleDocument))
	f.Add([]byte(workedExamplePayload))
	f.Add([]byte(`{"n":1}`))
	f.Add([]byte(`{"n":1.0}`))
	f.Add([]byte(`{"n":1e0}`))
	f.Add([]byte(`{"b":true,"a":[1,2,{"z":null}]}`))
	f.Add([]byte(`"😀"`))
	f.Add([]byte(`{"a":1,"a":2}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(nil))
	f.Add([]byte("not json"))

	entries, err := schemaDocumentSources.ReadDir(schemaDocumentDirectory)
	if err != nil {
		f.Fatalf("read embedded schema documents: %v", err)
	}
	for _, entry := range entries {
		document, err := schemaDocumentSources.ReadFile(schemaDocumentDirectory + "/" + entry.Name())
		if err != nil {
			f.Fatalf("read embedded schema document %q: %v", entry.Name(), err)
		}
		f.Add(document)
	}

	f.Fuzz(func(t *testing.T, document []byte) {
		payload, err := canonicalJSONPayload(document)
		if err != nil {
			if payload != nil {
				t.Fatalf("rejected input returned a payload: %q", payload)
			}
			return
		}

		again, err := canonicalJSONPayload(payload)
		if err != nil {
			t.Fatalf("canonical payload was rejected on re-canonicalization: %v", err)
		}
		if !bytes.Equal(payload, again) {
			t.Fatalf("canonicalization is not a fixed point:\n once %q\ntwice %q", payload, again)
		}

		framed, err := canonicalJSONSerialization(document)
		if err != nil {
			t.Fatalf("payload canonicalized but framing failed: %v", err)
		}
		header := canonicalJSONAlgorithm + "\n" + strconv.Itoa(len(payload)) + "\n"
		if !bytes.HasPrefix(framed, []byte(header)) {
			t.Fatalf("framed serialization does not start with %q: %q", header, framed)
		}
		if len(framed) != len(header)+len(payload) {
			t.Fatalf("framed serialization is %d bytes, want exactly %d header + %d payload",
				len(framed), len(header), len(payload))
		}
		if !bytes.Equal(framed[len(header):], payload) {
			t.Fatal("framed serialization does not end with exactly the canonical payload")
		}
	})
}
```

- [ ] Run both targets over their seed corpora only (this is what `go test` without `-fuzz` does, and what the ordinary unit lane will run):

```
go test ./agent/snapshot/ -run 'FuzzCanonicalCapture' -count=1
go test ./agent/snapshot/contracts/ -run 'FuzzCanonicalJSON' -count=1
```

Expect `ok` for both.

- [ ] **Prove `FuzzCanonicalCapture` is load-bearing.** In `agent/snapshot/archive.go:1336`, temporarily change `epoch := time.Unix(0, 0).UTC()` to `epoch := time.Now().UTC()` (emission stops normalizing time, so capture is no longer a fixed point). Run `go test ./agent/snapshot/ -run '^$' -fuzz='^FuzzCanonicalCapture$' -fuzztime=10s -fuzzminimizetime=1s` and confirm:

```
--- FAIL: FuzzCanonicalCapture (0.05s)
    archive_fuzz_test.go:NN: canonicalization is not a fixed point: re-capture changed the emitted bytes
    Failing input written to testdata/fuzz/FuzzCanonicalCapture/xxxxxxxxxxxxxxxx
FAIL
```

Then revert the mutation and delete the false crasher: `rm -rf agent/snapshot/testdata`.

- [ ] **Prove `FuzzCanonicalJSON` is load-bearing.** In `agent/snapshot/contracts/canonical_json.go:213`, temporarily change `buffer.WriteString(typed.String())` to `buffer.WriteString(typed.String() + "0")` (number literals grow on every pass). Run `go test ./agent/snapshot/contracts/ -run '^$' -fuzz='^FuzzCanonicalJSON$' -fuzztime=10s -fuzzminimizetime=1s` and confirm:

```
--- FAIL: FuzzCanonicalJSON (0.02s)
    canonical_json_fuzz_test.go:NN: canonicalization is not a fixed point:
 once {"n":10}
twice {"n":100}
    Failing input written to testdata/fuzz/FuzzCanonicalJSON/xxxxxxxxxxxxxxxx
FAIL
```

Then revert the mutation and delete the false crasher: `rm -rf agent/snapshot/contracts/testdata`.

- [ ] Confirm the tree is clean of fuzz artifacts before proceeding: `git status --porcelain` must show only the four intended files. A real crasher would live in `agent/snapshot/testdata/fuzz/<TargetName>/<hash>` and **must** be committed as a permanent regression seed alongside its fix — but there is no real crasher yet, so nothing under `testdata/` belongs in this commit.
- [ ] Add the `test-fuzz` target to the `Makefile`, immediately after the `test-dev-mcp` block (`Makefile:19-21`), exactly as written:

```make
# Native Go fuzz targets, time-boxed as a smoke lane (~1 min)
# Requires: nothing. Longer campaigns are manual — raise -fuzztime.
#
# -fuzzminimizetime is NOT optional. Its default is 60s per interesting input
# and the coordinator stops fuzzing while it minimizes, so a 30s budget with the
# default is spent almost entirely on minimization: measured 223 executions in
# 30s with the default, versus 34,790 in the same 30s at a 1s minimize budget.
#
# New corpus entries go to the build cache, not the repository. A FAILING input,
# however, is written to <package>/testdata/fuzz/<Target>/<hash> — that file is a
# permanent regression seed and must be committed together with its fix.
test-fuzz:
	@echo "==> Running fuzz targets (30s each)..."
	go test ./agent/snapshot/ -run '^$$' -fuzz='^FuzzCanonicalCapture$$' -fuzztime=30s -fuzzminimizetime=2s
	go test ./agent/snapshot/contracts/ -run '^$$' -fuzz='^FuzzCanonicalJSON$$' -fuzztime=30s -fuzzminimizetime=2s
```

- [ ] Add `test-fuzz` to the `.PHONY` line at `Makefile:1` and to the `test-all` target at `Makefile:66`, so it reads:

```make
test-all: test-unit test-ci-agent test-fuzz test-fly-integration test-integration test-k8s
```

Do **not** add it to `test-quick` (that target is the fast local iteration loop) and do **not** touch either pipeline file — plan `01-ci-execution.md` owns the CI step.

> **Merge note:** plan 01 also edits this one `test-all` line (it appends `test-dev-mcp`). If plan 01 landed first, the line already reads `test-all: test-unit test-ci-agent test-dev-mcp test-fly-integration test-integration test-k8s` — insert `test-fuzz` after `test-dev-mcp` and keep everything else. Do not revert plan 01's addition.

- [ ] Run `make test-fuzz` and confirm both targets execute for 30 seconds each and pass, with execution counts in the thousands (capture) and millions (JSON):

```
==> Running fuzz targets (30s each)...
fuzz: elapsed: 3s, execs: 1039 (346/sec), new interesting: 13 (total: 18)
...
fuzz: elapsed: 30s, execs: 34790 (1458/sec), new interesting: 51 (total: 56)
PASS
ok  	github.com/concourse/concourse/agent/snapshot	30.8s
...
fuzz: elapsed: 25s, execs: 8267470 (338531/sec), new interesting: 321 (total: 327)
PASS
ok  	github.com/concourse/concourse/agent/snapshot/contracts	30.3s
```

If `execs` plateaus at a few hundred and stays flat, `-fuzzminimizetime` was dropped — restore it rather than raising `-fuzztime`.

- [ ] Run `gofmt -l` on both new files (no output) and `go test ./agent/snapshot/... -count=1`.
- [ ] Commit `test(snapshot): fuzz canonical capture and canonical JSON`.

---

### Task 4: Give capture re-run determinism a test-backed answer

**Files:**
- Modify: `agent/resourcecapture/capture_test.go`
- Modify: `agent/resourcecapture/capture.go` (package doc comment only)
- Create (then DELETE, uncommitted): `agent/snapshot/zz_capture_determinism_observation_test.go`

`captureIdentity` (`capture.go:378-419`) makes an operation key out of the team, pipeline, resource, pinned version, resource-config hash and task image — nothing derived from bytes. The capture task is `cp -a source/. snapshot/` (`capture.go:449`) over a git resource's output, `.git` included. "Same version twice gives the same snapshot" is therefore either an invariant or memoization, and the codebase says neither. `capture_test.go:213` re-runs after expiry and never compares a digest — there is not even a second manifest to compare against (fact 13).

This task **observes first, then commits the matching branch.** Do not skip the observation and do not pick a branch from intuition.

#### Step A — observe

- [ ] Create the throwaway observation file `agent/snapshot/zz_capture_determinism_observation_test.go` in `package snapshot`. It is deleted before the commit; it exists only to answer the question with the real `Canonicalizer` over two real git checkouts of one commit.

```go
package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// TestObserveGitCheckoutCaptureDeterminism is a THROWAWAY observation, not a
// test to keep. It answers one question: do two independent checkouts of one
// exact commit — which is what two runs of the same pinned `get` produce, and
// what `cp -a source/. snapshot/` then copies verbatim — capture to the same
// canonical digest?
//
// Delete this file once the answer is recorded.
func TestObserveGitCheckoutCaptureDeterminism(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH; run this observation on a machine that has it")
	}
	run := func(dir string, args ...string) {
		t.Helper()
		command := exec.Command(git, args...)
		command.Dir = dir
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=obs", "GIT_AUTHOR_EMAIL=obs@example.invalid",
			"GIT_COMMITTER_NAME=obs", "GIT_COMMITTER_EMAIL=obs@example.invalid",
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}

	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	if err := os.MkdirAll(origin, 0755); err != nil {
		t.Fatal(err)
	}
	run(origin, "init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(origin, "add", "README.md")
	run(origin, "commit", "-q", "-m", "one")

	digests := make([]Digest, 2)
	for i, name := range []string{"first", "second"} {
		clone := filepath.Join(base, name)
		run(base, "clone", "-q", origin, clone)
		tree, err := (Canonicalizer{}).Capture(context.Background(), bytes.NewReader(observeTarDirectory(t, clone)))
		if err != nil {
			t.Fatalf("capture %s: %v", name, err)
		}
		defer tree.Close()
		digests[i] = tree.Digest
		t.Logf("%s = %s (%d entries, %d bytes)", name, tree.Digest, tree.FileCount, tree.ByteSize)
	}

	if digests[0] == digests[1] {
		t.Log("OBSERVATION: two independent checkouts of one commit capture to the SAME digest -> take BRANCH (a)")
		return
	}
	t.Log("OBSERVATION: two independent checkouts of one commit capture to DIFFERENT digests -> take BRANCH (b)")
}

func observeTarDirectory(t *testing.T, root string) []byte {
	t.Helper()

	var names []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		names = append(names, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatalf("walk %q: %v", root, err)
	}
	sort.Strings(names)

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range names {
		full := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if err != nil {
			t.Fatalf("stat %q: %v", full, err)
		}
		header := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Format: tar.FormatGNU}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(full)
			if err != nil {
				t.Fatalf("read symlink %q: %v", full, err)
			}
			header.Typeflag = tar.TypeSymlink
			header.Linkname = link
		case info.IsDir():
			header.Typeflag = tar.TypeDir
		case info.Mode().IsRegular():
			header.Typeflag = tar.TypeReg
			header.Size = info.Size()
		default:
			t.Fatalf("unsupported file mode %v for %q", info.Mode(), name)
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write header %q: %v", name, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		file, err := os.Open(full)
		if err != nil {
			t.Fatalf("open %q: %v", full, err)
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatalf("copy %q: %v", name, copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close %q: %v", name, closeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buffer.Bytes()
}
```

- [ ] Run it **once** and record the verdict verbatim in the commit body later:

```
go test ./agent/snapshot/ -run 'TestObserveGitCheckoutCaptureDeterminism' -v -count=1
```

Expected shape of output (one of the two `OBSERVATION:` lines will appear):

```
    zz_capture_determinism_observation_test.go:NN: first = sha256:... (N entries, M bytes)
    zz_capture_determinism_observation_test.go:NN: second = sha256:... (N entries, M bytes)
    zz_capture_determinism_observation_test.go:NN: OBSERVATION: two independent checkouts of one commit capture to DIFFERENT digests -> take BRANCH (b)
--- PASS: TestObserveGitCheckoutCaptureDeterminism (0.35s)
```

- [ ] Delete the observation file: `rm agent/snapshot/zz_capture_determinism_observation_test.go`. It must not appear in any commit.

> Note on the likely answer: `.git/index` stores per-file `ctime`/`mtime`/`dev`/`ino`/`size` and `.git/logs/HEAD` records the clone's wall-clock time. The canonicalizer zeroes filesystem metadata but never rewrites file *content*, and git stores metadata as content — so branch (b) is expected. **Take the branch the run actually printed anyway.** If it printed branch (a), that is the more interesting result and it is the one to land.

#### Step B — restructure the retry test so there is a second digest at all

- [ ] In `agent/resourcecapture/capture_test.go`, replace `TestCaptureRetriesTheExactSucceededGenerationWhenItsOutputExpired` (`:213-250`) with the version below. The retry generation now **succeeds** and finalizes a real manifest, which is the only way a second digest exists to compare. The single `<BRANCH>` marker is the one place Step C fills in; everything else is final.

```go
func TestCaptureRetriesTheExactSucceededGenerationWhenItsOutputExpired(t *testing.T) {
	resolver := &fakeResolver{resolve: func(_ context.Context, _ resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		return repositoryResource(), true, nil
	}}
	templates := &fakeTemplates{save: func(_ context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
		return resourcecapture.TemplateRef{ID: 41, TeamID: 7, Name: spec.Name, ConfigVersion: 2, FullHash: spec.FullHash}, nil
	}}

	firstDigest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	secondDigest := snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	manifestFor := func(id snapshot.SnapshotID, digest snapshot.Digest) snapshot.Snapshot {
		return snapshot.Snapshot{
			ID: id, Type: snapshot.TypeRef("repository/v1"), Digest: digest,
			ByteSize: 4096, FileCount: 9, Representation: "application/x-tar",
			ContentState: snapshot.ContentStateAvailable, CreatedAt: repositoryResource().CapturedAt,
		}
	}

	// Generation 51 succeeded and was finalized once; its bytes then expired.
	// Generation 52 is the retry the platform binds under the SAME capture
	// identity, and it finalizes its own manifest.
	executions := &fakeExecutions{}
	executionCall := 0
	executions.start = func(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
		executionCall++
		switch executionCall {
		case 1, 2:
			if request.RetryPipelineRunID != 0 {
				t.Fatalf("initial execution requested a retry: %#v", request)
			}
			return resourcecapture.Execution{PipelineRunID: 51, TemplatePipelineID: 41, InstancePipelineID: 61, Status: db.PipelineRunSucceeded}, false, nil
		case 3:
			if request.RetryPipelineRunID != 51 {
				t.Fatalf("retry did not bind the expired generation: %#v", request)
			}
			return resourcecapture.Execution{PipelineRunID: 52, TemplatePipelineID: 41, InstancePipelineID: 62, Status: db.PipelineRunSucceeded}, true, nil
		default:
			t.Fatalf("unexpected execution call %d: %#v", executionCall, request)
			return resourcecapture.Execution{}, false, nil
		}
	}
	outputs := &fakeOutputs{}
	capturer := newCapturer(t, resolver, templates, executions, outputs)

	// The first Capture finalizes generation 51 normally.
	outputs.finalize = func(_ context.Context, request resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error) {
		if request.PipelineRunID != 51 {
			t.Fatalf("unexpected finalize for run %d", request.PipelineRunID)
		}
		return manifestFor(71, firstDigest), true, nil
	}
	first, err := capturer.Capture(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot == nil || first.Snapshot.Digest != firstDigest {
		t.Fatalf("first capture = %#v", first)
	}

	// The second Capture finds generation 51's bytes gone and retries it.
	outputs.finalize = func(_ context.Context, request resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error) {
		if request.PipelineRunID == 51 {
			return snapshot.Snapshot{}, false, resourcecapture.ErrOutputUnavailable
		}
		if request.PipelineRunID != 52 {
			t.Fatalf("unexpected finalize for run %d", request.PipelineRunID)
		}
		return manifestFor(72, secondDigest), true, nil
	}
	second, err := capturer.Capture(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}

	if second.Execution.PipelineRunID != 52 || !second.Created {
		t.Fatalf("retry result = %#v", second)
	}
	if second.Snapshot == nil {
		t.Fatalf("retry produced no snapshot: %#v", second)
	}
	if len(executions.calls) != 3 || len(outputs.calls) != 3 {
		t.Fatalf("retry calls = executions %d outputs %d", len(executions.calls), len(outputs.calls))
	}

	// <BRANCH> — the digest comparison goes here (Step C).
}
```

- [ ] Confirm the imports at the top of `agent/resourcecapture/capture_test.go` already cover everything used above (`context`, `errors`, `strings`, `testing`, `resourcecapture`, `snapshot`, `atc`, `db` are all present at `:1-16`); add nothing. `manifestFor` is local to this test, so it cannot collide with the package-level fixtures.
- [ ] Run `go vet ./agent/resourcecapture/` and confirm it is silent — at this point the test compiles but the branch assertion is still missing, so it will pass vacuously. Do not commit here.

#### Step C — commit the branch the observation printed

- [ ] **Branch (a) — the observation printed "SAME digest".** Change `secondDigest` to `firstDigest` (`secondDigest := firstDigest`) and replace the `<BRANCH>` marker with:

```go
	// Capture re-run determinism is an INVARIANT: the same pinned version,
	// captured again after its bytes expired, produces the same tree digest.
	// Observed 2026-07-25 by capturing two independent checkouts of one commit
	// through the real Canonicalizer and comparing.
	if second.Snapshot.Digest != first.Snapshot.Digest {
		t.Fatalf("re-capture of the same version bound digest %s, want the original %s",
			second.Snapshot.Digest, first.Snapshot.Digest)
	}
	if second.OperationKey != first.OperationKey {
		t.Fatalf("re-capture changed the capture identity: %q != %q", second.OperationKey, first.OperationKey)
	}
```

and add this paragraph to the `agent/resourcecapture/capture.go` package doc (after the existing "safe identifiers and hashes." sentence):

```go
// Capture is deterministic in the strong sense: the same pinned resource version
// captured twice produces the same tree digest, so the operation key and the
// content digest agree. TestCaptureRetriesTheExactSucceededGenerationWhenItsOutputExpired
// is the assertion; do not weaken it to a memoization claim without re-running
// the observation behind it.
```

- [ ] **Branch (b) — the observation printed "DIFFERENT digests".** Keep `secondDigest` as written (distinct from `firstDigest`) and replace the `<BRANCH>` marker with:

```go
	// Capture re-run is NOT byte-deterministic, and this is the assertion that
	// says so out loud. A git resource's output carries .git, whose index stores
	// per-file stat data and whose logs store wall-clock times; the canonicalizer
	// zeroes filesystem metadata but never rewrites file content, and git stores
	// its metadata as content. Observed 2026-07-25 by capturing two independent
	// checkouts of one commit through the real Canonicalizer.
	//
	// What IS stable is the capture identity. The retry binds a NEW digest under
	// the SAME operation key, and nothing in the capture path may compare the two
	// or refuse the mismatch — if a future change adds such a comparison, this
	// test is what fails.
	if second.Snapshot.Digest == first.Snapshot.Digest {
		t.Fatalf("the retry generation was expected to bind a new digest, got %s twice", second.Snapshot.Digest)
	}
	if second.OperationKey != first.OperationKey {
		t.Fatalf("re-capture changed the capture identity: %q != %q", second.OperationKey, first.OperationKey)
	}
	if second.Snapshot.ID == first.Snapshot.ID {
		t.Fatalf("the retry generation reused snapshot ID %s", second.Snapshot.ID)
	}
```

and add this paragraph to the `agent/resourcecapture/capture.go` package doc (after the existing "safe identifiers and hashes." sentence):

```go
// Capturing the same pinned version twice is MEMOIZATION, not determinism. The
// operation key is derived from the request — team, pipeline, resource, pinned
// version, resource-config hash, task image — and never from bytes, so a second
// generation under the same key may legitimately bind a different tree digest:
// a git resource's output carries .git, whose index and reflog record per-run
// stat data and wall-clock times as file CONTENT, which canonicalization does
// not normalize. Callers must therefore treat "same version" as "same request",
// never as "same digest", and nothing here may refuse a generation because its
// digest differs from an earlier one. See
// TestCaptureRetriesTheExactSucceededGenerationWhenItsOutputExpired.
```

- [ ] Run `go test ./agent/resourcecapture/ -run 'TestCaptureRetriesTheExactSucceededGenerationWhenItsOutputExpired' -v -count=1` and confirm:

```
--- PASS: TestCaptureRetriesTheExactSucceededGenerationWhenItsOutputExpired (0.00s)
PASS
ok  	github.com/concourse/concourse/agent/resourcecapture	0.21s
```

- [ ] Confirm the observation file is gone: `git status --porcelain` must list only `agent/resourcecapture/capture.go` and `agent/resourcecapture/capture_test.go`, and `rg -n 'observeTarDirectory|TestObserveGitCheckoutCaptureDeterminism' agent/` must print nothing.
- [ ] Run `gofmt -l agent/resourcecapture/capture.go agent/resourcecapture/capture_test.go` (no output) and `go test ./agent/resourcecapture/ -count=1`.
- [ ] Commit `test(resourcecapture): pin capture re-run digest behavior`, and put the observation's two digest lines in the commit body so the evidence is recoverable.

---

### Task 5: Verify exposed path digests against the exposed content

**Files:**
- Create: `agent/snapshot/exposure_verify.go`
- Create: `agent/snapshot/exposure_verify_test.go`
- Modify: `agent/snapshot/sealer.go`
- Modify: `agent/snapshot/exposure.go` (doc comment on `ExposedPath` only)

**Which branch of the spec item this is, and why.** The spec allows two shapes: hash-while-writing at the static-selector materialization site, or — if per-path digests are never materialized anywhere — a verification helper plus a test at the recording site. Scouting settles it (facts 5, 6, 7): **there is no static-selector materialization site.** `NewStaticSelectorExposure` has zero production callers; the only production recorder is `recordExposure`, which always writes `FullTreeExposure` with no paths. So the fallback lands, sharpened: the helper goes in, and the seal gate — the one place with authorized access to the exposed input's bytes — calls it, so that the moment a static selector *is* produced, its claim is checked instead of trusted.

**The two-gate argument (required by the design's cross-cutting constraints).** This tightens the *seal* gate only.

1. It is unreachable from any read path. The only caller is `BatchSealer.Seal`. `RevalidateSealed`, `readSealedRecord`, `load_snapshot`, and `SnapshotArtifact.StreamOut` are untouched.
2. It cannot invalidate stored bytes even in principle: exposure lineage is production-occurrence data that never enters the sealed archive and never enters `ValidationResult.IntrinsicMetadata` (`exposure.go:141-144`, pinned by `TestSealCarriesExposureLineageToValidatorsButNotIntoContentIdentity`). No digest, descriptor, or schema revision moves.
3. It has no existing corpus to disagree with: `agent_snapshot_exposure_paths` can only be populated by a static-selector exposure, and nothing writes one, so the table is empty in every deployment.

- [ ] Create `agent/snapshot/exposure_verify.go` with exactly this content.

```go
package snapshot

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// ErrExposureMismatch reports that recorded exposure lineage disagrees with the
// bytes it claims to describe. It is a refusal, not a warning: an exposure whose
// path digests are wrong is a false answer to "what did this step actually see",
// which is the only question the lineage exists to answer.
var ErrExposureMismatch = errors.New("snapshot: exposure does not match the exposed content")

// VerifyExposedPaths recomputes every enumerated exposed-path digest from the
// exposed snapshot's canonical archive and refuses on any disagreement.
//
// It exists because the per-path digest is the one part of exposure lineage that
// is a CLAIM ABOUT CONTENT rather than a server-observed fact. Everything else is
// already bound: the mode is chosen by the platform, and the tree digest is
// checked against the exposed input's own SnapshotRef by validateDeclaredExposures
// and again by the artifact reader on every read. The path digests were validated
// for FORMAT only and recomputed by nobody.
//
// A path digest is DEFINED HERE, because nothing defined it before: it is SHA-256
// over the exact content bytes of the regular file at that archive-relative path,
// rendered "sha256:<lowercase hex>" — the same algorithm and the same spelling as
// a tree digest, one level down. Directories and symlinks are not documents and
// cannot be exposed as one.
//
// The walk is a single streaming pass with no random access, because both
// sequences are already sorted bytewise: canonical archive entries by
// sortedCaptureEntries, exposed paths by sortExposedPaths. A claimed path that
// sorts before the current header can never appear later, so it is absent.
//
// This is a seal-time check. It reads no stored record and rejects no stored
// bytes.
func VerifyExposedPaths(ctx context.Context, canonicalArchive io.Reader, exposure InputExposure) error {
	if canonicalArchive == nil {
		return fmt.Errorf("snapshot: exposed archive reader is required")
	}
	if err := exposure.Validate(); err != nil {
		return err
	}
	if exposure.Mode != MaterializationStaticSelector {
		// Full materialization enumerates nothing: the tree digest already
		// records every path, and it is bound elsewhere.
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	reader := tar.NewReader(contextReader{ctx: ctx, reader: canonicalArchive})
	next := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("snapshot: read exposed archive: %w", err)
		}
		if next < len(exposure.Paths) && exposure.Paths[next].Path < header.Name {
			return fmt.Errorf("%w: exposed path %q is absent from the exposed tree",
				ErrExposureMismatch, exposure.Paths[next].Path)
		}
		if next >= len(exposure.Paths) || exposure.Paths[next].Path != header.Name {
			continue
		}
		expected := exposure.Paths[next]
		next++

		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("%w: exposed path %q is not a regular file", ErrExposureMismatch, expected.Path)
		}
		hasher := sha256.New()
		copied, err := io.Copy(hasher, contextReader{ctx: ctx, reader: reader})
		if err != nil {
			return fmt.Errorf("snapshot: read exposed path %q: %w", expected.Path, err)
		}
		if copied != header.Size {
			return fmt.Errorf("%w: exposed path %q is truncated at %d of %d bytes",
				ErrExposureMismatch, expected.Path, copied, header.Size)
		}
		actual := Digest(fmt.Sprintf("sha256:%x", hasher.Sum(nil)))
		if actual != expected.Digest {
			return fmt.Errorf("%w: exposed path %q hashes to %s but the exposure claims %s",
				ErrExposureMismatch, expected.Path, actual, expected.Digest)
		}
	}
	if next < len(exposure.Paths) {
		return fmt.Errorf("%w: exposed path %q is absent from the exposed tree",
			ErrExposureMismatch, exposure.Paths[next].Path)
	}
	return nil
}
```

- [ ] Create `agent/snapshot/exposure_verify_test.go` in `package snapshot` with exactly this content. Write it **before** wiring the sealer; the second test is the failing one.

```go
package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func contentDigest(body string) Digest {
	return Digest(fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body))))
}

const exposedRecordBody = `{"schema_version":"1.0.0"}`
const exposedNoteBody = "a note\n"

// exposedTree is a canonical archive holding one record, one note, one
// directory and one symlink — enough to exercise every refusal below.
func exposedTree(t *testing.T) []byte {
	t.Helper()
	return canonicalBody(t, makeTar(t, []tarEntry{
		{name: "docs", typeflag: tar.TypeDir},
		{name: "docs/note.txt", typeflag: tar.TypeReg, content: exposedNoteBody},
		{name: "link", typeflag: tar.TypeSymlink, linkname: "record.json"},
		{name: "record.json", typeflag: tar.TypeReg, content: exposedRecordBody},
	}))
}

func TestVerifyExposedPathsAcceptsTruthAndRefusesEveryDisagreement(t *testing.T) {
	t.Parallel()

	archive := exposedTree(t)
	tree := mustTestDigest(t)

	tests := []struct {
		name  string
		paths []ExposedPath
		want  string
	}{
		{
			name: "every claimed digest matches",
			paths: []ExposedPath{
				{Path: "docs/note.txt", Digest: contentDigest(exposedNoteBody)},
				{Path: "record.json", Digest: contentDigest(exposedRecordBody)},
			},
		},
		{
			name:  "one claimed digest is wrong",
			paths: []ExposedPath{{Path: "record.json", Digest: contentDigest(exposedNoteBody)}},
			want:  `exposed path "record.json" hashes to`,
		},
		{
			name:  "claimed path is absent",
			paths: []ExposedPath{{Path: "missing.json", Digest: contentDigest(exposedRecordBody)}},
			want:  `exposed path "missing.json" is absent from the exposed tree`,
		},
		{
			name:  "claimed path sorts after every entry",
			paths: []ExposedPath{{Path: "zzz.json", Digest: contentDigest(exposedRecordBody)}},
			want:  `exposed path "zzz.json" is absent from the exposed tree`,
		},
		{
			name:  "claimed path is a directory",
			paths: []ExposedPath{{Path: "docs", Digest: contentDigest("")}},
			want:  `exposed path "docs" is not a regular file`,
		},
		{
			name:  "claimed path is a symlink",
			paths: []ExposedPath{{Path: "link", Digest: contentDigest("record.json")}},
			want:  `exposed path "link" is not a regular file`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exposure, err := NewStaticSelectorExposure("/tmp/build/plan/base", tree, tt.paths...)
			if err != nil {
				t.Fatalf("NewStaticSelectorExposure() error = %v", err)
			}
			err = VerifyExposedPaths(context.Background(), bytes.NewReader(archive), exposure)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("VerifyExposedPaths() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("VerifyExposedPaths() error = %v, want substring %q", err, tt.want)
			}
			if !errors.Is(err, ErrExposureMismatch) {
				t.Fatalf("VerifyExposedPaths() error = %v, want ErrExposureMismatch", err)
			}
		})
	}
}

func TestVerifyExposedPathsIgnoresFullMaterialization(t *testing.T) {
	t.Parallel()

	// A full-tree exposure enumerates nothing, so there is nothing to recompute
	// and the reader must never be consumed as if there were.
	exposure := FullTreeExposure("/tmp/build/plan/base", mustTestDigest(t))
	if err := VerifyExposedPaths(context.Background(), bytes.NewReader(nil), exposure); err != nil {
		t.Fatalf("VerifyExposedPaths(full) error = %v, want nil", err)
	}
}

// TestSealRefusesAStaticSelectorExposureThatDisagreesWithTheStoredContent is the
// corrupted-store case: metadata authorizes the exact input reference, and the
// content store hands back a tree whose bytes do not match the per-path digest
// the exposure claims. The seal must refuse, and it must refuse BEFORE it stages,
// uploads or commits anything.
func TestSealRefusesAStaticSelectorExposureThatDisagreesWithTheStoredContent(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	archive := exposedTree(t)
	inputDigest := mustTestDigest(t)
	inputRef := SnapshotRef{ID: 41, Type: TypeRef("repository/v1"), Digest: inputDigest}

	newSealer := func() (*BatchSealer, *sealerMetadataStore, *sealerContentStore, *sealerLocks) {
		metadata := &sealerMetadataStore{authorized: map[SnapshotID]Snapshot{41: {
			ID: 41, Type: inputRef.Type, Digest: inputRef.Digest,
			ByteSize: int64(len(archive)), FileCount: 4, Representation: "application/x-tar",
			ContentState: ContentStateAvailable, CreatedAt: now,
		}}}
		content := &sealerContentStore{
			events:      &metadata.events,
			exists:      map[Location]bool{},
			openContent: map[SnapshotID][]byte{41: archive},
		}
		locks := &sealerLocks{lease: &sealerLease{digests: map[Digest]bool{}}}
		sealer := mustNewSealer(t, t.TempDir(), metadata, content, locks,
			sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
				return ValidationResult{}, nil
			}),
			WithBatchSealerClock(func() time.Time { return now }),
		)
		return sealer, metadata, content, locks
	}

	request := func(paths ...ExposedPath) SealRequest {
		t.Helper()
		exposure, err := NewStaticSelectorExposure("/tmp/build/plan/base", inputDigest, paths...)
		if err != nil {
			t.Fatalf("NewStaticSelectorExposure() error = %v", err)
		}
		value := sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", tarBytes(t, "value", "output"))})
		value.InputOrder = []string{"base"}
		value.Inputs = map[string]SnapshotRef{"base": inputRef}
		value.InputExposures = map[string]InputExposure{"base": exposure}
		return value
	}

	t.Run("a truthful exposure seals", func(t *testing.T) {
		sealer, metadata, _, _ := newSealer()
		if _, err := sealer.Seal(context.Background(), request(
			ExposedPath{Path: "record.json", Digest: contentDigest(exposedRecordBody)},
		)); err != nil {
			t.Fatalf("Seal() error = %v", err)
		}
		if metadata.commit == nil {
			t.Fatal("a truthful exposure did not commit")
		}
	})

	t.Run("a corrupted store refuses before any storage", func(t *testing.T) {
		sealer, metadata, content, locks := newSealer()
		_, err := sealer.Seal(context.Background(), request(
			ExposedPath{Path: "record.json", Digest: mustOtherDigest(t)},
		))
		if err == nil || !errors.Is(err, ErrExposureMismatch) {
			t.Fatalf("Seal() error = %v, want ErrExposureMismatch", err)
		}
		if !strings.Contains(err.Error(), `exposed path "record.json" hashes to`) {
			t.Fatalf("Seal() error = %v, want the operator-actionable path and digests", err)
		}
		if len(locks.acquired) != 0 {
			t.Fatalf("a refused exposure acquired %d digest leases", len(locks.acquired))
		}
		if len(metadata.stages) != 0 || metadata.commit != nil || content.putCalls != 0 {
			t.Fatalf("a refused exposure reached storage: stages=%d commit=%v puts=%d",
				len(metadata.stages), metadata.commit != nil, content.putCalls)
		}
	})
}
```

- [ ] Run `go test ./agent/snapshot/ -run 'TestVerifyExposedPaths|TestSealRefusesAStaticSelectorExposure' -v -count=1` and confirm the helper tests pass while the seal test fails, because nothing calls the helper yet:

```
--- PASS: TestVerifyExposedPathsAcceptsTruthAndRefusesEveryDisagreement (0.05s)
--- PASS: TestVerifyExposedPathsIgnoresFullMaterialization (0.00s)
--- FAIL: TestSealRefusesAStaticSelectorExposureThatDisagreesWithTheStoredContent (0.06s)
    --- FAIL: TestSealRefusesAStaticSelectorExposureThatDisagreesWithTheStoredContent/a_corrupted_store_refuses_before_any_storage (0.03s)
        exposure_verify_test.go:NNN: Seal() error = <nil>, want ErrExposureMismatch
FAIL
```

- [ ] Wire the gate into `agent/snapshot/sealer.go`. Insert the call immediately after the `NewValidationContext` block in `Seal` (`sealer.go:130-138`), before `sourcesByPort` is built:

```go
	if err := sealer.verifyExposedPaths(ctx, request); err != nil {
		return nil, err
	}
```

and add the method next to `inputOpener` (after `sealer.go:488`):

```go
// verifyExposedPaths recomputes every enumerated exposed-path digest against the
// exposed input's own stored bytes, using the same authorized opener a validator
// would get. It runs before any capture, stage, upload or commit, so a false
// exposure costs nothing and reaches no storage.
//
// Only static selectors are checked, because only they make a claim: a full-tree
// exposure enumerates nothing and its tree digest is already bound to the input
// reference by validateDeclaredExposures.
//
// This is a seal-time gate. It never runs on a read path and can therefore never
// reject stored bytes; exposure lineage is production-occurrence data that is not
// part of any sealed archive.
func (sealer *BatchSealer) verifyExposedPaths(ctx context.Context, request SealRequest) error {
	if len(request.InputExposures) == 0 {
		return nil
	}
	ports := make([]string, 0, len(request.InputExposures))
	for port := range request.InputExposures {
		if request.InputExposures[port].Mode == MaterializationStaticSelector {
			ports = append(ports, port)
		}
	}
	if len(ports) == 0 {
		return nil
	}
	sort.Strings(ports)

	opener := sealer.inputOpener(request.TeamID)
	for _, port := range ports {
		if err := ctx.Err(); err != nil {
			return err
		}
		ref, exposed := request.Inputs[port]
		if !exposed {
			return fmt.Errorf("snapshot: exposure input port %q is not an exposed input", port)
		}
		archive, err := opener(ctx, port, ref)
		if err != nil {
			return fmt.Errorf("snapshot: open exposed input %q for verification: %w", port, err)
		}
		if archive == nil {
			return fmt.Errorf("snapshot: exposed input %q opened no reader", port)
		}
		verifyErr := VerifyExposedPaths(ctx, archive, request.InputExposures[port])
		closeErr := archive.Close()
		if verifyErr != nil || closeErr != nil {
			return errors.Join(
				wrapIfNonNil(fmt.Sprintf("snapshot: verify exposed paths for input %q", port), verifyErr),
				wrapIfNonNil(fmt.Sprintf("snapshot: close exposed input %q", port), closeErr),
			)
		}
	}
	return nil
}
```

- [ ] Re-run `go test ./agent/snapshot/ -run 'TestVerifyExposedPaths|TestSealRefusesAStaticSelectorExposure' -v -count=1` and confirm everything passes:

```
--- PASS: TestSealRefusesAStaticSelectorExposureThatDisagreesWithTheStoredContent (0.09s)
    --- PASS: TestSealRefusesAStaticSelectorExposureThatDisagreesWithTheStoredContent/a_truthful_exposure_seals (0.05s)
    --- PASS: TestSealRefusesAStaticSelectorExposureThatDisagreesWithTheStoredContent/a_corrupted_store_refuses_before_any_storage (0.03s)
PASS
ok  	github.com/concourse/concourse/agent/snapshot	0.42s
```

- [ ] Document the digest's now-defined meaning on `ExposedPath` in `agent/snapshot/exposure.go:95-102`, replacing the existing comment body above the struct with:

```go
// ExposedPath is one exact document a static selector materialized, with the
// digest of the bytes that were exposed. The path is relative to the canonical
// archive root — the only namespace verifiable at seal time — never to a pod
// mount path.
//
// Digest is SHA-256 over the exact content bytes of the regular file at Path,
// spelled "sha256:<lowercase hex>": the same algorithm and spelling as a tree
// digest, one level down. Validate checks only that it parses; VerifyExposedPaths
// is what recomputes it against the exposed input's stored bytes, and the seal
// gate calls that on every static selector. A path digest is the one piece of
// exposure lineage that is a claim rather than a server-observed fact, which is
// exactly why it is the one piece that gets recomputed.
```

- [ ] Run `gofmt -l agent/snapshot/exposure.go agent/snapshot/exposure_verify.go agent/snapshot/exposure_verify_test.go agent/snapshot/sealer.go` (no output).
- [ ] Run `go test ./agent/snapshot/... ./atc/exec/... -count=1` and confirm both are green — the exec packages construct `FullTreeExposure` only, so the new gate is a no-op for them, and this proves it.
- [ ] Commit `feat(snapshot): verify exposed path digests against exposed content`.

---

### Task 6: Seal identical content under a digest lease that actually excludes

**Files:**
- Create: `agent/snapshot/sealer_contention_test.go`

`agent/snapshot/sealer.go` and `store.go` contain zero goroutines in tests, and `sealerLocks` hands every caller the same lease without ever blocking (fact 10). The convergence story — two steps producing byte-identical content deduplicate to one stored value — is asserted only by a sequential test with a lock that cannot exclude.

- [ ] Create `agent/snapshot/sealer_contention_test.go` in `package snapshot` with exactly this content.

```go
package snapshot

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// blockingDigestLocks is a DigestLockManager that actually excludes.
//
// sealerLocks, the fake every other sealer test uses, returns the same lease to
// every caller and never blocks: two sealers can both be "holding" the same
// digest at once, which is adequate for sequencing assertions and useless for
// contention. This one models the PostgreSQL session advisory lock the real
// manager takes — one holder per digest, acquired in the caller's already-sorted
// order, released on Close — and records the maximum number of simultaneous
// holders, so exclusion is asserted rather than assumed.
type blockingDigestLocks struct {
	mu        sync.Mutex
	semaphore map[Digest]chan struct{}
	holding   map[Digest]int
	maxHeld   int
	acquires  int
}

func newBlockingDigestLocks() *blockingDigestLocks {
	return &blockingDigestLocks{
		semaphore: map[Digest]chan struct{}{},
		holding:   map[Digest]int{},
	}
}

func (l *blockingDigestLocks) gate(digest Digest) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	gate, found := l.semaphore[digest]
	if !found {
		gate = make(chan struct{}, 1)
		l.semaphore[digest] = gate
	}
	return gate
}

func (l *blockingDigestLocks) enter(digest Digest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holding[digest]++
	if l.holding[digest] > l.maxHeld {
		l.maxHeld = l.holding[digest]
	}
}

func (l *blockingDigestLocks) leave(digest Digest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holding[digest]--
}

func (l *blockingDigestLocks) AcquireMany(ctx context.Context, digests []Digest) (DigestLease, error) {
	l.mu.Lock()
	l.acquires++
	l.mu.Unlock()

	lease := &blockingDigestLease{manager: l, covered: map[Digest]bool{}}
	for _, digest := range digests {
		select {
		case <-ctx.Done():
			return lease, ctx.Err()
		case l.gate(digest) <- struct{}{}:
		}
		lease.acquired = append(lease.acquired, digest)
		lease.covered[digest] = true
		l.enter(digest)
	}
	return lease, nil
}

func (l *blockingDigestLocks) stats() (acquires int, maxHeld int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acquires, l.maxHeld
}

type blockingDigestLease struct {
	manager   *blockingDigestLocks
	covered   map[Digest]bool
	acquired  []Digest
	closeOnce sync.Once
}

func (l *blockingDigestLease) Covers(digest Digest) bool { return l.covered[digest] }

func (l *blockingDigestLease) Close() error {
	l.closeOnce.Do(func() {
		for i := len(l.acquired) - 1; i >= 0; i-- {
			digest := l.acquired[i]
			l.manager.leave(digest)
			<-l.manager.gate(digest)
		}
	})
	return nil
}

// concurrentSealStore is a metadata store that behaves like the real one where
// convergence is decided: identity is keyed on the digest, so the second sealer
// to arrive finds the first's committed manifest and reuses it rather than
// creating a second value.
type concurrentSealStore struct {
	MetadataStore
	mu        sync.Mutex
	now       time.Time
	nextID    SnapshotID
	stages    int
	commits   int
	created   int
	snapshots map[Digest]Snapshot
	locations map[Digest][]Location
}

func newConcurrentSealStore(now time.Time) *concurrentSealStore {
	return &concurrentSealStore{
		now:       now,
		snapshots: map[Digest]Snapshot{},
		locations: map[Digest][]Location{},
	}
}

func (s *concurrentSealStore) StageUpload(_ context.Context, _ DigestLease, request StageUploadRequest) (StagedUpload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stages++
	return StagedUpload{
		ID: int64(s.stages), Digest: request.Digest, TeamID: request.TeamID,
		Attempt: request.Attempt, LeaseExpiresAt: request.LeaseExpiresAt,
		CreatedAt: request.LeaseExpiresAt.Add(-time.Hour),
	}, nil
}

func (s *concurrentSealStore) DigestState(_ context.Context, _ DigestLease, digest Digest, _ time.Time) (DigestState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := DigestState{Digest: digest}
	if manifest, found := s.snapshots[digest]; found {
		state.Snapshots = []Snapshot{manifest.Clone()}
		state.Locations = append([]Location(nil), s.locations[digest]...)
	}
	return state, nil
}

func (s *concurrentSealStore) CommitSealBatch(_ context.Context, _ DigestLease, commit SealCommit) (map[string]SealedOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits++
	result := make(map[string]SealedOutput, len(commit.Outputs))
	for _, output := range commit.Outputs {
		manifest, found := s.snapshots[output.Digest]
		if !found {
			s.created++
			s.nextID++
			manifest = Snapshot{
				ID: s.nextID, Type: output.Port.Type, Digest: output.Digest,
				ByteSize: output.ByteSize, FileCount: output.FileCount,
				Representation: output.Representation, IntrinsicMetadata: output.IntrinsicMetadata,
				ContentState: ContentStateAvailable, CreatedAt: s.now,
			}
			s.snapshots[output.Digest] = manifest
			s.locations[output.Digest] = append([]Location(nil), output.Locations...)
		}
		result[output.ClientKey] = SealedOutput{
			Port:     output.Port,
			Snapshot: SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest},
		}
	}
	return result, nil
}

func (s *concurrentSealStore) counts() (stages, commits, created int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stages, s.commits, s.created
}

type concurrentContentStore struct {
	ContentStore
	mu     sync.Mutex
	puts   int
	stored map[Digest][]byte
}

func newConcurrentContentStore() *concurrentContentStore {
	return &concurrentContentStore{stored: map[Digest][]byte{}}
}

func (s *concurrentContentStore) Put(_ context.Context, digest Digest, reader io.Reader) ([]Location, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	s.stored[digest] = body
	return []Location{{Digest: digest, Driver: "test", Key: digest.String()}}, nil
}

func (s *concurrentContentStore) Exists(_ context.Context, location Location) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found := s.stored[location.Digest]
	return found, nil
}

func (s *concurrentContentStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

// TestConcurrentIdenticalContentSealsConvergeOnOneStoredValue is the sealer's
// first concurrent test. Two independent steps produce byte-identical output at
// the same moment through one metadata store, one content store and one digest
// lease manager that genuinely excludes.
//
// What must hold:
//
//   - Both seals succeed. Contention is not an error condition.
//   - Both converge on ONE digest and ONE snapshot ID. A second stored value for
//     identical bytes would silently fork the corpus.
//   - The bytes are stored ONCE. The loser of the race must find the winner's
//     committed, verified locations and reuse them rather than re-uploading.
//   - Exactly one commit CREATES content; the other binds to it.
//   - The lease was never held by both at once. Without that, the three
//     assertions above could pass by luck on a fast machine.
//
// Run under -race as well as plain; plan 01's race lane executes it.
func TestConcurrentIdenticalContentSealsConvergeOnOneStoredValue(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	body := tarBytes(t, "value", "identical")
	wantDigest := canonicalDigest(t, body)

	metadata := newConcurrentSealStore(now)
	content := newConcurrentContentStore()
	locks := newBlockingDigestLocks()
	validator := sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	})

	const sealers = 2
	results := make([]map[string]SealedOutput, sealers)
	failures := make([]error, sealers)
	start := make(chan struct{})
	var group sync.WaitGroup

	for i := 0; i < sealers; i++ {
		sealer, err := NewBatchSealer(
			Canonicalizer{TempDir: t.TempDir()},
			sealerRegistry{TypeRef("opaque/v1"): validator},
			metadata, content, locks,
			WithBatchSealerClock(func() time.Time { return now }),
		)
		if err != nil {
			t.Fatal(err)
		}
		request := sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", body)})
		request.PlanID = fmt.Sprintf("plan-%d", i)

		group.Add(1)
		go func(index int, sealer *BatchSealer, request SealRequest) {
			defer group.Done()
			<-start
			results[index], failures[index] = sealer.Seal(context.Background(), request)
		}(i, sealer, request)
	}
	close(start)
	group.Wait()

	for i, err := range failures {
		if err != nil {
			t.Fatalf("sealer %d failed: %v", i, err)
		}
	}

	first, found := results[0]["result"]
	if !found {
		t.Fatalf("sealer 0 returned %#v", results[0])
	}
	second, found := results[1]["result"]
	if !found {
		t.Fatalf("sealer 1 returned %#v", results[1])
	}
	if first.Snapshot.Digest != wantDigest || second.Snapshot.Digest != wantDigest {
		t.Fatalf("sealed digests = %s and %s, want %s both",
			first.Snapshot.Digest, second.Snapshot.Digest, wantDigest)
	}
	if first.Snapshot.ID != second.Snapshot.ID {
		t.Fatalf("identical content forked into snapshots %s and %s", first.Snapshot.ID, second.Snapshot.ID)
	}

	if puts := content.putCount(); puts != 1 {
		t.Fatalf("content store received %d writes for identical bytes, want exactly 1", puts)
	}
	stages, commits, created := metadata.counts()
	if created != 1 {
		t.Fatalf("%d commits created content, want exactly 1", created)
	}
	if commits != sealers {
		t.Fatalf("commits = %d, want one per production (%d)", commits, sealers)
	}
	if stages != sealers {
		t.Fatalf("stages = %d, want one per sealer (%d)", stages, sealers)
	}

	acquires, maxHeld := locks.stats()
	if acquires != sealers {
		t.Fatalf("digest lease acquisitions = %d, want %d", acquires, sealers)
	}
	if maxHeld != 1 {
		t.Fatalf("the digest lease was held by %d callers at once; the lease does not exclude", maxHeld)
	}
}
```

- [ ] **Prove the test detects a lease that does not exclude** — that is, that it would have failed against `sealerLocks`. Temporarily make `blockingDigestLocks` grant without gating: in `AcquireMany`, delete the four-line `select` statement (keeping the `lease.acquired`, `lease.covered` and `l.enter` lines), and in `Close`, delete the `<-l.manager.gate(digest)` line (which would otherwise block forever on an empty channel). Run `go test ./agent/snapshot/ -run 'TestConcurrentIdenticalContentSealsConvergeOnOneStoredValue' -v -count=5` and confirm every run fails — on the double write, or on the holder count, or on both, depending on interleaving:

```
--- FAIL: TestConcurrentIdenticalContentSealsConvergeOnOneStoredValue (0.07s)
    sealer_contention_test.go:NNN: content store received 2 writes for identical bytes, want exactly 1
FAIL
```

```
--- FAIL: TestConcurrentIdenticalContentSealsConvergeOnOneStoredValue (0.07s)
    sealer_contention_test.go:NNN: the digest lease was held by 2 callers at once; the lease does not exclude
FAIL
```

- [ ] Restore both deleted fragments exactly and re-run `-count=5`; confirm five consecutive passes.
- [ ] Run it under the race detector, which is the condition plan 01's lane will impose:

```
go test -race ./agent/snapshot/ -run 'TestConcurrentIdenticalContentSealsConvergeOnOneStoredValue' -count=10
```

Expect `ok` with no `WARNING: DATA RACE` anywhere in the output. If a race is reported inside `agent/snapshot` production code, that is a finding — record it and stop; do not paper over it by serializing the test.

- [ ] Run `gofmt -l agent/snapshot/sealer_contention_test.go` (no output) and `go test ./agent/snapshot/... -count=1`.
- [ ] Commit `test(snapshot): seal identical content under real digest-lease contention`.

---

### Task 7: Prove the operation lock actually excludes

**Files:**
- Modify: `agent/resourcecapture/operation_locker_test.go`

`DBOperationLocker.WithLock` is the mutual-exclusion primitive the resource-capture path relies on, and its only test simulates contention with a call counter that returns `false` once (fact 12). That proves the retry loop retries. Nothing proves two callers cannot both be inside the action — which is the entire point of the lock.

A genuinely exclusive in-memory `lock.LockFactory` needs no new production code: `lock.NewTestLockFactory` returns the real factory, whose real `lockRepo` excludes in-process, and it takes a `LockDB` (fact 11).

- [ ] Add exactly four imports to `agent/resourcecapture/operation_locker_test.go`'s import block (`:3-13`): `"strconv"`, `"strings"`, `"sync"`, `"sync/atomic"`. Everything else the new code needs — `context`, `errors`, `testing`, `time`, `lager`, `resourcecapture`, `dblock`, `lockfakes` — is already imported, and `lockfakes` stays because the two existing tests still use it.
- [ ] Append the following to the same file.

```go
// inMemoryLockDB is a lock database with pg_try_advisory_lock semantics and no
// PostgreSQL: an id that is already held is refused, and releasing an unheld id
// reports false the way the real one does.
//
// Combined with lock.NewTestLockFactory it produces a REAL lock.LockFactory —
// the same *lockFactory type production uses, with the same lockRepo and the
// same acquireMutex — so the exclusion under test is the code's own, not the
// test's. The existing tests in this file fake contention with a call counter
// and therefore prove only that WithLock retries.
type inMemoryLockDB struct {
	mu   sync.Mutex
	held map[string]bool
}

func newInMemoryLockDB() *inMemoryLockDB {
	return &inMemoryLockDB{held: map[string]bool{}}
}

func (db *inMemoryLockDB) key(id dblock.LockID) string {
	parts := make([]string, len(id))
	for i, value := range id {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, "+")
}

func (db *inMemoryLockDB) Acquire(id dblock.LockID) (bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	key := db.key(id)
	if db.held[key] {
		return false, nil
	}
	db.held[key] = true
	return true, nil
}

func (db *inMemoryLockDB) Release(id dblock.LockID) (bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	key := db.key(id)
	if !db.held[key] {
		return false, nil
	}
	delete(db.held, key)
	return true, nil
}

// TestDBOperationLockerExcludesConcurrentHoldersOfOneKey is the mutual-exclusion
// proof: eight real goroutines contend for one key against a lock factory that
// genuinely excludes, and an atomic counter incremented on entry and decremented
// on exit must never be observed above one.
//
// Overlap is counted rather than asserted per iteration so a failure reports how
// often exclusion broke, and every action sleeps long enough that two
// simultaneous holders would overlap in wall-clock time rather than merely in
// principle.
func TestDBOperationLockerExcludesConcurrentHoldersOfOneKey(t *testing.T) {
	factory := dblock.NewTestLockFactory(newInMemoryLockDB())
	locker, err := resourcecapture.NewDBOperationLocker(lager.NewLogger("test"), factory)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	var inside, overlaps, completed atomic.Int64
	start := make(chan struct{})
	failures := make(chan error, goroutines)
	var group sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			err := locker.WithLock(context.Background(), "resource-capture/shared", func() error {
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				time.Sleep(5 * time.Millisecond)
				inside.Add(-1)
				completed.Add(1)
				return nil
			})
			if err != nil {
				failures <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(failures)

	for err := range failures {
		t.Fatalf("WithLock() error = %v", err)
	}
	if got := overlaps.Load(); got != 0 {
		t.Fatalf("%d critical-section overlaps: the operation lock does not exclude", got)
	}
	if got := completed.Load(); got != goroutines {
		t.Fatalf("%d of %d contenders ran their action", got, goroutines)
	}
	if got := inside.Load(); got != 0 {
		t.Fatalf("critical-section counter settled at %d, want 0", got)
	}
}

// TestDBOperationLockerDoesNotSerializeDistinctKeys is the other half: exclusion
// that is really a global mutex would pass the test above and would quietly
// serialize every unrelated capture. Distinct keys must all be held at once.
func TestDBOperationLockerDoesNotSerializeDistinctKeys(t *testing.T) {
	factory := dblock.NewTestLockFactory(newInMemoryLockDB())
	locker, err := resourcecapture.NewDBOperationLocker(lager.NewLogger("test"), factory)
	if err != nil {
		t.Fatal(err)
	}

	// WithLock retries forever on an uncancellable context, so the failure path
	// below must be able to unwedge the goroutines it is about to wait on.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const goroutines = 4
	entered := make(chan struct{}, goroutines)
	release := make(chan struct{})
	failures := make(chan error, goroutines)
	var group sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		key := "resource-capture/key-" + strconv.Itoa(i)
		group.Add(1)
		go func() {
			defer group.Done()
			err := locker.WithLock(ctx, key, func() error {
				entered <- struct{}{}
				<-release
				return nil
			})
			if err != nil {
				failures <- err
			}
		}()
	}
	for i := 0; i < goroutines; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			cancel()
			group.Wait()
			t.Fatalf("only %d of %d distinct keys were held at once; distinct operations are being serialized", i, goroutines)
		}
	}
	close(release)
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("WithLock() error = %v", err)
	}
}
```

- [ ] **Prove the exclusion test detects a lock that does not exclude — using the exact faking style this task replaces.** Temporarily swap the factory in `TestDBOperationLockerExcludesConcurrentHoldersOfOneKey` for the always-granting counterfeit the rest of this file already uses:

```go
	factory := &lockfakes.FakeLockFactory{}
	factory.AcquireReturns(&releaseLockStub{}, true, nil)
```

Every caller is now granted immediately, which is exactly what the two pre-existing tests in this file assume and never notice. Run `go test ./agent/resourcecapture/ -run 'TestDBOperationLockerExcludesConcurrentHoldersOfOneKey' -v -count=3` — plain, **not** `-race`, because `releaseLockStub.calls` is an unguarded int and the detector would report that instead of the point being made — and confirm:

```
--- FAIL: TestDBOperationLockerExcludesConcurrentHoldersOfOneKey (0.01s)
    operation_locker_test.go:NNN: 7 critical-section overlaps: the operation lock does not exclude
FAIL
```

- [ ] Restore `factory := dblock.NewTestLockFactory(newInMemoryLockDB())` and re-run `-count=3`; confirm three consecutive passes.
- [ ] Run under the race detector:

```
go test -race ./agent/resourcecapture/ -run 'TestDBOperationLocker' -count=10
```

Expect `ok` with no `WARNING: DATA RACE`.

- [ ] Run `gofmt -l agent/resourcecapture/operation_locker_test.go` (no output) and `go test ./agent/resourcecapture/ -count=1`.
- [ ] Commit `test(resourcecapture): prove operation lock mutual exclusion`.

---

## Acceptance check (run after the last task lands)

The spec's WS6 acceptance criteria, each mapped to the thing that discharges it.

- [ ] *"The collision table and identity-boundary tests exist and are cited from the archive package docs."* — `rg -n 'TestCanonicalCaptureDistinguishesNearMissTrees|TestCanonicalCaptureIdentityBoundaryIsExecBitOnly' agent/snapshot/archive.go` prints two doc-comment lines.
- [ ] *"Round trip covers disk."* — `go test ./agent/snapshot/ -run 'TestCanonicalCaptureRoundTripsThroughMaterializedDisk' -v -count=1` passes, and the test materializes to a real directory via the real extraction path.
- [ ] *"`make test-fuzz` is green and wired."* — `make test-fuzz` passes; `rg -n 'test-fuzz' Makefile` shows it on `.PHONY`, as its own target, and in `test-all`. CI wiring is plan 01's step, by the cross-plan contract above.
- [ ] *"The capture determinism question has a test-backed answer."* — `agent/resourcecapture/capture.go`'s package doc states either determinism or memoization, and `TestCaptureRetriesTheExactSucceededGenerationWhenItsOutputExpired` asserts the matching property. The commit body records the observed digests.
- [ ] *"Concurrent seal converges without corruption under `-race`."* — `go test -race ./agent/snapshot/ ./agent/resourcecapture/ -count=1` is clean.
- [ ] Exposure verification: `rg -n 'VerifyExposedPaths' agent/snapshot/` shows the helper, its seal-gate caller, and its tests; `rg -n 'VerifyExposedPaths|RevalidateSealed' agent/snapshot/validator.go atc/exec/load_snapshot_step.go` shows no read path calls it.
- [ ] Whole-tier regression: `go test ./agent/... -count=1` is green, and `make test-quick` is green on a machine with PostgreSQL.
