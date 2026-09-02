Feature: Remaining persisted build completion and event behavior

  Scenario: Finish emits the exact persisted successful status event
    When the final real DB build evaluates profile "finish-event"
    Then the final DB build observation is "status=succeeded;exact=true"

  Scenario: Finish persists the requested terminal status
    When the final real DB build evaluates profile "finish-status"
    Then the final DB build observation is "status=succeeded"

  Scenario: Finish clears the private execution plan
    When the final real DB build evaluates profile "finish-private-plan"
    Then the final DB build observation is "empty=true"

  Scenario: Finish persists successful input and output versions
    When the final real DB build evaluates profile "finish-versions"
    Then the final DB build observation is "exact=true"

  Scenario: Events emits started and finished states then reaches the real end
    When the final real DB build evaluates profile "events-lifecycle"
    Then the final DB build observation is "started=true;finished=true;ended=true"

  Scenario: Events reads rows written before the bigint build ID migration
    When the final real DB build evaluates profile "events-legacy-id"
    Then the final DB build observation is "exact=true"

  Scenario: SaveEvent persists ordered logs supports offsets wakes subscribers and closes
    When the final real DB build evaluates profile "save-event"
    Then the final DB build observation is "ordered=true;offset=true;woke=true;closed=true"

  Scenario: Resources returns the exact persisted build inputs and outputs
    When the final real DB build evaluates profile "resources-exact"
    Then the final DB build observation is "exact=true"

  Scenario: Resources derives true for a first version occurrence
    When the final real DB build evaluates profile "resources-first"
    Then the final DB build observation is "first=true"

  Scenario: Resources derives false after an earlier build used the version
    When the final real DB build evaluates profile "resources-repeated"
    Then the final DB build observation is "first=false"
