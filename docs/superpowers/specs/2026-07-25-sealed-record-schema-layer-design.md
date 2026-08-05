# Sealed Record Schema Layer

- **Date:** 2026-07-25
- **Status:** Approved for implementation
- **Base:** `3e16271c28` — the six sealed-record contracts integrated onto the v3 platform
- **Supersedes nothing.** Extends the prototype-informed sealed record contracts
  and the record section of
  [Agentic Workflows as Functions](2026-07-21-agentic-workflows-as-functions-design.md).

## Why

The sealed-record layer landed the physical and trust properties correctly: one
sealed tree per value, status outside identity, producer claims carrying no
authority, subjects bound to exposed inputs, selection resolving to an existing
reference. It did not land the semantic layer, and one choice actively raises
the cost of ever landing it: the wire field named `schema` holds the digest of
an opaque one-line revision stamp, and all structure lives in hand-written Go.

That forecloses everything downstream of schema-as-data — field-level indexing,
generic rendering, entity-set trackers, and schema-derived authoring tools — and
makes their arrival a rewrite rather than a switch-flip.

This design makes the schema real, and lands three things that get strictly more
expensive the longer they wait.

## The prerequisite: accepted schema digests

**Nothing in this design may change a frozen descriptor digest until this lands.**

`Record.validateEnvelopeShape` requires exact equality between the record's
`schema` field and the single digest the server computes today
(`agent/snapshot/contracts/record.go:140-142`). Stored records are re-decoded
through that same path on read:

- `agent/publisher/gateway.go:961`
- `agent/projection/review.go:116`
- `agent/projection/repository_change.go:372`

So a descriptor bump is not a versioning event. It retroactively invalidates
every record already sealed under the previous digest, and the failure surfaces
as `ErrCorruptSnapshot` on read rather than at seal time.

**Decision.** Replace the single-digest lookup with an ordered digest history,
newest first, per type.

- Writes always pin index 0. `NewRecord` keeps calling `SchemaDigestFor`, which
  keeps returning the newest digest, so newly authored records are unambiguous.
- Validation accepts any digest in the type's history.
- A digest is only ever appended. Removing one is a data-loss change and is
  forbidden.

This lands **alone**, before any other task in this design.

## Seal-time admission and read-time revalidation are two gates

**Amendment, 2026-07-25.** The original text above said only "validation accepts any
digest in the type's history," without distinguishing when. That was wrong, and
adversarial review caught two separate defects flowing from the same omission.
Today one predicate — `Record.validateEnvelopeShape` — serves both gates. It must not.

- **Seal-time admission** judges an agent-authored candidate. The producer has
  authority over nothing. It must pin the **current** descriptor digest (index 0)
  exactly, and every fact the validator relies on must come from a server-side
  declaration. Accepting a superseded digest here would let a producer choose which
  contract identity its own output advertises, which breaks *a producer's claims
  never create authority* — and would falsify the promise the runner prompt already
  makes to the agent, that the values it was handed are verified again at seal time.

- **Read-time revalidation** judges bytes the platform already sealed and certified.
  It must accept **any** accepted digest for the type, and it must rely on the sealed
  record's own contents — the seal already certified them — rather than on live
  workflow declarations that no longer exist when a reader loads the record.

The corollary is a rule, not a nicety: **a validator that cannot re-validate a stored
record is a defect**, because it makes the corpus unreadable. Any validation input
that exists only in process memory at seal time must therefore either be persisted, or
be reconstructible from the sealed bytes whose correctness the seal already
established. Concretely: selection candidacy is declared server-side at seal time, and
at read time is taken from the sealed subject roles that seal-time validation
certified.

## Fork 1: the `schema` field becomes a declared schema

**Decision: declared schema as data, with a generic validator enforcing a core
subset.** Per-type Go validators shrink to genuinely semantic cross-field rules.

### Presence is derived, never authored

The single most dangerous failure mode in this design is a declared schema that
is *stricter* than the Go validator it describes. Because stored records are
re-validated on read, over-declaring `required` on a field the validator
tolerates as absent retroactively corrupts the corpus.

Concrete instance found in review: `review/v1` accepts a missing `blocking`
bool and a null `findings` slice. A hand-authored declaration calling either
`required` would invalidate already-sealed reviews.

**Rule.** Required-versus-optional is **mechanically derived** from the
validator's actual tolerance, never from author judgment. A field is `required`
only if the validator rejects its absence. The derivation is asserted by test,
per field, per type. Author judgment is admissible for *documentation* fields
(descriptions, display hints) and nowhere else.

