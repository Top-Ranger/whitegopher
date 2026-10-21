-- Binary collation: usernames, slugs and URIs are case-sensitive, as in the
-- SQLite schema.
CREATE TABLE users (
    id            BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    username      VARCHAR(190) NOT NULL,
    display_name  VARCHAR(255) NOT NULL DEFAULT '',
    email         VARCHAR(255) NOT NULL DEFAULT '',
    digest_ha1    VARCHAR(64) NOT NULL DEFAULT '',
    password_hash VARCHAR(255) NOT NULL DEFAULT '',
    is_admin      TINYINT(1) NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE collections (
    id           BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    owner_id     BIGINT NOT NULL,
    kind         VARCHAR(16) NOT NULL,
    slug         VARCHAR(128) NOT NULL,
    display_name VARCHAR(255) NOT NULL DEFAULT '',
    description  TEXT NOT NULL,
    color        VARCHAR(32) NOT NULL DEFAULT '',
    sort_order   INT NOT NULL DEFAULT 0,
    components   VARCHAR(64) NOT NULL DEFAULT 'VEVENT,VTODO',
    timezone     MEDIUMTEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY collections_owner_kind_slug (owner_id, kind, slug),
    CONSTRAINT collections_owner FOREIGN KEY (owner_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT collections_kind CHECK (kind IN ('calendar','addressbook'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE objects (
    id            BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    collection_id BIGINT NOT NULL,
    uri           VARCHAR(255) NOT NULL,
    uid           TEXT NOT NULL,
    etag          VARCHAR(64) NOT NULL,
    data          LONGBLOB NOT NULL,
    component     VARCHAR(16) NOT NULL DEFAULT '',
    first_occ     BIGINT NULL,
    last_occ      BIGINT NULL,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY objects_collection_uri (collection_id, uri),
    KEY objects_uid (collection_id, uid(191)),
    CONSTRAINT objects_collection FOREIGN KEY (collection_id) REFERENCES collections (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE changes (
    id            BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    collection_id BIGINT NOT NULL,
    uri           VARCHAR(255) NOT NULL,
    deleted       TINYINT(1) NOT NULL DEFAULT 0,
    KEY changes_collection (collection_id, id),
    CONSTRAINT changes_collection FOREIGN KEY (collection_id) REFERENCES collections (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
