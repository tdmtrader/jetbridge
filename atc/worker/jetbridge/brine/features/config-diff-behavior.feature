Feature: Pipeline config diffs explain operator-visible changes

  Source: all 11 specs in atc/config_diff_test.go. Scenarios call Config.Diff
  with concrete old/new configs and inspect its rendered, ANSI-stripped output.

  Scenario Outline: Config diff profile <profile>
    Given the production config diff evaluates profile "<profile>"
    Then the config diff observation <comparison> "<expected>"

    Examples:
      | profile                   | comparison | expected                                                                          |
      | jobs/none                 | is         | changed=false;output=                                                             |
      | jobs/added                | contains   | changed=true ;; job some-job has been added: ;; + name: some-job ;; + plan: ;; + - get: some-name ;; + resource: some-resource ;; + trigger: true |
      | jobs/removed              | contains   | changed=true ;; job some-job has been removed: ;; - name: some-job ;; - plan: ;; - - get: some-name ;; - resource: some-resource ;; - trigger: true |
      | jobs/unchanged            | is         | changed=false;output=                                                             |
      | jobs/remove-default-field | contains   | changed=true ;; job some-job has changed: ;; - trigger: true                     |
      | jobs/replace-field        | contains   | changed=true ;; job some-job has changed: ;; - resource: some-resource ;; + resource: some-other-resource |
      | display/none              | is         | changed=false;output=                                                             |
      | display/added             | contains   | changed=true ;; display configuration has been added: ;; + background_image: some-background.jpg |
      | display/removed           | contains   | changed=true ;; display configuration has been removed: ;; - background_image: some-background.jpg |
      | display/unchanged         | is         | changed=false;output=                                                             |
      | display/replaced          | contains   | changed=true ;; display configuration has changed: ;; - background_image: some-background.jpg ;; + background_image: some-other-background.jpg |
