CREATE TABLE sessions (
    token_hash CHAR(64) NOT NULL PRIMARY KEY,
    user_id    BIGINT NOT NULL,
    csrf       VARCHAR(64) NOT NULL,
    expires_at BIGINT NOT NULL,
    KEY sessions_user (user_id),
    CONSTRAINT sessions_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
