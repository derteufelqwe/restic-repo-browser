-- The cleanup job no longer has an "hourly" keep rule.
ALTER TABLE jobs DROP COLUMN keep_hourly;

-- Optional cron expression of when backups are expected. Empty: judged from the gaps between snapshots.
ALTER TABLE repos ADD COLUMN backup_cron TEXT NOT NULL DEFAULT '';
