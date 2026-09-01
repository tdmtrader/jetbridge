Feature: In-memory checks become persisted build history when they start

  Scenario: A new in-memory check exposes its complete transient identity
    When a real in-memory check build evaluates profile "creation-identity"
    Then the in-memory build result is "creation=true"

  Scenario: A new in-memory check exposes exact Lager fields
    When a real in-memory check build evaluates profile "creation-lager"
    Then the in-memory build result is "lager=true"

  Scenario: A new in-memory check exposes exact tracing fields
    When a real in-memory check build evaluates profile "creation-tracing"
    Then the in-memory build result is "tracing=true"

  Scenario: A new in-memory check retains its creation span context
    When a real in-memory check build evaluates profile "creation-span"
    Then the in-memory build result is "span=true"

  Scenario: A new in-memory check has no persisted event stream
    When a real in-memory check build evaluates profile "creation-events-error"
    Then the in-memory build result is "events-error=true"

  Scenario: Saved transient start events remain unavailable before persistence
    When a real in-memory check build evaluates profile "started-events-error"
    Then the in-memory build result is "events-error=true"

  Scenario: Successful completion before check start allocates no build ID
    When a real in-memory check build evaluates profile "prestart-success-id"
    Then the in-memory build result is "id=0"

  Scenario: Errored completion before check start updates the resource summary
    When a real in-memory check build evaluates profile "prestart-error-summary"
    Then the in-memory build result is "summary=errored"

  Scenario: Errored completion before check start saves status event three
    When a real in-memory check build evaluates profile "prestart-error-event"
    Then the in-memory build result is "event=status;id=3;matches=true"

  Scenario: Starting an in-memory check allocates a positive build ID
    When a real in-memory check build evaluates profile "started-id"
    Then the in-memory build result is "id-positive=true"

  Scenario: Starting an in-memory check marks the resource summary started
    When a real in-memory check build evaluates profile "started-summary"
    Then the in-memory build result is "summary=started"

  Scenario: A persisted in-memory check exposes exact Lager fields
    When a real in-memory check build evaluates profile "started-lager"
    Then the in-memory build result is "lager=true"

  Scenario: A persisted in-memory check exposes exact tracing fields
    When a real in-memory check build evaluates profile "started-tracing"
    Then the in-memory build result is "tracing=true"

  Scenario: Persistence copies status initialize and start events in order
    When a real in-memory check build evaluates profile "started-events"
    Then the in-memory build result is "events=true"

  Scenario: A persisted in-memory check saves a new log event at event three
    When a real in-memory check build evaluates profile "started-log"
    Then the in-memory build result is "event=log;id=3;matches=true"

  Scenario: A persisted in-memory check retains its transient cache identity
    When a real in-memory check build evaluates profile "started-cache-user"
    Then the in-memory build result is "cache-user=true"

  Scenario: A persisted in-memory check creates its transient container owner
    When a real in-memory check build evaluates profile "started-owner"
    Then the in-memory build result is "owner=true"

  Scenario: A persisted in-memory check retains its transient run-state ID
    When a real in-memory check build evaluates profile "started-run-state"
    Then the in-memory build result is "run-state=in-memory-check-build:1"

  Scenario: A held production tracking lock cannot be acquired twice
    When a real in-memory check build evaluates profile "started-tracking-lock"
    Then the in-memory build result is "second-lock=false"

  Scenario: Successful persisted completion updates the resource summary
    When a real in-memory check build evaluates profile "finish-summary"
    Then the in-memory build result is "summary=succeeded"

  Scenario: Successful persisted completion saves status event three
    When a real in-memory check build evaluates profile "finish-event"
    Then the in-memory build result is "event=status;id=3;matches=true"

  Scenario: The API factory reconstructs the complete in-memory check identity
    When a real in-memory check build evaluates profile "api-find"
    Then the in-memory build result is "api-find=true"

  Scenario: The API in-memory check exposes exact Lager fields
    When a real in-memory check build evaluates profile "api-lager"
    Then the in-memory build result is "api-lager=true"

  Scenario: The API in-memory check exposes an event stream
    When a real in-memory check build evaluates profile "api-events"
    Then the in-memory build result is "api-events=true"
