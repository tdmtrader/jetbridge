# Bug: `read_stage` throws a parse error on busy looks

**Reported:** 2026-06-25, from this evening's cue-building session on the venue console
**Severity:** high — `read_stage` is the only way to see what is actually live, and it
fails on exactly the looks worth looking at

## What happened

Mid-session, building the 5.x / 6.x cue chain, `read_stage` stopped returning anything.
Every call against the crowded looks came back as a tool error instead of a channel list:

```
read stage: eos: cannot parse channel "10.." in active-channel string
"2,7-8,10-11,14-15,17-18,21-22,24-25,28-29,31-32,35-36,38-39,42-43,45-46,56-57,59-60,76-78,80-85,10.."
```

It is not intermittent and it is not a timeout — the same look fails the same way every
time, immediately.

The thing that makes it usable-but-broken rather than just broken: **small looks read
fine.** A handful of channels up and `read_stage` answers correctly, every time. It only
falls over once a lot of the rig is live. The failing look above is a real one (cue 6.1 —
desires, ladders, back pars, the circle o'clock specials, plus a couple of lonestars).

Nothing else in the read path changed today, and the other read tools
(`get_channel_level`, `get_color`) still answer for individual channels on the same look
— it is specifically the whole-stage read that dies.

## Impact

This is the tool I use to check my work before recording a cue. Right now, on any cue
worth checking, I get nothing back at all: no channels, no levels, no colors — just the
error. I ended up eyeballing the console's own display and hand-transcribing channel
numbers into the session notes for the rest of the night.

## Expected behavior

`read_stage` stays usable on a busy look — on a crowded cue I need to see what is live,
not a tool error. What I must not get is a *silently* short answer that I mistake for the
real live set: if what comes back is not the whole live set, the result has to say so.

Small/complete looks must keep reading exactly as they do now, and I don't want this tool
quietly inventing a stage state out of junk.

## Constraints

- The venue console is not available for a long debugging session. Work out what is
  happening from the captured string above and from the code; a fix I can confirm in one
  short live pass is worth more than one that needs the console all evening.
- TDD per `forge/workflow.md`: the change needs a test against the in-repo fake
  (`internal/osctest`, or the mock transport in the `eos` tests) that fails without it.
  No hardware in CI.
- Full gate green: `make test` (`go test -race ./...`), `make vet`, `make fmt-check`.
- Don't regress the existing `read_stage` / `ActiveChannels` tests — in particular the
  blackout case, the delayed-feedback case, and the existing `partial` behaviour when a
  channel goes silent during the per-channel reads.
