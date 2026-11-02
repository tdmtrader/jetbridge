# JetBridge 0.4.2

JetBridge 0.4.2 follows jb-0.4.1 (d2fbd85155). It fixes two problems with upgrading a deployment configured differently from the home cluster: resource version history appearing lost, and SSO-only setups that the chart refused.

## Fixes

- **Resource version history survives the upgrade.** 0.4.0 made global resources unconditional. A deployment that ran with them off, upstream's default, kept each resource's versions in a scope of its own. The web then started an empty global scope, so every resource's version history appeared lost. Migration `1789793167` folds each per-resource scope into its config's global scope and keeps its versions:
  - A config with no global scope promotes its most recently checked per-resource scope.
  - A deployment that already ran 0.4.0 or 0.4.1 gets its old history merged under the global scope it started, and the versions found since stay newest.
  - Resource types that ask for a unique version history, such as `time`, are left alone.
  - Build history, pins and disabled versions are keyed by resource and version, so they carry over unchanged.
  - A database that ran with global resources on is untouched.
- **SSO-only deployments render.** `web.localUsers` and `web.mainTeamLocalUser` are no longer required. The main team still needs an owner, so the render fails unless `web.mainTeamLocalUser` is set or `web.env` names a `CONCOURSE_MAIN_TEAM_*` variable, such as `CONCOURSE_MAIN_TEAM_OIDC_GROUP`. The chart README shows an SSO-only values block.

## Chart (0.2.1 → 0.2.2)

- `web.localUsers` and `web.mainTeamLocalUser` are optional, with the main-team-owner rule above.

## Operator notes

- Upgrade straight from 0.3.1 to 0.4.2. A deployment that ran 0.4.0 or 0.4.1 with global resources previously off gets its history back on upgrade. The exception is a resource whose source changed since: its old config was retired, and its history with it.
- If two resources share an identical config (same type and source), they now share one version history. That is what global resources means.
