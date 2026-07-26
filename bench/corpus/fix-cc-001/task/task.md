# `set_pipeline` uses the wrong value when `instance_vars` and `vars` share a name

**Type:** bug / small fix
**Component:** `atc` — the in-build `set_pipeline` step
**Reported:** 2025-11-16

## Symptom

We generate one pipeline instance per git branch from a single template. An
`across` step fans out over the branch list and each iteration runs a
`set_pipeline` step that passes the branch through `instance_vars`, plus a block
of shared `vars` that our template tooling emits identically for every instance.

That shared block recently grew a key called `branch` — the same name we use as
our instance var. Since then every instance is configured with the *shared*
value instead of its own.

Reduced to the smallest thing that shows it:

```yaml
# pipeline.yml, in some-resource
jobs:
- name: some-job
  plan:
  - task: some-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: [((branch))]
```

```yaml
# the step in the build
- set_pipeline: some-pipeline
  file: some-resource/pipeline.yml
  instance_vars: {branch: feature/foo}
  vars: {branch: some-shared-default}
```

Observed: the pipeline instance is registered under the right identity —
`fly pipelines` shows `some-pipeline/branch:feature/foo`, and the instance var is
stored correctly on the pipeline — but the configuration that lands has
`args: [some-shared-default]`. Expected `args: [feature/foo]`.

Nothing errors and nothing warns. Every instance in the fan-out therefore ends up
with a byte-identical config, and because the instance names are all still
distinct the mistake is invisible until someone reads a job's config and notices
the branch is wrong. In our case it silently pointed several instances at the
wrong branch for two days.

## Expected behavior

`instance_vars` are what a pipeline instance *is* — they are its identity, they
are shown as its identity, and they are the reason there is more than one
instance. If some other source on the same `set_pipeline` step happens to define
the same name, the instance var is the one that must be used to interpolate the
config. Anything else lets a generic block of variables quietly reconfigure an
instance to be something other than what it is named.

Concretely, for a single `set_pipeline` step:

1. A name defined by both `instance_vars` and the step's `vars` resolves to the
   `instance_vars` value.
2. A name defined by both `instance_vars` and a file listed in `var_files`
   resolves to the `instance_vars` value.
3. Steps whose variable names do not collide render exactly as they do today.

## Constraints

- The change is limited to how a `set_pipeline` step resolves the pipeline
  configuration it is about to set. How instance vars are stored on the pipeline,
  how the instance is named and displayed, and how `vars` and `var_files` relate
  to each other when no instance var is involved must all stay as they are.
- Variable resolution is shared machinery used by other steps and by `fly`. Do
  not change the general resolution contract to get this one case right —
  everything that consumes it today must keep behaving identically.
- No exported signature changes, no new dependencies.
- Add a regression test to the existing `atc/exec` Ginkgo suite that fails
  without the change. It has to pin the actual rendered configuration — asserting
  only that the step succeeded, or only that the instance var is stored on the
  pipeline ref, would have passed throughout this bug.

## Notes

- `fly set-pipeline`'s own `-v` / `-l` / `--instance-var` handling is out of
  scope. This is about the `set_pipeline` step running inside a build.
- We are not asking for a warning or an error when names collide. Collisions are
  legitimate — a shared default that an instance overrides is exactly the shape
  we want. We just need the override to go the right way.
