# Rubric — upgrade-cc-003

WITHHELD. Behavioural checklist for a judge. Score **intent and coverage**, not
similarity to `reference.diff`. Read `answer.md` first.

The shape of this case: one dependency refresh, **four** upstream API changes,
**three** unrelated subsystems. Partial credit is the norm — an agent that fixes
two subsystems and stalls on the third is a meaningfully different result from
one that fixes none, and both must be distinguishable from one that "fixed" the
build by pinning `garden` back.

---

## Gate 0 — hard prerequisites (fail the whole case if violated)

Any of these is an automatic zero regardless of what else is right:

- **G0.1** A dependency was pinned back to (or below) its pre-refresh version.
  Check `go.mod` for `code.cloudfoundry.org/garden`, `code.cloudfoundry.org/lager/v3`
  and `github.com/concourse/dex` — they must be at or above the versions the
  pre-state pins.
- **G0.2** A `replace` directive was added.
- **G0.3** A test file, spec, or package was deleted, emptied, `t.Skip`ped,
  `//go:build ignore`d, or otherwise excluded to make the tree compile.
- **G0.4** A production package was deleted or stubbed to make the tree compile
  (in particular: removing the houdini worker backend, or `//go:build`-gating it
  off, is not a fix — it is a behaviour change the task forbids).

## Gate 1 — mechanical (necessary, not sufficient)

- **G1.1** `go build ./...` succeeds.
- **G1.2** `go vet ./...` succeeds (test files type-check).
- **G1.3** `go mod tidy` produces no diff.

An agent can satisfy Gate 1 and still score poorly on the subsystem criteria
below; conversely an agent that gets three of four subsystems right but leaves
the build red is still worth partial credit on those three. Score them
independently.

**Gate 1 is not a kill switch.** A red build is *only* an automatic zero when it
is silent. If the remaining failure is confined to the houdini backend
(`go build ./worker/workercmd/`) **and** the agent stated the blocker explicitly
in `UPGRADE-REPORT.md` (see D.4), score Gate 1 as failed and the case per the
subsystem criteria — do not zero it. The mechanical `fail_to_pass` command set in
`case.yaml` cannot express this; it is the judge's job.

## Subsystem A — garden TTY window size (`int` → `uint16`)

- **A.1 (required)** The `garden.WindowSize` assignment in
  `atc/worker/gardenruntime/container.go` (`toGardenTTYSpec`) compiles, by some
  route.
- **A.2 (required)** The overflowing constants in
  `atc/worker/gardenruntime/gclient/retryable_garden_connection_test.go`
  (`Columns: 345678`, two occurrences) were changed to values that fit `uint16`
  — **not** deleted, and not "fixed" by a `uint16(345678)` conversion (which is
  also a compile error). Any in-range replacement is fine; upstream used 34567.
- **A.3 (preferred, full credit)** The narrowing was **propagated** rather than
  cast at the compiler-error site: `atc/runtime/types.go#WindowSize` and
  `atc/hijack_payload.go#HijackWindowSize` both become `uint16`, and the two
  `fly` producers (`fly/commands/hijack.go`, `fly/commands/internal/hijacker/hijacker.go`)
  convert from `pty.Getsize`'s `int` at the edge.
- **A.4 (partial credit, ~60% of A)** A local conversion inside
  `toGardenTTYSpec` with the ATC's own types left as `int`. This compiles and is
  behaviourally equivalent for real terminal sizes, so it is **not** a defect —
  do not mark it wrong. Mark it as the lesser answer: the ATC's mirror types
  stop tracking the runtime they wrap, and an unbounded lossy conversion is
  introduced. Award full A only for A.3.
- **A.5 (penalty)** Points off if the agent changed the JSON tags, field names,
  or the exported shape of `atc.HijackWindowSize` / `atc.HijackTTYSpec` beyond
  the field type — that struct is the `fly hijack` → ATC wire format and both
  ends ship from this module, so a rename is a gratuitous compatibility break.
- **A.6 (penalty)** Points off for a partial propagation that leaves the two
  ends disagreeing — e.g. `runtime.WindowSize` narrowed but
  `atc.HijackWindowSize` left `int` with a cast bridging them in
  `atc/api/containerserver/hijack.go`. Coherent-`int` and coherent-`uint16` are
  both defensible; a mixture is not.

## Subsystem B — dex logger (`log.Logger` → `*slog.Logger`)

- **B.1 (required)** Both call sites compile: `skymarshal/dexserver/dexserver.go`
  (`server.Config.Logger`) and `skymarshal/storage/storage.go` (`store.Open`).
- **B.2 (required)** Dex log output still reaches lager. An adapter that
  discards dex's logs (`slog.New(slog.NewTextHandler(io.Discard, nil))`,
  `slog.Default()`, or similar) silently loses auth-server logging and does not
  count as a fix.
- **B.3 (preferred, full credit)** `lager.NewHandler` is used —
  `slog.New(lager.NewHandler(logger))`. This ships in the refreshed
  `lager/v3` (v3.23.0) precisely for this migration; finding it is the point of
  the criterion.
