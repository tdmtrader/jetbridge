#!/usr/bin/env python3
"""Re-derive the complete spec-vs-capture discrepancy set for review-ld-001.

WITHHELD grading aid. Never expose to the agent under test.

Expands every per-cue `look:` in task/workspace/show-current.yaml into a
{channel: level} map using the group tables from rig.yaml (including the
`boom_order: [1, 3, 5, 2, 4, 6]` indirection), then set-diffs it against the
captured channel list for the same cue in
task/workspace/show-captured-live-20260625.yaml.

Stdlib only -- PyYAML is not available on the curation machine, so this carries a
small hand-rolled parser for the YAML subset the look blocks use.

    python3 ground_truth/derive_discrepancies.py           # run from the case dir
    python3 ground_truth/derive_discrepancies.py --workspace /path/to/workspace

Verified 2026-07-25 against the sealed copies in task/workspace/ (sha256 recorded
in notes.md).
"""
import argparse
import os
import re
import sys

BOOM_ORDER = [1, 3, 5, 2, 4, 6]  # rig.yaml: index -> boom number

LADDERS = {
    "blue": [7, 8, 9, 10, 11, 12],
    "yellow": [14, 15, 16, 17, 18, 19],
    "green": [21, 22, 23, 24, 25, 26],
    "lt_blue": [28, 29, 30, 31, 32, 33],
    "orange": [35, 36, 37, 38, 39, 40],
    "pink": [42, 43, 44, 45, 46, 47],
    "red": [49, 50, 51, 52, 53, 54],
    "dk_blue": [56, 57, 58, 59, 60, 61],
    "white": [63, 64, 65, 66, 67, 68],
}

GROUPS = {
    "entrance_aisle": {None: 1},
    "audience_stair": {None: 2},
    "house": {None: [3, 4]},
    "alcove": {None: 5},
    "trough": {"sl": 70, "sr": 71},
    "pars": {"dsl": 73, "dsc": 74, "dsr": 75, "usl": 76, "usc": 77, "usr": 78},
    "circle": {1: 80, 3: 81, 5: 82, 7: 83, 9: 84, 11: 85},
    "specials": {"center_a": 87, "center_b": 88, "dsc": 89, "dsr": 90,
                 "msr": 91, "usr": 92, "msl": 95},
    "spots": {"hr": 98, "hl": 99},
    "under_bench": {"sr": 101, "cs": 102, "sl": 103},
    "strip": {"sr": 105, "cs": 106, "sl": 107},
    "lonestars": {"dsl": 108, "dsr": 109, "hsl": 110, "hcs": 111, "hsr": 112},
    "desires": {"usl": 113, "sl": 114, "dsl": 115, "dcs": 116, "dsr": 117,
                "sr": 118, "usr": 119},
}

CHANNEL_NAME = {}
for _g, _m in GROUPS.items():
    for _k, _v in _m.items():
        for _c in (_v if isinstance(_v, list) else [_v]):
            CHANNEL_NAME[_c] = _g if _k is None else f"{_g}.{_k}"
for _color, _chans in LADDERS.items():
    for _i, _c in enumerate(_chans):
        CHANNEL_NAME[_c] = f"ladders.{_color}.boom{BOOM_ORDER[_i]}"


def boom_ch(color, boom):
    return LADDERS[color][BOOM_ORDER.index(int(boom))]


def coerce(v):
    v = v.strip()
    if v.startswith('"') and v.endswith('"'):
        return v[1:-1]
    try:
        f = float(v)
        return int(f) if f == int(f) else f
    except ValueError:
        return v


