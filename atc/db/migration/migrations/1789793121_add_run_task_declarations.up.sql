-- An identity can survive template edits, but never migrate to another base.
CREATE TABLE pipeline_template_task_identities (
    task_id uuid PRIMARY KEY,
    -- A tombstone intentionally survives deletion of an unreferenced template.
    -- Pipeline IDs are never reused; deleting the template cannot free its UUIDs.
    template_pipeline_id integer NOT NULL,
    CHECK (task_id <> '00000000-0000-0000-0000-000000000000')
);

CREATE FUNCTION immutable_template_task_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'stable task identity ownership is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER template_task_identity_immutable BEFORE UPDATE ON pipeline_template_task_identities
    FOR EACH ROW EXECUTE FUNCTION immutable_template_task_identity();
