@authentication @mcp-operations
Feature: Precise MCP operations preserve application authority
  The real issuer, PostgreSQL, SDK transport and wrapped API execute each action.

  Scenario: Complete pipeline and build workflow with exact instance identity
    Given an authentication server with a private pipeline
    When an MCP client requests "read pipelines:write builds:write" and the user selects "read pipelines:write builds:write"
    Then precise MCP completes the pipeline and build workflow

  Scenario: Write-only consent and separate custom-role actions
    Given an authentication server with a private pipeline
    When an MCP client requests "read" and the user selects "read"
    Then precise MCP preserves independent and changing authorities

  Scenario: Metadata-only diagnosis and guessed calls with an admin-only grant
    Given an authentication server with a private pipeline
    When an MCP client requests "admin" and the user selects "admin"
    Then precise MCP distinguishes missing consent from unsupported and malformed calls
