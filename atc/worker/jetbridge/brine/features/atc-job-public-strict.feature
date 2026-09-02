Feature: ATC job visibility lookup

  These scenarios call Config.JobIsPublic with concrete production config values.

  Scenario: A public job is reported public
    When a concrete ATC config checks the "public-job" job
    Then the job visibility is "public"

  Scenario: A private job is reported private
    When a concrete ATC config checks the "private-job" job
    Then the job visibility is "private"

  Scenario: Looking up a public job succeeds
    When a concrete ATC config checks the "public-job" job
    Then the job lookup succeeds

  Scenario: Looking up a private job succeeds
    When a concrete ATC config checks the "private-job" job
    Then the job lookup succeeds

  Scenario: Looking up a missing job fails
    When a concrete ATC config checks the "missing" job
    Then the job lookup fails
