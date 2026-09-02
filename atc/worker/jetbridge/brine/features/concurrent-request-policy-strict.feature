Feature: Concurrent request policy configuration

  These scenarios exercise production route parsing and pool lookup with concrete values.

  Scenario: LimitedRoute unmarshals a supported action
    When the production concurrent request policy evaluates profile "unmarshal-supported"
    Then the concurrent request policy observation is "ListAllJobs"

  Scenario: LimitedRoute unsupported error names the action
    When the production concurrent request policy evaluates profile "unmarshal-unsupported"
    Then the concurrent request policy observation contains "action 'CreateJobBuild' is not supported"

  Scenario: LimitedRoute unsupported error describes supported actions
    When the production concurrent request policy evaluates profile "unmarshal-unsupported"
    Then the concurrent request policy observation contains "Supported actions are:"

  Scenario: HandlerPool finds a limited action
    When the production concurrent request policy evaluates profile "limited-found"
    Then the concurrent request policy observation is "found=true"

  Scenario: HandlerPool does not find an unlimited action
    When the production concurrent request policy evaluates profile "unlimited-not-found"
    Then the concurrent request policy observation is "found=false"

  Scenario: HandlerPool returns the shared pool
    When the production concurrent request policy evaluates profile "shared-pool"
    Then the concurrent request policy observation is "second-acquire=false"
