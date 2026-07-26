# Work item — turn the 2026-06-26 cue notes into something we can actually run

**Show:** *DareToDream2026*, ETC Eos console.
**Filed:** 2026-06-26, after the operator walked the show and called out changes.

## What happened

I sat with the operator through a run and captured everything he wanted changed.
Those are written up in `workspace/cue-notes-20260626.md` — one section per item,
his exact words plus my read on what he meant. **Nothing has been applied to the
console yet.** The notes are a capture, not a plan — I wrote each section as I went,
in the order he said things, and I have not been back over them since.

## What I need

A single document I can hand to a model that has the lighting MCP server connected
to the console, and have it work through the whole list in one console session
without me re-explaining anything.

Concretely, that document has to be **executable by that model, not by a human
who already knows the rig**:

- The model driving the console will have the MCP tools and this document. Assume
  it will *not* be going back to the rig files mid-session to look things up, and
  that it does not know our booth vocabulary.
- It should be able to read a step and know which tool to call. Our tool surface
  is documented in `workspace/repo-README.md`; don't invent tools we don't have.
- I want to be able to hand it over and walk away with the operator, so anything
  that needs the operator's eye or a decision from him has to be obvious up front,
  not discovered halfway through.

## Constraints (things we already know, don't rediscover them)

- **Song 8 is out of scope for this pass.** The operator pulled it — he wants to
  sit with the end-of-8 sequencing himself. Whatever the notes say about Song 8,
  it is not part of this document.
- **Command-line cue editing (`Cue X Chan Y At Z`) does NOT store on this
  console.** We established this on 2026-06-25 — it looks like it took, and the
  change is gone the next time you fire the cue. Anything that has to persist has
  to go through the typed tools.
- This may end up being run against the **live show console**, not Nomad. Bear
  that in mind.
- The notes cross-reference some of my other working documents (punchlists,
  earlier delta lists). Those are not part of this handoff — work from what the
  notes restate inline, and say so if something is genuinely unresolvable
  without them.

## Reference material in `workspace/`

| file | what it is |
|---|---|
| `cue-notes-20260626.md` | the operator's changes, captured 2026-06-26 — **the input** |
| `rig.yaml` | semantic rig map: what every channel is, by meaning |
| `venue.yaml` | the MCP server's connection config + named channel groups |
| `show-map-README.md` | the show-map vocabulary and per-cue schema |
| `repo-README.md` | the lighting MCP server's README, incl. the full tool table |
| `verification.md` | live-hardware verification checklists, incl. what is still unconfirmed |

Deliverable is the document itself. Don't drive the console — there's no console
attached to you.