def parse_block(lines, base_indent):
    """Minimal YAML-subset parser -> nested dict / list."""
    out = {}
    i = 0
    while i < len(lines):
        line = lines[i]
        if not line.strip() or line.strip().startswith("#"):
            i += 1
            continue
        indent = len(line) - len(line.lstrip())
        if indent < base_indent:
            break
        stripped = line.strip()
        if stripped.startswith("- "):
            lst = []
            while i < len(lines):
                l = lines[i]
                if not l.strip():
                    i += 1
                    continue
                ind = len(l) - len(l.lstrip())
                if ind != base_indent or not l.strip().startswith("- "):
                    break
                lst.append(coerce(l.strip()[2:]))
                i += 1
            return lst, i
        m = re.match(r"^([^:]+):\s*(.*)$", stripped)
        if not m:
            i += 1
            continue
        key = m.group(1).strip().strip('"')
        val = m.group(2).strip()
        if val:
            out[key] = coerce(val)
            i += 1
            continue
        j = i + 1
        sub = []
        while j < len(lines):
            l = lines[j]
            if not l.strip():
                sub.append(l)
                j += 1
                continue
            ind = len(l) - len(l.lstrip())
            if ind <= base_indent:
                break
            sub.append(l)
            j += 1
        child_indent = next((len(l) - len(l.lstrip()) for l in sub if l.strip()), None)
        out[key] = None if child_indent is None else parse_block(sub, child_indent)[0]
        i = j
    return out, i


def spec_channels(look):
    """Expand one `look:` into {channel: level}. None => not a parseable look."""
    if look is None or look == "BLACKOUT":
        return {}
    if isinstance(look, str):
        return None  # e.g. "ABSENT (new cue -- create with record_cue)"
    res = {}
    for gname, gval in look.items():
        if gname == "ladders":
            for color, cfg in gval.items():
                if "boom_levels" in cfg:
                    for boom, lvl in cfg["boom_levels"].items():
                        res[boom_ch(color, boom)] = lvl
                elif "booms" in cfg:
                    for boom in cfg["booms"]:
                        res[boom_ch(color, boom)] = cfg.get("level")
                elif "level" in cfg:
                    for b in BOOM_ORDER:
                        res[boom_ch(color, b)] = cfg["level"]
            continue
        if gname not in GROUPS:
            continue
        gmap = GROUPS[gname]
        if not isinstance(gval, dict):
            tgt = gmap[None]
            for c in (tgt if isinstance(tgt, list) else [tgt]):
                res[c] = gval
            continue
        if isinstance(gval.get("levels"), dict):
            for member, lvl in gval["levels"].items():
                key = member if member in gmap else coerce(str(member))
                if key in gmap:
                    res[gmap[key]] = lvl
            continue
        lvl = gval.get("level")
        at = gval.get("at")
        if at is None:
            tgt = gmap.get(None)
            targets = ((tgt if isinstance(tgt, list) else [tgt])
                       if tgt is not None else list(gmap.values()))
            for c in targets:
                res[c] = lvl
        else:
            for member in (at if isinstance(at, list) else [at]):
                key = member if member in gmap else coerce(str(member))
                if key in gmap:
                    res[gmap[key]] = lvl
    return res


def load_spec(path):
    lines = open(path).read().split("\n")
    starts = [(i, re.match(r'  - number: "([0-9.]+)"', l).group(1))
              for i, l in enumerate(lines)
              if re.match(r'  - number: "([0-9.]+)"', l)]
    out = {}
    for idx, (i, num) in enumerate(starts):
        end = starts[idx + 1][0] if idx + 1 < len(starts) else len(lines)
        block, _ = parse_block([l[4:] for l in lines[i:end]], 0)
        out[num] = {"look": block.get("look"), "line": i + 1, "end": end}
    return out


