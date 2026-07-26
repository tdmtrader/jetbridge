# Bug: `set_look` doesn't replace the look on a real console

**Reported:** 2026-06-20, from the `venue_profile_20260619` hardware smoke
**Severity:** high — `set_look` is documented as "replaces the look", and it silently doesn't

## What happened

The hardware smoke for `set_look` is the last open item in the track plan. The venue
console (`192.168.1.145:52999`) was still offline, so I ran the smoke against **ETC
Nomad** (offline console software, sanctioned for smokes by `forge/workflow.md`) on
`127.0.0.1:3037`, TCP+SLIP.

Repro from the session:

1. Brought a prior look up by hand: channels `1 Thru 10` at 80.
2. Called the `set_look` tool with `channels: "3 + 4 + 5"`, `level: 50`.
3. **Expected:** only 3, 4 and 5 live at 50 — everything else out.
   **Got:** 1–10 still up at 80 *and* 3/4/5 now at 50 on top of them. The old look was
   never cleared; `set_look` behaved like a plain `set_channel_level`.

No error was returned. The call reported success and returned a look; the stage just
wasn't what the tool says it will be. It is not reliably reproducible — a repeat of the
same sequence sometimes does clear the prior look.

Other observations from the same session, in case they're related:

- `read_stage` intermittently reports an **empty stage** on this console even when
  channels are visibly up on the console's own display. A retry usually makes it
  converge. (Feedback flakiness on this rig isn't new — there's a note about it in
  the track cgx.)
- Once the target does come up, it comes up correctly — levels and the color command
  land fine. It's the *clearing* half of `set_look` that is unreliable.

## Expected behavior

`set_look` establishes a look in one call: after it returns, only the requested
channels are live, at the requested level (and color, when given). That has to hold on
real hardware, not just against the in-repo fake — every fake-backed test currently
passes, so the suite gives no warning about this.

## Constraints

- Keep `set_look` **reversible**: it must continue to record nothing (no cues, no subs).
- Keep the existing behavior that an **invalid channel selection is rejected before
  anything on stage changes** — a bad command must not be able to black out the room and
  then fail. There is a test for this; don't weaken it.
- Compose from the primitives the `eos` package already has. The server stays numeric;
  don't invent a new wire form or a new console feature, and don't add hardware to CI.
- TDD per `forge/workflow.md`: the change needs a test against the `osctest` fake that
  fails without the fix.
- Full gate must be green: `make test` (`go test -race ./...`), `make vet`, `make fmt-check`.
- Don't regress the existing `set_look` tests (replaces-prior-look, from-blackout,
  validates-before-changing) or the `set_look` MCP tool tests.

## Notes

I can't leave the console up much longer, so a fix that can be verified in the fake and
then confirmed in one short live pass is preferable to something that needs a long
hardware session to trust.
