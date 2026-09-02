Feature: Production team factory persistence
  Scenario: Creating a team preserves its exact name auth and identity
    When the production team factory evaluates profile "create"
    Then the team factory observation is "name=some-team;auth=true;found=true;same-id=true"

  Scenario: Finding an existing team preserves its exact name and auth
    When the production team factory evaluates profile "find-existing"
    Then the team factory observation is "name=some-team;auth=true"

  Scenario: Finding a missing team returns nil and not found
    When the production team factory evaluates profile "find-missing"
    Then the team factory observation is "nil=true;found=false"

  Scenario: Creating the default team promotes the persisted team to admin
    When the production team factory evaluates profile "default-create"
    Then the team factory observation is "after-admin=true;found=true;same-id=true"

  Scenario: Creating the default team twice reuses its identity
    When the production team factory evaluates profile "default-idempotent"
    Then the team factory observation is "same-id=true"

  Scenario: Listing one team returns its exact persisted name and auth
    When the production team factory evaluates profile "list-one"
    Then the team factory observation is "count=1;names=some-team;auth=true"

  Scenario: Listing two teams returns both in production name order
    When the production team factory evaluates profile "list-two"
    Then the team factory observation is "count=2;names=some-other-team,some-team;auth=true"
