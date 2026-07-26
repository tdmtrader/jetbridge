# Cue audit request — live console capture vs. the design reference

**Show:** DareToDream2026 · **Console:** ETC Eos, cue list 1
**Requested:** 2026-06-25, at the end of the console session

## Situation

I re-read the console live in this session and dumped what it actually has stored,
cue by cue, into `show-captured-live-20260625.yaml` (`captured_at: 2026-06-25T21:38:49Z`).

The design reference is `show-current.yaml` — the per-cue looks written in rig-group
vocabulary rather than raw channel numbers. That file is what each cue is *supposed*
to contain. Its header calls itself a capture because that is how it was originally
produced; treat it as the design reference for this audit — it is the last state I
was happy with.

The two files are not in the same shape. The reference is written in groups; the live
dump is raw channel numbers with levels (and hue/sat where the fixture is an LED).
`rig.yaml` and `channel-hookup.csv` are the translation between the two;
`show-map-README.md` documents the group vocabulary and how a `look:` is expressed.

## What I need

Go cue by cue through the range the capture covers and tell me **where the console
diverges from the design reference** — things that are lit that the design does not
call for, things the design calls for that are dark, wrong colour, wrong fixtures.

Give it to me as a ranked list, worst first, with the cue numbers and the channels,
and a one-line correction for each. I want to walk into the next session and work
top-down. If a whole class of problem repeats across many cues, say so as one item
with its blast radius rather than restating it once per cue.

Also tell me what you checked and found **clean** — I need to know which parts of the
top of the show I can stop worrying about.

## How to hand it back

Write it up as a single Markdown document called `AUDIT.md`, at the top level of the
files I gave you. That is the file I will have open in the booth.

Do not edit the show data itself — this pass is read-and-report only. I make the
console changes by hand next session, from your list.

## Scope

Cues **0.1 through 4.99**. That is what got read back today. Cue entries further
down the capture file are list metadata without a look; ignore them.

## Constraints you need to know before you start

These two are known and deliberate. Do **not** report them as problems — reporting
them wastes the session.

1. **The +30% level bump is baked into the stored cue values.** It is not a live
   layer sitting on top; it was recorded into the cues. The design reference is the
   *pre-bump* intent, so the console legitimately reads about **×1.3** on intensity
   almost everywhere (clipping at 100). Intensity differences that are explained by
   that factor are expected and are not findings. Work in live/actual numbers.
2. **Channel 2 is the audience-stair safety light.** It is intentionally at 100 in
   every cue, whatever the reference says. Never flag it.

## Notes on reading the capture

- A cue whose `look.channels` is an empty list means the read came back with nothing.
  That is my capture failing on that cue, not the console holding an empty cue.
- One cue's look carries `partial: true` — the read was cut short there, so its
  channel list may be incomplete.
- Gelled fixtures (the ladders, pars, circle, specials) are intensity-only — a colour
  word selects which channels, it is not a parameter on the fixture. Only the LED
  groups carry hue/sat.
