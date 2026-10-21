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
`addressbook-query`, `sync-collection`) and a one-time Baikal importer.

Admin web UI at `/admin/` (see below).

Not yet: Postgres, free-busy, scheduling (iTIP), sharing,
partial `calendar-data` retrieval/`expand`, config for limits.

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
databases). Without the variable below they are **skipped, not failed**:

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
    internal/store/    SQLite store + embedded migrations
    internal/auth/     password hashing, HTTP auth middleware
    internal/admin/    web UI (templates and static assets embedded)
    internal/dav/      WebDAV/CalDAV/CardDAV HTTP handlers and REPORTs
    internal/index/    object validation and occurrence indexing
    internal/importer/ one-time Baikal import

## Roadmap

1. Free-busy, scheduling, sharing
2. Hardening and docs
3. Postgres, if wanted

## Dependencies and licences

galdav is MIT. Direct dependencies are MIT/BSD/Apache-2.0 licensed except
`github.com/go-sql-driver/mysql`, which is MPL-2.0 (file-level copyleft: it
only affects modifications to that library's own files, not galdav).
