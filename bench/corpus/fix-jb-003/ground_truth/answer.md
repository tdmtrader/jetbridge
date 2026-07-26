# Root cause and the change that was merged

Terminal artifact: `a07237787d05da53f7a44fb5eba077e79052faac`
Pre-state: `3f4f161e10ce33ae17663e556c285fd347edd1d8`

## Root cause

`harvest.StampTrailer` (`agent/harvest/trailer.go`) adds the `Agent-Ticket:
<id>` trailer by rewriting the tip commit's message with `git commit --amend
--no-verify -m <msg>`, executed through the package-local helper
`trailerGitRun`.

`trailerGitRun` builds the `exec.Cmd` with `cmd.Dir` set and **no `cmd.Env`**,
so the child `git` inherits whatever identity the ambient environment happens
to supply. Every other git command harvest issues is a read (`log`,
`rev-parse`, `diff`) or a `push` — none of which need a committer identity.
`--amend` is the *only* place harvest creates a commit object, and creating a
commit object requires an identity.

The harvest pod is built from `deploy/agent-runner/Dockerfile`, which installs
`git` and configures no identity. So in production the amend failed with

```
Committer identity unknown
```

and `StampTrailer` returned an error. The call site
(`agent/harvest/runner.go`, the `if cfg.TicketID > 0` block) treats that as
best-effort — it records `facts.TrailerErr` and continues — precisely so a
stamping failure never sinks an otherwise-good delivery. The consequence is
that **every trailer had been silently degrading to the error path in
production**, defeating the SCM-agnostic delivery-detection backstop that
`docs/superpowers/specs/2026-07-20-platform-owned-merge-design.md` §5 exists
to provide.

Every developer's `~/.gitconfig` masked the defect completely: locally, the
amend succeeded, so the four specs that go through it passed. Only the bare
container exposed it. CI build 587725 is the first environment that ever ran
the code.

## The fix

`trailerGitRun` sets its own deterministic identity on every invocation
rather than inheriting one:

```go
cmd.Env = append(os.Environ(),
    "GIT_AUTHOR_NAME="+BotName,
    "GIT_AUTHOR_EMAIL="+BotEmail,
    "GIT_COMMITTER_NAME="+BotName,
    "GIT_COMMITTER_EMAIL="+BotEmail,
)
```

with new package constants mirroring the identity already established
elsewhere in the tree (`agent/api/outcomes.BotAuthor`,
`agent/merge.BotName`/`BotEmail`, `agent/gitcheck.BotAuthor`):

```go
const (
    BotName  = "concourse-agent[bot]"
    BotEmail = "agent@concourse.invalid"
)
```

That makes the behaviour environment-independent, which was the actual
requirement — the container is fixed *and* the laptop now behaves identically
to the container.

## The subtlety that makes this case discriminating

`git` prints its own remediation hint in the very error text the report
carries:

> `git commit --amend --reset-author`

Taking that advice makes CI green and is **wrong**. `--amend` on its own
*preserves the original author* (verified empirically; also confirmed in this
case's curation probe: amending under a different ambient identity left `%an`
untouched while `%cn` changed). Preservation is load-bearing:
`00-shared-contracts.md` §1.11 defines the human-touch delta as the numstat
over commits **whose author is not `concourse-agent[bot]`**. If the stamp
reset the author, every human-authored commit that passed through harvest
would be re-attributed to the bot, and the human-touch delta — and therefore
`merged_with_fixes` — would silently read zero forever. Setting
`GIT_AUTHOR_*` alongside `GIT_COMMITTER_*` is safe for the same reason:
`--amend` ignores `GIT_AUTHOR_*` in favour of the existing commit's author.

So the correct fix has two halves that pull in opposite directions:
make the committer deterministic, and leave authorship alone.

## Alternatives that are also correct

The merged change used `GIT_COMMITTER_*` env vars. Equivalent and acceptable:

- `git -c user.name=... -c user.email=... commit --amend ...`
- `cmd.Env` with only `GIT_COMMITTER_NAME` / `GIT_COMMITTER_EMAIL` set
  (setting `GIT_AUTHOR_*` too is harmless but not required for `--amend`)

Not acceptable: writing to `~/.gitconfig` or running `git config --global`
(mutates shared state outside the workspace), configuring the identity in the
Dockerfile or the CI task instead of in the code (does not make the *behaviour*
environment-independent — the task explicitly rules this out), or anything
using `--reset-author`.
