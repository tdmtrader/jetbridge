Feature: Database mutations wake the components that consume them

  Source: all 21 specs in atc/db/component_notifications_test.go. Every signal
  crosses PostgreSQL LISTEN/NOTIFY; grouped scenarios subscribe to all affected
  component channels before invoking the production database object.

  Scenario: Resource mutations notify the scanner only when work changed
    When real PostgreSQL component notifications evaluate profile "resource"
    Then the component notification result is "scope=true;same=false;pin=true;unpin=true;disable=true;enable=true;new=true;existing=false;check-time=false"

  Scenario: Finishing a build wakes every build and cache consumer
    When real PostgreSQL component notifications evaluate profile "build-finish"
    Then the component notification result is "all=true;count=6"

  Scenario: Pipeline lifecycle changes wake pipeline and task-cache collectors
    When real PostgreSQL component notifications evaluate profile "pipeline"
    Then the component notification result is "archive=true;destroy=true;pause=true"

  Scenario: Resource-type scope changes notify only when the scope changes
    When real PostgreSQL component notifications evaluate profile "resource-type"
    Then the component notification result is "changed=true;same=false"
