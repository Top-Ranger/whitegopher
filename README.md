# galdav

A pure-Go CalDAV and CardDAV server, MIT licensed.

Clean-room: implemented from the RFCs and observed client behaviour, not from
the source of Baikal or sabre/dav. Baikal databases can be imported once with
`galdav import-baikal` (not yet implemented).

## Status

Working: Basic and Digest auth, discovery (`.well-known`, principals, home
sets), calendars and address books (MKCALENDAR, extended MKCOL, PROPPATCH,
DELETE), objects (PUT/GET/DELETE with ETags and preconditions, validation,
UID-conflict detection), REPORTs (`calendar-multiget`, `calendar-query` with
time-range/prop/text filters and recurrence expansion, `addressbook-multiget`,
`addressbook-query`, `sync-collection`, `free-busy-query`), scheduling
(below), an admin web UI at `/admin/` and a one-time Baikal importer.

Not yet: sharing, `Schedule-Tag`/`If-Schedule-Tag-Match`, partial
`calendar-data` retrieval/`expand`, inbound iMIP (replies by email), quotas,
configurable request limits.

## Scheduling and free-busy

galdav implements CalDAV scheduling (RFC 6638) with server-side ("implicit")
processing, advertised as `calendar-auto-schedule`:

- Every user has a scheduling **inbox** (`/dav/calendars/<user>/inbox/`, a
  read-only collection clients may delete from) and an **outbox**
  (`.../outbox/`, POST only). The names `inbox` and `outbox` are reserved.
- Users are matched by their account **email** (case-insensitive, unique) or
  principal URL; they are listed in `calendar-user-address-set`.
- When an organizer saves or deletes an event, attendees get a REQUEST or
  CANCEL; when an attendee changes their PARTSTAT (or deletes the event) the
  organizer gets a REPLY. Local recipients get the iTIP message in their inbox
  **and** the event in their default calendar (settable in the admin UI or via
  `schedule-default-calendar-URL`); organizers' copies are updated with the
  replies, and delivery results are recorded as `SCHEDULE-STATUS`.
  `SCHEDULE-AGENT=CLIENT` attendees are left to the client.
- Attendees without an account receive an iMIP email if `smtp` is configured
  (below); otherwise they are marked `5.3` (no scheduling support).
- `free-busy-query` REPORTs and VFREEBUSY requests POSTed to the outbox
  return busy time from all of a user's non-transparent calendars
  (`schedule-calendar-transp`), expanding recurrences and honouring overrides,
  `STATUS`, `TRANSP` and declined invitations. **Any authenticated user can
  query anyone's free-busy** (times only, no details).

```yaml
smtp:
  host: smtp.example.com
  port: 587                      # STARTTLS is used when offered
  username: galdav
  from: "galdav <noreply@example.com>"
# password: set GALDAV_SMTP_PASSWORD
```

Known limits: invitations are sent on every organizer change (not only
"significant" ones); recurrence overrides added by an attendee's reply to a
series are not created; the inbox is not pruned automatically.

## Run

    go run ./cmd/galdav serve

With `db.dsn` configured, open `http://localhost:8080/admin/` and create the first administrator on the
setup page. **The setup page is open to whoever reaches the server first**, so
on a public host create the admin from the command line before exposing it:

    go run ./cmd/galdav useradd -admin alice     # password read from stdin

Config is read from `config.yaml` (see `internal/config`).
Digest auth needs the realm to stay stable: it is part of the stored hash.

## Database

galdav needs MySQL or MariaDB (the driver is pure Go, so
`CGO_ENABLED=0 go build ./cmd/galdav` yields a static binary). Create an empty
utf8mb4 database and a user, then configure:

    db:
      dsn: "galdav:secret@tcp(127.0.0.1:3306)/galdav"

`GALDAV_DB_DSN` overrides `db.dsn`, to keep the password out of the file.
Tables are created and migrated on start.

Tests need a server too (rights to create and drop `galdav_test_*`
databases). Without the variable below, database tests **fail** with an
explanatory message, so a green run always means they ran
(`GALDAV_TEST_SKIP_DB=1` skips them on purpose):

    GALDAV_TEST_MYSQL_DSN='root@tcp(127.0.0.1:3306)/' go test -p 1 ./...

## Admin UI

Served under `/admin/` (disable with `admin: {enabled: false}`). Users manage
their own calendars, address books and password; administrators also manage
users. Sessions are stored server-side (24 h), cookies are HttpOnly and
SameSite=Lax (Secure behind HTTPS or `X-Forwarded-Proto: https`), forms carry
CSRF tokens, cross-origin POSTs are rejected, and login attempts are limited
per client IP (the direct peer address; put a trusted proxy's limits in front
if needed). Changing a password ends that user's sessions.

## Import from Baikal

    galdav import-baikal -dry-run /path/to/baikal/Specific/db/db.sqlite   # Baikal's default SQLite database
    galdav import-baikal /path/to/baikal/Specific/db/db.sqlite
    galdav import-baikal -mysql 'user:pass@tcp(127.0.0.1:3306)/baikal'   # Baikal on MySQL/MariaDB

SQLite is supported only as a read-only import source; the server itself
stores everything in MySQL/MariaDB.

Set `realm` in `config.yaml` to Baikal's realm (default `BaikalDAV`) first so
imported password hashes keep working. Sync tokens are not carried over;
clients do one full resync.

## Layout

    cmd/galdav/        CLI: serve, useradd
    internal/config/   YAML config
    internal/store/    MySQL/MariaDB store + embedded migrations
    internal/sched/    free-busy, iTIP scheduling, iMIP email
    internal/auth/     password hashing, HTTP auth middleware
    internal/admin/    web UI (templates and static assets embedded)
    internal/dav/      WebDAV/CalDAV/CardDAV HTTP handlers and REPORTs
    internal/index/    object validation and occurrence indexing
    internal/importer/ one-time Baikal import

## Roadmap

1. Sharing
2. `Schedule-Tag`, partial retrieval/`expand`, inbound iMIP
3. Hardening and docs

## Dependencies and licences

galdav is MIT. Direct dependencies are MIT/BSD/Apache-2.0 licensed except
`github.com/go-sql-driver/mysql`, which is MPL-2.0 (file-level copyleft: it
only affects modifications to that library's own files, not galdav).
