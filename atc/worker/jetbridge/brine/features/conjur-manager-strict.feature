Feature: Conjur manager configuration detection

  Scenario Outline: Production Conjur manager configuration profile <profile> is <configured>
    When production go-flags parses the Conjur manager profile "<profile>"
    Then the Conjur manager is configured "<configured>" without a parse error

    Examples:
      | profile       | configured |
      | empty         | false      |
      | appliance-url | true       |
