Feature: Worker API behavior through real HTTP and PostgreSQL

  Scenario: Worker listing includes global and authorized team workers
    Given the production workers API executes profile "list-visible"
    Then the workers API observation exactly matches profile "list-visible"

  Scenario: Administrator worker listing includes every persisted worker
    Given the production workers API executes profile "list-admin"
    Then the workers API observation exactly matches profile "list-admin"

  Scenario: Worker listing rejects an unauthenticated request
    Given the production workers API executes profile "list-unauthenticated"
    Then the workers API observation exactly matches profile "list-unauthenticated"

  Scenario: Global worker registration persists the exact production fields and TTL
    Given the production workers API executes profile "register-global"
    Then the workers API observation exactly matches profile "register-global"

  Scenario: Team worker registration persists the exact team association
    Given the production workers API executes profile "register-team"
    Then the workers API observation exactly matches profile "register-team"

  Scenario: Worker registration rejects a missing team
    Given the production workers API executes profile "register-missing-team"
    Then the workers API observation exactly matches profile "register-missing-team"

  Scenario: Worker registration rejects a non-system caller
    Given the production workers API executes profile "register-not-system"
    Then the workers API observation exactly matches profile "register-not-system"

  Scenario: Worker registration rejects an empty worker name without persistence
    Given the production workers API executes profile "register-empty-name"
    Then the workers API observation exactly matches profile "register-empty-name"

  Scenario: Worker registration rejects an invalid TTL without persistence
    Given the production workers API executes profile "register-invalid-ttl"
    Then the workers API observation exactly matches profile "register-invalid-ttl"

  Scenario: Worker registration rejects an invalid version without persistence
    Given the production workers API executes profile "register-invalid-version"
    Then the workers API observation exactly matches profile "register-invalid-version"

  Scenario: Worker registration rejects an unauthenticated request without persistence
    Given the production workers API executes profile "register-unauthenticated"
    Then the workers API observation exactly matches profile "register-unauthenticated"

  Scenario: System caller deletes a global persisted worker
    Given the production workers API executes profile "delete-system"
    Then the workers API observation exactly matches profile "delete-system"

  Scenario: Administrator deletes a global persisted worker
    Given the production workers API executes profile "delete-admin"
    Then the workers API observation exactly matches profile "delete-admin"

  Scenario: Authorized team caller deletes its persisted worker
    Given the production workers API executes profile "delete-team"
    Then the workers API observation exactly matches profile "delete-team"

  Scenario: Deleting an absent worker returns a server error
    Given the production workers API executes profile "delete-missing"
    Then the workers API observation exactly matches profile "delete-missing"
