-- Per-device credentials for DAV clients. Tokens are random, so a plain
-- SHA-256 of the token is enough to store.
CREATE TABLE app_passwords (
    id           BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id      BIGINT NOT NULL,
    name         VARCHAR(100) NOT NULL,
    token_hash   CHAR(64) NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME NULL,
    UNIQUE KEY app_passwords_hash (token_hash),
    KEY app_passwords_user (user_id),
    CONSTRAINT app_passwords_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