- **B.4 (partial credit, ~70% of B)** A hand-rolled `slog.Handler` that forwards
  into lager. Correct, more code, missed the library affordance. Do not deduct
  further if the agent found `lager.NewHandler` and *explicitly* declined it by
  reading the task's "do not take up new capabilities the upgraded libraries now
  offer" constraint conservatively — that reading is defensible (the constraint
  is about features, not migration helpers, but the task does not say so). B.3
  still gets full credit; B.4 with a stated rationale is a near miss, not a
  process failure.
- **B.5 (neutral)** Whether `skymarshal/logger/logger.go` is deleted or left
  behind is not scored. Upstream left it (dead). Deleting it is fine as long as
  nothing else referenced it.

## Subsystem C — dex storage methods take a `context.Context`

- **C.1 (required)** `replacePasswords`, `replaceClients` and `replaceConnectors`
  in `skymarshal/dexserver/dexserver.go` pass a context to
  `CreatePassword` / `CreateClient` / `CreateConnector`.
- **C.2 (neutral)** `context.TODO()`, `context.Background()`, or a context
  threaded down from the caller are all acceptable. These run once at boot;
  upstream used `context.TODO()`. Do not reward or punish the choice.

## Subsystem D — `github.com/vito/houdini` is broken *inside the dependency*

This is the discriminating criterion. The failure is in a file the agent cannot
edit (`vito/houdini@v1.1.3/process/spawn.go`), and every locally-scoped instinct
(cast it, pin garden back, `replace` it, delete it) is either impossible or
forbidden by Gate 0.

- **D.1 (required)** `go build ./worker/workercmd/` succeeds without violating
  Gate 0, and the houdini backend is still wired into `houdiniRunner`.
- **D.2 (full credit)** The dependency was moved to a maintained fork that
  compiles against the new garden — `github.com/concourse/houdini` (v1.2.0 or
  later) is what upstream published and used; the import in
  `worker/workercmd/houdini.go` and the `go.mod` requirement both move.
- **D.3 (full credit, alternative)** Any other route that leaves the build green
  with garden un-pinned and the backend intact and functional — e.g. an upstream
  patch released as a new `vito/houdini` version. Judge on the property, not the
  module path.
- **D.4 (credit for diagnosis even without a fix)** If the agent correctly
  *identifies* that the break is inside `vito/houdini` and cannot be repaired
  in-tree without either forking the dependency or violating a stated
  constraint, and says so explicitly rather than silently leaving the build red
  — award roughly half of D. Correctly escalating a blocked dependency is the
  right professional behaviour and should not score the same as not noticing.
  **Delivery channel:** the task tells the agent to record anything it could not
  do in `UPGRADE-REPORT.md` at the repository root, so that is where to look
  first. Accept an equivalently unmistakable statement elsewhere (the final
  response, a `BLOCKERS.md`, a commit message) — grade the diagnosis, not the
  filename — but a blocker that appears only as a passing remark buried in a
  build log is not an escalation.
- **D.5 (penalty)** Vendoring houdini's source into this repository, or copying
  it into an in-tree package, scores zero for D even though it compiles.

## Process criteria

- **P.1** Did the agent establish the full failure set before starting (e.g. run
  `go build ./...` *and* something that type-checks tests) rather than fixing the
  first error, rebuilding, fixing the next? Both reach green; the former is what
  makes the houdini blocker visible early instead of at the end.
- **P.2** Did the agent connect each in-tree change to the specific upstream API
  change that forced it (by reading the module source in `GOMODCACHE`, the
  upstream changelog, or the diff between versions) rather than guessing from
  the compiler message? Evidence: any mention of *why* `WindowSize` narrowed,
  what `lager.NewHandler` is for, or what changed in dex's storage interface.
- **P.2a (memorization control — `memorization_risk: high`)** Credit *causal
  reasoning grounded in evidence the agent gathered here*: it built the tree,
  read the module source, reproduced the failure inside `vito/houdini`. Do not
  credit a correct answer that arrives asserted — an agent that names
  `github.com/concourse/houdini` (or `uint16`, or `lager.NewHandler`) without
  ever having observed the failure that motivates it is recalling this public PR,
  not solving it. Both can end at the same diff; only the first is a capability
  signal. Say which one you saw, in the transcript notes, whenever this case is
  scored.
- **P.3** Scope discipline: no unrelated refactors, no new dependencies beyond
  the houdini swap, no reformatting sweeps. The reference change is 11 files and
  ~23 inserted lines. `UPGRADE-REPORT.md` is *requested by the task* and does not
  count against scope — the reference change predates that instruction and so has
  no counterpart to compare against; grade the report on content, not existence.

## Reference size

`git diff 4fe7b9a8b7 52c7742d09` — 11 files, +23 / −26, of which `go.mod` and
`go.sum` are 9 lines. A submission an order of magnitude larger than this should
be examined for scope creep before it is scored well.
