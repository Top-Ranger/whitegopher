-- NULL means "use the server default"; 0 means unlimited.
ALTER TABLE users ADD COLUMN quota_bytes BIGINT NULL;
