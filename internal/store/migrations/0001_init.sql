CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL DEFAULT '',
    email         TEXT NOT NULL DEFAULT '',
    -- md5(username:realm:password), for Digest and imported accounts.
    digest_ha1    TEXT NOT NULL DEFAULT '',
    -- argon2id PHC string; empty until the user sets/upgrades a password.
    password_hash TEXT NOT NULL DEFAULT '',
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE collections (
    id           INTEGER PRIMARY KEY,
    owner_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('calendar','addressbook')),
    slug         TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    color        TEXT NOT NULL DEFAULT '',
    sort_order   INTEGER NOT NULL DEFAULT 0,
    components   TEXT NOT NULL DEFAULT 'VEVENT,VTODO', -- calendars only
    timezone     TEXT NOT NULL DEFAULT '',             -- VTIMEZONE, calendars only
    created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (owner_id, kind, slug)
);

CREATE TABLE objects (
    id            INTEGER PRIMARY KEY,
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    uri           TEXT NOT NULL,
    uid           TEXT NOT NULL DEFAULT '',
    etag          TEXT NOT NULL,
    data          BLOB NOT NULL,
    component     TEXT NOT NULL DEFAULT '', -- VEVENT/VTODO/VJOURNAL/VCARD
    first_occ     INTEGER,                  -- unix seconds, for time-range queries
    last_occ      INTEGER,                  -- NULL = unbounded recurrence
    updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (collection_id, uri)
);
CREATE INDEX objects_uid ON objects (collection_id, uid);

-- Change log. The sync-token of a collection is the highest change id
-- seen; tokens are "<base>/sync/<id>".
CREATE TABLE changes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    uri           TEXT NOT NULL,
    deleted       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX changes_collection ON changes (collection_id, id);
