<!--
Observations from the instrumented `k8s-e2e` run that was triggered by the push
recorded at pre_state (44697823b4, "instrumentation in place; pushing to run
instrumented chain"). Transcribed from the run; conclusions and interpretation
removed. The raw Concourse build log itself was not archived, so this is the
observation set as recorded, not a byte-for-byte log dump. Error text is quoted
as it was captured, including the elision the original capture used.
-->

# Instrumented `k8s-e2e` run — observations

The instrumented chain was pushed to `origin/jetbridge` and the pipeline was
triggered. Two commits on that push added output specifically so this run could
be read:

- `ded0ca4ae7` — `ensureConcourseImage` now always logs the deployed image's id
  and creation time, as `Using Concourse image "<ref>": <id> created=<time>`.
- `e9de3901fe` — the behavioral suite's `AfterEach` now dumps the last 800 lines
  of `concourse-web` logs whenever a spec fails, so web-side diagnostics
  (`scope-deleted-during-check` / `scope-deleted-before-check` vs a raw
  `save versions:` build error) are visible in CI output.

Both are ancestors of the pushed branch head.

## Chain outcome

| Job | Build | Result |
|---|---|---|
| `build-kind-runner` | #177 | **succeeded** — rebuilt and pushed after the branch push landed |
| `k8s-integration-tests` | retry | **green**, 122/122 |
| `k8s-behavioral-tests` | — | **failed** |

## Behavioral failure

`runs a pipeline with custom resource types` failed again — but not on the same
FK path as build #100. The captured build error:

```
update resource config scope: set resource scope: ERROR: ... violates foreign key
constraint "resources_resource_config_scope_id_fkey" (SQLSTATE 23503)  -> errored
```

Attribution: `atc/exec/check_step.go:169` wrapping
`atc/engine/check_delegate.go:225` (`SetResourceConfigScope`) — i.e. the
`PointToCheckedConfig` path at `check_step.go:162-169`, which is one of the two
guarded surfaces.

For comparison, build #100's error was on the other guarded surface:

```
save versions: ERROR: insert or update on table "resource_config_versions"
violates foreign key constraint "resource_config_versions_resource_config_scope_id_fkey"
(SQLSTATE 23503)
```

## Searches performed against the run output

| Searched for | Added by | Matches |
|---|---|---|
| `Using Concourse image ` (image id + created time) | `ded0ca4ae7` | **0** |
| the on-failure `concourse-web` log dump (800 lines) | `e9de3901fe` | **0** — no web-side logs appear anywhere in the failure output |
| `Concourse image %q not found locally, building from source` (the string `ded0ca4ae7` replaced) | pre-existing | **0** |

Note on the third row: that pre-existing line was only emitted when the image was
absent, so its absence on a run where the image is present carries no
information either way.

## Not available

- The raw build log for #100 beyond the error text already quoted in the
  diagnostic record.
- Any web-side (`concourse-web`) logging from this run — see the table above.
- Timing/ordering detail inside the GC race itself.
