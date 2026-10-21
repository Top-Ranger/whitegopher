-- A share mounts a collection owned by someone else into a user's namespace.
-- display_name, color, sort_order and transparent are the sharee's own
-- settings, like per-user calendar instances.
CREATE TABLE collection_shares (
    id            BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    collection_id BIGINT NOT NULL,
    user_id       BIGINT NOT NULL,
    kind          VARCHAR(16) NOT NULL,
    slug          VARCHAR(128) NOT NULL,
    access        VARCHAR(16) NOT NULL,
    display_name  VARCHAR(255) NOT NULL DEFAULT '',
    color         VARCHAR(32) NOT NULL DEFAULT '',
    sort_order    INT NOT NULL DEFAULT 0,
    transparent   TINYINT(1) NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY shares_collection_user (collection_id, user_id),
    UNIQUE KEY shares_user_kind_slug (user_id, kind, slug),
    CONSTRAINT shares_collection FOREIGN KEY (collection_id) REFERENCES collections (id) ON DELETE CASCADE,
    CONSTRAINT shares_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT shares_access CHECK (access IN ('read','readwrite'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
