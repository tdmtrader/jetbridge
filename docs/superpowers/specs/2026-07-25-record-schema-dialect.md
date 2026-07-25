# The Declared-Schema Dialect for Sealed Records

- **Date:** 2026-07-25
- **Status:** Draft for implementation
- **Governed by:** [Sealed Record Schema Layer](2026-07-25-sealed-record-schema-layer-design.md),
  Fork 1. Where this document and that one disagree, that one wins.
- **Base:** `9b376febc6` — the digest-history mechanism (wave A) landed.

This document defines the dialect a record type's schema is written in, and the
rules under which a schema document becomes that type's next descriptor revision.
It does not define any type's schema; the six documents under
`agent/snapshot/contracts/schemas/` do that.

## 1. What a schema document is, and what it is not

A schema document is **one type's contract identity, expressed as data**. Its
canonical serialization is the descriptor string the digest-history mechanism
hashes, so a schema document is not documentation about the contract — it *is*
the contract, in the same sense the revision-1 one-line stamp was.

It is deliberately not four other things:

- **Not the whole validator.** The generic validator enforces the core in §6.
  Every semantic rule beyond that stays in Go, and each document lists the ones
  that apply to it in `go_only_rules` so the declaration cannot be mistaken for
  the contract (§9).
- **Not an epistemic table.** A schema document carries **no epistemic field of
  any kind**. Epistemic status is keyed by `(type, revision)` in
  `agent/snapshot/contracts/epistemic_declarations.go`, outside every canonical
  digest, precisely so a mislabelled status is a one-line edit and not a
  descriptor bump.
- **Not a record of occurrence.** No run, producer, model, timing, attempt,
  exposure, or status vocabulary appears in a schema document, for the same
  reason none of it appears in sealed bytes.
- **Not a source-code index.** No `file:line` citations appear inside a schema
  document. Line numbers rot within days, and a stale citation inside a frozen
  digest cannot be corrected without a revision bump. Citations live in this
  document, in the derivation test, and in review reports.

## 2. Where the documents live

```
agent/snapshot/contracts/schemas/<name>.<version>.rev<N>.json
```

for example `agent/snapshot/contracts/schemas/repository-change.v1.rev2.json`.

Four constraints pick this location, and together they leave no other choice:

1. **`go:embed` cannot escape its package directory.** The loader lives in
   package `contracts` beside `record_schema.go`, so `//go:embed schemas/*.json`
   is legal only for a directory beneath `agent/snapshot/contracts/`.
2. **The standalone-module boundary must not move.** `agent/schema` is a separate
   module shared with `ci-agent` and must never import the main module.
   `agent/snapshot/contracts` is already in the main module, so putting the
   documents there touches that boundary not at all. Putting them in
   `agent/schema` would drag the record contracts across the boundary in the
   wrong direction.
3. **The descriptor table and its inputs must not be able to drift apart.**
   `recordSchemaHistories` and the documents it derives descriptors from are one
   commit's worth of change or none.
4. **Revisions are append-only files.** The filename carries the revision, so a
   revision-3 document is a *new file* and the revision-2 file is never edited.
   Editing a file whose bytes are already a frozen descriptor is the same defect
   as editing a superseded descriptor string.

The document on disk is **human-readable** (two-space indent, keys in reading
order, comments impossible in JSON so none). The bytes that get hashed are the
canonical serialization computed at load time (§10), never the file bytes. This
is the only arrangement in which the auditability claim — diff revision N against
N+1 — actually pays off, because a canonicalized one-line file is undiffable.

### 2.1 The dialect version lives inside the document

The filename carries the type, the version and the revision. It does not carry the
**dialect** version, and the dialect version cannot live in the filename or in the
loader alone, because it has to be inside the bytes the digest covers:

```json
"dialect": "record-schema-dialect/1"
```

exactly that spelling, as the **first declaration** of every document (§3).

The reason is the composite kinds. A `score`, `blob` or `anchor` declaration
expands to a fixed leaf subtree — six leaves, three leaves, seven leaves — that
appears **nowhere in the document's own bytes**, and which leaves those are, what
presence each gets, and how `forbidden` is derived (§6.10, §6.15, §5.3) are
decided by the dialect, not by the document. A frozen descriptor whose bytes do
not say which expansion applied names a subtree a later reader cannot reconstruct:
add a leaf to `anchor` in some future revision of *this* document and every
already-frozen descriptor silently starts meaning something else.

So the marker is versioned the same way §10.2 versions the canonicalization, and
for the same reason: a new expansion, a new kind, or any change to a derived
presence is `record-schema-dialect/2` and is visibly a different thing rather than
a silent reinterpretation of frozen bytes. The loader rejects a **missing** marker
(there is no default — "absent means dialect 1" is exactly the hidden rule §3
forbids) and rejects a marker naming a version it does not implement, refusing to
guess rather than reading the document under rules that may not be its own. Both
are package-initialisation panics, not read-time reports.

This is also why the marker had to be added before the bump rather than after: a
top-level key added later changes every descriptor, so "add it in revision 3"
means "bump every type twice".

## 3. Document shape

```json
{
  "dialect":       "record-schema-dialect/1",
  "contract":      "<TypeRef>",
  "envelope":      "record/v1",
  "revision":      <int >= 2>,
  "supersedes":    <int, exactly revision - 1>,
  "description":   "<one or two sentences>",
  "subject_shape": { ... },
  "fields":        { "<declaration path>": { ... } },
  "go_only_rules": [ { "id": "<slug>", "rule": "<one sentence>" } ]
}
```

Every key is required. There is no optional top-level key and no default, because
"absent means X" is exactly the kind of rule that hides a change inside a frozen
digest.

**`dialect`** is first, and is the version of *this* document whose rules the rest
of the file is to be read under — the composite-kind expansions above all, since
they are dialect-fixed subtrees that the file itself never spells out (§2.1).
Reading order puts it first for the same reason a file format puts its magic number
first: nothing below it can be interpreted until it is known. Canonical key sorting
means its position in the file has no effect on the descriptor bytes, so "first" is
a convention for readers and the loader's check is what makes it binding.

**Header and revision linkage.** `contract` is the type's `TypeRef` and must equal
the key this document is registered under in `recordSchemaHistories`. `envelope`
names the envelope *shape* — `record/v1` — whose fixed field set is §8; the
envelope also pins the wire constant `record_version` to `1.0.0`. `revision` is
this document's place in the type's history and `supersedes` is `revision - 1`,
which makes the linkage readable from the descriptor bytes alone rather than only
from the Go table. The loader cross-checks both against
`recordSchemaHistories` and panics at package initialisation on disagreement, so
the redundancy cannot go stale. `revision` is at least 2: revision 1 is the
pre-dialect one-line stamp and stays immutable and unparsed forever.

