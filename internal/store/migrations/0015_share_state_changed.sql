-- When a share last changed state: the invitation was sent, answered, or sent
-- again after a decline. Unanswered invitations expire by this, not by
-- created_at, which a re-invitation does not reset.
ALTER TABLE collection_shares
    ADD COLUMN state_changed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP;

UPDATE collection_shares SET state_changed_at = created_at;
