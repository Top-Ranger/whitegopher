-- special marks scheduling inboxes ('inbox'); transparent calendars do not
-- count towards free-busy.
ALTER TABLE collections ADD COLUMN special VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE collections ADD COLUMN transparent TINYINT(1) NOT NULL DEFAULT 0;
-- Slug of the calendar that receives scheduling invitations; '' = first calendar.
ALTER TABLE users ADD COLUMN default_calendar VARCHAR(128) NOT NULL DEFAULT '';
-- Emails are stored lower-case so attendee lookups can use the index.
UPDATE users SET email = LOWER(email);
CREATE INDEX users_email ON users (email);
