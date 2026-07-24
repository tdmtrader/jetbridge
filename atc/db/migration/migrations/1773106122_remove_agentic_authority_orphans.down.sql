-- This migration irreversibly retires authority. Do not recreate
-- questions:answer grants or the legacy-publish bypass on downgrade.
SELECT 1;
