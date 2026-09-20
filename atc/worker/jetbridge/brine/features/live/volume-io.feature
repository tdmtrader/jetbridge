@live-kubernetes @VT-02 @VT-03 @VT-04 @VT-05
Feature: Moving artifacts through real pod volumes

  Real BusyBox pods provide isolated emptyDir mounts and SPDY exec.
  No host executor creates or rewrites the requested extraction paths.
  These direct-volume API tests do not replace daemon-backed artifact handoff.

  Scenario: An artifact comes back out as it went in
    Given a volume "inputs" mounted at "/tmp/build/inputs" with "direct" binding
    And a file "hello.txt" containing "hello world" is put into volume "inputs" at "." using "gzip"
    And a file "pipeline.yml" containing "jobs: []" is put into volume "inputs" at "." using "raw"
    When volume "inputs" is read from "."
    Then the artifact "hello.txt" containing "hello world" is there
    And reading only "pipeline.yml" from volume "inputs" at "/tmp/build/inputs" yields "jobs: []"
    And volume transfers retain their declared execution metadata

  # The only caller that passes a non-"." path to Volume.StreamIn. The
  # destination is created before extraction (including for an empty
  # archive) and travels as an argument, never as shell source.
  Scenario Outline: A nested upload keeps its destination — <encoding>, read <read path>
    Given a volume "inputs" mounted at "/tmp/build/inputs" with "direct" binding
    When a file "nested.txt" containing "deep content" is put into volume "inputs" at "sub/dir" using "<encoding>"
    Then the upload reaches volume "inputs" at "/tmp/build/inputs/sub/dir"
    When volume "inputs" is read from "<read path>"
    Then the artifact "sub/dir/nested.txt" containing "deep content" is there
    And volume transfers retain their declared execution metadata

    Examples:
      | encoding | read path |
      | gzip     | sub/dir   |
      | gzip     | .         |
      | raw      | .         |

  Scenario Outline: One step's output becomes the next step's input — <binding> binding
    Given a volume "output" mounted at "/tmp/build/workdir/output" with "<binding>" binding
    And another volume "input" mounted at "/tmp/build/workdir/input" with "<binding>" binding
    And a file "result.json" containing "built ok" is put into volume "output" at "." using "gzip"
    When the contents of volume "output" are moved into volume "input"
    And volume "input" is read from "."
    Then the artifact "result.json" containing "built ok" is there
    And volume transfers retain their declared execution metadata

    Examples:
      | binding  |
      | direct   |
      | deferred |
