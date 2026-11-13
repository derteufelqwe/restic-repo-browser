-- Times are stored as unix milliseconds; 0 means "not set".

CREATE TABLE repos (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT    NOT NULL UNIQUE,
    path           TEXT    NOT NULL,
    password_enc   BLOB    NOT NULL,
    created_at     INTEGER NOT NULL,
    refreshed_at   INTEGER NOT NULL DEFAULT 0,
    refresh_ok_at  INTEGER NOT NULL DEFAULT 0,
    refresh_error  TEXT    NOT NULL DEFAULT '',
    size_bytes     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE snapshots (
    repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    id           TEXT    NOT NULL,
    short_id     TEXT    NOT NULL,
    time         INTEGER NOT NULL,
    host         TEXT    NOT NULL,
    username     TEXT    NOT NULL DEFAULT '',
    paths        TEXT    NOT NULL, -- JSON array
    tags         TEXT    NOT NULL, -- JSON array
    size_bytes   INTEGER,          -- NULL until known
    added_bytes  INTEGER,
    files        INTEGER,
    PRIMARY KEY (repo_id, id)
) WITHOUT ROWID;

CREATE INDEX snapshots_by_time ON snapshots (repo_id, time);

CREATE TABLE locks (
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    id         TEXT    NOT NULL,
    time       INTEGER NOT NULL,
    host       TEXT    NOT NULL,
    username   TEXT    NOT NULL,
    pid        INTEGER NOT NULL,
    exclusive  INTEGER NOT NULL,
    PRIMARY KEY (repo_id, id)
) WITHOUT ROWID;

CREATE TABLE jobs (
    repo_id       INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
    cron          TEXT    NOT NULL,
    keep_last     INTEGER NOT NULL DEFAULT 0,
    keep_hourly   INTEGER NOT NULL DEFAULT 0,
    keep_daily    INTEGER NOT NULL DEFAULT 0,
    keep_weekly   INTEGER NOT NULL DEFAULT 0,
    keep_monthly  INTEGER NOT NULL DEFAULT 0,
    keep_yearly   INTEGER NOT NULL DEFAULT 0,
    keep_within   TEXT    NOT NULL DEFAULT '',
    keep_tag      TEXT    NOT NULL DEFAULT '',
    prune         INTEGER NOT NULL DEFAULT 1,
    check_mode    TEXT    NOT NULL DEFAULT 'none',
    enabled       INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER NOT NULL DEFAULT 0,
    status       TEXT    NOT NULL,
    summary      TEXT    NOT NULL DEFAULT '',
    log          TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX runs_by_repo ON runs (repo_id, started_at DESC);

CREATE TABLE settings (
    key    TEXT PRIMARY KEY,
    value  TEXT NOT NULL
) WITHOUT ROWID;

-- A single row: the one login password.
CREATE TABLE auth (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash  TEXT NOT NULL
);

CREATE TABLE sessions (
    token_hash  TEXT    PRIMARY KEY,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
) WITHOUT ROWID;
