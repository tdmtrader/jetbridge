Feature: Build factory persistence and query policy

  Scenario: Build loads the exact persisted build identity
    When the real build factory evaluates profile "strict-lookup"
    Then the build factory result is "found=true;same=true"

  Scenario: A succeeded one-off build remains interceptible inside grace
    When the real build factory evaluates profile "strict-one-off-within-succeeded"
    Then the build factory result is "interceptible=true"

  Scenario: An aborted one-off build is non-interceptible inside grace
    When the real build factory evaluates profile "strict-one-off-within-aborted"
    Then the build factory result is "interceptible=false"

  Scenario: An errored one-off build is non-interceptible inside grace
    When the real build factory evaluates profile "strict-one-off-within-errored"
    Then the build factory result is "interceptible=false"

  Scenario: A failed one-off build is non-interceptible inside grace
    When the real build factory evaluates profile "strict-one-off-within-failed"
    Then the build factory result is "interceptible=false"

  Scenario: A succeeded one-off build is non-interceptible after grace
    When the real build factory evaluates profile "strict-one-off-past-succeeded"
    Then the build factory result is "interceptible=false"

  Scenario: An aborted one-off build is non-interceptible after grace
    When the real build factory evaluates profile "strict-one-off-past-aborted"
    Then the build factory result is "interceptible=false"

  Scenario: An errored one-off build is non-interceptible after grace
    When the real build factory evaluates profile "strict-one-off-past-errored"
    Then the build factory result is "interceptible=false"

  Scenario: A failed one-off build is non-interceptible after grace
    When the real build factory evaluates profile "strict-one-off-past-failed"
    Then the build factory result is "interceptible=false"

  Scenario: A running one-off build remains interceptible
    When the real build factory evaluates profile "strict-one-off-running"
    Then the build factory result is "interceptible=true"

  Scenario: Failed builds across pipelines become non-interceptible immediately
    When the real build factory evaluates profile "strict-pipeline-failed-four"
    Then the build factory result is "interceptible=false,false,false,false"

  Scenario: A succeeded pipeline build is non-interceptible when completed
    When the real build factory evaluates profile "strict-pipeline-completed-succeeded"
    Then the build factory result is "interceptible=false"

  Scenario: An aborted pipeline build is non-interceptible when completed
    When the real build factory evaluates profile "strict-pipeline-completed-aborted"
    Then the build factory result is "interceptible=false"

  Scenario: An errored pipeline build is non-interceptible when completed
    When the real build factory evaluates profile "strict-pipeline-completed-errored"
    Then the build factory result is "interceptible=false"

  Scenario: A failed pipeline build is non-interceptible when completed
    When the real build factory evaluates profile "strict-pipeline-completed-failed"
    Then the build factory result is "interceptible=false"

  Scenario: Pending and started pipeline builds remain interceptible
    When the real build factory evaluates profile "strict-pipeline-running"
    Then the build factory result is "pending=true;started=true"

  Scenario: A failed GC candidate becomes non-interceptible immediately
    When the real build factory evaluates profile "strict-gc-failed-immediate"
    Then the build factory result is "interceptible=false"

  Scenario: VisibleBuilds returns exact ordered builds for authorized teams
    When the real build factory evaluates profile "strict-visible"
    Then the build factory result is "count=4;order=true;excluded=true"

  Scenario: AllBuilds returns every private and public team build
    When the real build factory evaluates profile "strict-all"
    Then the build factory result is "count=4;match=true"

  Scenario: PublicBuilds returns only builds from public pipelines
    When the real build factory evaluates profile "strict-public"
    Then the build factory result is "count=1;match=true"

  Scenario: GetDrainableBuilds excludes checks running and drained builds
    When the real build factory evaluates profile "strict-drainable"
    Then the build factory result is "count=1;match=true"

  Scenario: GetAllStartedBuilds returns started one-off and pipeline builds
    When the real build factory evaluates profile "strict-started"
    Then the build factory result is "count=2;match=true"

  Scenario: Date pagination returns only builds inside the requested window
    When the real build factory evaluates profile "strict-date-inside"
    Then the build factory result is "count=2"

  Scenario: Date pagination returns nothing after a future lower boundary
    When the real build factory evaluates profile "strict-date-future"
    Then the build factory result is "count=0"

  Scenario: Date pagination returns nothing before an old upper boundary
    When the real build factory evaluates profile "strict-date-old"
    Then the build factory result is "count=0"
