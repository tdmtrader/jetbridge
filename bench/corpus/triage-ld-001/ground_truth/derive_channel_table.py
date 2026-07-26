#!/usr/bin/env python3
"""Adjudication tool for triage-ld-001.

Re-derives the authoritative ladder boom -> channel table (and the other named
groups the plan resolves) straight from the *sealed* rig.yaml, so a grader can
settle any channel claim in a candidate execution plan in about a second.

Stdlib only -- PyYAML is not installed on the curation machine, so this carries a
tiny parser for the flat subset of YAML rig.yaml actually uses.

Run from anywhere:

    python3 bench/corpus/triage-ld-001/ground_truth/derive_channel_table.py
    python3 .../derive_channel_table.py --check   # assert the reference plan's table

The single fact that matters: `ladders.boom_order: [1, 3, 5, 2, 4, 6]` means the
six channels listed for a colour are given IN BOOM ORDER, i.e. list index 0 is
boom 1, index 1 is boom 3, index 2 is boom 5, index 3 is boom 2, index 4 is
boom 4, index 5 is boom 6.  Inverting that is the whole trap: a plan that assumes
"boom N = index N-1" resolves "white booms 3/4" to 65+66 instead of 64+67.
"""

import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
RIG = os.path.join(HERE, os.pardir, "task", "workspace", "rig.yaml")

# What the reference plan (ground_truth/answer-artifact.md section 2) asserts.
# colour -> [boom1..boom6]
REFERENCE_TABLE = {
    "blue":    [7, 10, 8, 11, 9, 12],
    "yellow":  [14, 17, 15, 18, 16, 19],
    "green":   [21, 24, 22, 25, 23, 26],
    "lt_blue": [28, 31, 29, 32, 30, 33],
    "orange":  [35, 38, 36, 39, 37, 40],
    "pink":    [42, 45, 43, 46, 44, 47],
    "red":     [49, 52, 50, 53, 51, 54],
    "dk_blue": [56, 59, 57, 60, 58, 61],
    "white":   [63, 66, 64, 67, 65, 68],
}

REFERENCE_GROUPS = {
    "house":            [3, 4],
    "audience_stair":   [2],
    "trough":           [70, 71],
    "circle_ring":      [80, 81, 82, 83, 84, 85],
    "center_specials":  [87, 88],
    "lone_stars":       [108, 109, 110, 111, 112],
    "desires":          [113, 114, 115, 116, 117, 118, 119],
}


def strip_comment(s):
    out, in_str = [], False
    for ch in s:
        if ch == '"':
            in_str = not in_str
        if ch == "#" and not in_str:
            break
        out.append(ch)
    return "".join(out)


def parse_rig(path):
    """Return {section: {key: value}} for the flat two-level subset rig.yaml uses."""
    tree, section = {}, None
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            line = strip_comment(raw.rstrip("\n"))
            if not line.strip():
                continue
            indent = len(line) - len(line.lstrip(" "))
            body = line.strip()
            if indent == 0 and body.endswith(":"):
                section = body[:-1]
                tree[section] = {}
                continue
            if indent == 0:
                continue
            if section is None or ":" not in body:
                continue
            key, _, val = body.partition(":")
            tree[section][key.strip()] = parse_value(val.strip())
    return tree


def parse_value(val):
    if val.startswith("[") and val.endswith("]"):
        inner = val[1:-1].strip()
        return [int(x) for x in inner.split(",") if x.strip()] if inner else []
    if val.startswith("{") and val.endswith("}"):
        out = {}
        for part in val[1:-1].split(","):
            if ":" not in part:
                continue
            k, _, v = part.partition(":")
            out[k.strip()] = int(v.strip()) if v.strip().lstrip("-").isdigit() else v.strip()
        return out
    if re.fullmatch(r"-?\d+", val):
        return int(val)
    return val


def boom_table(rig):
    ladders = rig["ladders"]
    boom_order = ladders["boom_order"]                # [1, 3, 5, 2, 4, 6]
    # index_of[boom] -> position in the colour's channel list
    index_of = {boom: i for i, boom in enumerate(boom_order)}
    table = {}
    for colour, chans in ladders.items():
        if colour == "boom_order" or not isinstance(chans, list):
            continue
        table[colour] = [chans[index_of[b]] for b in (1, 2, 3, 4, 5, 6)]
    return table, boom_order


def named_groups(rig):
    front, back = rig["front"], rig["back"]
    specials, leds = rig["specials"], rig["leds"]
    circle = specials["circle"]
    return {
        "house":           list(front["house"]),
        "audience_stair":  [front["audience_stair"]],
        "trough":          [back["trough"]["sl"], back["trough"]["sr"]],
        "circle_ring":     [circle[k] for k in sorted(circle, key=lambda x: int(x))],
        "center_specials": [specials["center_a"], specials["center_b"]],
        "lone_stars":      [leds["lone_star"][k] for k in ("dsl", "dsr", "hsl", "hcs", "hsr")],
        "desires":         [leds["desires"][k]
                            for k in ("usl", "sl", "dsl", "dcs", "dsr", "sr", "usr")],
    }


def main():
    check = "--check" in sys.argv
    rig = parse_rig(RIG)
    table, boom_order = boom_table(rig)
    groups = named_groups(rig)

    print(f"boom_order = {boom_order}   (list index i  ->  boom {boom_order})")
    print()
    print("Ladders -- channel by BOOM number")
    print("colour   |  b1  b2  b3  b4  b5  b6  | raw list order")
    print("---------+--------------------------+----------------")
    for colour in ("blue", "yellow", "green", "lt_blue", "orange",
                   "pink", "red", "dk_blue", "white"):
        row = table[colour]
        raw = rig["ladders"][colour]
        print(f"{colour:8s} | " + "".join(f"{c:4d}" for c in row)
              + "  | " + " ".join(str(c) for c in raw))
    print()
    print("Other named groups")
    for name in ("house", "audience_stair", "trough", "circle_ring",
                 "center_specials", "lone_stars", "desires"):
        print(f"  {name:16s} {groups[name]}")
    print()
    print("Spot checks the plan's tasks depend on:")
    print(f"  T3  white booms 3+4          -> {table['white'][2]} + {table['white'][3]}")
    print(f"  T11 pink ladders all booms   -> {sorted(rig['ladders']['pink'])}")
    print(f"  T19 boost booms 1/2 (yellow) -> {table['yellow'][0]}, {table['yellow'][1]}")
    print(f"  T19 cut booms 3-6   (yellow) -> "
          f"{table['yellow'][2]}, {table['yellow'][3]}, {table['yellow'][4]}, {table['yellow'][5]}")

    if not check:
        return 0

    failures = []
    for colour, expected in REFERENCE_TABLE.items():
        if table.get(colour) != expected:
            failures.append(f"ladders.{colour}: derived {table.get(colour)} != plan {expected}")
    for name, expected in REFERENCE_GROUPS.items():
        if groups.get(name) != expected:
            failures.append(f"{name}: derived {groups.get(name)} != plan {expected}")
    print()
    if failures:
        print("CHECK FAILED")
        for f in failures:
            print("  " + f)
        return 1
    print("CHECK OK -- every channel in the reference plan's section 2 table "
          "re-derives from rig.yaml.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
