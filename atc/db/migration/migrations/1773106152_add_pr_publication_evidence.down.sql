DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_publication_inputs) THEN
        RAISE EXCEPTION
            'cannot remove PR publication evidence while additional publication inputs exist';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM agent_publication_approval_evidence evidence
        LEFT JOIN agent_publication_occurrences occurrence
          ON occurrence.id = evidence.publication_id
        WHERE evidence.evidence_kind <> 'human_wait'
           OR occurrence.id IS NULL
           OR evidence.human_wait_id IS DISTINCT FROM occurrence.approval_wait_id
           OR evidence.question_snapshot_id IS DISTINCT FROM occurrence.approval_question_snapshot_id
           OR evidence.answer_snapshot_id IS DISTINCT FROM occurrence.approval_answer_snapshot_id
           OR evidence.resolved_by IS DISTINCT FROM occurrence.approved_by
           OR evidence.resolved_at IS DISTINCT FROM occurrence.approval_resolved_at
    ) THEN
        RAISE EXCEPTION
            'cannot remove PR publication evidence that legacy publication columns cannot represent';
    END IF;
END
$$;

DROP TABLE agent_publication_approval_evidence;
DROP TABLE agent_publication_inputs;

DROP INDEX agent_workflow_waits_id_team_publication_evidence;
DROP INDEX agent_publication_occurrences_id_team_evidence;
DROP INDEX agent_snapshots_id_team_publication_evidence;
