Feature: Fly downloads return the correct binary and reject path traversal

  Source: all 12 specs in atc/api/cli_test.go. Real tar.gz and zip archives
  are served by the production router and download handler.

  Scenario: Darwin downloads return headers and the fly binary
    When the real CLI download API handles profile "darwin"
    Then the CLI API returned status 200
    And the CLI API returned Unix download headers
    And the CLI API returned binary "soi soi soi"

  Scenario: Windows downloads return headers and the executable
    When the real CLI download API handles profile "windows"
    Then the CLI API returned status 200
    And the CLI API returned Windows download headers
    And the CLI API returned binary "soi soi soi.notavirus.bat"

  Scenario Outline: Platform matching is case insensitive for <platform>
    When the real CLI download API handles profile "<platform>"
    Then the CLI API returned status 200
    And the CLI API returned binary "<body>"

    Examples:
      | platform | body                         |
      | Darwin   | soi soi soi                  |
      | Windows  | soi soi soi.notavirus.bat    |

  Scenario Outline: Path traversal in <field> is rejected
    When the real CLI download API handles profile "path-<field>"
    Then the CLI API returned status 400

    Examples:
      | field    |
      | arch     |
      | platform |
