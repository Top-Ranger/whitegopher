ALTER TABLE users
    ADD COLUMN totp_secret    VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN totp_enabled   TINYINT(1) NOT NULL DEFAULT 0,
    ADD COLUMN totp_last_step BIGINT NOT NULL DEFAULT 0;

-- One-time codes for signing in without the authenticator.
CREATE TABLE recovery_codes (
    user_id   BIGINT NOT NULL,
    code_hash CHAR(64) NOT NULL,
    PRIMARY KEY (user_id, code_hash),
    CONSTRAINT recovery_codes_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
