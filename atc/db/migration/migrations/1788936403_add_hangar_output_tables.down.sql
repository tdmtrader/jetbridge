DO $$ BEGIN RAISE EXCEPTION 'migration 1788936403 is forward-only: restore the pre-upgrade database backup instead'; END $$;
