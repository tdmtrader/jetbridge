Feature: Metric primitives report real concurrent and database activity

  Source: all 3 specs in atc/metric/counter_test.go, all 3 in
  atc/metric/gauge_test.go, and all 3 in atc/metric/query_counter_test.go.
  Query profiles wrap the scenario's real PostgreSQL DbConn; its error profile
  wraps a second connection only after that connection is genuinely closed.

  Scenario Outline: Metric primitive profile <profile> produces <result>
    When the production metric primitive evaluates profile "<profile>"
    Then the metric primitive result is "<result>"

    Examples:
      | profile           | result                                  |
      | counter-inc       | value=3                                 |
      | counter-delta     | value=3                                 |
      | counter-reset     | first=3;second=0                        |
      | gauge-max         | value=2                                 |
      | gauge-concurrent  | value=30                                |
      | gauge-reset       | first=2;second=1                        |
      | query-passthrough | ping=true;name=true;count=0             |
      | query-errors      | ping-error=true;query-error=true        |
      | query-count       | query=1;exec-row=2;transaction-query=1 |
