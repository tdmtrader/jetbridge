# upgrade-cc-002 — behavioral rubric

WITHHELD. Score intent and behavior, not diff similarity. The reference diff
is one correct answer, not the only one — in particular, any `go.sum` that is
`go mod tidy`-consistent is fine, and a differently-ordered `go.mod` is fine.

Primary rubric is `mechanical` (see `case.yaml#grading`). This checklist is
for the judge pass that runs on top of it, and for diagnosing *why* a
mechanical run failed.

**Two standing instructions for the judge.**

- *Credit causal reasoning from evidence, not recall or quotation.* This case
  carries `memorization_risk: high`: the mpb v4→v8 delta is public, documented,
  October-2023 knowledge, and an agent may simply know it. Score the work the
  agent can show — compiler/vet output it acted on, the `vars` spec failures it
  actually observed and attributed to the dependency, the import sites it
  enumerated — above an answer that arrives correct but unevidenced. A correct
  unevidenced answer still passes the Must items; note the difference in the
  written judgement so a reader can discount it.
- *Do not credit the agent for warnings it was never given.* As of the
  2026-07-25 fixup, `task.md` no longer pre-announces the `go mod tidy` revert
  mechanism, and no longer hints that some packages are test-only. It states
  only the outcome requirement ("the superseded majors must be gone") and lists
  `go vet ./...` among the acceptance gates. Items 2 and 3 are therefore
  genuine discoveries, not instruction-following; weight them accordingly.

## Must (each is independently pass/fail)

1. **The module builds and vets clean.** `go build ./...` and
   `go vet ./fly/... ./topgun/k8s/` both exit 0 on the agent's tree.
2. **The old majors are gone.** `go.mod` contains no
   `github.com/vbauerster/mpb/v4` and no `github.com/caarlos0/env/v6`, in
   either the direct or the indirect block. An agent that reached green by
   running `go mod tidy` (which re-adds both) fails here even though its build
   is green — this is the single most important discriminator in the case.
3. **The test-only site was migrated.** `topgun/k8s/k8s_suite_test.go` imports
   `github.com/caarlos0/env/v9`. Missing this is the failure mode that hides
   behind a green `go build ./...`.
4. **The mpb v8 API was actually ported, not worked around.**
   `fly/ui/progress/progress.go` compiles against v8 with the spinner
   constructed through v8's API. Specifically the agent must have dealt with
   all four removals — the `AddSpinner` constructor, the `SpinnerOnLeft`
   option, the `decor.UnitKiB` speed unit, and `BarClearOnComplete`. Any
   equivalent v8 spelling counts; see §Acceptable variants.
5. **Behavior of the progress UI is preserved.** The spinner is still
   left-positioned, the prepended `decor.Name` still uses the same
   `DSyncWidthR` width sync, the appended decorator is still an
   `OnComplete(AverageSpeed(<1024-based unit>, "(%.1f)"), " "+bold("done"))`,
   and the bar still clears its filler on completion. Dropping a decorator, a
   width-sync, or the clear-on-complete to make it compile is a fail.
6. **The exported surface of `fly/ui/progress` is unchanged.**
   `func New() *Progress`, `func (prog *Progress) Go(name string, f func(*mpb.Bar) error)`
   and `func (prog *Progress) Wait() error` keep their names and shapes; the
   `errgroup` structure, `bar.Abort(false)` and
   `bar.SetTotal(bar.Current(), true)` survive.
7. **The yaml bump was declined, not absorbed.** All four `gopkg.in/yaml.v2`
   call sites at the cut (`atc/atccmd/command.go`,
   `atc/engine/build_step_delegate.go`, `tsa/tsacmd/command.go`,
   `vars/template.go`) still import `gopkg.in/yaml.v2`, and `go.mod` requires
   `gopkg.in/yaml.v2` directly. `go test ./vars/` passes.
8. **No test was edited to accommodate a dependency.** `git diff` over the
   agent's change touches no `_test.go` file except
   `topgun/k8s/k8s_suite_test.go`, and that file's only change is the import
   path. Rewriting `vars/template_test.go` expectations to accept yaml.v3
   indentation is an explicit fail even if everything is green afterwards.
9. **`go.mod` is well formed.** No duplicated `require` entries (the pre-state
   has three); `go mod tidy` produces no further change.

## Should

10. **The decline is recorded and enforced, in the tree.** The change adds a
    Renovate constraint pinning `gopkg.in/yaml.v2` below 3.0.0 (or an
    equivalent `ignoreDeps` / `matchPackageNames` rule in
    `.github/renovate.json`) so the bot stops re-proposing it, with a stated
    reason. The task asks for the reason to live in the change itself, so the
    delivery channel is the repository: the bot rule plus a reason recorded
    anywhere durable in the tree (the rule's own comment/`_context` field, the
    commit message, or a short note file at repo root) all count. A decline
    explained only in the agent's chat reply, with nothing in the tree, scores
    this item as a miss. The humans did it as a `_context` string on the rule,
    in the same commit. Score it, but do not let its absence mask items 1–9.
11. **The reason given for declining yaml.v3 is the right one** — a
    marshalling/indentation behavior change that would force test updates, not
    a vague "risky" or an invented compile error.
12. **The change is scoped.** No refactors, renames or unrelated version
    moves ride along; no dependency is vendored, forked or shimmed.

## Acceptable variants (do not penalize)

- Any v8 spelling of the spinner that preserves item 5's behavior, e.g.
  `mpb.SpinnerStyle().PositionLeft()` passed as the filler vs. building the
  same filler through `mpb.SpinnerStyle(frames...)`; `decor.SizeB1024(0)` vs.
  another 1024-based unit spelling with identical output.
- `go.sum` produced by the agent's own `go mod tidy` rather than byte-matching
  the reference.
- `gopkg.in/yaml.v3` appearing as an `// indirect` requirement (it must — the
  build graph still needs it); only a *direct* dependency on it, or a source
  file importing it, is a fail.
- Migrating `atc/engine/build_step_delegate.go` and friends is **not**
  expected in either direction; leaving them exactly as they are is correct.
- Extra hygiene the humans did not do (e.g. a comment in `go.mod`) is neutral.

## Known wrong answers (call these out explicitly when seen)

- **`go mod tidy` and stop.** Green build, zero upgrade: `mpb/v4` and
  `env/v6` are back and `mpb/v8`/`env/v9` are gone. Fails items 2 and 3.
- **Build-only validation.** Migrates the seven `fly` files, never looks at
  `topgun/k8s`, lets `go mod tidy` re-add `caarlos0/env/v6`. Fails 2 and 3.
- **Absorb the yaml bump.** Migrates the yaml.v2 call sites to v3, then
  "fixes" the 10 failing `vars` specs by updating their expected indentation.
  Fails 7 and 8 — and it is the exact thing the maintainers wrote, then
  reverted, in this PR.
- **Pin around the API break.** Downgrading mpb to a v8 patch that still has
  `AddSpinner` (there is none), or holding `fly` on v4 by keeping both majors
  in `go.mod`. Fails 2.
- **Delete the decorators.** Getting `progress.go` to compile by dropping the
  speed decorator or the clear-on-complete option. Fails 5.
