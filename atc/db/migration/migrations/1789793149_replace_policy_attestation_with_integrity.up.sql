-- Policy configuration is an operator responsibility. Preserve old evidence for audit,
-- but gate admission only on unresolved failures observed during storage operations.
CREATE OR REPLACE FUNCTION hangar_check_policy_admission() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM hangar_policy_violations
               WHERE activation_epoch = NEW.activation_epoch AND resolved_at IS NULL
                 AND violation IN ('out_of_band_absence', 'runtime_principal_denied')) THEN
        RAISE EXCEPTION 'hangar: epoch % has unresolved storage integrity findings; repair the cause and explicitly reconcile each finding before admitting new work', NEW.activation_epoch
            USING ERRCODE = 'JB002';
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION hangar_check_reclaim_admission() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM hangar_policy_violations
               WHERE activation_epoch = NEW.activation_epoch AND resolved_at IS NULL
                 AND violation = 'out_of_band_absence') THEN
        RAISE EXCEPTION 'hangar: epoch % has unexplained object loss; reclamation requires explicit reconciliation', NEW.activation_epoch
            USING ERRCODE = 'JB002';
    END IF;
    RETURN NULL;
END $$;
