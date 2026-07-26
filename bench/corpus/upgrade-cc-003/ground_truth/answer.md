# What actually broke, and why

WITHHELD. Grading material only — never exposed to the agent under test.

The pre-state (`4fe7b9a8b789ba47e240d9038e9dfb977c98c03a`, "bump dependencies")
moves `go.mod`/`go.sum` and nothing else. Four upstream API changes land at once
and break three unrelated subsystems. Upstream fixed all of them in two commits
(`f25fca2ebc728cd1b870c2fbd53d432474437b03`, `52c7742d09208e28813eb8365956581362a4c4fa`),
merged as PR #9059.

---

## Breakage A — `garden.WindowSize` narrowed from `int` to `uint16`

`code.cloudfoundry.org/garden` went
`v0.0.0-20241106020650-8b5ced818ad7` → `v0.0.0-20250122021912-268f66e008f4`.
The only relevant change in the whole module (verified by diffing the two trees)
is in `container.go`:

```go
 type WindowSize struct {
-	Columns int `json:"columns,omitempty"`
-	Rows    int `json:"rows,omitempty"`
+	Columns uint16 `json:"columns,omitempty"`
+	Rows    uint16 `json:"rows,omitempty"`
 }
```

Two consequences in-tree:

1. **Production compile error** at `atc/worker/gardenruntime/container.go:125-127`
   (`toGardenTTYSpec`), which assigns `runtime.WindowSize.Columns` (`int`)
   straight into `garden.WindowSize.Columns` (now `uint16`).
2. **Test compile error** at
   `atc/worker/gardenruntime/gclient/retryable_garden_connection_test.go:382` and
   `:498`: `garden.WindowSize{Columns: 345678, ...}` — an untyped constant that
   overflows `uint16`. `go build` never sees this; `go vet ./...` /
   `go test` does.

Upstream's resolution **propagated the narrowing rather than casting at the
error site**. Both mirror structs became `uint16`:

- `atc/runtime/types.go` — `runtime.WindowSize{Columns, Rows}` → `uint16`
- `atc/hijack_payload.go` — `atc.HijackWindowSize{Columns, Rows}` → `uint16`
  (this one is a JSON wire type: `fly hijack` POSTs it to the ATC)

which in turn forced casts at the two producers, where `pty.Getsize` returns
`int`:

- `fly/commands/hijack.go:162-163`
- `fly/commands/internal/hijacker/hijacker.go:170-171`

and left `atc/worker/gardenruntime/container.go` and
`atc/api/containerserver/hijack.go` (which copies `atc.HijackWindowSize` into
`runtime.WindowSize`) untouched, because both sides now agree.

The overflowing test constants were changed to fit:
`Columns: 345678` → `Columns: uint16(34567)` (a digit dropped, not a cast of the
original value — `uint16(345678)` is also a compile error), `Rows: 45689`
already fits.

Note for judging: the alternative — leaving `runtime.WindowSize` and
`atc.HijackWindowSize` as `int` and writing
`Columns: uint16(tty.WindowSize.Columns)` inside `toGardenTTYSpec` — **also
compiles and is behaviourally equivalent for any real terminal size**. It is a
worse answer (the ATC's own types stop mirroring the runtime they wrap, and a
lossy conversion is introduced with no bound check) but it is not a defect. See
`rubric.md` for how to score it.

## Breakage B — dex now wants a `*slog.Logger`

`github.com/concourse/dex` v1.8.0 → v1.9.0:

| symbol | v1.8.0 | v1.9.0 |
|---|---|---|
| `server.Config.Logger` | `log.Logger` (logrus-ish) | `*slog.Logger` |
| `storage/sql.(*Postgres).Open` | `Open(log.Logger)` | `Open(*slog.Logger)` |

In-tree, `skymarshal/logger.New(lager.Logger) *logrus.Logger` was the bridge.
It no longer fits either call site:

- `skymarshal/dexserver/dexserver.go:129` — `Logger: logger.New(config.Logger)`
- `skymarshal/storage/storage.go:41` — `store.Open(logger.New(log))`

Upstream's resolution uses `lager.NewHandler`, which
`code.cloudfoundry.org/lager/v3` gained in the same refresh (v3.18.0 → v3.23.0;
`handler.go:22 func NewHandler(l Logger) slog.Handler`):

```go
Logger: slog.New(lager.NewHandler(config.Logger))
...
return store.Open(slog.New(lager.NewHandler(log)))
```

`skymarshal/logger/logger.go` was left in the tree, now unreferenced. Deleting
it would also have been fine; keeping it is what upstream did.

## Breakage C — dex storage writes now take a `context.Context`

Same dex bump. `storage.Storage` gained a leading `ctx` on the create methods
(`storage/storage.go:81,84,86`). Three call sites in
`skymarshal/dexserver/dexserver.go` (`replacePasswords`, `replaceClients`,
`replaceConnectors`) had to pass one; upstream used `context.TODO()` rather than
threading a real context through, because these run once at boot.

## Breakage D — `github.com/vito/houdini` is itself broken by the garden bump

This is the one that cannot be fixed in this repository. `houdini` is the
process-isolation-free "worker" backend used on non-Linux hosts.
`github.com/vito/houdini v1.1.3` compiles `garden.WindowSize` fields as `int`:

```
# github.com/vito/houdini/process
.../vito/houdini@v1.1.3/process/spawn.go:37:20: cannot use ttySpec.WindowSize.Columns (variable of type uint16) as int value in assignment
.../vito/houdini@v1.1.3/process/spawn.go:38:17: cannot use ttySpec.WindowSize.Rows (variable of type uint16) as int value in assignment
.../vito/houdini@v1.1.3/process/spawn.go:98:49: cannot use size.Columns (variable of type uint16) as int value in argument to ptyutil.SetWinSize
.../vito/houdini@v1.1.3/process/spawn.go:98:63: cannot use size.Rows (variable of type uint16) as int value in argument to ptyutil.SetWinSize
```

(Reproduced by the extractor against the pre-state module graph — see
`notes.md#validation`.)

So `go build ./worker/workercmd/` fails inside a dependency's source. The only
routes out are: pin `garden` back (forbidden by the task), `replace` to a
patched local copy (forbidden by the task), drop the houdini backend (a
behaviour change), or move to a maintained fork.

Upstream took the fork: they published `github.com/concourse/houdini v1.2.0`
and switched the import.

```go
-	"github.com/vito/houdini"
+	"github.com/concourse/houdini"
```

plus the `go.mod` swap (`github.com/vito/houdini v1.1.3` out,
`github.com/concourse/houdini v1.2.0` in; `github.com/nxadm/tail v1.4.11 //
indirect` drops out with it). The extractor confirmed
`github.com/concourse/houdini` builds clean against the bumped garden.

## Not a breakage: `dexserver_test.go`

`f25fca2e` also changed `skymarshal/dexserver/dexserver_test.go` from
`lagertest.NewTestLogger("dex")` to `lager.NewLogger("dex")`. This is **not**
required to compile — `*lagertest.TestLogger` still satisfies `lager.Logger`.
It is noise reduction (the slog bridge makes dex chatty through the test
logger). Do not require it.
