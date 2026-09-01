Feature: Runtime metrics preserve their externally emitted meaning

  Source: all 2 specs in atc/metric/metrics_test.go, all 5 in
  atc/metric/http_test.go, all 3 in atc/metric/periodic_test.go, and all 3 in
  atc/metric/error_sink_collector_test.go. HTTP profiles use a real httptest
  server and production middleware; periodic database emission reads the
  scenario's real PostgreSQL connection. The recording emitter is the output
  sink under observation, not a replacement for application behavior.

  Scenario Outline: Runtime metric profile <profile> emits <result>
    When the production runtime metric evaluates profile "<profile>"
    Then the runtime metric result is "<result>"

    Examples:
      | profile             | result                                                        |
      | workers-empty       | all-states=true;all-zero=true                                  |
      | workers-running     | running=1                                                      |
      | http-root           | status=404;method=GET;route=ApiEndpoint;path=/                  |
      | http-success        | status=200;path=/success                                       |
      | http-failure        | status=500;path=/failure                                       |
      | periodic-database   | queries=4;connections=A,B                                      |
      | periodic-concurrent | requests=123;limit-hit=10;action=ListAllSomething              |
      | periodic-waiting    | value=123;teamId=42;teamName=teamdev;type=task                 |
      | error-log           | emitted=1;message=err-msg                                      |
      | recursive-error     | emitted=0                                                      |
      | info-log            | emitted=0                                                      |