def load_capture(path):
    lines = open(path).read().split("\n")
    starts = [(i, re.match(r'    - number: "([0-9.]+)"', l).group(1))
              for i, l in enumerate(lines)
              if re.match(r'    - number: "([0-9.]+)"', l)]
    out = {}
    for idx, (i, num) in enumerate(starts):
        end = starts[idx + 1][0] if idx + 1 < len(starts) else len(lines)
        block = lines[i:end]
        chans, cur = {}, None
        for l in block:
            m = re.match(r"\s*- channel: (\d+)", l)
            if m:
                cur = int(m.group(1))
                chans[cur] = {}
                continue
            m = re.match(r"\s+(level|hue|sat): ([0-9.]+)", l)
            if m and cur is not None:
                chans[cur][m.group(1)] = float(m.group(2))
        out[num] = {
            "chans": chans,
            "line": i + 1,
            "end": end,
            "has_look": any(re.match(r"\s+look:", l) for l in block),
            "partial": any("partial: true" in l for l in block),
        }
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--workspace", default=os.path.join(
        os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "task", "workspace"))
    ap.add_argument("--max-cue", type=float, default=5.0)
    args = ap.parse_args()

    spec = load_spec(os.path.join(args.workspace, "show-current.yaml"))
    cap = load_capture(os.path.join(args.workspace, "show-captured-live-20260625.yaml"))

    cues = sorted((n for n in cap if float(n) < args.max_cue), key=float)
    print(f"# review-ld-001 -- spec vs capture, cues < {args.max_cue}")
    print(f"# workspace: {args.workspace}\n")

    for n in cues:
        c = cap[n]
        if not c["has_look"]:
            print(f"{n:>5}  NO LOOK IN CAPTURE (list metadata only)")
            continue
        if not c["chans"]:
            print(f"{n:>5}  READ RETURNED NO CHANNELS -- cue was not audited (not 'dark')")
            continue
        sc = spec_channels(spec[n]["look"]) if n in spec else None
        if sc is None:
            print(f"{n:>5}  spec look is not a channel look: {spec[n]['look']!r}")
            continue
        cc = {ch: v["level"] for ch, v in c["chans"].items() if v.get("level")}
        lit_unspecced = sorted(ch for ch in cc if ch not in sc)
        dark_specced = sorted(ch for ch in sc if ch not in cc and (sc[ch] or 0) > 0)
        off_ratio = []
        for ch in sorted(set(sc) & set(cc)):
            if sc[ch] and cc[ch] < 99 and not (1.15 <= cc[ch] / sc[ch] <= 1.45):
                off_ratio.append((ch, sc[ch], cc[ch], round(cc[ch] / sc[ch], 2)))

        flag = " [capture marked partial:true]" if c["partial"] else ""
        print(f"{n:>5}  spec L{spec[n]['line']}  capture L{c['line']}{flag}")
        if lit_unspecced:
            print("       LIT, NOT IN SPEC:")
            for ch in lit_unspecced:
                # ch2 is the audience-stair safety light: intentionally on in every
                # cue including blackouts, so a BLACKOUT spec surfaces it here.
                # Declared non-finding N2 -- never a defect.
                tag = "   <- NON-FINDING N2 (safety light)" if ch == 2 else ""
                print(f"         ch{ch:<4} @{cc[ch]:<6g} "
                      f"{CHANNEL_NAME.get(ch, '(unmapped channel)')}{tag}")
        if dark_specced:
            print("       SPECCED, DARK ON CONSOLE:")
            for ch in dark_specced:
                print(f"         ch{ch:<4} spec {sc[ch]}  {CHANNEL_NAME.get(ch, '(unmapped)')}")
        if off_ratio:
            print("       LEVEL NOT EXPLAINED BY THE x1.3 BUMP:")
            for ch, s, cv, r in off_ratio:
                print(f"         ch{ch:<4} spec {s} -> captured {cv:g}  (x{r})")
        if not (lit_unspecced or dark_specced or off_ratio):
            print("       clean")
        print()

    first_ls = next((n for n in sorted(spec, key=float)
                     if isinstance(spec[n]["look"], dict) and "lonestars" in spec[n]["look"]), None)
    print(f"# first cue whose spec contains a `lonestars:` group: {first_ls}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
