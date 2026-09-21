Feature: Run cancellation is an authorized durable command

  @core-review
  Scenario: Archiving the template does not disable cancellation
    Given a Run cancellation API client as "owner"
    When the client archives its Run template
    And it posts a "valid" cancellation request for the "known" Run
    Then the cancellation API reports "accepted" with a running Run

  @core-review
  Scenario: An authorized request can be repeated after a lost response
    Given a Run cancellation API client as "owner"
    When it posts a "valid" cancellation request for the "known" Run
    Then the cancellation API reports "accepted" with a running Run
    When it posts a "replacement" cancellation request for the "known" Run
    Then the cancellation API reports "already_requested" with a running Run
    And its first cancellation reason and requester remain authoritative

  @core-review
  Scenario Outline: Cancellation permission is checked before Run lookup
    Given a Run cancellation API client as "<caller>"
    When it posts a "valid" cancellation request for the "known" Run
    Then its cancellation request is denied with HTTP <status>
    When it posts a "valid" cancellation request for the "unknown" Run
    Then its cancellation request is denied with HTTP <status>
    Examples:
      | caller     | status |
      | viewer     | 403    |
      | cross-team | 403    |
      | anonymous  | 401    |

  @core-review
  Scenario: An authorized unknown Run has an ordinary not-found response
    Given a Run cancellation API client as "owner"
    When it posts a "valid" cancellation request for the "unknown" Run
    Then its cancellation request is denied with HTTP 404

  @core-review
  Scenario Outline: Cancellation accepts only its closed request shape
    Given a Run cancellation API client as "owner"
    When it posts a "<body>" cancellation request for the "known" Run
    Then its cancellation request is denied with HTTP 400
    And the rejected API request leaves no cancellation facts
    Examples:
      | body            |
      | forged owner    |
      | forged time     |
      | forced status   |
      | duplicate field |
      | wrong case      |
      | null reason     |
      | control reason  |
      | oversized       |
      | trailing JSON   |
      | invalid UTF-8   |

  @core-review
  Scenario: Authorized status exposes cancellation without changing Run status
    Given a Run cancellation API client as "owner"
    When it posts a "valid" cancellation request for the "known" Run
    And it reads the Run detail through the authorized API
    Then its Run detail describes the accepted cancellation while still running

  @core-review
  Scenario Outline: Cancel availability follows the caller permission
    Given a Run cancellation API client as "<caller>"
    When it reads the Run detail through the authorized API
    Then its Run cancellation control is "<availability>"
    Examples:
      | caller | availability |
      | owner  | available    |
      | viewer | unavailable  |

  @core-review
  Scenario: An exposed Run does not disclose cancellation facts publicly
    Given a Run cancellation API client as "owner"
    When it posts a "valid" cancellation request for the "known" Run
    And the client exposes the template and reads its Run anonymously
    Then the public Run detail omits every cancellation fact

  @core-review
  Scenario: Audit records include the outcome without the reason
    Given a Run cancellation API client as "owner"
    When it posts cancellation with private reason text also in the query
    Then the cancellation API reports "accepted" with a running Run
    And cancellation audit records omit private reason text
