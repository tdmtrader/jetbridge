ALTER TABLE components ADD COLUMN interval text NOT NULL DEFAULT '0s';
ALTER TABLE components ADD COLUMN last_ran timestamp with time zone;
