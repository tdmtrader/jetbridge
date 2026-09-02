Feature: Remaining resource persistence behavior

  Scenario: SetResourceConfigScope schedules only the consuming job
    When the remaining real resource domain evaluates profile "scope-schedule"
    Then the remaining resource domain result is "consumer=true;unrelated=false"

  Scenario: CreateBuild persists every started resource build field
    When the remaining real resource domain evaluates profile "build-created"
    Then the remaining resource domain result is "exact=true"

  Scenario: CreateBuild writes created and log events to the check partition
    When the remaining real resource domain evaluates profile "build-events"
    Then the remaining resource domain result is "events=2"

  Scenario: CreateBuild refuses a second automatic build while one is running
    When the remaining real resource domain evaluates profile "build-blocked"
    Then the remaining resource domain result is "created=false;nil=true"

  Scenario: CreateBuild permits and marks a manual build while one is running
    When the remaining real resource domain evaluates profile "build-manual"
    Then the remaining resource domain result is "created=true;manual=true;resource-id=true"

  Scenario: CreateInMemoryBuild preserves every transient resource build field
    When the remaining real resource domain evaluates profile "memory-created"
    Then the remaining resource domain result is "exact=true"

  Scenario: CreateInMemoryBuild keeps check events out of PostgreSQL
    When the remaining real resource domain evaluates profile "memory-events"
    Then the remaining resource domain result is "events=0"

  Scenario: ClearVersions reports zero for an empty resource history
    When the remaining real resource domain evaluates profile "clear-zero"
    Then the remaining resource domain result is "deleted=0"

  Scenario: ClearVersions deletes the complete resource version history
    When the remaining real resource domain evaluates profile "clear-history"
    Then the remaining resource domain result is "deleted=3;absent=true"

  Scenario: ClearVersions preserves disabled and pinned state for recreated versions
    When the remaining real resource domain evaluates profile "clear-state"
    Then the remaining resource domain result is "deleted=3;disabled=true;pinned=true"

  Scenario: ClearVersions deletes shared global resource history
    When the remaining real resource domain evaluates profile "clear-shared"
    Then the remaining resource domain result is "deleted=3;both-absent=true"

  Scenario: CheckPlan produces the exact base resource plan
    When the remaining real resource domain evaluates profile "plan-base"
    Then the remaining resource domain result is "exact=true"

  Scenario: CheckPlan produces exact nested image check and get plans
    When the remaining real resource domain evaluates profile "plan-custom"
    Then the remaining resource domain result is "exact=true"

  Scenario: CheckPlan uses the custom resource type check interval
    When the remaining real resource domain evaluates profile "plan-interval"
    Then the remaining resource domain result is "exact=true"

  Scenario: CheckPlan propagates privileged mode to the type image
    When the remaining real resource domain evaluates profile "plan-privileged"
    Then the remaining resource domain result is "exact=true"

  Scenario: CheckPlan skips only the resource interval without recursion
    When the remaining real resource domain evaluates profile "plan-local-skip"
    Then the remaining resource domain result is "exact=true"

  Scenario: CheckPlan skips both resource and type intervals recursively
    When the remaining real resource domain evaluates profile "plan-recursive-skip"
    Then the remaining resource domain result is "exact=true"

  Scenario: BuildSummary is absent before any resource check starts
    When the remaining real resource domain evaluates profile "summary-empty"
    Then the remaining resource domain result is "nil=true"

  Scenario: BuildSummary reflects the current in-memory started check
    When the remaining real resource domain evaluates profile "summary-started"
    Then the remaining resource domain result is "exact=true"

  Scenario: BuildSummary reflects a failed persisted scope check
    When the remaining real resource domain evaluates profile "summary-failed"
    Then the remaining resource domain result is "exact=true"

  Scenario: BuildSummary reflects another successful check sharing the scope
    When the remaining real resource domain evaluates profile "summary-shared"
    Then the remaining resource domain result is "exact=true"

  Scenario: BuildSummary returns to the resource's newest started check
    When the remaining real resource domain evaluates profile "summary-newest"
    Then the remaining resource domain result is "exact=true"