**`fields`** is a map from **declaration path** (§4) to one field declaration. It
is a map and not a list so that canonical key sorting makes ordering
non-authorial, and so a reader can look a path up the same way it looks the
epistemic table up. It declares the `body` subtree only — the envelope's own
fields are fixed by `envelope` (§8) and would otherwise be copy-pasted six times
with six chances to drift.

Containers are declared as well as leaves: `body` is an `object`, `body/findings`
is an `entity-set`, `body/findings/*/severity` is an `enum`. This is the one place
the schema and the epistemic table intentionally differ in granularity — the
epistemic table declares leaves only, because a container carries no claim, while
the schema must declare containers because their shape *is* the schema. §7 states
the resulting cross-table invariant, which is machine-checkable.

**`go_only_rules`** is inside the digest on purpose. It costs a revision bump to
edit prose, and buys the property that a Go semantic rule cannot be deleted
without either the descriptor changing or the document lying — and the parity gate
catches the lie. The array is ordered for reading and carries no precedence; `id`
values are unique within a document and are referenced from field declarations by
`go_rules`.

A document lists only the rules about its **own** declared fields and its
`subject_shape`. The Go rules that apply to the `record/v1` envelope in every type
belong to the envelope and are stated once, in §9, rather than copy-pasted into
six documents where they would drift.

## 4. The field-path grammar

Frozen exactly as the design froze it, and already implemented in
`agent/snapshot/contracts/field_path.go`:

- `/`-separated segments.
- A segment's charset is the record identifier charset,
  `^[A-Za-z0-9][A-Za-z0-9._:-]*$` (`record.go` `recordIdentifierPattern`).
- `*` is legal **only as a whole segment**, and **only in a declaration path**:
  `body/findings/*/severity`. A `*` never appears in a stored address.
- A stable element id is legal **only in a stored address**:
  `body/findings/f-001/severity`. It never appears in a declaration.
- Array **positions** appear in neither form. A position is not an identity;
  reordering an entity set would silently repoint every address built from one.

Two consequences the documents depend on:

- `*` in a declaration matches exactly one address segment — never zero, never
  several. Declaration and address describe the same tree at the same depth.
- An array with no `id_field` has declaration paths but **no addressable
  elements**. `body/checks/*/attempts/*/number` is a legal declaration; no legal
  stored address names one attempt. The same hole exists for anchors, which have
  no id at all, and is why an anchor-resolution record must address the owning
  entity plus the locator's field values.

## 5. Presence

### 5.1 The rule

Presence is **derived from what the Go validator actually tolerates**, never
authored. It is the single most dangerous value in a schema document: because
stored records are re-validated on read, declaring `required` on a field the
validator tolerates as absent retroactively corrupts every record that omitted it,
and the failure surfaces as `ErrCorruptSnapshot` on some later read rather than at
seal time.

Three values, and the predicate for each:

| presence | holds when |
|---|---|
| `required` | the validator rejects the field's **absent-image** in *every* otherwise-valid record |
| `forbidden` | the validator rejects *every* non-absent-image value in *every* otherwise-valid record |
| `optional` | neither — at least one otherwise-valid record has the absent-image and at least one has a real value |

"Otherwise-valid record" means: a record that the Go validator accepts once this
one field is restored, with every other field held fixed. Presence is therefore a
statement about a *quantified* set of records, which is why the derivation is a
test and not a judgment.

### 5.2 The absent-image equivalence

The record decoder cannot distinguish absence from the type's zero value, so
presence cannot be a statement about the JSON key. It is a statement about the
decoded value. The **absent-image** of a field is what the decoder leaves behind
when the key is missing:

| declared kind | Go type | absent-image |
|---|---|---|
| `string`, `markdown`, `identifier`, `duration`, `timestamp` | `string` | `""` |
| `int` | `int` | `0` |
| `float` | `float64` | `0` |
| `bool` | `bool` | `false` |
| `int`/`float` behind a pointer (`*int`, `*float64`) | pointer | `nil` |
| `array`, `entity-set` | slice | `nil` |
| `object`, `score`, `blob`, `anchor` | struct | the struct with every field at its own absent-image |

**JSON `null` is indistinguishable from absence for every field of every record
type.** `encoding/json` sets pointers and slices to `nil` for `null` and leaves
every other kind at its zero value without error, so `"summary": null` and a
missing `summary` decode identically and both fail the blank check;
`"minimum": null` and a missing `minimum` are both absent. Presence therefore
means *present and not null*, and the dialect has no separate nullability
concept.

### 5.2.1 `absent_image` — the one place the Go representation shows through

For `int`, `float` and `bool` the absent-image depends on whether the Go field is
a value or a pointer, and the difference is load-bearing rather than incidental:
an explicit `0` in a `*float64` field is *distinguishable* from absence and can be
rejected on its own, while an explicit `0` in a `float64` field cannot be
distinguished from absence at all. A metric with `"minimum": 0` and no `maximum` is
rejected; a metric with `"value": 0` is indistinguishable from one that omitted the
value.

So every `int`, `float` and `bool` declaration carries a required `absent_image`:

- `"zero"` — the field is a Go value. Absence, `null`, and an explicit `0`/`false`
  all decode to the same thing, and no rule can tell them apart.
- `"null"` — the field is a Go pointer. Absence and `null` decode to `nil`; an
  explicit `0` is a real, distinguishable value.

It is required rather than defaulted because a wrong default here silently changes
what the parity gate compares. No other kind needs it: every string-shaped field
in the six types is a plain `string`, every array is a plain slice, and the leaves
of `score` and `anchor` are fixed by §6.10 and §6.15 (`value` is `"zero"`;
`minimum`, `maximum`, `target`, `locator/start` and `locator/end` are `"null"`).

Two corollaries that decide many of the six documents:

- **Blank-is-absent.** For every string-shaped kind, `required` is the same
  statement as "the validator rejects blank". `body/summary` is `required`
  because `strings.TrimSpace(...) == ""` is rejected, even though JSON absence
  decodes to `""` rather than being detected as absence. Every string field the
  validators require, they require via a blank check (`requireStrings`,
  `ValidateIdentifier`, an inline `TrimSpace`), so there is no case where the two
  readings diverge.
- **Zero-is-absent.** For `int`, `float` and `bool`, `required` is the same
  statement as "the validator rejects the zero value". `body/hypotheses/*/rank`
  is `required` because `rank < 1` is rejected unconditionally.
  `body/findings/*/blocking` is `optional` because `false` is valid for an
  observation, low or medium finding. `body/hypotheses/*/confidence/value` is
  `optional` because `0` is a legal higher-is-better unit-interval score — an
  uncomfortable but true reading, and exactly the sort the mechanical rule exists
  to force.

