-- A read-only repository never gets a cleanup run. Its saved cleanup job is kept.
ALTER TABLE repos ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0;
