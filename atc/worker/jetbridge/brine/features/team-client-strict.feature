Feature: Production team client preserves team administration contracts

  Source: nine leaves in go-concourse/concourse/teams_test.go. The two 404
  leaves are intentionally separate source leaves even though both exercise
  the same client response contract.

  Scenario: ListTeams decodes every exact team returned by the real API
    When strict team client profile "list-teams" is exercised over real HTTP
    Then the strict team observation is "teams=alpha,beta,brine-access;exact=true"

  Scenario: FindTeam returns the persisted name and provider auth
    When strict team client profile "find-found" is exercised over real HTTP
    Then the strict team observation is "name=target;auth=owner"

  Scenario: FindTeam describes a missing team exactly
    When strict team client profile "find-missing" is exercised over real HTTP
    Then the strict team observation is "error=team 'missing' does not exist"

  Scenario: A second 404 preserves the duplicate FindTeam error contract
    When strict team client profile "find-not-belonging-404" is exercised over real HTTP
    Then the strict team observation is "error=team 'not-belonging' does not exist"

  Scenario: CreateOrUpdate returns the exact persisted team
    When strict team client profile "create-returned-team" is exercised over real HTTP
    Then the strict team observation is "name=target;auth=owner;id=persisted"

  Scenario: CreateOrUpdate reports a newly created team
    When strict team client profile "create-flags" is exercised over real HTTP
    Then the strict team observation is "created=true;updated=false"

  Scenario: CreateOrUpdate reports an updated existing team
    When strict team client profile "update-flags" is exercised over real HTTP
    Then the strict team observation is "created=false;updated=true"

  Scenario: CreateOrUpdate decodes the exact compatibility warning
    When strict team client profile "create-warning" is exercised over real HTTP
    Then the strict team observation is "created=true;updated=false;type=invalid_identifier;message=team: '_warning' is not a valid identifier: must start with a lowercase letter or a number"

  Scenario: DestroyTeam accepts a production no-content response
    When strict team client profile "destroy-success" is exercised over real HTTP
    Then the strict team observation is "error=nil"