### The enforced core

The generic validator enforces exactly:

- scalars: `string`, `int`, `float`, `bool`, `identifier`, `markdown`,
  `timestamp`, `duration`
- `enum` with a closed value set
- `score` with scale, direction, and bounds
- `blob` — a content reference with a declared media type and a digest
- `entity-set` — an array with a declared `id_field`, unique and
  lexicographically sorted by that id
- `array`, `object`
- `anchor` — resolving through a declared subject
- `record-ref` — see Fork 2

Everything else stays in Go: cross-field implications, derived conclusion
precedence, git apply and tree verification, cross-variant stability.

### Canonical serialization

The schema document's digest is frozen, so its serialization must be
byte-stable forever: recursive key sort, `json.Number` literals preserved (the
precedent is `rawJSONEqual` at `agent/snapshot/types.go:1428-1443`, which uses
`decoder.UseNumber()`), no insignificant whitespace, and explicit length
framing.

### Parity gate

The declared schema and the Go validator are two descriptions of one truth. A
CI test drives generated instances through both and fails on any divergence.
This is a gate, not a shipping mode — there is no "advisory" runtime path.

### Field-path grammar — one spelling

Six incompatible field-path grammars were proposed. **Freeze one:**
`/`-separated segments, segment charset matching the existing identifier
pattern (`agent/snapshot/contracts/record.go:191`).

- `*` is legal **only** in a declaration path (`body/findings/*/severity`) and
  never in a stored address.
- Stable element ids are legal **only** in a stored address
  (`body/findings/f-001`) and never in a declaration.

## Fork 2: sub-record addressing

**Decision: a structured reference, not a string fragment.**
`StableSnapshotRef` and `Subject.StableRef()` already exist and are dead code
(`record.go:31`, `record.go:55` — the only mentions in the tree); they are the
foundation.

Rationale, beyond the judgment: a digest is `sha256:` + 64 hex (contains `:`)
and a `TypeRef` contains exactly one mandatory `/`, so a single string carrying
type, digest, and path overloads `/` across three syntactic roles and `:` across
two.

### Rules

- A ref addresses **either** a declared `subject` **or** an explicit record
  identity, never both. This XOR is load-bearing twice over: for meaning, and
  because it is the only reason an element digest is a pure function of the
  sealed bytes.
- Refs sort **field-wise**. No display string is ever a stored sort key —
  that would freeze the display grammar as hard as the wire grammar.
- Refs resolving through a `subject` copy **zero** digests. Requiring an
  explicit `record:{type,digest}` on every ref would make a 40-verdict record
  hand-copy the same 64 hex characters 41 times, each copy a seal-failure
  trigger. The subject already pins type and digest against the exposed input
  (`record.go:111-124`).
- A single-direction display projection may exist for UI and prose. There is no
  parser for it, and a test asserts none is added.
- `resolution: resolved | pinned` is a declared field. It is the only thing that
  tells a reader whether a stored address was existence-checked.
- Declared constraints use one spelling: a **singular** record type, not a list.
- Fragment depth is at least 2. Depth-2 addressing is already legal in-tree via
  `CandidateAssessment.ID` plus `NamedScore.ID` (`selection.go:19`, `:26`).
- Caps (depth, byte length) are **not** frozen. Raising a cap invalidates
  nothing.

### Element digests

An element digest is a pure function of the sealed bytes, because
`subject.Digest` lives inside `record.json` and the store is content-addressed.
File-region anchor hashes are **not** such a function, because the body pins no
hash of the region. These two cases must be documented distinctly or a reviewer
will re-litigate the difference.

Element digest normalization is recursive key sort with preserved
`json.Number` literals and length framing. `json.Compact` alone is
insufficient: `record.json` is agent-authored, so key order and number spelling
are model-chosen and unstable across re-runs — which defeats the exact purpose
the digest exists for.

### Intrinsic metadata carries no new grammar

`IntrinsicMetadata` is compared for agreement per `(type_name, type_version,
digest)` in five places, including SQL at
`atc/db/agent_snapshots_factory.go:498-502`. It therefore has **no
version-bump path**: sealed bytes can be versioned by `TypeRef`, intrinsic
metadata cannot. No dotted field-path strings may be introduced there.

## Epistemic status

**Decision.** Four values — `platform`, `derived`, `constrained`, `asserted` —
declared **outside** any canonical digest, keyed by `(type, revision)`.

