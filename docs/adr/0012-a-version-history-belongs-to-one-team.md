---
status: accepted
date: 2026-11-18
---

# A version history belongs to one team

A resource config scope holds a version history: the versions its checks
found and its puts saved, in check order, with their metadata and the last
check's timing. Until 0.4.6 a config's shared scope served every team whose
resources or resource types had that config, keyed on the config alone. One
team's check, put, history carry, `fly copy-resource-versions` or
`fly clear-versions` therefore wrote into another team's history, and its
metadata, last check and check build showed there. Guards that refused a carry
or copy into a history another team used covered only the resources and types
attached when the write ran, never puts or clear-versions, and never a team
that attached afterwards.

The owner's ruling: stop one team from attaching to another team's resource
history. So a scope belongs to one team. `resource_config_scopes.team_id`
names it, a team's shared scope of a config is found or created by
(config, team), and a unique index allows one live shared scope per pair. A
resource's unique-history scope and a deprecated scope carry their resource's
team. Every finder passes a team: a resource's or resource type's check its
own, a put the build's, a task `image_resource` check and a type-image fetch
in a build the build's, and lidar's native resolve the resource's or type's.
A resource type has no scope column; it reads its team's live shared scope of
its config through a join on the type's pipeline's team.

The store enforces it: a trigger on `resources` refuses any row pointing at a
scope of another team (`check_violation`), whatever path wrote it. That is
the invariant, not a check made at attach time, so a write path nobody
anticipated (an outgoing web of an older version during a rolling upgrade,
say) cannot attach a resource to another team's history.

Reads and writes by a version's row id are confined the same way. The API
names a version by its row id (get, pin, enable, disable, the builds it was
an input to or an output of, and the version list's page bounds), and each
now resolves the id only within the history of the resource the URL names.
An id from any other history, the same team's or another's, is a version
that resource does not have.

## Consequences

- Sharing within a team is unchanged: a team's identical resources and types,
  in any of its pipelines, share one history, and the guards that protect
  teammates' history (`ErrCopyIntoSharedScope`) stay.
- A carry and a copy always write into the team's own history, so the
  refusal for another team's history is gone.
- Each team checks a config it shares with another team on its own, so
  checks multiply by the number of teams sharing a config. The checking lock
  is still keyed on the config, so those checks take turns. That is an
  accepted coupling of timing only: no state crosses teams through it.
- Versions are stored once per team. A team's scope that nothing attaches to
  any more lives as long as its config does.
- Migration 1789793171 gives each team that used a shared scope its own copy
  of that scope's history (versions, check order and metadata). Build inputs
  and outputs, next build inputs, pins and disabled versions name a resource
  and a version digest, never a scope or version row, so they resolve in each
  copy. Rows a 0.4.6 pre-release carried into a history another team read are
  not marked, and are copied with the rest.
- The migration takes the resources, scopes and teams tables together, retrying
  instead of deadlocking against an outgoing web, and holds them until it
  commits. A multi-team deployment should upgrade with the web stopped
  (`web.strategy: {type: Recreate}`, or `web.replicas` at 0): an outgoing
  0.4.5 web's reads of the history block for the migration's duration, and
  afterwards its checks that attach no resource (resource-type checks, task
  image checks, native type resolves, type-image fetches) find a history by
  config alone and can write into another team's copy. Its resource checks
  and puts fail closed. There is no rollback to 0.4.5 without restoring the
  database.

## Excluded: resource caches stay cluster-wide

A resource cache is the fetched content of one version of one config with
given params, identified by config, version and params. It is not a history
and nothing attaches to it, and identical inputs produce identical bytes. So
it stays cluster-wide, as do worker resource caches and the artifact daemon's
node-local caches. Splitting it per team would mean keying configs by team
too, every team re-fetching every version. A team can invalidate a cache
another team also uses (`fly clear-resource-cache`), and can read a cache
another team's get filled, but only by naming the same config, version and
params.
