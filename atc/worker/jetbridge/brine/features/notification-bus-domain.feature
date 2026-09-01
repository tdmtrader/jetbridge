Feature: Notification subscribers observe the real PostgreSQL bus

  Source: 11 of 23 specs in atc/db/notifications_bus_test.go. The three
  injected-error specs, two synthetic reconnect specs, four stub-only success
  specs, two synchronous callback-pressure specs, and duplicate-LISTEN
  call-count spec remain in Go.

  Scenario Outline: Strict notification bus profile <profile> produces <result>
    When the real PostgreSQL notification bus evaluates strict profile "<profile>"
    Then the notification bus result is "<result>"

    Examples:
      | profile                  | result                          |
      | notify-channel           | received=true                   |
      | listen-channel           | received=true                   |
      | unlisten-last            | received=false                  |
      | keep-underlying-multiple | remaining=true                  |
      | fanout                   | first=true;second=true           |
      | survivor-after-unlisten  | removed=false;remaining=true    |
      | route-specific           | first=true;second=false          |
      | coalesce-once            | one=true;extra=false             |
      | coalesce-rearm           | first=true;again=true            |
      | postgres-round-trip      | received=true                   |
      | wrong-channel            | received=false                  |