Placement is the whole point: a mislabelled epistemic status must be
correctable without a digest churn. Anchor work will later move several types'
anchor fields from `asserted` to `derived`, and if the vocabulary lived inside
the digested subtree that would force a second unavoidable bump.

The declared schema must therefore **not** carry an epistemic field of its own;
it references this table.

## Exposure and materialization lineage

Capture, at mount time, the materialization mode and the exact mounted path set
with per-path digests, persisted outside sealed bytes.

`ValidationContext` today is input identity only — a `map[name]SnapshotRef` plus
an opener (`agent/snapshot/validator.go:38-41`). "Did the judge actually look at
the diff?" is unanswerable.

Mode rules:

- **Full materialization is the default for agentic steps.** An agent could read
  anything, so lineage records the whole tree. This is honest, not lazy.
- **Static-selector record materialization** is for deterministic steps.
- **Dynamic, agent-driven partial mounting is prohibited**, because lineage
  cannot be known at admission.

## Selection: candidate ports

Confirmed defect. `selection.go:118-120` requires subjects to cover every
exposed input, and `selection.go:43-45` requires every subject to be a
candidate. Together a judge step may receive **only** candidates — it cannot be
given the base repository, a prior review, or a rubric, even though the target
model describes exactly that.

**Decision.** Candidate ports are declared in the workflow function's port
declarations. Inferring candidacy from producer-written subject roles is
rejected outright: producer claims must never create authority.

The integrity property is unchanged and non-negotiable: **a judge may only
select from what it was exposed to.**

## Deferred, with reasons

**Anchor content hashes.** Blocked on a genuine ambiguity, not on effort:
nothing in the code defines whether `Locator.Path` is relative to the subject
archive root or to the pod mount path, and no code converts between the two
namespaces. Only archive-relative is verifiable at seal time, so that is what
will be frozen — but the authoring surface must make it unambiguous rather than
leaving an agent to guess, and that is a change to how records are authored, not
a validator tweak.

Also noted for whoever implements it: there is no byte-range read at any layer,
so hashing anchors means streaming each subject archive in full, exactly once,
with all of that subject's anchors batched into that single pass. And anchors
have no id (`record.go:247-250`), so an anchor-resolution record must address
the **owning entity** plus the locator's field values — never the evidence
array position.

**Fragment refs' first consumer.** `review-feedback/v1` does not exist, and
`agent_feedback` already does: `finding_id TEXT NOT NULL` with a unique index on
`(review_snapshot_id, review_team_id, finding_id, reviewer)`, plus
`finding_snapshot` — a JSONB *copy* of the finding that nothing verifies against
its source. Fragment refs should land converting that existing shape, not ahead
of it. Freezing an address grammar against two hypothetical consumers is how a
vocabulary gets designed to fit what exists rather than what is right.

## Invariants this design must not break

- Production occurrence — run, producer, model, timing, attempt — never enters
  sealed bytes.
- Mutable status never enters content identity.
- Server-derived data stays out of the record body
  (`agent/snapshot/validator.go:12-24`;
  `agent/snapshot/contracts/repository_change.go:152-161`).
- A producer's claims never create authority.
- Anchors resolve through a declared subject and never embed an independent
  snapshot reference.
- `agent/schema` is a standalone module shared with `ci-agent` and must never
  import the main module.

## One coordinated descriptor bump

A naive task-by-task adoption produces three distinct `record.json` byte layouts
for `review`, `diagnosis`, `validation`, and `measurements`. All descriptor
changes in this design land as **one** coordinated bump, after
`AcceptedSchemaDigests`.

## Migration numbering

Head is `1773106126`. The next number is `1773106127`, assigned to exposure
lineage. Any new migration must bump both pinned constants in lockstep:

- `atc/db/migration/legacy_upgrade_test.go:37` — `jetbridgeHeadMigration`
- `docs/migration/migrate-preflight.sh:38` — `JETBRIDGE_VERSION`

## Open question deliberately left open

`Subject.Input` is a workflow port name inside sealed bytes
(`record.go:69-71` is `TrimSpace`-only; `record.go:111-115` checks it against
the live `ValidationContext`). One reading is that this is a defect — renaming a
port yields different sealed bytes for the same semantic value. The other is
that it is cheap insurance binding the record to the shape it was authored
against. Both are defensible; this design does not change it, and does not
pretend to have settled it.
