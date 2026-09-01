Feature: Small Go client surfaces cross the production API

  Source: 10 initial specs: all 7 in
  go-concourse/concourse/{info,user,users,workers,cli}_test.go, plus the version
  response in atc/api/info_test.go:50 and persisted worker registration/admin
  listing in atc/api/workers_test.go:221/:168. The users and CLI handler specs
  traversed here are deliberately not counted because users-api.feature and
  cli-api.feature already own them.

  Scenario Outline: Small client surface <surface> produces <result>
    When the production Go client handles small surface "<surface>"
    Then the small client result is "<result>"

    Examples:
      | surface     | result                         |
      | info        | version=brine;worker=brine-worker |
      | user        | user=brine-user;admin=true     |
      | users       | bob                            |
      | worker-save | name=client-worker;state=running |
      | worker-list | client-worker                  |
      | cli         | body=soi soi soi;length=11     |
