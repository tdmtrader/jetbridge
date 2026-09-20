@live-kubernetes @VT-06 @VT-08
Feature: Reading artifacts from a real node

  The daemon serves owned storage on its actual scheduled node. Kubernetes
  supplies the node address; files are seeded through the independent observer.
  Production volume construction still resolves the node name through the API.

  @VT-06
  Scenario: An artifact comes back from the node that produced it
    Given an artifact daemon on a real node
    And it holds the artifact "abc" containing "tar data here"
    And the artifact was produced on that real node
    When a consumer reads the artifact "abc"
    Then the artifact arrives as "tar data here"

  @VT-06
  Scenario: A daemon that does not have the artifact says so
    Given an artifact daemon on a real node
    And the artifact was produced on that real node
    When a consumer reads the artifact "missing"
    Then the read fails saying "not found"

  @VT-08
  Scenario: A consumer that asks for compression can gunzip what arrives
    Given an artifact daemon on a real node
    And it holds the artifact "abc" containing the file "task.yml" with "platform: linux"
    And the artifact was produced on that real node
    And the consumer asks for a gzip-compressed stream
    When a consumer reads the artifact "abc"
    Then the stream is gzip compressed
    And the archive holds "task.yml" containing "platform: linux"

  @VT-08
  Scenario: A consumer that asks for no compression can hand the bytes straight to tar
    Given an artifact daemon on a real node
    And it holds the artifact "abc" containing the file "README.md" with "hello"
    And the artifact was produced on that real node
    When a consumer reads the artifact "abc"
    Then the stream is not compressed
    And the archive holds "README.md" containing "hello"

  @VT-08
  Scenario: Asking for one file inside an artifact gets that file and nothing else
    Given an artifact daemon on a real node
    And it holds the artifact "abc" containing the file "README.md" with "# My Repo"
    And the artifact "abc" also contains the file "ci/task.yml" with "platform: linux"
    And the artifact "abc" also contains the file "src/main.go" with "package main"
    And the artifact was produced on that real node
    And the consumer asks for the sub-path "ci/task.yml"
    And the consumer asks for a gzip-compressed stream
    When a consumer reads the artifact "abc"
    Then the archive holds "ci/task.yml" containing "platform: linux"
    And the archive holds that entry and nothing else

  @VT-08
  Scenario: Asking for the artifact root gets everything in it
    Given an artifact daemon on a real node
    And it holds the artifact "abc" containing the file "file1.txt" with "aaa"
    And the artifact "abc" also contains the file "file2.txt" with "bbb"
    And the artifact was produced on that real node
    When a consumer reads the artifact "abc"
    Then the archive holds exactly 2 entries

  @VT-08
  Scenario: Asking for a file that is not there succeeds with an empty archive
    Given an artifact daemon on a real node
    And it holds the artifact "abc" containing the file "README.md" with "hello"
    And the artifact was produced on that real node
    And the consumer asks for the sub-path "nonexistent.yml"
    And the consumer asks for a gzip-compressed stream
    When a consumer reads the artifact "abc"
    Then the read succeeds
    And the archive is empty

  Scenario: The producer's own copy wins over a mirrored one
    Given an artifact daemon on a real node
    And it holds the artifact "handle/output" containing the file "f.txt" with "producer-content"
    And it holds a mirrored copy of the artifact "handle/output" containing the file "f.txt" with "stale-peer-content"
    And the artifact was produced on that real node
    And the ATC can fall back to other daemons
    When a consumer reads the artifact "handle/output"
    Then the archive holds "f.txt" containing "producer-content"
