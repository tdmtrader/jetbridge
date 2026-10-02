GRANT INSERT, UPDATE, DELETE, TRUNCATE ON hangar_output_activation_epochs TO CURRENT_USER;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = hangar_activation_role_name()) THEN
        EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', hangar_activation_role_name());
    END IF;
END $$;

DROP FUNCTION hangar_activation_grants();
DROP FUNCTION hangar_activation_role_name();
