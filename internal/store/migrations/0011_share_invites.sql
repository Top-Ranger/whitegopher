-- A share can wait for the sharee to accept it (state 'pending'), or have been
-- declined. Only accepted shares appear in the sharee's home. invite_uid
-- identifies the invitation in CalendarServer-style notifications.
ALTER TABLE collection_shares
    ADD COLUMN state      VARCHAR(16) NOT NULL DEFAULT 'accepted',
    ADD COLUMN invite_uid VARCHAR(64) NOT NULL DEFAULT '';

UPDATE collection_shares SET invite_uid = REPLACE(UUID(), '-', '') WHERE invite_uid = '';

ALTER TABLE collection_shares ADD UNIQUE KEY shares_invite_uid (invite_uid);
