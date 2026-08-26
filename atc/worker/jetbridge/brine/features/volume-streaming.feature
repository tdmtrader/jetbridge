@VT-02 @VT-03 @VT-04 @VT-05
Feature: Moving artifacts through volumes

  What a consumer of a volume actually does: put an artifact in, take it back
  out, hand it to the next step. Where it lands on disk, what command moved it,
  and which container ran that command are all mechanism.

  Source: jetbridge_storage_behavioral_spec_20260330 — VT-02 (StreamIn),
  VT-03 (StreamOut), VT-04 (path resolution), VT-05 (stub volumes).

  These scenarios replace ginkgo tests that asserted `call.podName`,
  `call.containerName`, `call.attrs.Purpose` and
  `call.command == ["tar","xf","-","-C","/tmp/build/inputs"]` against a
  recording double. Each scenario below fails for a real consumer; those did
  not.

  Scenario: An artifact comes back out as it went in
    Given a volume "inputs" mounted at "/tmp/build/inputs"
    And a file "hello.txt" containing "hello world" is put into volume "inputs" at "."
    When volume "inputs" is read from "."
    Then the artifact "hello.txt" containing "hello world" is there

  # The round trip is NOT symmetric, and a consumer has to know it. StreamIn
  # resolves the path into the extraction target, so the artifact lands at
  # <mount>/sub/dir/nested.txt. StreamOut keeps the mount as the extraction
  # root and passes the path as a member SELECTOR, so members come back
  # carrying their path — whichever path you ask for. VT-02 and VT-03 specify
  # exactly this; no test had made it visible, because the ginkgo tests
  # compared command strings and command strings do not show it.
  Scenario: A member keeps its path when read back from that path
    Given a volume "inputs" mounted at "/tmp/build/inputs"
    And a file "nested.txt" containing "deep content" is put into volume "inputs" at "sub/dir"
    When volume "inputs" is read from "sub/dir"
    Then the artifact "sub/dir/nested.txt" containing "deep content" is there

  Scenario: The same member is reachable from the volume root
    Given a volume "inputs" mounted at "/tmp/build/inputs"
    And a file "nested.txt" containing "deep content" is put into volume "inputs" at "sub/dir"
    When volume "inputs" is read from "."
    Then the artifact "sub/dir/nested.txt" containing "deep content" is there

  # The story the volume-to-volume ginkgo test was really about. It asserted
  # two pod names, two command strings, and that the fake's canned bytes
  # reached the fake's recorded stdin. What matters is that the artifact
  # arrives.
  Scenario: One step's output becomes the next step's input
    Given a volume "output" mounted at "/tmp/build/workdir/output"
    And another volume "input" mounted at "/tmp/build/workdir/input"
    And a file "result.json" containing "built ok" is put into volume "output" at "."
    When the contents of volume "output" are moved into volume "input"
    And volume "input" is read from "."
    Then the artifact "result.json" containing "built ok" is there

  @VT-05
  Scenario: A stub volume refuses to be read rather than panicking
    Given a volume "real" mounted at "/tmp/build/inputs"
    And a stub volume "stub" with no cluster behind it
    When volume "stub" is read from "."
    Then it fails rather than panicking, saying "no executor"

  @VT-05
  Scenario: A stub volume refuses to be written rather than panicking
    Given a volume "real" mounted at "/tmp/build/inputs"
    And a stub volume "stub" with no cluster behind it
    When a file is put into volume "stub"
    Then it fails rather than panicking, saying "no executor"

  Scenario: A cluster failure reaches the reader rather than being swallowed
    Given a volume "real" mounted at "/tmp/build/inputs"
    And volume "broken" sits on a cluster that cannot run commands
    When volume "broken" is read from "."
    Then it fails rather than panicking, saying "exec failed"
