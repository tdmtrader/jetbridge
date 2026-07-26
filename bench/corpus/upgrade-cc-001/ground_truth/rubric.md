# Behavioral rubric — upgrade-cc-001 (go-jose v3 → v4)

Score the agent's change against intent, not against diff similarity with
`reference.diff`. The upstream reference is one valid solution; several of the
items below have more than one acceptable form and those are called out.

The mechanical gate (see `case.yaml#grading`) already answers "did it compile and
did the unit suites stay green". This rubric exists because the mechanical gate
**cannot** see the two things that actually matter semantically (items 3 and 4).

## A. Goal achieved (mechanical, but restate for the judge)

1. **No `go-jose/v3` left.** No non-vendored `.go` file imports
   `github.com/go-jose/go-jose/v3` or any package under it, and `go.mod` no
   longer requires it. Partial migrations that leave both majors linked in fail
   the request outright.
2. **Everything still builds.** `go build ./atc/... ./skymarshal/... ./fly/...`
   succeeds and the test packages compile, including `testflight/` (which cannot
   be *run* here). An agent that only fixes the packages it happens to run tests
   for has not finished — `testflight/idtoken_test.go` and
   `fly/integration/suite_test.go` are easy to miss.

## B. The two API breaks (must be handled, not worked around)

3. **`jwt.ParseSigned` gained a required second parameter** — the caller must
   now declare which signature algorithms it is willing to accept. Every call
   site must pass a list, and the list must be *narrow and correct for that call
   site*:
   - `skymarshal/token/access_token.go` (`claimsParserNoVerify.ParseClaims`)
     parses **Dex-issued** ID tokens. Dex signs with **RS256**, so the list must
     be `RS256` (equivalently, some constant that resolves to it). Dex does not
     export the constant, so the correct value has to be established by reading
     Dex, not guessed.
   - `atc/creds/idtoken` call sites parse tokens this codebase itself issued;
     the right list is derived from the generator's own configuration
     (`idtoken.DefaultAlgorithm`, or the generator's `Algorithm` field where the
     test configures a non-default one) rather than hard-coded.
   - `testflight/idtoken_test.go` parses one default-algorithm token and one
     explicitly `ES256`-configured token; the two call sites must not use the
     same list.
   - **Fail this item** for any of: passing every algorithm go-jose supports;
     passing a wide list "to be safe"; passing `none`; introducing a helper that
     accepts whatever algorithm the token header claims. Each of those compiles
     and passes every runnable test while silently widening what Concourse will
     accept — exactly the behaviour change the request forbids.
4. **`jwt.Builder.CompactSerialize` was renamed to `Serialize`** in
   `atc/creds/idtoken/token_generator.go`. Accept the rename. **Fail** any
   attempt to keep the old name alive by hand-rolling compact serialization, or
   by switching to `FullSerialize`/JSON serialization — the wire format of
   issued ID tokens must stay compact JWS.

## C. Collateral the migration forces

5. **The dead helper.** `skymarshal/token/token_suite_test.go` contains a
   `parse()` function that nothing calls and that will not compile under v4.
   Deleting it (upstream's choice) and repairing it to the new signature are
   *both* acceptable; the pass condition is that the file compiles and no other
   test loses coverage. Deleting is slightly preferred — the agent noticing it is
   unreachable is a small positive signal. Also accept the follow-on cleanup of
   the now-unused `time` / `rsa` imports.
6. **Generated fakes.** `atc/db/dbfakes/fake_signing_key.go` and
   `fake_signing_key_factory.go` embed `jose.JSONWebKey` in their signatures and
   must move to v4 or the package stops satisfying `db.SigningKey`. Either
   re-running counterfeiter or a mechanical import-path edit is acceptable.
   Prefer the import-path edit if re-running the generator would drag in
   unrelated churn from a different counterfeiter version (upstream's run did
   exactly that and rewrote ~90 unrelated fakes). **Fail** only if a fake was
   hand-edited into something the generator would not produce, or if the fakes
   were left on v3.

## D. Discipline

7. **Scope.** No behaviour changes outside the migration, no reformatting pass,
   no other dependency bumps. `go.mod`/`go.sum` churn that `go mod tidy` produces
   on its own is fine (upstream's run also collapsed
   `go 1.24.2` + `toolchain go1.25.0` into `go 1.25.0` and dropped two now-unused
   indirect requirements — accept, but do not require).
   Note that upstream's PR also added `atc/util/seq_generator_test.go`, an
   unrelated drive-by. It is deliberately excluded from `reference.diff`; an
   agent that adds it has gone out of scope, and an agent that does not has done
   nothing wrong.
8. **The write-up.** `task.md` asks for `UPGRADE-REPORT.md` at the repository
   root; that file is the `report` output port, so a change delivered without it
   is incomplete. Note that `reference.diff` contains **no** such file — the
   humans wrote no report — so grade this item on the report's own content, never
   on similarity to the reference.
   The strongest reports say which suites were actually run, and flag that
   `testflight/` and the Dex-signed path in `skymarshal/token` are not covered by
   any runnable test here — so the algorithm choice in item 3 rests on reading
   Dex, not on a green suite. An agent that claims "all tests pass, therefore the
   migration is verified" is overclaiming; dock it.

## Scoring shape

- Items 1, 2, 4 are pass/fail and are also caught mechanically.
- Item 3 is the discriminating item. It is worth as much as everything else
  combined: it is the only part of this task that requires reading a *different*
  project's source to answer, and no test in this repository will catch a wrong
  answer.
- Items 5–8 are quality signals; a change that satisfies 1–4 and misses 5–8 is
  still a usable upgrade. One exception inside item 8: the *presence* of
  `UPGRADE-REPORT.md` is pass/fail because the task asks for it by name and it is
  a declared output port — its *content* (what was run, what stayed unverified)
  is the quality signal.

## Mechanical caveats the judge must carry

- **A green gate is necessary, not sufficient.** The gate in `case.yaml#grading`
  answers items 1, 2 and 4 and is blind to item 3: the wrong algorithm compiles
  and leaves every runnable suite green (measured — see `answer.md`). Never
  record a mechanical pass alone as a case pass; item 3 has to be read off the
  diff.
- **Credit the reasoning, not the token.** This case is harvested from a public
  pre-cutoff PR (`memorization_risk: high`), so a correct `RS256` may be recalled
  rather than derived. Score item 3 on the evidence the agent gives for the
  choice — where it established what the issuer signs with, and why that call
  site's list is narrow — and treat a bare correct constant with no account of
  how it was reached as a weak pass. Quoting or restating a source is not itself
  the signal; the causal chain from evidence to the per-call-site list is.
- **Where the fix goes is not pinned.** The gate asserts the *goal* (no v3
  anywhere, tree builds, suites green), not a file list. Any placement that
  satisfies items 1–6 is acceptable, including a narrow allow-list expressed via
  a shared constant instead of a literal at each call site — provided the
  resulting per-site sets are the same ones item 3 requires.
