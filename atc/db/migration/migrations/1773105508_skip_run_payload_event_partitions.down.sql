DO $$ BEGIN RAISE EXCEPTION 'migration 1773105508 is forward-only: restore the pre-upgrade database backup instead'; END $$;
