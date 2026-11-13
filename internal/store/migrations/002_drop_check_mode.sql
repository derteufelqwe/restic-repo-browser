-- Cleanup jobs no longer run an integrity check.
ALTER TABLE jobs DROP COLUMN check_mode;
