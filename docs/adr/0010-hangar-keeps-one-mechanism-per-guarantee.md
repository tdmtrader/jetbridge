---
status: accepted
date: 2026-10-08
amends: ADR-0009 (keys, refcounts, reclamation, the control-key generation); ADR-0005 (the delete precondition)
---

# Hangar keeps one mechanism per guarantee

ADR-0009 cut Hangar from seven binaries and twenty-two tables to two and nine.
What remained was still built for threats and scale the deployment does not
have: four HMAC key kinds, two refcounts (claims and read leases), a reclaim
workflow with jobs, attempts and four outcomes, a resumable budgeted orphan
sweep, a control-key generation that no longer put anything in service, ed25519
signatures on acknowledgements that already travel over mTLS, and a
checksummed, versioned, quarantining envelope around a marker file the daemon
itself had just written by fsync and rename. The owner's ruling: cut to the
minimum Hangar needs; cutting too much and restoring a feature beats carrying
complexity nobody can account for.

We cut it to one mechanism per guarantee:

- **One marker format.** A step marker and an execution record are one JSON
  file each, written temp-file → fsync → rename → directory fsync. A record
  that does not decode refuses the one execution it names; the source ledger
  answers `unavailable` for the directory. No envelope, checksum, record
  version, quarantine or fault-injection seam. An object marker is the
  `hangar-output-*` metadata keys with no version key; an object with none of
  them is unmanaged, a malformed one is corrupt.
- **One refcount.** A claim is the only hold on a generation. A consumer's
  claim has no expiry; a reader's claim expires on the database clock at the
  materialization timeout plus a fixed margin. Read leases, fences, renewal
  and the lease cleaner are gone. A read warrant binds the reader's claim id,
  ref, destination, node and the claim's own window; two mints of one claim
  are byte-identical, so there is no nonce.
- **One reclaim action.** The pass holds the lifecycle row and the tree lock,
  deletes the exact generation, and stamps `reclaimed_at` in the same
  transaction when the store answers deleted, already-absent or
  generation-conflict (the exact generation is gone in all three); an
  unauthorized delete records an integrity finding and leaves the row;
  timeout and infrastructure leave it for the next pass. A lifecycle row is
  registered or reclaimed, nothing else. The orphan sweep is one bounded pass
  from the start of the listing each interval. The delete precondition is
  the generation alone; the recorded metageneration is not a precondition.
- **No control-key generation and no signed acknowledgements.** The node
  daemon is inside the trusted computing base and reached over mTLS from the
  web alone (ADR-0009), so an acknowledgement is a response on that channel,
  not a signed statement. The `activation_epoch` columns, the marker key, the
  scope-v1 derivation, the control-key ring and the ed25519 signing are gone.
  A result binds as a Run input while its Run succeeded and its claim is live.
  The Run contract's own activation epoch is untouched.
- **One warrant key.** `hangar.key` signs every warrant the daemon verifies —
  result read, base execution control, capture control — under one canonical
  encoding whose first field is the purpose; a route admits only its own
  purpose. The run-input signing key stays web-only:
  a node holding it could forge an input grant for any team's Run.
- **No vocabulary without a live path.** Deferred exports, numbered citations
  to a requirements list not in the tree, and comments describing removed
  mechanisms are gone; the glossary names only what runs.
- **One tree path.** The strict-input namespace, its daemon service and
  routes, the `materialize-input` warrant purpose, the
  `concourse.dev/hangar-v1` label, the disk store's input role and the
  chart's `artifactDaemon.hangar.{store,bucket,endpoint,prefix,enabled,
  webEnabled,scratchPath,scratchSizeLimit,maxContentBytes,maxEntries}`
  values are gone. A Run input is an input publication in the output
  namespace and reaches a task as a managed read, under a read warrant
  bound to the reader's claim; the `.hangar-materialized` receipt file
  stays, written by the managed read and checked by the managed-input init.
  The strict path had no product caller: every bound input already read
  through the output plane.

## Consequences

- Six tables: captures, claims, enabled, lifecycles, input publications,
  integrity findings. Migrations 1789793156 and 1789793157; both down
  migrations are lossy and say so.
- Two Hangar Secrets (`hangar.key` and the web-only run-input key) instead of
  five; the chart loses eight values.
- A read's hold now expires silently; a crashed reader pins its generation
  for at most the materialization timeout plus five minutes.
- A corrupt record on a node is found at the point of use, not at startup;
  the daemon no longer withholds readiness for it.
- A key change invalidates in-flight warrants and nothing else; earlier
  results stay bindable.
- Two namespaces, cache and output, checked unequal at startup; three
  warrant purposes, read, execution control and output capture; the
  daemon's `--hangar-key` goes with `--execution-control` alone, and the
  web's `--kubernetes-hangar-key` with `--kubernetes-hangar-output-enabled`.
- ADR-0002 stands (the cache fails open, Hangar fails closed, separate
  namespaces). ADR-0005's storage contract stands; its delete precondition is
  the generation alone. ADR-0009 stands except as amended above.
- Accepted losses: no confirmed-versus-inferred distinction on a reclamation;
  no replay refusal for a read warrant beyond its window (the init
  container retries after a 503 with the same token); no node-side readiness
  gate for a corrupt ledger.
