-- Schedule-Tag (RFC 6638 3.2.10): changes only when a scheduling-significant
-- change is made to a scheduling object resource.
ALTER TABLE objects ADD COLUMN schedule_tag VARCHAR(64) NOT NULL DEFAULT '';
