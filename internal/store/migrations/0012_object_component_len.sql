-- Notification resources record their kind and shared type here.
ALTER TABLE objects MODIFY component VARCHAR(48) NOT NULL DEFAULT '';
