-- A read-only public link to a collection, for subscribing without a login.
CREATE TABLE public_links (
    collection_id BIGINT NOT NULL PRIMARY KEY,
    token         CHAR(32) NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY public_links_token (token),
    CONSTRAINT public_links_collection FOREIGN KEY (collection_id) REFERENCES collections (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
