Feature: Resource type persistence and check behavior

  Scenario: ResourceTypes returns every exact persisted field with unique identifiers
    When the real resource type domain evaluates profile "strict-collection"
    Then the resource type domain result is "exact=true;unique-ids=true"

  Scenario: ResourceTypes excludes resource types made inactive by a pipeline update
    When the real resource type domain evaluates profile "strict-inactive"
    Then the resource type domain result is "names=some-type"

  Scenario: Filter resolves a resource and resource type with the same name
    When the real resource type domain evaluates profile "strict-filter-same-name"
    Then the resource type domain result is "tree=some-name:some-custom-type"

  Scenario: Filter follows only the exact persisted dependency tree in its pipeline
    When the real resource type domain evaluates profile "strict-filter-dependency"
    Then the resource type domain result is "tree=some-custom-type:some-other-foo-type,some-other-foo-type:some-other-type,some-other-type:registry-image,registry-image:registry-image"

  Scenario: Deserialize merges custom parent defaults and preserves every field
    When the real resource type domain evaluates profile "strict-deserialize-parent"
    Then the resource type domain result is "exact=true"

  Scenario: Deserialize merges base resource type defaults and preserves every field
    When the real resource type domain evaluates profile "strict-deserialize-base"
    Then the resource type domain result is "exact=true"

  Scenario: SetResourceConfigScope persists the exact scope association
    When the real resource type domain evaluates profile "strict-scope"
    Then the resource type domain result is "before-zero=true;after-equal=true"

  Scenario: CreateBuild persists every started resource type build field
    When the real resource type domain evaluates profile "strict-build-created"
    Then the resource type domain result is "exact=true"

  Scenario: CreateBuild writes the created and log events to the check partition
    When the real resource type domain evaluates profile "strict-build-events"
    Then the resource type domain result is "events=2"

  Scenario: CreateBuild propagates the production trace context
    When the real resource type domain evaluates profile "strict-build-trace"
    Then the resource type domain result is "trace=true"

  Scenario: CreateBuild refuses a second automatic build while one is running
    When the real resource type domain evaluates profile "strict-build-blocked"
    Then the resource type domain result is "created=false;nil=true"

  Scenario: CreateBuild permits and marks a manual build while one is running
    When the real resource type domain evaluates profile "strict-build-manual"
    Then the resource type domain result is "created=true;manual=true;type-id=true"

  Scenario: CreateBuild permits an automatic build after the previous build finishes
    When the real resource type domain evaluates profile "strict-build-after"
    Then the resource type domain result is "created=true;type-id=true"

  Scenario: CheckPlan produces the exact base resource type plan
    When the real resource type domain evaluates profile "strict-plan-base"
    Then the resource type domain result is "exact=true"

  Scenario: CheckPlan produces the exact nested image check and get plans
    When the real resource type domain evaluates profile "strict-plan-custom"
    Then the resource type domain result is "exact=true"

  Scenario: CheckPlan uses the custom resource type check interval
    When the real resource type domain evaluates profile "strict-plan-interval"
    Then the resource type domain result is "exact=true"

  Scenario: CheckPlan propagates privileged mode to the type image
    When the real resource type domain evaluates profile "strict-plan-privileged"
    Then the resource type domain result is "exact=true"

  Scenario: CheckPlan skips only the resource interval when recursion is disabled
    When the real resource type domain evaluates profile "strict-plan-local-skip"
    Then the resource type domain result is "exact=true"

  Scenario: CheckPlan skips both resource and type intervals when recursion is enabled
    When the real resource type domain evaluates profile "strict-plan-recursive-skip"
    Then the resource type domain result is "exact=true"

  Scenario: ClearVersions reports zero when the resource type has no versions
    When the real resource type domain evaluates profile "strict-clear-zero"
    Then the resource type domain result is "deleted=0"

  Scenario: ClearVersions deletes the complete resource type version history
    When the real resource type domain evaluates profile "strict-clear-history"
    Then the resource type domain result is "deleted=3;absent=true"

  Scenario: ClearVersions deletes the shared global resource type history
    When the real resource type domain evaluates profile "strict-clear-shared"
    Then the resource type domain result is "deleted=3;shared-absent=true"
