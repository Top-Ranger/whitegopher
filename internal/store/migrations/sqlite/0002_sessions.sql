CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,          -- sha256 hex of the cookie value
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf       TEXT NOT NULL,
    expires_at INTEGER NOT NULL           -- unix seconds
);
CREATE INDEX sessions_user ON sessions (user_id);
