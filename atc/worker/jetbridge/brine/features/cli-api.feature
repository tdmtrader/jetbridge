Feature: Fly downloads return the correct binary and reject path traversal

  Source: all 12 specs in atc/api/cli_test.go. Real tar.gz and zip archives,
  each with an unrelated member before the executable, are served over a real
  TCP listener by the production route, authentication/accessor wrappers, and
  download handler. Each scenario reads one complete HTTP response.

  Scenario: Darwin downloads return status 200
    When the real CLI download API handles profile "darwin"
    Then the CLI API returned status 200

  Scenario: Darwin downloads return Unix headers
    When the real CLI download API handles profile "darwin"
    Then the CLI API returned Unix download headers

  Scenario: Darwin downloads return the fly binary
    When the real CLI download API handles profile "darwin"
    Then the CLI API returned binary "soi soi soi"

  Scenario: Windows downloads return status 200
    When the real CLI download API handles profile "windows"
    Then the CLI API returned status 200

  Scenario: Windows downloads return executable headers
    When the real CLI download API handles profile "windows"
    Then the CLI API returned Windows download headers

  Scenario: Windows downloads return the executable binary
    When the real CLI download API handles profile "windows"
    Then the CLI API returned binary "soi soi soi.notavirus.bat"

  Scenario: Darwin platform matching is case insensitive for status
    When the real CLI download API handles profile "Darwin"
    Then the CLI API returned status 200

  Scenario: Darwin platform matching is case insensitive for the binary
    When the real CLI download API handles profile "Darwin"
    Then the CLI API returned binary "soi soi soi"

  Scenario: Windows platform matching is case insensitive for status
    When the real CLI download API handles profile "Windows"
    Then the CLI API returned status 200

  Scenario: Windows platform matching is case insensitive for the binary
    When the real CLI download API handles profile "Windows"
    Then the CLI API returned binary "soi soi soi.notavirus.bat"

  Scenario: Architecture path traversal is rejected
    When the real CLI download API handles profile "path-arch"
    Then the CLI API returned status 400

  Scenario: Platform path traversal is rejected
    When the real CLI download API handles profile "path-platform"
    Then the CLI API returned status 400
