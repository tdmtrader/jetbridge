# Operator observations — `agent_run_transcripts` empty after every run

Field notes taken 2026-07-20 against the live cluster, before any hypothesis was
formed. These record what was run and what came back; where a conclusion is
stated it is only what that experiment directly showed. Ticket/build identifiers
have been normalised.

---

## 1. Deployment state

| Component | State |
|---|---|
| `concourse-web` | rebuilt and rolled out from the tip of `jetbridge` — contains the transcript migration, the store construction in `atc/atccmd/command.go`, the step-factory wiring, the read route, and the ingestion code |
| `agent-runner` image | rebuilt and rolled out from a commit that includes the `flight/transcript.ndjson` capture; this is the image the dispatched runs actually pulled |
| migration head | `1773106093` — applied, no pending migrations |
| dispatcher | enabled; runs dispatched normally |

Both halves of ticket #43 are therefore live. This is not a "not deployed yet"
problem.

## 2. The table exists and is empty

```
cicd=> \d agent_run_transcripts
                    Table "public.agent_run_transcripts"
   Column   |           Type           | Nullable |      Default
------------+--------------------------+----------+-------------------
 build_id   | integer                  | not null |
 plan_id    | text                     | not null |
 ticket_id  | integer                  |          |
 step_name  | text                     |          |
 ndjson     | text                     | not null |
 byte_len   | integer                  | not null |
 truncated  | boolean                  | not null | false
 created_at | timestamp with time zone | not null | now()
Indexes:
    "agent_run_transcripts_pkey" PRIMARY KEY, btree (build_id, plan_id)
    "agent_run_transcripts_ticket" btree (ticket_id) WHERE ticket_id IS NOT NULL

cicd=> SELECT count(*) FROM agent_run_transcripts;
 count
-------
     0
(1 row)
```

Zero rows overall — not zero rows for one ticket. Nothing has ever been inserted.

## 3. The same runs DID record metrics

Taking one representative completed run (ticket #45, build 588241, workflow
`develop` v2, finished `succeeded`):

```
cicd=> SELECT step_name, status, turns, round(cost_usd::numeric, 3) AS cost,
              event_counts ? 'step.end' AS saw_step_end
       FROM agent_run_metrics WHERE build_id = 588241 ORDER BY step_name;
 step_name | status | turns | cost  | saw_step_end
-----------+--------+-------+-------+--------------
 harvest   | ok     |     4 | 0.180 | t
 implement | ok     |    61 | 3.410 | t
(2 rows)

cicd=> SELECT count(*) FROM agent_run_transcripts WHERE build_id = 588241;
 count
-------
     0
(1 row)
```

Spot-checked the same way on four more completed runs: same shape every time —
two `agent_run_metrics` rows, zero `agent_run_transcripts` rows.

`agent_cost_ledger` also has its normal entries for these builds.

## 4. The read path behaves exactly as it should for "no row"

```
$ fly -t cicd agent runs transcript --ticket 45 --build 588241
error: no transcript available for run

$ curl -sS -o /dev/null -w '%{http_code}\n' \
    -H "Authorization: Bearer $TOKEN" \
    "$ATC/api/v1/agent/tickets/45/runs/588241/transcript"
404
```

Hand-inserting a row for `(588241, <a plan id from that build>)` with `psql`
makes both of the above return the transcript immediately. The route, the client
method and the CLI are fine; they are being asked for something that is not
there.

## 5. Nothing failed visibly

- Build 588241 is green. Both steps are green.
- The build log carries the usual runner output. In particular it contains **no**
  `agent-runner: write transcript: ...` line — that is the message the runner
  prints to stderr if persisting the transcript fails, so the runner did not
  report a write failure.
- The step summaries, the ticket transition and the pushed branch are all normal.
- No user-visible error appears anywhere in the UI or the build output relating
  to transcripts.

Nobody noticed for two days; it was found only because someone went looking for a
transcript to debug an unrelated empty run.

## 6. What has NOT been done

- No one has shelled into a completed agent pod or dumped a flight volume — the
  pods are reaped shortly after the build ends, and the runs above are long gone.
- No cluster access is available for this analysis. Treat the repository at the
  current tip of `jetbridge` plus these notes as the whole evidence base.