### 5.3 `forbidden` is derived too

`forbidden` never comes from an author's sense that a field does not belong. It
arises only where a *pinning* elsewhere in the same declaration closes the domain
to the absent-image. In the six documents it arises in exactly one place, via the
`score` pinning rule in §6.10: a `score` whose `scale` is pinned to
`unit-interval` forbids `minimum` and `maximum`, and a `score` whose `direction`
is pinned to `higher-is-better` or `lower-is-better` forbids `target`. Both are
mechanical consequences, so `forbidden` is emitted by the loader rather than
written by hand.

Where presence depends on a *sibling value* rather than a pinning — `blocking`
under `severity`, `result_commit` under `representation`, every locator detail
under `locator/kind`, `attempts` under `status`, `explanation` under `conclusion`
— the declared presence is `optional` and the conditional rule is a
`go_only_rules` entry. **`optional` never means unconstrained**, and the
`go_rules` back-reference on the field declaration is how a reader is stopped
from concluding that it does.

### 5.4 What an author may decide

Author judgment is admissible for `label`, `description`, the *order* of an
`enum`'s `values` array, and per-value display hints. Nowhere else. In
particular, `markdown` versus `string` is a display hint — the two are validated
identically, so the choice can never change whether a record is accepted, and it
is therefore admissible. Nothing else in a schema document is a matter of taste,
and the derivation test asserts it per field, per type.

## 6. The enforced core

