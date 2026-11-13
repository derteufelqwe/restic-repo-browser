-- The cleanup job no longer supports "keep within" and "keep tagged" rules.
ALTER TABLE jobs DROP COLUMN keep_within;
ALTER TABLE jobs DROP COLUMN keep_tag;
