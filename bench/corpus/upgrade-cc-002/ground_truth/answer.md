# upgrade-cc-002 — what the humans actually did

WITHHELD. Never place this in the exposure manifest.

Terminal artifact: merge `750512af503e11d391f48e6ed097bbda4e28d5d0`
(concourse/concourse#8843, branch `renovate/all`, merged 2023-10-18).
Branch tip: `57e50564b6abf2c5954ee9084f3c50bdde386be2`.

## The accepted answer, in one line

Adopt two of the three proposed major bumps (`mpb` v4→v8, `caarlos0/env`
v6→v9) by moving the import paths and rewriting the one place where mpb's API
actually changed; **decline** the third (`gopkg.in/yaml.v2` → `v3`), restore
`yaml.v2` as a direct requirement, and add a Renovate rule pinning it so the
bot stops re-proposing it.

## 1. `github.com/vbauerster/mpb` v4 → v8

Eight import lines across seven files, all a mechanical `v4` → `v8`:

- `fly/commands/execute.go`
- `fly/commands/internal/executehelpers/downloads.go`
- `fly/commands/internal/executehelpers/inputs.go`
- `fly/commands/internal/executehelpers/uploads.go`
- `fly/commands/sync.go` (both `mpb` and `mpb/decor`)
- `fly/ui/progress/progress.go` (both `mpb` and `mpb/decor`)

The only real work is `fly/ui/progress/progress.go`, in `(*Progress).Go`.
v8 deleted the v4 spinner API; four distinct edits are required and none of
them is guessable from the compiler error alone:

| v4 | v8 |
|---|---|
| `prog.progress.AddSpinner(0, …)` | `prog.progress.New(0, …)` |
| `mpb.SpinnerOnLeft` (a `BarOption` constant) | `mpb.SpinnerStyle().PositionLeft()` (a `BarFiller`, passed as the 2nd positional arg) |
| `decor.AverageSpeed(decor.UnitKiB, "(%.1f)")` | `decor.AverageSpeed(decor.SizeB1024(0), "(%.1f)")` |
| `mpb.BarClearOnComplete()` | `mpb.BarFillerClearOnComplete()` |

Note the shape change hiding inside the first two rows: `AddSpinner(total,
alignment, opts...)` became `New(total, filler, opts...)`, so the second
positional argument changes *kind* — from an alignment option to a bar filler.

Everything else in the file survives untouched and must not be disturbed:
`mpb.New(mpb.WithWidth(1))`, `decor.Name(name, decor.WC{W: len(name), C:
decor.DSyncWidthR})`, `decor.OnComplete(…, " "+ui.Embolden("done"))`,
`bar.Abort(false)`, `bar.SetTotal(bar.Current(), true)`, and the
`errgroup`-based `Go`/`Wait` structure. The exported signature
`func (prog *Progress) Go(name string, f func(*mpb.Bar) error)` is unchanged
apart from `*mpb.Bar` now resolving to the v8 type.

## 2. `github.com/caarlos0/env` v6 → v9

One line, in a **test-only** file: `topgun/k8s/k8s_suite_test.go` imports
`github.com/caarlos0/env/v6` → `github.com/caarlos0/env/v9`. No API change —
`env.Parse(&cfg)` is call-compatible.

This is the trap for anyone who validates with `go build ./...` alone:
`topgun/k8s` contains only `_test.go` files, so `go build` never looks at it,
`go mod tidy` then re-adds `caarlos0/env/v6` to satisfy the stale import, and
the upgrade is half-reverted while the build looks green.

## 3. `gopkg.in/yaml.v2` → `v3` — declined

`gopkg.in/yaml.v2` is imported by four files at the cut
(`atc/atccmd/command.go`, `atc/engine/build_step_delegate.go`,
`tsa/tsacmd/command.go`, `vars/template.go`). The maintainer's first attempt
(`091b31d8754967c2588a1b1e91cde41b4339fee1`) migrated three of them to
`yaml.v3`; the next commit (`57e50564b6abf2c5954ee9084f3c50bdde386be2`)
reverted that part entirely.

Reason, quoted from the `renovate.json` rule they added in the same commit:
v3 changes marshalling indentation, "which requires test updates and we dont
know the full impact" (go-yaml/yaml issue 661).

Mechanically observable: with `vars/template.go` on `yaml.v3`,
`go test ./vars/` fails **10 of 91 specs**. Under this task's "do not edit
tests to accommodate a dependency" constraint, that forecloses the migration.

The accepted change therefore:

- keeps all four call sites on `gopkg.in/yaml.v2`;
- restores `gopkg.in/yaml.v2 v2.4.0` to the direct `require` block;
- demotes `gopkg.in/yaml.v3 v3.0.1` to `// indirect`;
- adds to `.github/renovate.json`:
  ```json
  {
    "matchPackageNames": ["gopkg.in/yaml.v2"],
    "allowedVersions": "<3.0.0",
    "_context": "v3 will cause indent problem when marshalling, which requires test updates and we dont know the full impact. See https://github.com/go-yaml/yaml/issues/661."
  }
  ```

## 4. `go.mod` / `go.sum` hygiene

The bot's edit left three literal duplicate `require` lines (it replaced each
old-major line with a new-major line that was *already* present a line below).
The final `go.mod`:

- drops the duplicates;
- drops `github.com/caarlos0/env/v6` and `github.com/vbauerster/mpb/v4`
  entirely;
- adds `github.com/mattn/go-runewidth v0.0.15 // indirect` and
  `github.com/rivo/uniseg v0.4.4 // indirect` (mpb v8's new transitive deps);
- moves `gopkg.in/yaml.v3` to the indirect block and restores
  `gopkg.in/yaml.v2` to the direct block.

`go.sum` loses 125 lines of now-unreachable hashes and gains the `h1:` hashes
for `mpb/v8 v8.6.2` and `env/v9 v9.0.0` (at the cut, `go.sum` carried only the
`/go.mod` hashes for those two — they were required but unimported).

## Not part of the answer

The intermediate commit `091b31d875` also touched `atc/atccmd/command.go`,
`tsa/tsacmd/command.go` and `vars/template.go` for the yaml migration. All of
that was undone before the merge. An agent whose final change contains yaml.v3
edits to those files has reproduced the mistake, not the fix.