Every declaration has `kind` and `presence`, and may have `label` (short display
string), `description` (markdown prose) and `go_rules` (array of `id`s from this
document's `go_only_rules`). The kinds below are the complete set; there is no
extension point, because an extension point in a frozen digest is a second
vocabulary waiting to happen.

### 6.1 `string`
Any JSON string. `required` additionally means non-blank (§5.2).

### 6.2 `markdown`
Validated exactly as `string`. The kind is a rendering hint and nothing else.

### 6.3 `identifier`
A string matching `^[A-Za-z0-9][A-Za-z0-9._:-]*$`. Open set — a closed set is
`enum`. Note that a `sha256:`-prefixed digest and a lowercase git object id both
satisfy this charset, which is why the two digest-shaped envelope and body fields
that are not inside a `blob` are declared `identifier` with their real rule in
`go_only_rules`.

### 6.4 `int`
A JSON integer. Required `absent_image` (§5.2.1). Optional `minimum` and
`maximum`, inclusive, as integer literals. Bounds are declarable only because the
three places the six types bound an integer bound it unconditionally; conditional
bounds stay in Go.

### 6.5 `float`
A finite JSON number — `NaN` and both infinities are rejected. Required
`absent_image` (§5.2.1). **No declarable bounds**: every float bound in the six
types is either scale-dependent or supplied by the record itself, so a declared
constant would be a lie. The asymmetry with `int` is deliberate and is the reason
it is documented here rather than discovered.

### 6.6 `bool`
`true` or `false`. Required `absent_image` (§5.2.1), which for a plain Go `bool` is
`"zero"`, meaning `false`.

### 6.7 `timestamp`
An RFC 3339 timestamp with a mandatory offset. **No field of any of the six types
uses it at revision 2.** It is defined so that the first type that needs one does
not invent a second spelling.

### 6.8 `duration`
A string parseable by Go's `time.ParseDuration`. Optional `minimum` and `maximum`
as duration strings, **inclusive**, exactly as `int` bounds are inclusive (§6.4).

The inclusivity is stated rather than left to the reader because the one live use
sits exactly on its boundary: `validation/v1` declares
`"minimum": "0s"` on an attempt's duration, and the Go validator accepts a zero
duration (it rejects only a *negative* one). An exclusive reading would make the
declared schema reject a record the validator it claims to describe accepts —
over-declaration, the failure mode §5.1 exists to prevent, arriving through a
comparison operator. Both bounds and both kinds are pinned at the boundary by
`TestDeclaredIntAndDurationBoundsAreInclusiveAtTheBoundary`.

### 6.9 `enum`
```json
{ "kind": "enum", "presence": "required",
  "values": [ { "value": "observation", "label": "Observation" }, ... ] }
```
A closed value set. `values` is an array, not a map, because its **order is the
declared display order** and that order is often meaningful (severity, priority).
Order carries no validation meaning. `value` is unique within the array; `label`
and `description` are optional display hints.

### 6.10 `score`
```json
{ "kind": "score", "presence": "required", "scale": "unit-interval",
  "direction": "higher-is-better" }
```
Declares the six-leaf score subtree: `value`, `scale`, `direction`, `minimum`,
`maximum`, `target`. `scale` is either `"open"` or one of `unit-interval`,
`bounded`, `unbounded`; `direction` is either `"open"` or one of
`higher-is-better`, `lower-is-better`, `target`. `"open"` is written explicitly —
an omitted key meaning "open" would be a second spelling.

The presence of the six leaves is **derived from the pinning**, never declared:

| leaf | presence |
|---|---|
| `value` | `optional` always — the absent-image `0` is a legal value under some scale |
| `scale` | `required` always |
| `direction` | `required` always |
| `minimum`, `maximum` | `forbidden` if `scale` is pinned to `unit-interval` or `unbounded`; `required` if pinned to `bounded`; `optional` if `scale` is `"open"` |
| `target` | `forbidden` if `direction` is pinned to `higher-is-better` or `lower-is-better`; `required` if pinned to `target`; `optional` if `direction` is `"open"` |

The same Go `Score` type therefore gets two different declarations in the six
documents — pinned in `diagnosis/v1`, open in `selection/v1` — which is the
clearest available demonstration that a declaration describes a *site*, not a Go
type.

### 6.11 `blob`
```json
{ "kind": "blob", "presence": "required",
  "path_prefix": "content/", "media_types": "open" }
```
A content reference inside the same sealed tree: the three leaves `path`,
`digest` and `media_type`, all `required`. `path_prefix` is the mandatory prefix
of `path`; `media_types` is either `"open"` or a closed array of media types.
Verifying that the digest matches the bytes at `path` is a Go rule, not a schema
rule — the schema declares that the reference exists and is well-formed.

### 6.12 `entity-set`
```json
{ "kind": "entity-set", "presence": "optional", "id_field": "id" }
```
An array of objects with a declared `id_field`, whose values are identifiers,
**unique**, and **lexicographically ascending** by byte comparison of that id.
This is exactly `ValidateEntityIDs`. Element fields are declared as separate flat
entries at `<path>/*/<name>`.

`entity-set` is `array` plus `id_field` plus unique plus sorted-by-id; it is a
distinct kind rather than three flags because an addressable element set is a
different thing from a bag, and only the former can appear in a stored address.

No count bounds are declarable. Every cardinality rule in the six types depends on
a sibling `conclusion` or on the subject count, so a declared constant would
always be wrong.

### 6.13 `array`
```json
{ "kind": "array", "presence": "optional", "element": "anchor" }
```
`element` is one of the scalar kinds, or `object`, or `anchor`. Optional `unique`
and `sorted` booleans, which apply only when `element` is a scalar kind.

- An array whose `element` is a **scalar kind is itself a leaf**: its declaration
  path is the leaf path, because the grammar cannot address an element of it
  (scalars have no id, positions are not addresses).
- An array whose `element` is `object` or `anchor` implies leaves below it, and
  has no addressable elements unless it is an `entity-set`.

### 6.14 `object`
A JSON object whose fields are declared as flat entries below it. No additional
attributes.

### 6.15 `anchor`
```json
{ "kind": "anchor", "presence": "required" }
```
No attributes: the anchor subtree is fixed by the dialect, because "resolves
through a declared subject" is not a per-site choice. The fixed subtree, with its
derived presence:

| leaf | kind | presence |
|---|---|---|
| `subject` | `identifier` | `required` — must name a subject this record declares, and `""` never does |
| `locator` | `object` | `required` |
| `locator/kind` | `enum` `[file-lines, log-lines, json-pointer, byte-range, opaque]` | `required` |
| `locator/path` | `string` | `optional` |
| `locator/start` | `int` | `optional` |
| `locator/end` | `int` | `optional` |
| `locator/pointer` | `string` | `optional` |
| `locator/value` | `string` | `optional` |

The five detail leaves are `optional` because `locator/kind` decides which of them
must appear and forbids the rest — `file-lines` and `log-lines` require
`path`/`start`/`end`, `byte-range` requires the same three with different bounds,
`json-pointer` requires `pointer` alone, `opaque` requires `value` alone. That
selection is a Go rule, spelled `anchor-locator-kind-selects-which-fields-appear`,
present under exactly that id in every document that declares an anchor. The
spelling is normative here **because it is inside the frozen bytes**: a `go_rules`
back-reference is resolved by string equality against the same document's
`go_only_rules` ids, so a document whose two spellings disagree fails to load, and
a document whose id disagrees with this section is merely undiscoverable from the
prose — the reason to reconcile the two is that the id in the documents cannot be
corrected after the bump while this sentence can. An anchor's locator is **not**
verified to point at anything: anchor content hashes are deferred, so every locator
detail is a producer claim.

### 6.16 `record-ref`
```json
{ "kind": "record-ref", "presence": "optional",
  "record_type": "repository/v1", "resolution": "resolved" }
```
A structured reference to another record: `record_type` is a **singular** TypeRef,
never a list; `resolution` is `resolved` or `pinned` and is the only thing telling
a reader whether a stored address was existence-checked. **No field of any of the
six types declares a `record-ref` at revision 2**, and none should be added
speculatively — the first consumer is the conversion of the existing
`agent_feedback` shape, which is where the address grammar should be settled
against something real.

## 7. Composite kinds and the epistemic table

`score`, `blob` and `anchor` each expand to a fixed set of leaves. The expansion
is exactly the leaf set the epistemic table already declares at the same prefix,
and that correspondence is a machine-checkable invariant the loader asserts:

> For each `(type, revision)`, the set of leaf paths implied by the schema
> document equals the key set of `epistemicFieldStatuses` for that key, exactly —
> no missing entry, no extra one.

- a `score` at `P` implies `P/value`, `P/scale`, `P/direction`, `P/minimum`,
  `P/maximum`, `P/target` — the six paths `epistemic_declarations.go` lists for
  `body/hypotheses/*/confidence` and `body/candidates/*/scores/*/score`;
- a `blob` at `P` implies `P/path`, `P/digest`, `P/media_type` — the three paths
  listed for `body/payload`;
- an `anchor` at `P` implies `P/subject` and the six `P/locator/*` paths — exactly
  `anchorEpistemicFields(P)`.

The two tables answer different questions about the same leaves — the schema says
what shape a value has, the epistemic table says on whose authority it stands —
and the invariant is what stops one from silently growing a field the other does
not know about.

## 8. The `record/v1` envelope

Fixed by `"envelope": "record/v1"`, identical for all six types, and therefore
declared here once rather than in six documents. A change to any of it is a
`record/v2` envelope, not an edit.

| path | kind | presence | why |
|---|---|---|---|
| `record_version` | `enum` `["1.0.0"]` | `required` | compared for exact equality with the `RecordVersion` constant |
| `type` | `enum` `[<the document's contract>]` | `required` | compared for exact equality with the type the platform expected |
| `schema` | `identifier` | `required` | current descriptor digest at seal time, any accepted one at read time |
| `subjects` | `entity-set`, `id_field: id` | derived from `subject_shape.minimum`: `required` when it is at least 1, `optional` when it is 0 | ids unique and sorted |
| `subjects/*/id` | `identifier` | `required` | |
| `subjects/*/role` | `enum` `[primary, base, evidence, context, candidate, reference]` | `required` | |
| `subjects/*/input` | `string` | `required` | must be an exactly-named exposed input port |
| `subjects/*/type` | `string` | `required` | a canonical `TypeRef`; `/` puts it outside the identifier charset |
| `subjects/*/digest` | `identifier` | `required` | `sha256:` + 64 lowercase hex |
| `body` | `object` | `required` | declared per type |

Two envelope rules are generically enforced and are not per-document:

- **`subjects/*/input` is unique across subjects.** A duplicate input is an
  envelope error for every record type.
- **Unknown fields are rejected everywhere.** `record.json` is decoded with
  `DisallowUnknownFields` and must contain exactly one JSON value, so every
  schema document is closed at every level and no `additional_fields` attribute
  exists to get wrong.

### 8.1 `subject_shape`

The one envelope constraint a document declares, because it is the one that
genuinely differs per type.

```json
"subject_shape": {
  "minimum": 1,
  "maximum": 1,
  "roles": { "base": { "minimum": 1, "maximum": 1 } },
  "subject_type": "repository/v1",
  "uniform_subject_type": false,
  "ports": "any-exposed-input"
}
```

- `minimum` / `maximum` — cardinality of the whole subject set. `maximum` may be
  `null` for unbounded, spelled explicitly.
- `roles` — a map from allowed role to `{minimum, maximum}`, each explicitly
  `null` where unbounded. **A role absent from this map is forbidden**, and every
  allowed role must be listed even where it carries no bounds, because "omitted
  means allowed" would hide the difference between the four types that restrict
  roles and the one that does not.
- `subject_type` — a `TypeRef` every subject must have, or `null`.
- `uniform_subject_type` — every subject must share one common type, whatever it
  is. Orthogonal to `subject_type`: `selection/v1` requires uniformity without
  naming the type.
- `ports` — either `"any-exposed-input"` or `"candidate-ports-exactly"`. The
  latter means one subject per declared candidate port and no others, and is the
  integrity property that a judge may only select from what it was exposed to.
  **Where the candidate-port set comes from is a Go rule and cannot be a schema
  rule**: at seal time it is the server's compiled port declarations, and at read
  time it is the sealed candidate-role subjects that seal-time admission already
  certified, because a reader holds no declarations.

The presence of the envelope's `subjects` field is derived from
`subject_shape.minimum` and never declared separately. The consequence worth
flagging, because it is the least expected fact in the six documents:
**`measurements/v1` has `minimum: 0` and allows every role, so its `subjects` field
is `optional`.** Its body validator does not check the subject count or any role at
all; the only thing tying its subjects to its body is that an evidence anchor must
resolve to a declared subject, so a metrics record with no subjects and no evidence
is valid, and one with no subjects and any evidence is not. The other five types
have `minimum: 1` and therefore a `required` `subjects`.

## 9. What a schema deliberately cannot express

The generic validator enforces §6 and §8. Everything below is Go, and every
entry appears in the relevant document's `go_only_rules`. This inventory is the
answer to "is the declaration the contract?" — it is not, and the gap is
enumerated rather than implied.

**Every type (envelope).** Exact equality of `record_version`, `type` and
`schema` against server-supplied values; the two-gate schema-digest rule (current
digest at seal time, any accepted digest at read time); binding each subject's
`input`, `type` and `digest` to an exposed input; the 1 MiB `record.json` limit;
regular-file and no-trailing-JSON requirements; canonical relative POSIX paths.

**`review/v1`.** `changes-required` requires at least one blocking finding;
`accept` forbids any blocking finding; an `observation` finding may not block and
a `high` or `critical` finding must; a non-`observation` finding requires at least
one evidence anchor.

**`diagnosis/v1`.** `identified` and `suspected` require at least one hypothesis;
hypothesis ranks are unique and contiguous from 1; `identified` requires evidence
on the rank-1 hypothesis; every `addresses` entry must name a hypothesis this same
record declares.

**`validation/v1`.** `conclusion` must equal the conclusion derived from the
checks — `error` dominates, then `failed`, then any `skipped` or no checks yields
`incomplete`, else `passed`; a `skipped` check must have no attempts and every
other status requires at least one; an attempt's `number` must equal its position
plus one; a check's `status` must equal its final attempt's status.

**`repository-change/v1`.** `repository_id` must parse as a snapshot digest and
equal the base repository's; `base_sha` must be a full sha1 or sha256 object id,
its width fixes the object format, and it must equal the base repository's HEAD;
`result_tree` must be a valid object id in that format and must equal the tree the
platform recomputes; `patch` forbids `result_commit` while `git-tree` and
`git-bundle` require it; the payload must be a regular file under 10 GiB whose
sha256 equals `payload.digest`; the change must apply, verify, and descend from
`base_sha`; `changed_files` is derived into intrinsic metadata and is *not* a body
field.

**`selection/v1`.** Where candidacy comes from at each gate; one subject per
declared candidate port and no others; all candidate subjects share one snapshot
type; `candidates` must assess every candidate subject exactly once; each
`candidates/*/id` must be a declared candidate subject id; ranks unique within
`1..len(candidates)`; `selected` must occur exactly once among the candidates.

**`measurements/v1`.** `measured` and `partial` require at least one metric while
`not-applicable` requires none; `partial` and `not-applicable` require an
explanation; `minimum` and `maximum` must be declared together, be finite and
ordered, and must contain both `value` and `target`; `target` direction requires
a finite target while the other two directions forbid one.

**All anchors.** `locator/kind` selects which locator fields must appear and
forbids the rest; a JSON pointer must be a valid non-root pointer; an anchor's
`subject` must be a subject this record declares.

## 10. Canonical serialization

The schema document's digest is frozen forever, so its serialization must be
byte-stable forever. Two named layers, and confusing them is the failure this
naming exists to prevent.

### 10.1 `canonical-value(v)` — the payload

Recursively, producing UTF-8 bytes with no insignificant whitespace anywhere:

- **object** → `{` then members joined by `,` then `}`. A member is
  `canonical-value(key)` `:` `canonical-value(value)`. Members are ordered by
  **unsigned byte comparison of the key's UTF-8 encoding**. A duplicate key is an
  error; last-wins is not a rule, it is a bug surface.
- **array** → `[` then elements joined by `,` then `]`. Order is preserved, never
  sorted: array order is data.
- **string** → `"` then the contents then `"`, escaping **only**: `"` as `\"`,
  `\` as `\\`, U+0008 as `\b`, U+0009 as `\t`, U+000A as `\n`, U+000C as `\f`,
  U+000D as `\r`, and every other C0 control as `\u00xx` with **lowercase** hex.
  Nothing else is escaped — not `/`, not `<`, `>`, `&`, not U+2028/U+2029. Input
  that is not valid UTF-8, or that contains an unpaired surrogate, is an **error**
  and is never replaced with U+FFFD: replacement would map two distinct inputs to
  one canonical form, which is precisely what a content digest must not do.
- **number** → the source literal, **preserved byte for byte**. Decoding uses
  `decoder.UseNumber()` (the precedent is `rawJSONEqual`, `agent/snapshot/types.go`),
  so `1`, `1.0` and `1e0` are three distinct canonical forms with three distinct
  digests. Nothing is normalized, because every normalization of a decimal
  literal either loses information or requires a float round-trip that is not
  stable across implementations. Schema documents therefore write integers in
  plain form and contain no floats at revision 2.
- **`true` / `false` / `null`** → the bare literal.

Go's default encoder does **not** produce this: it HTML-escapes `<`, `>` and `&`,
escapes U+2028/U+2029, and re-renders numbers from `float64`. An implementation
must emit the bytes itself rather than reach for `json.Marshal`, and
`json.Compact` is likewise insufficient — `record.json` is agent-authored, so key
order and number spelling are model-chosen and unstable across re-runs, which is
the exact instability the digest exists to defeat.

**The decoder is the harder half, and `encoding/json` cannot be used as one
naively.** Two of the rules above are rules the standard library silently breaks in
the *default* API, so stating them is not enough — this is how the shipped
canonicalizer (`agent/snapshot/contracts/canonical_json.go`) enforces each, and an
implementation that departs from this has to explain how it does better:

- **Duplicate keys.** `json.Unmarshal` into a `map` or a struct applies last-wins
  and reports nothing, so a document with `"kind"` twice decodes cleanly and hashes
  as if only the second one existed. The canonicalizer therefore never decodes into
  a map: it drives `json.Decoder.Token()` and builds each object as an **ordered
  slice of members**, rejecting a key it has already seen. A map cannot express the
  violation, which is why the intermediate representation is a slice — the rule is
  enforced by the data structure and not only by a check.
- **Unpaired surrogates.** `json.Decoder` substitutes U+FFFD for an unpaired
  `\uD800`-range escape while decoding, so by the time a decoded string is in hand
  two distinct inputs have already collapsed into one and no later check can
  separate them. The rejection therefore runs over the **raw bytes, before
  decoding**, scanning string literals for a surrogate escape without a valid low
  partner. Invalid UTF-8 is rejected the same way, by `utf8.Valid` on the raw
  document.
- **Numbers** survive because the decoder is put in `UseNumber()` mode, which keeps
  the source literal, and **exactly one value** is required by demanding `io.EOF`
  from the next token after the top-level value.

The same token tree is what the loader walks for its "no epistemic key anywhere"
check, deliberately: a second, differently-decoded copy of the document could
disagree with the one that gets hashed.

### 10.2 `canonical-serialization(v)` — what gets hashed

```
"snapshot-canonical-json/1" 0x0A <decimal byte length of payload> 0x0A <payload>
```

The length is decimal ASCII with no leading zeros (`0` for empty). This is the
"explicit length framing" the design requires: it makes the encoding prefix-free,
so no concatenation of canonical values can be confused with a different one, and
the header makes the canonicalization algorithm itself versioned — a future
algorithm is `snapshot-canonical-json/2` and is visibly a different thing rather
than a silent re-digest.

**A descriptor string for revision ≥ 2 is exactly this serialization**, and its
digest is `sha256` over these bytes — which is what `schemaDescriptorDigest`
already computes. Revision-1 descriptors are the unframed one-line stamps and stay
that way forever; they are trivially distinguishable, since a framed serialization
begins with `s` and a revision-1 stamp with `{`.

### 10.3 Worked example

Source file (human-readable, what a reviewer diffs):

```json
{
  "dialect": "record-schema-dialect/1",
  "contract": "example/v1",
  "envelope": "record/v1",
  "revision": 2,
  "supersedes": 1,
  "description": "A minimal illustration.",
  "subject_shape": {
    "minimum": 1,
    "maximum": 1,
    "roles": { "primary": { "minimum": 1, "maximum": 1 } }
  },
  "fields": {
    "body": { "kind": "object", "presence": "required" },
    "body/note": { "kind": "markdown", "presence": "required" }
  },
  "go_only_rules": []
}
```

`canonical-value` — 371 bytes, one line, every object's keys byte-sorted
(`contract` < `description` < `dialect` < `envelope` < `fields` <
`go_only_rules` < `revision` < `subject_shape` < `supersedes`; note that `dialect`
sorts into the middle even though it is written first, and that
`subject_shape` < `supersedes` because `b` < `p`):

```
{"contract":"example/v1","description":"A minimal illustration.","dialect":"record-schema-dialect/1","envelope":"record/v1","fields":{"body":{"kind":"object","presence":"required"},"body/note":{"kind":"markdown","presence":"required"}},"go_only_rules":[],"revision":2,"subject_shape":{"maximum":1,"minimum":1,"roles":{"primary":{"maximum":1,"minimum":1}}},"supersedes":1}
```

`canonical-serialization` — 401 bytes; the first 30 are
`snapshot-canonical-json/1\n371\n` and the rest is the payload above:

```
73 6e 61 70 73 68 6f 74 2d 63 61 6e 6f 6e 69 63   snapshot-canonic
61 6c 2d 6a 73 6f 6e 2f 31 0a 33 37 31 0a 7b 22   al-json/1.371.{"
63 6f 6e 74 72 61 63 74 22 3a 22 65 78 61 6d 70   contract":"examp
...
```

Digest of the serialization, which is what a `recordSchemaHistories` entry would
carry:

```
sha256:c314139dfaf43d9ca2fbc9524a359f2443da50cfb3442708a08f76f6ee59c61b
```

For contrast, hashing the payload without the frame gives
`sha256:fc9930c990b71132260ae8cea99c63023a9702e1360bd8096533e53fb4fbb0b5`; the two
are not interchangeable and only the framed one is a descriptor.

Every number above is pinned by `TestCanonicalPayloadMatchesTheWorkedExample` and
`TestCanonicalSerializationFramesThePayloadWithItsLength`, which hold this exact
source verbatim, so this section cannot drift from the implementation without a
failing test.

The mechanism is verifiable against what already exists: revision 1 of `review/v1`
has descriptor `{"contract":"review/v1","envelope":"record/v1","revision":1}`, 60
bytes, and `sha256` over exactly those bytes is
`sha256:01d9f0644151274e8577875373f110b11f0ec34ff29ba12b143379744416fdb5` — the
digest the running validator reports as current for `review/v1`.

## 11. How a schema document becomes the next descriptor revision

`record_schema.go` itself needs no new type and no new field, and that much was
confirmed by reading it rather than assumed:

- `recordSchemaRevision.descriptor` is a plain `string` field, and
  `recordSchemaHistories` is an ordinary package-level `var` map literal, so a
  descriptor may be any Go expression yielding a string — including
  `mustCanonicalSchemaDescriptor(<a loaded document>)`. Nothing requires a literal.
- `buildRecordSchemaIndex` makes exactly three demands of descriptor bytes: the
  revision numbers form the contiguous range `1..N`, `current` is the newest, and
  no two revisions anywhere share descriptor bytes. It then computes
  `sha256` over the string. It assumes nothing about format, length or content.
- `AdmitForSeal` pins `current` and `RevalidateSealed` accepts any accepted
  digest, so appending revision 2 is safe in both directions the moment it lands:
  records already sealed under revision 1 keep validating, and no new record can
  still pin revision 1.

What that reading missed is that the *loader* had to change first, and it is worth
being precise about why, because the naive recipe does not compile.

### 11.1 Why the naive recipe is an initialisation cycle

Writing the bump as

```go
descriptor: mustCanonicalSchemaDescriptor(reviewV1Rev2),
```

makes `recordSchemaHistories` depend on the loaded documents. If loading in turn
depends on the histories — and it did, because it checked every record type has a
document and cross-checked each document's revision against
`CurrentSchemaRevisionFor` — Go's initialisation-order analysis, which is
transitive through called functions, reports:

```
initialization cycle for recordSchemaHistories
    recordSchemaHistories refers to recordSchemaDocuments
    recordSchemaDocuments refers to mustLoadSchemaDocuments
    mustLoadSchemaDocuments refers to loadSchemaDocuments
    loadSchemaDocuments refers to recordSchemaHistories
```

That is a compile error, not a subtlety: with a single-phase loader the bump
cannot be written at all. It is also *not* fixable by reordering declarations or by
moving work into an `init()` function — `init()` bodies run after every
package-level variable initialiser, so a histories entry could not read a value
computed there.

### 11.2 The fix: two load phases, already in the tree

`schema_document_load.go` now loads in two phases, and the split exists for exactly
this reason:

```go
// PHASE 1 — parse. References the embedded FS and nothing that depends on
// recordSchemaHistories, directly or through a called function.
var recordSchemaDocuments, recordSchemaDocumentRevisions = mustParseSchemaDocuments()

// PHASE 2 — register. Cross-checks each document against the digest history, the
// Go record types and the epistemic table. A blank package-level var, so Go
// schedules it after everything it transitively references — including the
// histories.
var _ = mustRegisterSchemaDocuments()
```

`parseSchemaDocuments` judges everything judgeable from a document alone (§3–§8
well-formedness, the dialect version, the field grammar, composite-kind
resolution). `registerSchemaDocuments` holds the three checks that need the rest of
the package: `validateSchemaDocumentRegistration` (revision links to the digest
history, contract is a record type, declaration matches the Go struct, leaf set
equals the epistemic table) and the completeness check that every record type has a
document.

The dependency graph after the bump is therefore acyclic:

```
recordSchemaDocuments ──▶ (embed FS, canonical JSON)
recordSchemaHistories ──▶ recordSchemaDocuments
acceptedRecordSchemaDigests ──▶ recordSchemaHistories
_ (registration) ──▶ recordSchemaHistories, acceptedRecordSchemaDigests,
                     recordSchemaDocuments, epistemicFieldStatuses
```

Both properties that matter are preserved: **every** failure is still a
package-initialisation panic rather than a read-time corruption report, and the
descriptor is still a plain string computed from reviewed in-tree bytes.

The alternative — keeping one load phase and making revision-2 descriptors lazy
behind a `sync.Once` — was rejected on three counts. It would change
`descriptor` from a `string` to a function or getter at every call site;
`buildRecordSchemaIndex`'s empty-descriptor check runs *before* any lazy value could
fire, so an unset descriptor would pass the very check written to catch it; and it
would move the failure for a malformed document from package initialisation to
first use, which is read time — the exact relocation this whole design exists to
prevent. Nothing is gained in exchange, since the two-phase split costs no
behaviour at all.

### 11.3 The bump, step by step

1. **Add the revision-2 epistemic entries.** For each of the six types, add a
   `{ref: <type>, revision: 2}` key to `epistemicFieldStatuses` in
   `epistemic_declarations.go`. At this bump they are the revision-1 entries'
   content unchanged, because the bump changes contract identity and not one
   validator rule — but they must exist as their own keys, because the table is
   keyed by `(type, revision)` and a stored record resolves its declaration through
   its own revision.

   This step is easy to forget and is gated:
   `TestEveryRecordTypeDeclaresEpistemicStatusForEveryRevision` requires an entry
   for every revision `1..current` of every type, so bumping without it fails with
   `"review/v1" revision 2 has no epistemic declaration`. Nothing about a missing
   entry is silent, but nothing about it is inferred either — there is deliberately
   no "inherit from the previous revision" rule, since silently inheriting a status
   is how a validator change ships with a stale claim about who vouched for a value.

2. **Move each `current` into `superseded`, verbatim.** The revision-1 one-line
   stamp is immutable. `buildRecordSchemaIndex`'s contiguity check turns any edit,
   gap or deletion into a package-initialisation panic rather than a read-time
   corruption report.

3. **Point `current` at the document.** For each type:

   ```go
   "review/v1": {
       current: recordSchemaRevision{
           revision:   2,
           descriptor: mustCanonicalSchemaDescriptor(recordSchemaDocuments["review/v1"]),
       },
       superseded: []recordSchemaRevision{{
           revision:   1,
           descriptor: `{"contract":"review/v1","envelope":"record/v1","revision":1}`,
       }},
   },
   ```

   Reading from `recordSchemaDocuments` — the **phase-1** map — is what keeps the
   graph acyclic. Reading from anything that validates registration would
   reintroduce the cycle, and the compiler says so immediately.

4. **Move the three pinned expectations.** Exactly three places freeze what the
   histories currently say, and each needs a specific edit — no others exist
   in-tree, which was checked by grepping for a revision-1 digest:

   - `record_schema_test.go`, `TestRecordSchemaDigestsArePinnedForEveryRecordContract`
     — pins what newly authored records carry. **Replace** each type's digest with
     its revision-2 digest.
   - `record_schema_history_test.go`, `frozenAcceptedSchemaDigests` — pins the
     complete accepted history, newest first. **Prepend** the revision-2 digest and
     keep revision 1's below it. Never replace: this list is what stored records are
     still allowed to carry.
   - `schema_document_internal_test.go`,
     `TestSchemaDocumentCanonicalSerializationIsStable` — already pins each
     document's canonical descriptor digest, and additionally asserts today that the
     descriptor is **not** in the digest history. **Invert** that last check to the
     positive form. The pinned digests themselves do not move: they are pinned
     before the bump precisely so the bump installs reviewed bytes rather than
     freshly computed ones.

   Following steps 1–3 for one type and running the suite reports exactly these
   three failures and nothing else, which is the check that this list is complete.

5. **Re-run the four gates in §12.** All six types bump together.

**All six bump together** is a requirement and not a convenience: a task-by-task
adoption would produce several distinct `record.json` byte layouts across the four
types that share body helpers.

### 11.4 The digest history does not protect against a validator tightening

Worth stating because the digest history invites the opposite belief. Appending a
descriptor revision is safe, and re-declaring presence more strictly than the
validator tolerates is caught by the derivation test. Neither of those covers the
third case: **tightening the Go validator itself retroactively invalidates stored
records, and no revision number prevents it.** `RevalidateSealed` runs *today's*
validator over yesterday's bytes, so if a body validator gains a requirement that
a stored record does not satisfy, that record stops being readable and reports as
corruption — exactly the failure the accepted-digest history was introduced to
eliminate, arriving by the other door.

The consequence for this dialect is concrete: a schema document is a faithful
description of the validator as it is, **including where the validator is looser
than anyone intended**. `measurements/v1` accepting a record with no subjects at
all is the clearest instance, and it is declared faithfully rather than tidied up,
because declaring the intent instead of the behaviour is precisely the
over-declaration that corrupts a corpus. If that tolerance is judged to be a
defect, closing it is a **data migration** — survey the corpus, decide what happens
to non-conforming records — and not a schema revision. Recording it truthfully is
what makes that decision possible; recording the intention would hide the need for
it.

### 11.5 What the bump buys

And this is where the auditability claim stops being a promise: because a
revision's descriptor bytes are derived from an in-tree, human-readable document,
`diff schemas/review.v1.rev2.json schemas/review.v1.rev3.json` shows what changed
between two contract identities. A list of opaque hex strings cannot.

## 12. What CI must assert

The dialect is only as good as the gates around it. Four, all of them blocking —
there is no advisory runtime path.

1. **Presence derivation, per field, per type.** For each declared field, delete
   its key from an otherwise-valid record and assert the Go validator's verdict
   matches the declared presence: `required` rejects, `optional` accepts,
   `forbidden` accepts when absent and rejects when set. A field with no probe
   fails the test, so adding a field without deriving its presence is not
   possible.
2. **Parity.** Generated instances are driven through the declared schema and
   through the Go validator, and any divergence in either direction fails. This
   catches both over-declaring (corpus-corrupting) and under-declaring (a schema
   weaker than the validator it claims to describe).
3. **Leaf-set agreement with the epistemic table**, in both directions, per
   `(type, revision)` (§7).
4. **Byte stability.** The canonical serialization of each document is pinned as a
   golden value, so a reformat of the source file that changes canonical output —
   and only that — fails loudly. A pure whitespace reformat must not.

Two further assertions are worth naming because they guard things that become
unfixable at the bump rather than merely wrong:

- **The dialect version is in the frozen bytes**, checked against each document's
  *canonical payload* and not against the parsed struct — a marker the
  canonicalizer never sees would buy nothing (§2.1).
- **A conditionally constrained declaration names its Go rule.** The mechanically
  detectable case is a `score` site that leaves `scale` or `direction` open: its
  bounds and target are `optional` only because a sibling value decides them, so it
  must carry the `go_rules` back-reference §5.3 requires. `optional` never means
  unconstrained, and the back-reference is the only thing in the frozen bytes that
  says so.

Everything the loader can judge from a document alone — the dialect version, the
required key set, the field grammar, the closed per-kind attribute vocabulary, an
authored `forbidden` — is a **panic at package initialisation** rather than a test
failure. That is a deliberate difference in kind: a document that cannot be read is
not a CI finding to triage, it is a package that must not start.

## 13. Document inventory

Six documents, one per record type, all at revision 2 superseding revision 1:

| document | subject shape | implied leaves |
|---|---|---|
| `review.v1.rev2.json` | 1..n, exactly one `primary`, plus `evidence`/`context`/`reference` | 24 |
| `diagnosis.v1.rev2.json` | same | 38 |
| `validation.v1.rev2.json` | same | 26 |
| `repository-change.v1.rev2.json` | exactly one `base`, of type `repository/v1` | 16 |
| `selection.v1.rev2.json` | 1..n, all `candidate`, one common type, candidate ports exactly | 20 |
| `measurements.v1.rev2.json` | 0..n, every role allowed, nothing enforced | 24 |

The "implied leaves" column is the §7 invariant, checked: each document's implied
leaf set equals the epistemic table's key set for that type exactly, 148 leaves
across the six with nothing missing and nothing extra.

### 13.1 The post-bump state

The bump has landed. The documents are **loaded, validated, enforced, and are the
six types' contract identities**. Precisely:

- **Embedded and parsed.** `schema_document.go` carries
  `//go:embed schemas/*.json`, and both load phases (§11.2) run at package
  initialisation. A malformed document, a declaration the Go type does not have, a
  Go body field with no declaration, a leaf set that disagrees with the epistemic
  table, or an unknown dialect version is a panic at package initialisation.
- **Enforcing.** The generic core validator (`schema_core_validator.go`) runs from
  the gate-level wrappers at **both** gates, before each type's semantic rules, so
  the declared schema rejects records in production paths.
- **Adopted.** Each type's `recordSchemaHistories` entry is at revision 2, and its
  `current.descriptor` is `mustCanonicalSchemaDescriptor(recordSchemaDocuments[…])`
  — derived from the embedded document, never pasted, so there is exactly one copy
  of each contract in the tree. Revision 1 moved verbatim into `superseded` and is
  the pre-dialect one-line stamp, unparsed and immutable. The loader still permits
  a document one past the current revision, which is what a **staged** revision 3
  would be while its bytes are under review; document resolution in the core
  validator is **by type**, which is sound only while each type has one document,
  and `TestEveryEmbeddedDocumentLoadsForExactlyOneRecordType` is the assertion that
  forces that question the day a second revision is embedded.
- **Digests installed.** Each document's canonical descriptor digest is still
  golden in `TestSchemaDocumentCanonicalSerializationIsStable`, and that test now
  asserts the digest **is** in the type's digest history. Because the digests were
  pinned before the bump, the bump installed reviewed bytes; because they are
  pinned after it, editing a document is a three-test failure
  (`TestSchemaDocumentCanonicalSerializationIsStable`,
  `TestRecordSchemaDigestsArePinnedForEveryRecordContract`,
  `TestAcceptedSchemaDigestHistoriesAreFrozenAndAppendOnly`) rather than a silent
  re-digest. **The six documents are immutable from here on.**
- **Both gates moved together, in opposite directions.** `SchemaDigestFor` returns
  revision 2, so `AdmitForSeal` requires it and a producer can no longer author the
  revision-1 identity; `RevalidateSealed` accepts revision 1 and revision 2 both,
  so nothing already sealed became unreadable. Each half is asserted per type, for
  all six, in `record_schema_bump_test.go`.
- **Epistemic entries.** `epistemicFieldStatuses` carries a
  `(type, revision 2)` key for every type, with revision 1's content unchanged —
  the bump changed contract identity, not one validator rule. Both revisions name
  one per-type builder, which is a statement that the two declarations are
  identical rather than an inheritance rule: a revision 3 that changes a validator
  must add a new builder and leave the existing one untouched, because revision 1
  and 2's entries describe validators that no longer exist in the tree and
  `TestEveryEpistemicAssignmentIsPinnedByValidatorBehaviour` drives only the
  current declaration.

**What did not change:** the documents' own bytes, every revision-1 descriptor
string, and every leaf's epistemic status. The bump changed which digest is
current and nothing about what any validator accepts.
