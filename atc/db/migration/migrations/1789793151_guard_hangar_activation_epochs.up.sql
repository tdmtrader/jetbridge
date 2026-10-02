-- Only the activation database role writes the activation epochs
-- (hangar_activation_db_role R2). Web's role has its privileges revoked, but a
-- superuser keeps them, so this trigger is the guard that holds everywhere: no
-- web code path, by bug or injection, can move an epoch.
--
-- Same-event triggers fire in name order; this one sorts before
-- hangar_output_epoch_transition_guard so its refusal is the one a stray writer
-- sees.
CREATE OR REPLACE FUNCTION hangar_activation_role_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_user <> hangar_activation_role_name() THEN
        RAISE EXCEPTION 'hangar_output_activation_epochs is written only by the activation database role (%), not %',
            hangar_activation_role_name(), current_user
            USING ERRCODE = 'JB003';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER hangar_output_activation_role_guard
    BEFORE INSERT OR UPDATE OR DELETE ON hangar_output_activation_epochs
    FOR EACH ROW EXECUTE FUNCTION hangar_activation_role_guard();

CREATE TRIGGER hangar_output_activation_role_guard_truncate
    BEFORE TRUNCATE ON hangar_output_activation_epochs
    FOR EACH STATEMENT EXECUTE FUNCTION hangar_activation_role_guard();
