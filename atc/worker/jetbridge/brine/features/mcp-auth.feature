@authentication @mcp-auth
Feature: Scoped and renewable MCP authorization
  Each client receives the permissions the user selects. Grants retain their
  boundaries through renewal, server restart, and independent revocation.

  Scenario Outline: Consent grants only the selected categories
    Given an authentication server with a private pipeline
    When an MCP client requests "read pipelines:write hijack" and the user selects "<scopes>"
    Then the MCP grant has exactly "<scopes>"
    And the MCP client can read the private pipeline through the protocol
    And the MCP grant permits pipeline changes "<pipelines>" and hijack "<hijack>"

    Examples:
      | scopes               | pipelines | hijack |
      | read                 | no        | no     |
      | read pipelines:write | yes       | no     |
      | read hijack          | no        | yes    |

  Scenario: A viewer cannot gain pipeline write access by selecting a scope
    Given an authentication server with a private pipeline
    When a viewer authorizes an MCP client with pipeline write permission
    Then MCP denies changing the private pipeline

  Scenario: A read grant cannot use its owner's pipeline write permission
    Given an authentication server with a private pipeline
    When an MCP client requests "read" and the user selects "read"
    Then MCP denies changing the private pipeline

  Scenario Outline: Authorization codes remain bound to their request
    Given an authentication server with a private pipeline
    When an approved MCP authorization code is redeemed with the wrong "<binding>"
    Then the MCP token exchange is rejected

    Examples:
      | binding  |
      | client   |
      | verifier |
      | resource |

  Scenario: Refresh cannot broaden the user's consent
    Given an authentication server with a private pipeline
    When an MCP client requests "read" and the user selects "read"
    And the MCP client tries to refresh with pipeline write permission
    Then the MCP token exchange is rejected

  Scenario: A saved grant survives server restart and supports a real MCP client
    Given an authentication server with a private pipeline
    When an MCP client requests "read" and the user selects "read"
    And the MCP authorization server restarts with the same database
    And the MCP client renews its grant
    Then the MCP client can read the private pipeline through the protocol
    And MCP authorization state is encrypted in PostgreSQL

  Scenario: Reusing a rotated refresh credential revokes that grant
    Given an authentication server with a private pipeline
    When an MCP client requests "read" and the user selects "read"
    And the MCP client renews its grant
    And the MCP client replays the previous refresh credential
    Then the MCP token exchange is rejected
    And the replacement MCP access credential no longer authorizes requests

  Scenario: Revoking one client leaves another client's grant usable
    Given an authentication server with a private pipeline
    When two MCP clients receive independent read grants
    And the first MCP client revokes its grant
    Then the first MCP grant can neither read nor renew
    And the second MCP client can still read the private pipeline

  Scenario Outline: MCP preserves the API's authorization boundaries
    Given an authentication server with a private pipeline
    When a read grant encounters a "<boundary>" restriction
    Then both the API and MCP deny reading that pipeline

    Examples:
      | boundary    |
      | foreign team |
      | custom role  |
      | policy       |

  Scenario: API and MCP expose the same pipeline status
    Given an authentication server with a private pipeline
    When an MCP client requests "read" and the user selects "read"
    Then MCP pipeline status matches the authenticated API response

  Scenario: The MCP endpoint rejects expired access and legacy fly credentials
    Given an authentication server with a private pipeline
    When an MCP client requests "read" and the user selects "read"
    Then a legacy fly bearer cannot initialize MCP
    When the MCP access credential expires
    Then the expired MCP access credential is rejected

  Scenario: Calling a read tool without read consent requests the missing permission
    Given an authentication server with a private pipeline
    When an MCP client requests "hijack" and the user selects "hijack"
    Then calling pipeline status returns an insufficient read scope challenge

  Scenario: The reference client discovers, renews, persists and revokes its own grant
    Given an authentication server with a private pipeline
    When the reference MCP client logs in through discovery and browser consent
    And the reference client and authorization server restart with their saved state
    And the reference client's access credential expires
    Then the reference MCP client reads the private pipeline automatically
    And the reference client has saved the rotated refresh credential
    When the reference client logs out
    Then its saved grant is revoked and local credentials are removed
    When the reference MCP client logs in again
    Then the reference MCP client reads the private pipeline automatically
