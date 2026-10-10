DO $$ BEGIN RAISE EXCEPTION 'migration 1789792877 is forward-only: restore the pre-upgrade database backup instead'; END $$;
