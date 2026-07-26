# LightingDesign

An agentic, MCP-based assistant for programming theatrical lighting on **ETC Eos**
consoles over OSC — so an AI assistant (via text or voice) can help drive the
board. See [`forge/`](forge/) for product and design docs.

> **Status:** early development. The stack (TCP+SLIP transport → Eos core → MCP
> adapter) is proven end to end. Implemented tools cover connection health, cue
> playback (`go_to_cue` / `go` / `current_cue`), channel **intensity**
> (set/adjust/release/read), LED **color** (set/adjust/read hue & saturation),
> **recording** the live look into a cue, and **reading/exporting** a cue list
> (`list_cues` / `export_cues`, optionally capturing each cue's live look). See the
> [Tools](#tools) table.

## How it works

```
MCP client (Claude, etc.)  →  lighting-mcp (this server)  →  ETC Eos console
        stdio                  MCP adapter → eos core →            OSC over
                               TCP+SLIP transport                  TCP (SLIP)
```

The server talks OSC to Eos over **TCP with SLIP framing (OSC 1.1)**. It uses
`go-osc` as a wire codec only and owns the SLIP framing + transport itself.

## Requirements

- **Go 1.25+**
- An **ETC Eos** console *or* **ETC Nomad** (free offline software from ETC —
  great for development without a physical console).

## Build & test

```sh
make build   # -> bin/lighting-mcp
make test    # go test -race ./...
make vet
make fmt
```

## Configure Eos for OSC (SLIP)

On the console/Nomad: **Setup → System Settings → Show Control → OSC**

1. Enable **OSC**.
2. Use a **SLIP (OSC 1.1)** TCP port. The simplest is to enable **"Third Party
   OSC"**, which serves SLIP on **TCP 3037** (this project's default). You can
   also define a custom OSC TCP port — just make sure its framing is **SLIP**,
   not the default Packet-Length (OSC 1.0), which this server does not speak.

> Note: the default OSC port **3032** uses Packet-Length framing unless you
> change it — `health_check` will connect but get no reply there.

## Configure the server

Defaults: host `127.0.0.1`, port `3037`, transport `tcp+slip`. Override with
flags or an optional YAML file (flags win):

```sh
./bin/lighting-mcp -host 10.0.1.50 -port 3037
./bin/lighting-mcp -config venue.yaml
```

`venue.yaml` (the committed file targets this venue's console — copy and edit for
yours; machine-specific overrides go in `venue.local.yaml`, which is git-ignored):

```yaml
host: 10.0.1.50      # console IP (or 127.0.0.1 for local Nomad)
port: 3037           # OSC TCP port with SLIP framing
transport: tcp+slip
color_channels:      # LED/color-capable channels — read_stage/set_look read their color
  - 70
  - 113

all_channels: "1 Thru 119"   # what the selection "all" expands to (e.g. blackout)
groups:                       # name -> channels; usable anywhere a selection is taken
  "orange ladders": "35 Thru 40"
  "desires": "113 Thru 119"
  "blues": "7 Thru 12 + 28 Thru 33 + 56 Thru 61"
lone_star_home:               # Lone Star default aim for set_lone_star {home: true}
  108: { pan: -48, tilt: 50 }
  110: { pan: -31, tilt: 58 }
```

Unlike `rig.yaml` (human reference, never parsed), these `venue.yaml` keys ARE
read by the server: `groups` and `all_channels` let any channel selection use a
**name** ("desires") or **"all"**, and `lone_star_home` powers `set_lone_star
{home: true}`.

## Venue profile (rig map)

`rig.yaml` is a human- and agent-readable map of the rig: channels grouped by
meaning (positions, ladder colors, LED fixtures) plus a color palette. The
assistant reads it to translate a request — "the desires in blue," "ladders 3 and
4 in pink" — into the specific channels to drive, then calls the numeric tools. It
is **reference data**: the server does not parse it, and the agent always picks the
exact channels.

```yaml
ladders:
  boom_order: [1, 3, 5, 2, 4, 6]   # the 6 channels below are in this boom order
  pink: [42, 43, 44, 45, 46, 47]   # R39 gel — "ladders 3/4 pink" = 43 + 46
leds:                              # color-capable: take set_color
  desires: { usl: 113, sl: 114, dsl: 115, dcs: 116, dsr: 117, sr: 118, usr: 119 }
palette:
  blue:  { hue: 240, sat: 100 }
  amber: { hue: 35,  sat: 90 }
```

Gelled fixtures (ladders, etc.) are intensity-only — a color word there *selects*
those channels; color-capable fixtures (`leds:`) take `set_color`.

## Run it from an MCP client

`lighting-mcp` speaks MCP over **stdio**. Point your MCP client at the binary.
For example, a Claude Desktop `mcpServers` entry:

```json
{
  "mcpServers": {
    "lighting": {
      "command": "/absolute/path/to/bin/lighting-mcp",
      "args": ["-host", "127.0.0.1", "-port", "3037"]
    }
  }
}
```

## Tools

| Tool | What it does |
|------|--------------|
| `health_check` | Ping the console; report `{ connected, latency_ms, target }`. Read-only. |
| `go_to_cue` | Go to a specific cue: `{ cue: "5" or "2.3", list?: 1 }`. **Changes the live stage** (reversible — nothing is recorded). |
| `go` | Fire the next cue in sequence (the console GO button). **Changes the live stage.** |
| `current_cue` | Report the active + pending cue. Read-only. |
| `read_stage` | Read what's live on stage: active channels with level, plus hue/sat for configured color channels: `{}` → `{ channels, count, partial, target }`. Read-only (changes the channel selection, not the look). |
| `list_cues` | List a cue list's cues with number, label, and fade/follow/hang timing: `{ list?: 1 }`. Read-only. |
| `export_cues` | Read a cue list and write it to a YAML file: `{ list?: 1, path, capture_looks?: false, channels?: "1 Thru 100" }`. Metadata-only is read-only; `capture_looks: true` **fires every cue on stage** to read its look, then restores the prior cue (records nothing). |
| `set_channel_level` | Set intensity: `{ channels: "3" / "1 Thru 10" / "1 + 3 + 5", level: 0–100 }`. `channels` also takes commas, a **group name** ("desires", "orange ladders"), or **"all"** (see venue profile). **Changes the live stage** (reversible). |
| `adjust_channel_level` | Nudge intensity: `{ channels, delta: ±%, non-zero }`. **Changes the live stage** (reversible). |
| `release_channels` | Release channels' manual levels to neutral: `{ channels }`. **Changes the live stage** (reversible). |
| `get_channel_level` | Read a channel's current intensity: `{ channel }`. Read-only. |
| `set_color` | Set color on LED/color-capable channels: `{ channels, hue: 0–360, sat: 0–100 }` (0°=red, 120°=green, 240°=blue). `channels` takes group names / "all" too. **Lone Stars (108–112) are driven via native CMY automatically** — one hue/sat covers the whole rig (a mixed selection drives both). **Changes the live stage** (reversible). |
| `adjust_color` | Nudge color relatively: `{ channels, d_hue: ±°, d_sat: ±% }` — 0 leaves that parameter unchanged ("warmer", "more blue", "knock the saturation down"). **Changes the live stage** (reversible). |
| `get_color` | Read a channel's current color: `{ channel }` → `{ hue, sat, has_hue, has_sat }`. Read-only. |
| `set_lone_star` | Full control of the **Lone Star moving heads** (High End Lonestar, ch 108–112) beyond intensity/color: `{ channels?: "108 Thru 112", pan, tilt, zoom, iris, focus, cyan, magenta, yellow, cto, strobe, frost_light, frost_medium, frame_angle_a..d, frame_thrust_a..d (blade insertion — "shutter an edge"), frame_rotation, color_wheel, gobo, prism, prism2 }`, plus `home: true` (send units to their default aim) and motion `{ gobo_spin, prism_rotate, animation }` (signed −100..100; sign = direction, 0 = stop — ⚠️ *motion is provisional, not yet hardware-verified*). Only provided fields change; `channels` defaults to all five (a subset must be within 108–112). An empty call returns a teaching list of settable params. **pan/tilt physically move the heads.** **Changes the live stage** (reversible). Intensity → `set_channel_level`; general color → `set_color` (drives CMY for you). |
| `adjust_lone_star` | Nudge the Lone Stars' continuous params relatively: `{ channels?, pan, tilt, zoom, … }` (signed deltas, same units). Indexed wheels (color_wheel/gobo) can't be adjusted — set them by slot. **Changes the live stage** (reversible). |
| `get_lone_star` | Read a Lone Star's currently-reported parameters: `{ channel }` → `{ params: { zoom, pan, … } }`. Read-only. |
| `list_effects` | List the effects available to run — the palette: `{}` → `{ effects: [{ number, label, type }], count }` (the Eos factory effects 901–918 plus any built in the console's Effect Editor). Read-only. |
| `apply_effect` | Run an existing effect on channels: `{ channels, effect: 901, rate?: 1–500, size?: 0–100 }`. `rate` = speed % (100 = normal), `size` = amplitude %. `channels` takes group names / "all". **Changes the live look** (reversible). To keep it, run then `record_cue`. |
| `adjust_effect` | Re-tune the effect running on channels: `{ channels, rate?: 1–500, size?: 0–100 }` — "faster", "bigger". The channels must already be running an effect. **Changes the live look** (reversible). |
| `stop_effect` | Take the running effect off channels (their levels stay): `{ channels }`. **Changes the live look** (reversible). |
| `clone_effect` | Copy an effect to a new number so you can keep a tweaked version: `{ source: 915, dest?: 990 }` → `{ effect }` (auto-picks a free number 990–999 when `dest` is omitted). Creates a stored effect; changes nothing on stage. |
| `set_look` | Establish a look in one call: zero everything live, then set `channels` to `level` (default 100) and optional color: `{ channels, level?: 0–100, hue?: 0–360, sat?: 0–100 }` (`channels` takes group names / "all" too). **Replaces the look** — anything not in `channels` goes out. Reversible (records nothing); returns the resulting look. Refine from there with `set_channel_level` / `set_color`. |
| `record_cue` | Record the live look into a cue, optionally with a fade time and label: `{ cue, list?: 1, time?: seconds, label?, confirm?: false }`. A **new** cue records directly; an **existing** cue returns a *preview* and needs `confirm: true` to overwrite. |
| `update_cue` | Merge the current manual changes into a cue — the "tweak one thing and save it" move (Eos Update; not the whole look): `{ cue, list?: 1, confirm?: false }`. MODIFIES the stored cue; `confirm`-gated. (Eos Update doesn't set time/label — record them, or use `set_cue`.) |
| `set_cue` | Change a stored cue's **fade time** without re-recording its look: `{ cue, list?: 1, time: seconds, confirm?: false }`. MODIFIES the stored cue (its recorded levels/colors are untouched); `confirm`-gated. Fills the gap `update_cue` leaves (Update can't set timing). |

Example — ask the assistant to run **`health_check`**:

```json
{ "connected": true, "latency_ms": 24.1, "target": "tcp+slip 127.0.0.1:3037", "detail": "Console answered." }
```

If the console is unreachable, `connected` is `false` with a plain-language
`detail` instead of a hard error.

> **Cue control needs a show loaded.** `go_to_cue`, `go`, and `current_cue` only
> do something useful when the console/Nomad has a show with cues. With no cues
> loaded, the commands are still sent (and report `ok`), but nothing changes and
> `current_cue` stays empty.
>
> **Channel levels need a patched show.** `set_channel_level` etc. need patched
> channels to have a visible effect, and `get_channel_level` needs feedback to
> report a level.
>
> **Reading the live stage is reliable.** `read_stage` (and the look returned by
> `set_look`) finds the live channels with Eos *Select Active*. Because the console
> only reports the active set when the command-line selection actually changes, the
> reader nudges the selection first and waits for the console's reply — so a settled
> look reads back the same set every time (no spurious "no active channels"). A
> channel that doesn't answer in time is omitted and the result flagged `partial`.
>
> **Color needs a patched LED/color-capable fixture.** `set_color` / `adjust_color`
> only do something on fixtures with hue/saturation parameters, and `get_color`
> reports color only when the console streams it (a channel at 0, unpatched, or
> without color parameters reports none).
>
> **Effects run from a palette — new ones are built in the console's Effect Editor.**
> `apply_effect` / `adjust_effect` / `stop_effect` / `clone_effect` work with effects that
> already exist (the factory library 901–918, plus any you author by hand once in the Eos
> Effect Editor — like patching, a one-time setup). Eos effect *authoring* (type, step
> chases, waveforms) is not reachable over OSC, so the server runs, tunes, records, and
> copies effects rather than creating them from scratch. Use `list_effects` to see what's
> available, then `apply_effect` and `record_cue` to bake one into a cue. Applying a Linear
> factory effect (e.g. 904 "Can Can", 915 "Ramp") to a *row* of channels chases across them
> (each channel offset); on a single channel it just pulses.
>
> **Reading cues needs a show loaded.** `list_cues` and `export_cues` read stored
> cues via the Eos `/eos/get/cue` query, so the console/Nomad must have a show with
> cues. `export_cues` with `capture_looks: true` additionally **fires every cue on
> stage** (the rig visibly cycles through the whole show), reads each channel in
> `channels` (default `"1 Thru 100"`), then restores the previously-active cue —
> it records nothing. Look capture is **best-effort and selection-limited**: a
> channel at 0/unpatched reports nothing and is omitted (the look is flagged
> `partial`). It is also **slow** — one read per channel per cue — so narrow
> `channels` to the patched range for a large show.

### Recording a cue (destructive)

`record_cue` overwrites. It is a two-step confirm:

1. Call `record_cue { cue: "12" }` → returns a **preview** (`recorded: false`) of
   exactly what will be recorded, and records nothing.
2. Call `record_cue { cue: "12", confirm: true }` → records the live look into the
   cue (`recorded: true`), overwriting it if it exists.

Channel selections (`channels`) accept numbers, `Thru`, and `+` only — other text
is rejected, so command input can't smuggle extra console commands.

## Diagnostics

`cmd/eosping` is a quick bring-up tool that pings a console (or several candidate
ports) using the real transport stack:

```sh
go run ./cmd/eosping -host 127.0.0.1 -ports 3037,3032,58000 -count 3
```

`cmd/eoscmd` sends Eos command lines, key presses, and `/eos/get` queries and prints the
`/eos/out` feedback — handy for charting what the console accepts over OSC (it's how the
effects surface was mapped):

```sh
go run ./cmd/eoscmd -cmd "Chan 1 Thru 5 Effect 901#" -get /eos/get/fx/count -dwell 4s
```

## Layout

- `cmd/lighting-mcp/` — the MCP server entrypoint
- `cmd/eosping/` — connection/ping diagnostic
- `cmd/eoscmd/` — command-line / key / get-query OSC probe (effects bring-up)
- `internal/osc/` — OSC codec + TCP+SLIP transport (no Eos knowledge)
- `internal/eos/` — Eos domain core (MCP-agnostic): `Connect`, `Ping`
- `internal/mcp/` — thin MCP adapter (the `health_check` tool)
- `internal/config/` — connection settings (host / port / transport)
- `internal/osctest/` — fake Eos console for tests

## Testing notes

Automated tests use a fake Eos endpoint and never touch hardware. The end-to-end
test against a real console is opt-in:

```sh
LD_E2E_ADDR=127.0.0.1:3037 go test -run TestE2EHealthCheck ./cmd/lighting-mcp/
```

Other opt-in e2e tests (all non-destructive) exercise cue control, intensity, and
color against a live console — e.g. `-run TestE2EColor`. The destructive
build/record loops additionally need `LD_E2E_DESTRUCTIVE=1` and a disposable show.

Reading and exporting cues against a live console (needs a show with cues loaded):

```sh
LD_E2E_ADDR=127.0.0.1:3037 go test -run TestE2EReadCues ./cmd/lighting-mcp/
```

`list_cues` and the metadata-only `export_cues` are read-only. To also capture each
cue's look — which **fires every cue on stage** (disruptive but non-destructive) —
add `LD_E2E_CAPTURE=1`.
