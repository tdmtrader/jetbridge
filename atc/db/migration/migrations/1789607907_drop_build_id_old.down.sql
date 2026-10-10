DO $$ BEGIN RAISE EXCEPTION 'migration 1789607907 is forward-only: restore the pre-upgrade database backup instead'; END $$;
