Feature: Production team API persists and returns exact team state

  Source: seven persisted success and not-found leaves in
  atc/api/teams_test.go.

  Scenario: ListTeams returns only exact authorized persisted teams
    When strict team API profile "list-authorized" is exercised over real HTTP
    Then the strict team observation is "status=200;content-type=application/json;teams=alpha,brine-access;exact=true"

  Scenario: GetTeam returns the exact persisted representation
    When strict team API profile "get-persisted" is exercised over real HTTP
    Then the strict team observation is "status=200;content-type=application/json;name=target;auth=owner;id=persisted"

  Scenario: SetTeam creates and returns exact persisted state
    When strict team API profile "create-persisted" is exercised over real HTTP
    Then the strict team observation is "status=201;content-type=application/json;name=target;auth=owner;id=persisted"

  Scenario: SetTeam returns the exact compatibility warning and persisted state
    When strict team API profile "warning-persisted" is exercised over real HTTP
    Then the strict team observation is "status=201;type=invalid_identifier;message=team: '_warning' is not a valid identifier: must start with a lowercase letter or a number;name=_warning;id=persisted"

  Scenario: SetTeam updates persisted provider auth
    When strict team API profile "update-auth" is exercised over real HTTP
    Then the strict team observation is "status=200;auth=owner-user-and-new-group"

  Scenario: DestroyTeam removes the persisted team
    When strict team API profile "delete-persisted" is exercised over real HTTP
    Then the strict team observation is "status=204;persisted=absent"

  Scenario: DestroyTeam returns not found for a missing team
    When strict team API profile "delete-missing" is exercised over real HTTP
    Then the strict team observation is "status=404"
