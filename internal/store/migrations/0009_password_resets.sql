CREATE TABLE password_resets (
    token_hash CHAR(64) NOT NULL PRIMARY KEY,
    user_id    BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    KEY password_resets_user (user_id),
    CONSTRAINT password_resets_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
