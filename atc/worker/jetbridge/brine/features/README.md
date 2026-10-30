# Feature files and tags

Tags group scenarios by family (for example `@CO-`, `@RF-` and `@PW-`).
Existing numbered tags are historical labels, not a requirement to cite an
external specification. See [ADR-0010](../../../../../docs/adr/0010-hangar-keeps-one-mechanism-per-guarantee.md).

The vocabulary guards require every step definition to be used, every scenario
step to resolve, and no step line to match two definitions. The active-corpus
guard also requires runnable features in the local and live tiers.

## Removed row 2026-10-30: the "s2" artifact-handoff encoding

gzip is the one stream codec (`atc/compression` keeps only gzip; the
`--streaming-artifacts-compression` choice is gone), so the `s2` row of
live/artifact-handoff.feature's "Artifact handoff delivers exact files or
reports the transfer failure" outline has no codec to exercise. One expanded
live case leaves; the live floor moves 134 -> 133. The `raw` and `gzip` rows
still show the handoff with and without the codec, and the fault rows are
unchanged.

## Rewritten scenario 2026-10-30: the file-valued alias in daemon-containment

The artifact daemon no longer serves a file at an artifact key (the "legacy
tar file" branch of GET), takes no `PUT /artifacts/` and sweeps no flat files
under `artifacts/`: an artifact is a directory, bytes come in through
`PUT /stream-in/` alone. The daemon-containment scenario "An alias to a path
outside the storage root is refused, and never reaches the alias store"
registered `vol-file` at a stored tar file and read its bytes back, which
exercised the cut branch. It is rewritten, not removed: the non-vacuous
alias is now a directory (`vol-dir`), a file-valued alias is asserted refused
with "is not a directory" and absent from the alias store, and the served
answer is read as a tar containing the file's bytes. The step
"the answer's bytes are" went with the byte-equal read; "the answer contains"
replaces it. The per-artifact verb sweep is GET, HEAD and DELETE. Scenario
count unchanged; the local floor stays 919.

## Removed row 2026-10-30: the "policy" authorization boundary

The OPA policy checker (`atc/policy`, `atc/api/policychecker`, the
`--opa-url` flags) is gone, so the `policy` row of mcp-auth.feature's
"MCP preserves the API's authorization boundaries" outline has nothing to
pin. One expanded case leaves; the floor moves 920 -> 919. The foreign-team
and custom-role rows still show MCP denying what the API denies.

## Removed families 2026-10-09 and what still covers them

C11 removes nine feature files: GC caches (19 expanded cases), containers (10),
pipelines (25), reclamation (11), build scheduling (16), resource checking (23),
job admission (13), pipeline retention (9), and local step execution (11).
The parser counts 137 removed cases (101 scenario/outline declarations), leaving
921 local cases from 1058 and all 134 live cases. The local manifest floor is
recounted to 921; future additions can create headroom above that floor.

Missing Go specs were selectively restored from `4e6a2de5a5` and `e205c4984c`,
preserving the existing scope-aware fan-out test. Core remains a product
dependency: `atc/gc`, `atc/lidar`, `atc/scheduler`, `atc/engine`, `atc/exec` and
`atc/db` retain its coverage, including pipeline run reclamation and run-log
retention. Two mixed-row PostgreSQL GC tests preserve referenced-config survival
beside orphan collection, and check-session FK refusal of the entire sweep.

Accepted losses are integrated native resolver private-image authentication and
recovery after actual scope deletion (unit coverage remains in
`atc/imageresolver` and `atc/lidar`), the real cached-artifact byte/provenance
check, and real Kubernetes connection-refusal classification. Shared live
Git, time-resource, retry/abort, hooks and partial-put vocabulary remains in
`steps/exec_harness.go`. This is not equivalent coverage restoration: the removed
scenarios exercised the runtime and could contribute to its package coverage.
The 50% gate is unchanged; its post-cut result needs the Linux local/live tiers.

`DISPOSITION-gc-lidar.md` was already archived outside this repository; this cut
does not delete or rewrite the archive. No chart, flags, schema or ADR changes
are needed for this test-only removal.
