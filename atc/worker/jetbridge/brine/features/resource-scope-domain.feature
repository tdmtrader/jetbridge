Feature: Resource configuration scopes own durable version history

  Source: all 23 specs in atc/db/resource_config_scope_test.go. The scenarios
  use real schedules, version rows, deprecation timestamps, copy transactions,
  build summaries, and advisory locks in PostgreSQL.

  Scenario: Saving versions maintains order and schedules only direct consumers
    When the real resource scope evaluates profile "save"
    Then the resource scope result is "first=2;repeat=2;reordered=4;direct=true;passed=false;other=false"

  Scenario: Empty versions are rejected
    When the real resource scope evaluates profile "empty"
    Then the resource scope result is "error=true"

  Scenario: Latest and exact version lookup preserve enablement semantics
    When the real resource scope evaluates profile "latest-find"
    Then the resource scope result is "latest=v3;disabled=v3;updated=v5;v1=true:1;missing=false:true"

  Scenario: Check start and end times update the resource build summary
    When the real resource scope evaluates profile "check-times"
    Then the resource scope result is "start=true;summary=99:true;end=true;succeeded=true"

  Scenario: Config changes deprecate old history and support migration
    When the real resource scope evaluates profile "deprecate-copy"
    Then the resource scope result is "different=true;deprecated=1;old=3;copied=3;new=3;again=0"

  Scenario: Copying versions skips duplicates and self-copies
    When the real resource scope evaluates profile "copy"
    Then the resource scope result is "new=2;self=0"

  Scenario: Resource checking locks exclude contenders until release
    When the real resource scope evaluates profile "locks"
    Then the resource scope result is "first=true;second=false;after-release=true"
