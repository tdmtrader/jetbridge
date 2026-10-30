-- The syslog drainer was the only reader and writer of builds.drained. The
-- drainer is gone, so the column goes with it.
ALTER TABLE builds DROP COLUMN drained;
