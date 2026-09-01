Feature: Database mutations wake the components that consume them

  Source: all 21 specs in atc/db/component_notifications_test.go. Every
  scenario isolates one production database mutation and observes its real
  PostgreSQL LISTEN/NOTIFY channel.

  Scenario Outline: Component notification profile <profile> produces <result>
    When real PostgreSQL component notifications evaluate strict profile "<profile>"
    Then the component notification result is "<result>"

    Examples:
      | profile                     | result         |
      | resource-scope-changed      | received=true  |
      | resource-scope-same         | received=false |
      | resource-pin                | received=true  |
      | resource-unpin              | received=true  |
      | resource-disable            | received=true  |
      | resource-enable             | received=true  |
      | resource-save-new           | received=true  |
      | resource-save-existing      | received=false |
      | resource-check-time         | received=false |
      | finish-syslog               | received=true  |
      | finish-build-reaper         | received=true  |
      | finish-builds               | received=true  |
      | finish-cache-uses           | received=true  |
      | finish-checks               | received=true  |
      | archive-pipelines           | received=true  |
      | archive-task-caches         | received=true  |
      | destroy-pipelines           | received=true  |
      | pause-task-caches           | received=true  |
      | finish-resource-caches      | received=true  |
      | resource-type-scope-changed | received=true  |
      | resource-type-scope-same    | received=false |
