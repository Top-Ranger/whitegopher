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

Not yet: admin web UI, MySQL/Postgres, free-busy, scheduling (iTIP), sharing,
partial `calendar-data` retrieval/`expand`, config for limits.

## Run

    go run ./cmd/galdav useradd alice
    go run ./cmd/galdav serve

Config is read from `config.yaml` (see `internal/config`), defaults otherwise.
Digest auth needs the realm to stay stable: it is part of the stored hash.

## Import from Baikal

    galdav import-baikal -dry-run /path/to/baikal/Specific/db/db.sqlite
    galdav import-baikal /path/to/baikal/Specific/db/db.sqlite

Set `realm` in `config.yaml` to Baikal's realm (default `BaikalDAV`) first so
imported password hashes keep working. Sync tokens are not carried over;
clients do one full resync. Only SQLite sources are supported so far.

## Layout

    cmd/galdav/        CLI: serve, useradd
    internal/config/   YAML config
    internal/store/    SQLite store + embedded migrations
    internal/auth/     password hashing, HTTP auth middleware
    internal/dav/      WebDAV/CalDAV/CardDAV HTTP handlers and REPORTs
    internal/index/    object validation and occurrence indexing
    internal/importer/ one-time Baikal import

## Roadmap

1. Admin UI (users, calendars, address books)
2. MySQL/Postgres sources for the importer, Postgres as a store
3. Free-busy, scheduling, sharing
4. Hardening, Docker, docs
