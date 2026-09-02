Feature: Scheduler input resolution over persisted build history

  Source: all 77 leaves in atc/scheduler/algorithm/algorithm_test.go. Each
  scenario creates a fresh PostgreSQL database, persists the source case's jobs,
  resources, versions, build inputs, build outputs, and rerun relationships, and
  invokes algorithm.New(db.NewVersionsDB(...)).Compute. No scheduler collaborator
  is substituted and no observation is recorded outside the production database.

  Scenario Outline: Scheduler algorithm source case — <case>
    Given the production scheduler algorithm example "<case>" uses a real database
    When the production input algorithm resolves the example
    Then the resolution matches the source contract

    Examples:
      | case |
      | can fan-in |
      | propagates resources together |
      | correlates inputs by build, allowing resources to skip jobs |
      | resolve a resource when it has versions |
      | does not resolve a resource when it does not have any versions |
      | finds only versions that passed through together |
      | can collect distinct versions of resources without correlating by job |
      | resolves passed constraints with common jobs |
      | resolves passed constraints with common jobs, skipping versions that are not common to builds of all jobs |
      | finds the latest version for inputs with no passed constraints |
      | finds the non-disabled latest version for inputs with no passed constraints |
      | returns a missing input reason when no input version satisfies the passed constraint |
      | finds next version for inputs that use every version when there is a build for that resource |
      | finds next non-disabled version for inputs that use every version when there is a build for that resource |
      | finds current non-disabled version if all later versions are disabled for inputs that use every version when there is a build for that resource |
      | finds last non-disabled version if all later and current versions are disabled for inputs that use every version when there is a build for that resource |
      | finds last enabled version for inputs that use every version when there is no builds for that resource |
      | finds last version for inputs that use every version when there is no builds for that resource |
      | finds next version that passed constraints for inputs that use every version |
      | returns the first common version when the current job has no builds and there are multiple passed constraints with version every |
      | does not find candidates when the current job has no builds, there are multiple passed constraints with version every, and a passed job has no builds |
      | returns the next version when there is a passed constraint with version every |
      | returns current version if there is no version after it that satisifies constraints |
      | returns the common version when there are multiple passed constraints with version every |
      | returns the first version that satisfies constraints when using every version |
      | does not find candidates when there are multiple passed constraints with version every, and one passed job has no builds |
      | returns the latest enabled version when the current job has no builds, and there is a passed constraint with version every |
      | returns the current enabled version when there is a passed constraint with version every, and all later verisons are disabled |
      | returns the latest set of versions that satisfy all passed constraint with version every, and the current job has no builds |
      | returns the latest enabled set of versions that satisfy all passed constraint with version every, and the current job has no builds |
      | returns latest build outputs for the passed job that has not run with the current job when using every version |
      | finds next version that satisfies common constraints when using every version |
      | returns the only set of versions that satisfy constraints when the set is one that has already run |
      | returns the next set of versions that satisfy constraints when using every version |
      | returns earliest set of versions that satisfy the multiple passed constraints with version every when the current job latest build has un-ordered versions independent of the ordering (build ids ordered lowest to highest starting with shared-job) |
      | returns earliest set of versions that satisfy the multiple passed constraints with version every when the current job latest build has un-ordered versions independent of the ordering (build ids ordered lowest to highest starting with simple-a) |
      | returns earliest set of versions that satisfy the multiple passed constraints with version every when one of the passed jobs skipped a version |
      | returns the current set of versions that satisfy the multiple passed constraints with version every when one of the passed job has no newer versions |
      | returns an older set of versions that satisfy the multiple passed constraints with version every when the passed job versions are older than the current set |
      | returns the earliest non-disabled version that satisfies constraints when several versions do not satisfy when using every version |
      | when a passed constraint is added to a job that has already run before, it finds the latest |
      | returns a missing input reason when no input version satisfies the shared passed constraints |
      | resolves to the pinned version when it exists |
      | does not resolve a version when the pinned version is not in Versions DB (version is disabled or no builds succeeded) |
      | resolves the version that is pinned with passed |
      | does not resolve a version when the pinned version has not passed the constraint |
      | uses the build that includes the pinned with passed while there are multiple inputs |
      | check orders take precedence over version ID |
      | waiting on upstream job for shared version (ryv3) |
      | reconfigure passed constraints for job with missing upstream dependency (simple-c) |
      | finds a suitable candidate for any inputs resolved before an unresolveable candidates |
      | uses partially resolved candidates when there is an error with no passed |
      | finds the next every version scoped to a resource |
      | finds successful candidates when there are multiple outputs from passed constraints that are identical |
      | only uses the first build output/input to set a version candidate and disregards the other (it should use the output version first) |
      | with every and passed, with one rerun build, new versions are passed to downstream |
      | with every and passed, it does not use retrigger builds as latest build |
      | with every and passed, it does not use retrigger builds as latest build when there are multiple passed jobs |
      | with passed constraints, it does not use the retrigger build as latest build |
      | with multiple passed constraints, it does not use retrigger builds as latest build when there are multiple passed jobs |
      | with a build that has a disabled input of the same resource, still uses the other inputs to resolve |
      | with version every and passed and unused builds, has next is true |
      | with version every and passed and no unused builds, has next is false |
      | with version every and passed and the unused builds is not satisfiable, has next is false |
      | with version every and passed and multiple jobs with one that has unused builds, has next is true |
      | with version every and unused versions, has next is true |
      | with version every and no unused versions, has next is false |
      | with version every but has never used the version before, has next is false |
      | with both version every and version every with passed inputs, the has next value is recognized |
      | when the resource does not have it's resource config scope set, it should error |
      | with version every and passed using an old version, it finds latest version ran by job |
      | with version every without passed using an old version, it finds latest version ran by job |
      | if another job uses the same resource, that does not affect the next version found for the current job |
      | if the chosen version for an input with passed constraints does not exist, it will not select that version |
      | if there are multiple inputs with the same passed constraint job and the chosen version from a build does not exist, it will not use that build |
      | resolves passed constraints deterministically across multiple jobs |
      | doom detection prevents infinite recursion on unsatisfiable passed constraints |
