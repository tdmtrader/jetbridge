Feature: Build events parse through the production registry

  Source: all 26 specs in atc/event/parser_test.go. These scenarios call the
  production registry and JSON envelope implementation directly, without a
  fake parser or duplicated event table.

  Scenario: Older minor versions use the compatible registered parser
    When the production event parser handles profile "compatible-older"
    Then the event parser result is "steps.brineFakeEvent:sup"

  Scenario: Newer minor versions ignore compatible future fields
    When the production event parser handles profile "compatible-newer"
    Then the event parser result is "steps.brineFakeEvent:sup"

  Scenario: Unknown event types return their typed error
    When the production event parser handles profile "unknown-type"
    Then the event parser result is "event.UnknownEventTypeError"

  Scenario: Incompatible major versions return their typed error
    When the production event parser handles profile "incompatible-version"
    Then the event parser result is "event.UnknownEventVersionError"

  Scenario Outline: The production registry contains <event>
    When the production event parser handles profile "<event>"
    Then the event parser result is "registered"

    Examples:
      | event              |
      | InitializeCheck    |
      | InitializeTask     |
      | StartTask          |
      | FinishTask         |
      | InitializeGet      |
      | StartGet           |
      | FinishGet          |
      | InitializePut      |
      | StartPut           |
      | FinishPut          |
      | SetPipelineChanged |
      | Status             |
      | WaitingForWorker   |
      | SelectedWorker     |
      | Log                |
      | Error              |
      | ImageCheck         |
      | ImageGet           |
      | AcrossSubsteps     |

  Scenario: Event messages round-trip their concrete payload
    When the production event parser handles profile "message-round-trip"
    Then the event parser result is "event.Log:sup"

  Scenario: Missing envelope data returns an error rather than panicking
    When the production event parser handles profile "missing-data"
    Then the event parser result is "missing event data: log version 5.1"

  Scenario: Null envelope data returns an error rather than panicking
    When the production event parser handles profile "null-data"
    Then the event parser result is "missing event data: some-event version 1.0"
