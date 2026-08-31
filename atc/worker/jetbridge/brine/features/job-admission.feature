Feature: Which jobs the scheduler is allowed to look at, and what it is handed

  The scheduler's component runner does not choose what to work on. Every tick
  it asks `JobFactory.JobsToSchedule` for the whole cluster's answer, and works
  through whatever comes back. A job that is not in that set is a job that does
  not run — no build appears, nothing is logged, and the job page says exactly
  what it said before.

  So this is the admission policy, and it is four columns wide:

      j.schedule_requested > j.last_scheduled
      j.active
      NOT j.paused
      NOT p.paused

  Beside that, the query attaches what the scheduler will need once it has the
  job: the resources the job's plan names, the pipeline's custom resource
  types, and the pipeline's prototypes. Each is scoped, and each is scoped
  differently — resources through `job_inputs UNION job_outputs` for the JOB,
  types and prototypes by `pipeline_id` for the PIPELINE. Getting either scope
  wrong is not a crash; it is a scheduler that checks a resource belonging to
  somebody else's pipeline.

  This file is the neighbour of build-scheduling.feature, not a duplicate of
  it. That one starts after a job has been admitted: it hands
  `JobsToScheduleByIDs` an id it already has, and asks what the scheduler then
  does with the job. It cannot see the policy at all — passing an explicit id
  and asserting on the one job that came back cannot tell "the policy admitted
  this" apart from "the policy is gone". The vocabulary is shared where it
  fits; the subject is the prior question.

  There is no double here. Teams, pipelines and jobs are real rows on the
  scenario's own PostgreSQL, saved through `Team.SavePipeline`, and the answer
  is whatever `jobsToSchedule` returns.

  # ==========================================================================
  # Admission
  # ==========================================================================
  #
  # Six ways a job can stand with respect to the policy, and only the first of
  # them gets the job looked at.
  #
  # Every row carries a CONTROL job — its own team, its own pipeline, always
  # due — and asserts it FIRST. That is not decoration. Five of these six rows
  # assert that a job is ABSENT from a set, and an absent job is also what you
  # get from a query that has stopped answering. Measured: under M6 below the
  # query returns nothing at all, and without the control line every one of
  # those five rows would have gone green on a `JobsToSchedule` that had
  # stopped admitting anything.
  #
  # The control also sits in a different TEAM, which is the whole of what the
  # ginkgo suite's "multiple jobs with different times" case asserted beyond
  # the single-job cases: the set spans teams and pipelines, and the due job in
  # one team does not drag the not-due job in another along with it.
  #
  # The two time rows are the two sides of one strict inequality. "Scheduled
  # after it asked" is the ordinary state of a job the scheduler has already
  # been round for. "Scheduled at the very moment it asked" is the boundary,
  # and it is the row that pins `>` rather than `>=`: a scheduling pass sets
  # last_scheduled to exactly the schedule_requested it read, so under `>=`
  # that job never leaves the set and the scheduler re-runs it on every tick
  # for ever.
  #
  # Reddened by, measured, against an overlaid atc/db/job_factory.go:
  #
  #   M1  `j.schedule_requested > j.last_scheduled` -> `>=`
  #       1 red — the "was scheduled at the very moment it asked" row only:
  #
  #         And the scheduler will not schedule "subject"
  #           expected the jobs the scheduler will schedule not to include
  #           "subject", but it does: [control subject] (the scheduler came
  #           back with control{resources: nothing; types: nothing; prototypes:
  #           nothing}, subject{resources: nothing; types: nothing;
  #           prototypes: nothing})
  #
  #   M2  the whole `schedule_requested > last_scheduled` Where deleted
  #       2 red — both time rows, on the same line, and nothing else.
  #
  #   M3  `"j.active": true` dropped from the Eq map
  #       1 red — the "dropped from its pipeline's configuration" row only.
  #
  #   M4  `"j.paused": false` dropped
  #       1 red — the "has been paused" row only.
  #
  #   M5  `"p.paused": false` dropped
  #       1 red — the "has its pipeline paused" row only.
  #
  #   M6  `if len(jobIDs) > 0` -> `if len(jobIDs) >= 0`, so JobsToSchedule is
  #       narrowed by the empty id list the way JobsToScheduleByIDs is narrowed
  #       13 red — EVERY scenario in this file, each on its first Then:
  #
  #         Then the scheduler will schedule "control"
  #           expected the jobs the scheduler will schedule to include
  #           "control", found [] (the scheduler came back with no jobs at all)
  #
  #       This is the mutation build-scheduling.feature structurally cannot
  #       see: every scenario there passes an explicit id, so narrowing by id
  #       is invisible to it.
  #
  # One line here is DECORATIVE, and it is on the page rather than quietly
  # dropped. On the first row the second Then reads "will schedule", and no
  # mutation in the battery below makes it the first line to fail: control and
  # subject are structurally the same job on that row, so anything that stops
  # admitting the subject stops admitting the control first, and brine reports
  # the control. It is kept because the fate column needs both poles for the
  # table to say what it is contrasting, not because it discriminates.
  Scenario Outline: A job is looked at only when nothing is holding it back — <case>
    Given a job "control" in its own pipeline, asking to be scheduled
    And another job "subject" in its own pipeline, asking to be scheduled
    And the job "subject" <change>
    When the scheduler reads which jobs to schedule
    Then the scheduler will schedule "control"
    And the scheduler <fate> "subject"

    Examples:
      | case                                | change                                       | fate              |
      | nothing is                          | is left alone                                | will schedule     |
      | the scheduler has already been      | was scheduled after it asked                 | will not schedule |
      | the last pass landed on the request | was scheduled at the very moment it asked    | will not schedule |
      | the job is paused                   | has been paused                              | will not schedule |
      | the job is no longer configured     | is dropped from its pipeline's configuration | will not schedule |
      | the pipeline is paused              | has its pipeline paused                      | will not schedule |

  # ==========================================================================
  # What the job is handed: resources
  # ==========================================================================
  #
  # A job is handed the resources ITS PLAN NAMES, and no others. The pipeline
  # in every row below also holds an unused resource, and the scheduler must
  # never be given it: checking a resource nobody's job uses is work nobody
  # asked for, and handing it to the algorithm as an input the job does not
  # have would let a version of it decide whether the job runs.
  #
  # The four rows are the four ways a plan can mention a resource, and they are
  # not four spellings of one thing. `jobsToSchedule` reads
  # `job_inputs UNION job_outputs` — a get lands in one table, a put in the
  # other, and a resource named in both must still come back ONCE. Measured,
  # each of the three interesting rows is the ONLY row a different mutation
  # reddens.
  #
  # The comparison is the whole rendered line — name, type and source
  # together — because a resource carried without its type or its source is one
  # the scheduler cannot check. Splitting it into three assertions would leave
  # the reader holding them together for no gain, and would put two of them
  # after the first failing line, where brine never reaches them.
  #
  # Reddened by, measured:
  #
  #   M7  the `UNION SELECT jo.resource_id from job_outputs` arm deleted
  #       1 red — the "puts the resource" row only:
  #
  #         And the resources handed to "subject" are "some-resource (some-type, some:source)"
  #           expected the resources handed to the job for "subject" to be
  #           "some-resource (some-type, some:source)", got "nothing" (the
  #           scheduler came back with subject{resources: nothing; types:
  #           nothing; prototypes: nothing})
  #
  #       The "gets and puts" row stays GREEN under it — the resource is still
  #       found through job_inputs — which is why both rows are here.
  #
  #   M8  `UNION` -> `UNION ALL`
  #       1 red — the "gets and puts the resource" row only:
  #
  #         And the resources handed to "subject" are "some-resource (some-type, some:source)"
  #           expected the resources handed to the job for "subject" to be
  #           "some-resource (some-type, some:source)", got "some-resource
  #           (some-type, some:source), some-resource (some-type, some:source)"
  #
  #   M9  `Join inputs i on i.resource_id = r.id` replaced by a join on the
  #       job's pipeline, so the job is handed every resource the pipeline has
  #       5 red — all four rows here and the two-pipeline scenario below.
  #       Including the "names no resources" row, which is what makes that row
  #       worth keeping rather than folding away as a degenerate case:
  #
  #         And the resources handed to "subject" are "nothing"
  #           expected the resources handed to the job for "subject" to be
  #           "nothing", got "some-resource (some-type, some:source),
  #           unused-resource (some-type, other:source)"
  #
  #   M10 `Source: config.Source` -> `Source: nil`
  #       3 red — the three rows that name a resource:
  #
  #         expected the resources handed to the job for "subject" to be
  #         "some-resource (some-type, some:source)", got "some-resource (some-type)"
  Scenario Outline: A job is handed the resources its plan names — <case>
    Given a job "subject" whose plan <uses>, in a pipeline with a used and an unused resource
    When the scheduler reads which jobs to schedule
    Then the scheduler will schedule "subject"
    And the resources handed to "subject" are "<handed>"

    Examples:
      | case                      | uses                       | handed                                 |
      | it names none             | names no resources         | nothing                                |
      | it gets one               | gets the resource          | some-resource (some-type, some:source) |
      | it puts one               | puts the resource          | some-resource (some-type, some:source) |
      | it gets and puts the same | gets and puts the resource | some-resource (some-type, some:source) |

  # Resources are scoped to the JOB, and the scope is `job_inputs` — not the
  # resource's name. Both pipelines here call a resource "some-resource" and
  # mean different things by it, which is ordinary: resource names are unique
  # within a pipeline and nowhere else.
  #
  # The type is what makes the claim legible. If the lookup ever resolved by
  # name across pipelines, job-3 would be handed pipeline-one's
  # `some-resource (some-type)` instead of its own `(other-type)`, and the
  # scheduler would check the wrong resource for it — silently, since a
  # resource of the right name exists either way.
  #
  # job-1 and job-2 are in the same pipeline and are handed different sets,
  # which is the per-job half; job-3 is in the other pipeline, which is the
  # per-pipeline half. The unused resource in pipeline-one is handed to
  # neither.
  #
  # Reddened by, measured:
  #
  #   M11 the inputs arm's `where ji.job_id = $1` -> `where ji.job_id > 0`
  #       1 red — this scenario only, on its first line:
  #
  #         Then the resources handed to "job-1" are "some-resource (some-type)"
  #           expected the resources handed to the job for "job-1" to be
  #           "some-resource (some-type)", got "other-resource (some-type),
  #           some-resource (other-type), some-resource (some-type),
  #           some-resource-2 (other-type)"
  #
  #   M9 above also reddens this scenario, on the same line, with
  #   pipeline-one's unused resource included instead.
  #
  #   M17 ` LIMIT 1` appended to the resource query, so a job is handed only
  #       the first resource it names
  #       1 red — this scenario, on its SECOND line, because job-1 names one
  #       resource and still gets it:
  #
  #         And the resources handed to "job-2" are "other-resource (some-type), some-resource (some-type)"
  #           expected the resources handed to the job for "job-2" to be
  #           "other-resource (some-type), some-resource (some-type)", got
  #           "other-resource (some-type)"
  #
  #   M19 `SELECT r.name, r.type, ...` -> `SELECT r.name, 'some-type', ...`
  #       1 red — this scenario, on its THIRD line, and nowhere else in the
  #       file, because every other scenario's resource is of type
  #       "some-type" already. This is the mutation the shared resource NAME
  #       exists for: the two pipelines differ only in the type, so only a
  #       job in the second one can see a type that came from the first:
  #
  #         And the resources handed to "job-3" are "some-resource (other-type), some-resource-2 (other-type)"
  #           expected the resources handed to the job for "job-3" to be
  #           "some-resource (other-type), some-resource-2 (other-type)", got
  #           "some-resource (some-type), some-resource-2 (some-type)"
  #
  #   Between M9/M11, M17 and M19 each of this scenario's three lines has been
  #   the first to fail under some mutation. That is the property worth having
  #   here: brine stops at the first failing step, so an assertion that can
  #   only ever be preceded by another failing one is never actually run.
  Scenario: Two pipelines that use one resource name for two different things
    Given two pipelines that give one resource name two different types, with three jobs between them
    When the scheduler reads which jobs to schedule
    Then the resources handed to "job-1" are "some-resource (some-type)"
    And the resources handed to "job-2" are "other-resource (some-type), some-resource (some-type)"
    And the resources handed to "job-3" are "some-resource (other-type), some-resource-2 (other-type)"

  # ==========================================================================
  # What the job is handed: custom types
  # ==========================================================================
  #
  # Custom resource types and prototypes belong to a PIPELINE, not to a job.
  # Every job in a pipeline is handed all of them whether its plan mentions
  # them or not — it has to be, because the type a resource names may itself be
  # defined in terms of another one — and no job is handed another pipeline's.
  #
  # They are two separate queries in `jobsToSchedule`, each with its own
  # per-pipeline memo, so one fixture defines the same three names as BOTH and
  # the outline asserts each in turn rather than one standing in for the other.
  # Measured, that is not redundancy: M12 reddens the resource-types row and
  # leaves the prototypes row green, and M13 does the reverse.
  #
  # Reddened by, measured:
  #
  #   M12 the resource-types query's `Where(sq.Eq{"r.pipeline_id": ...})`
  #       dropped
  #       1 red — the resource-types row, on its first line:
  #
  #         Then the resource types handed to "job-1" are "alpha (base-type)"
  #           expected the resource types handed to the job for "job-1" to be
  #           "alpha (base-type)", got "alpha (base-type), beta (base-type),
  #           gamma (base-type)"
  #
  #   M13 the prototypes query's `Where(sq.Eq{"pt.pipeline_id": ...})` dropped
  #       1 red — the prototypes row, symmetrically:
  #
  #         Then the prototypes handed to "job-1" are "alpha (base-type)"
  #           expected the prototypes handed to the job for "job-1" to be
  #           "alpha (base-type)", got "alpha (base-type), beta (base-type),
  #           gamma (base-type)"
  #
  #       The ginkgo suite stays GREEN under M13: its single prototype spec
  #       builds one pipeline, so an unscoped prototype query returns the same
  #       rows. This scenario is the only thing in the repository that fails.
  #
  #   M18 both per-pipeline memos keyed on the constant 0, so the first
  #       pipeline's types are served to every job after it
  #       2 red — BOTH rows, each on its THIRD line, because job-1 populates
  #       the memo and job-2 is in the same pipeline and so is served the right
  #       answer by accident:
  #
  #         And the resource types handed to "job-3" are "beta (base-type), gamma (base-type)"
  #           expected the resource types handed to the job for "job-3" to be
  #           "beta (base-type), gamma (base-type)", got "alpha (base-type)"
  #
  #       This is the memo bug worth having a shape for, and it is the reason
  #       job-3 is in another pipeline rather than another job in this one.
  #
  #   M14 both memos keyed on `job.ID()` instead of `job.PipelineID()`
  #       0 red, here and in ginkgo — recorded next to M18 because it is the
  #       other direction of the same edit and looks equally like a bug. It is
  #       not one: a map keyed per job is a map that never HITS, so every job
  #       re-runs the query and gets the right answer. Only the query count
  #       changes, and a query count is not something a job page shows.
  #
  # The SECOND line of each row is decorative, and is on the page rather than
  # quietly dropped. Nothing in the battery makes "the <kind> handed to job-2"
  # the first line to fail: it is in job-1's pipeline, so M12 and M13 reach
  # job-1 first, and both memo mutations leave it right — M14 because the memo
  # never hits and M18 because a hit on job-1's own pipeline is correct. It is
  # kept because a reader needs to see that two jobs in one pipeline are handed
  # the same thing, which is the half of the scoping rule the other two lines
  # do not state.
  Scenario Outline: A job is handed its own pipeline's <kind>
    Given two pipelines that define their own custom types, with two jobs in the first and one in the second
    When the scheduler reads which jobs to schedule
    Then the <kind> handed to "job-1" are "alpha (base-type)"
    And the <kind> handed to "job-2" are "alpha (base-type)"
    And the <kind> handed to "job-3" are "beta (base-type), gamma (base-type)"

    Examples:
      | kind           |
      | resource types |
      | prototypes     |

  # ==========================================================================
  # Every mutation run against this file
  # ==========================================================================
  #
  # 19 mutations of atc/db/job_factory.go, each built with `go build -overlay`
  # and run against BOTH this file and the 15 ginkgo specs it replaces
  # (`-ginkgo.focus="JobFactory JobsToSchedule"`, 15 of 1032).
  #
  #        mutation                                  brine   ginkgo
  #   M1   `>` -> `>=` on the schedule times           1        1
  #   M2   the schedule-time Where deleted             2        3
  #   M3   `j.active` dropped                          1        1
  #   M4   `j.paused` dropped                          1        1
  #   M5   `p.paused` dropped                          1        1
  #   M6   JobsToSchedule narrowed by job id          13       10
  #   M7   the job_outputs arm of the UNION deleted     1        1
  #   M8   `UNION` -> `UNION ALL`                       1        1
  #   M9   the inputs join replaced by a pipeline join  5        4
  #   M10  a resource's Source dropped                  3        3
  #   M11  the inputs arm unscoped from the job         1        1
  #   M12  resource types unscoped from the pipeline    1        1
  #   M13  prototypes unscoped from the pipeline        1        0
  #   M14  both memos keyed per job (never hits)        0        0
  #   M15  `OrderBy` dropped from types and prototypes  0        0
  #   M16  ExposeBuildCreatedBy -> false                0        0
  #   M17  the resource query capped at one row         1        1
  #   M18  both memos keyed on a constant (always hits) 2        1
  #   M19  the resource type column hardcoded           1        1
  #
  # THREE of them reddened nothing, and they are recorded next to what they
  # disprove rather than discarded.
  #
  # M14 is discussed above: it disproves that the two-jobs-in-one-pipeline
  # shape is what pins the memo. M18 is the edit that does.
  #
  # M15 disproves the idea that the `OrderBy("r.name")` in the two type queries
  # is load-bearing. Neither suite pins it — ginkgo compares with `ConsistOf`,
  # and the checks here sort before comparing. That is deliberate: the
  # resources query has no ORDER BY at all, so a scenario that asserted an
  # order for one collection and not the others would be asserting the shape of
  # a SELECT rather than what the scheduler is handed.
  #
  # M16 is a real hole, and it is one the ginkgo suite has too:
  # `SchedulerResource.ExposeBuildCreatedBy` is projected out of the resource
  # config and NOTHING in either suite reads it. It is left open rather than
  # papered over, because the behaviour it drives — whether a build records who
  # triggered it, visibly — belongs to a scenario about a BUILD, not to one
  # about which jobs the scheduler may look at.
  #
  # ==========================================================================
  # Per-spec evidence for the ginkgo block this replaces
  # ==========================================================================
  #
  # atc/db/job_factory_test.go:349-1098, `Describe("JobsToSchedule")`, 15 It
  # blocks. Every one of them is red under a mutation that also reddens a named
  # scenario here — file-level both-red would not be evidence, so this is the
  # per-spec table.
  #
  #   ginkgo It                                       mutation  scenario here
  #   requested later -> fetches that job               M6      admission row 1
  #   requested earlier -> does not fetch               M2      admission row 2
  #   requested same -> does not fetch                  M1      admission row 3
  #   multiple jobs with different times                M2      admission rows 2, 3
  #   paused job -> does not fetch                      M4      admission row 4
  #   inactive job -> does not fetch                    M3      admission row 5
  #   paused pipeline -> does not fetch                 M5      admission row 6
  #   no resources -> job and no resources              M6      resources row 1
  #   uses resources -> the used resource               M9      resources rows 1-4
  #   multiple jobs use resources                       M17     two-pipeline scenario
  #   resources as puts                                 M7      resources row 3
  #   the resource as a put AND a get                   M8      resources row 4
  #   pipeline uses custom resource types               M6      kind row 1
  #   multiple pipelines use custom resource types      M18     kind rows 1, 2
  #   pipeline uses custom prototypes                   M6      kind row 2
  #
  # No mutation in this battery reddened a ginkgo spec while leaving this file
  # green. One went the other way: M13 reddens the prototypes row here and
  # nothing in ginkgo.
  #
  # ==========================================================================
  # What this file does not reach
  # ==========================================================================
  #
  # `JobFactory.VisibleJobs` and `AllActiveJobs` — the dashboard half of the
  # same file, and the other 200-odd lines of that ginkgo suite. Different
  # subject: what an operator is shown, not what the scheduler is allowed to
  # run. Those Describes stay.
  #
  # `SchedulerResource.ApplySourceDefaults` — called by the scheduler after
  # this query returns, not by it, and a pure function over a resource-type
  # list. A Gherkin sentence about it would be a unit test wearing one.
