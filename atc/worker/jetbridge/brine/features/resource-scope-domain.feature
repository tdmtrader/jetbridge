Feature: Resource configuration scopes own durable version history

  Source: all 23 specs in atc/db/resource_config_scope_test.go. Every scenario
  uses production domain objects and a real PostgreSQL database.

  Scenario: Saving new and existing versions advances their check order
    When the real resource scope evaluates profile "save-check-order"
    Then the resource scope result is "first=v3:2;second=v3:4"

  Scenario: Saving only existing versions preserves their check order
    When the real resource scope evaluates profile "save-existing-order"
    Then the resource scope result is "before=2;after=2"

  Scenario: A new version requests scheduling for a direct consumer
    When the real resource scope evaluates profile "schedule-direct"
    Then the resource scope result is "initial=v3:2;advanced=true"

  Scenario: A new version does not schedule a passed-constraint consumer
    When the real resource scope evaluates profile "schedule-passed"
    Then the resource scope result is "initial=v3:2;advanced=false"

  Scenario: A new version does not schedule an unrelated job
    When the real resource scope evaluates profile "schedule-unrelated"
    Then the resource scope result is "initial=v3:2;advanced=false"

  Scenario: An empty version returns the production validation error
    When the real resource scope evaluates profile "empty-error"
    Then the resource scope result is "error=resource output version is empty. Version must contain at least one key-value pair"

  Scenario: LatestVersion returns the highest check order
    When the real resource scope evaluates profile "latest"
    Then the resource scope result is "version=v3;order=2"

  Scenario: Disabling a version does not change LatestVersion
    When the real resource scope evaluates profile "latest-disabled"
    Then the resource scope result is "initial=v3:2;saved=1;disabled=1;enabled=1"

  Scenario: Saving new versions updates LatestVersion
    When the real resource scope evaluates profile "latest-updated"
    Then the resource scope result is "initial=v3:2;version=5"

  Scenario: FindVersion returns an existing exact version
    When the real resource scope evaluates profile "find-existing"
    Then the resource scope result is "found=true;version=v1;order=1"

  Scenario: FindVersion reports a missing exact version
    When the real resource scope evaluates profile "find-missing"
    Then the resource scope result is "found=false;nil=true"

  Scenario: Updating check start records time build and public plan
    When the real resource scope evaluates profile "check-start"
    Then the resource scope result is "updated=true;advanced=true;id=99;recent=true;plan=true"

  Scenario: Updating check end records a later completion time
    When the real resource scope evaluates profile "check-end"
    Then the resource scope result is "updated=true;advanced=true"

  Scenario: A config change soft deletes the old scope without losing versions
    When the real resource scope evaluates profile "scope-soft-delete"
    Then the resource scope result is "deprecated=true;resource=true;versions=2"

  Scenario: DeprecatedScopes returns the old scope after a config change
    When the real resource scope evaluates profile "deprecated-one"
    Then the resource scope result is "count=1;timestamp=true"

  Scenario: DeprecatedScopes is empty before a config change
    When the real resource scope evaluates profile "deprecated-empty"
    Then the resource scope result is "count=0"

  Scenario: A config-change migration preserves complete version history
    When the real resource scope evaluates profile "migration-lifecycle"
    Then the resource scope result is "different=true;old=3;new-before=0;copied=3;new-after=3;deprecated=true;again=0"

  Scenario: CopyVersionsFrom copies every source version into an empty target
    When the real resource scope evaluates profile "copy-all"
    Then the resource scope result is "copied=3;count=3"

  Scenario: CopyVersionsFrom skips a version already present in the target
    When the real resource scope evaluates profile "copy-duplicates"
    Then the resource scope result is "copied=1;count=2"

  Scenario: CopyVersionsFrom returns zero for a self copy
    When the real resource scope evaluates profile "copy-self"
    Then the resource scope result is "copied=0"

  Scenario: A held resource-checking lock rejects a contender
    When the real resource scope evaluates profile "lock-contended"
    Then the resource scope result is "first=true;second=false"

  Scenario: A released resource-checking lock can be reacquired
    When the real resource scope evaluates profile "lock-released"
    Then the resource scope result is "first=true;after-release=true"

  Scenario: A held resource-checking lock blocks periodic contenders until release
    When the real resource scope evaluates profile "lock-periodic"
    Then the resource scope result is "first=true;blocked=true;after-release=true"
