Feature: Auth providers migrate through real PostgreSQL

  Scenario: Down migration moves basic credentials to the legacy fields
    When the production auth providers migration evaluates profile "down-basic-fields"
    Then the auth providers migration observation exactly matches "down-basic-fields"

  Scenario: Down migration preserves an existing GitHub provider
    When the production auth providers migration evaluates profile "down-preserves-github"
    Then the auth providers migration observation exactly matches "down-preserves-github"

  Scenario: Down migration removes the basic provider from auth
    When the production auth providers migration evaluates profile "down-removes-basic-provider"
    Then the auth providers migration observation exactly matches "down-removes-basic-provider"

  Scenario: Down migration removes the no-auth provider from auth
    When the production auth providers migration evaluates profile "down-removes-noauth-provider"
    Then the auth providers migration observation exactly matches "down-removes-noauth-provider"

  Scenario: Up migration moves basic credentials into empty auth
    When the production auth providers migration evaluates profile "up-basic-empty-auth"
    Then the auth providers migration observation exactly matches "up-basic-empty-auth"

  Scenario: Up migration moves basic credentials into null auth
    When the production auth providers migration evaluates profile "up-basic-null-auth"
    Then the auth providers migration observation exactly matches "up-basic-null-auth"

  Scenario: Up migration merges basic credentials with an existing GitHub provider
    When the production auth providers migration evaluates profile "up-merges-basic-and-github"
    Then the auth providers migration observation exactly matches "up-merges-basic-and-github"

  Scenario: Up migration rejects malformed basic credential keys
    When the production auth providers migration evaluates profile "up-rejects-malformed-basic"
    Then the auth providers migration observation exactly matches "up-rejects-malformed-basic"

  Scenario: Up migration rejects empty and null basic credentials
    When the production auth providers migration evaluates profile "up-rejects-empty-basic"
    Then the auth providers migration observation exactly matches "up-rejects-empty-basic"

  Scenario: Up migration does not add no-auth beside basic credentials
    When the production auth providers migration evaluates profile "up-basic-does-not-add-noauth"
    Then the auth providers migration observation exactly matches "up-basic-does-not-add-noauth"

  Scenario: Up migration does not add no-auth beside an existing provider
    When the production auth providers migration evaluates profile "up-provider-does-not-add-noauth"
    Then the auth providers migration observation exactly matches "up-provider-does-not-add-noauth"

  Scenario: Up migration adds no-auth when no method is configured
    When the production auth providers migration evaluates profile "up-empty-adds-noauth"
    Then the auth providers migration observation exactly matches "up-empty-adds-noauth"
