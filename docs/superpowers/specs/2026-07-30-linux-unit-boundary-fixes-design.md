# Linux Unit Boundary Fixes

**Date:** 2026-07-30
**Status:** Approved for implementation
**Approval:** After reviewing the two failures from
`main/jetbridge/unit-tests/905`, the owner asked for the production fixes on
2026-07-30.

## Objective

Repair the two Linux-only failures observed at
`fabf9b83e56cba10f97e743875920b538b3a1a2c` without weakening either security
boundary:

1. `forge-pr` must reject a `record.json` replaced while the resource writes
   its protocol response, even when the filesystem reuses the original inode
   number.
2. The DirectGit bearer-authentication test must inspect the private Git
   configuration's permissions correctly on both GNU/Linux and macOS.

## Decision 1: Retain the published record's file identity

`forge-pr` currently remembers `os.FileInfo` for its private record, links that
inode into place as `record.json`, removes the private link, and closes the
writer. Its final check compares the cached metadata with a fresh `Lstat`
through `os.SameFile`.

On Unix, `os.SameFile` compares device and inode identifiers. After
`record.json` is unlinked, Linux may immediately reuse that inode number for a
replacement. The cached metadata can therefore accept a different file (an
ABA transition), and rollback can then delete a file that this invocation did
not create.

After syncing and closing the writer, `publishRecord` will open the private
record read-only through the destination's retained `os.Root`. It will verify
that descriptor against the metadata captured from the writer, publish the
hard link, remove the private link, and transfer the descriptor to the
`record.json` ownership entry.

The descriptor remains open until `In` either:

- completes both destination checks, including the check after writing the
  resource protocol result; or
- finishes fail-closed rollback.

An open descriptor pins the original inode, so a replacement cannot acquire
the same live identity. Final verification must compare all three views:

- metadata captured when the resource created the file;
- metadata from the retained descriptor; and
- metadata from `record.json` beneath the retained destination root.

Rollback must also use the retained descriptor when one is available. If the
path no longer identifies the retained file, rollback leaves the replacement
untouched and reports that it refused to remove a changed owned file.

The private hard link is still removed before the protocol response. Successful
output membership remains exactly:

- `record.json`
- `source-repository`
- `target-repository`

## Decision 2: Prefer the native `stat` form for the current platform

The DirectGit test currently probes BSD `stat -f` first. GNU `stat` accepts
`-f`, but gives it a different meaning: it prints filesystem information and
returns success. The BSD probe therefore suppresses the GNU permission probe
and stores a multiline filesystem report instead of `600`.

The test script will probe GNU `stat -c '%a'` first and fall back to BSD
`stat -f '%Lp'`. GNU/Linux then uses its permission format directly; macOS
rejects the GNU form and uses the BSD form. This changes no production
credential behavior: the production runner already creates and enforces the
private file as mode `0600`.

## Rejected alternatives

- **Retry, sleep, or filesystem churn in the test:** allocator timing would
  remain nondeterministic and the production ABA bug would remain.
- **Re-read only the record contents:** this cannot prove ownership and could
  still let rollback delete a foreign replacement with identical bytes.
- **Keep only cached `os.FileInfo`:** that is the representation that failed
  under inode reuse.
- **Keep the private hard link through the protocol response:** it also pins
  the inode, but temporarily exposes an implementation-only top-level member
  and complicates the exact destination-membership invariant.
- **Infer the host from `uname`:** capability probing is smaller and works for
  GNU and BSD userlands regardless of kernel naming.

## Verification

- Re-run the exact two failing tests.
- Re-run the complete `agent/pullrequest/resource` and
  `agent/publisher/directgit` package suites.
- Run the repository's standard unit-test target.
- Validate the repaired commit on the Linux `jetbridge` unit-test pipeline,
  because macOS/APFS does not reproduce the inode-reuse transition reliably.
