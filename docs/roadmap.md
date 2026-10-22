# Roadmap

Ordered by what I would do next. See [features.md](features.md) for what
exists and for the limits that motivate these items.

## Next: prove it with real clients

galdav has only been tested against its own test suite. Before adding more
features, run through DAVx5 (Android), iOS/macOS Calendar and Contacts,
Thunderbird and Evolution: account setup via `.well-known`, create/edit/delete,
sync after offline edits, invitations (accept/decline), sharing, free-busy.
The checklist, test data and seed script are in
[client-testing.md](client-testing.md). Fix whatever surfaces. This is the biggest open risk (scheduling and Apple
sharing especially), and the results decide the order of everything below.

## Protocol completeness

- `Schedule-Tag` and `If-Schedule-Tag-Match`, so clients can tell
  scheduling-significant changes from cosmetic ones.
- Send REQUESTs only for *significant* organizer changes (RFC 6638 §3.2.8).
- Partial `calendar-data` retrieval and `expand` in `calendar-query` /
  `calendar-multiget` (some clients rely on `expand`).
- Create recurrence overrides in the organizer's copy from an attendee's reply
  to a single instance of a series.
- Inbound iMIP: process replies and invitations arriving by email.
- Prune old scheduling-inbox items.
- `PROPFIND /` answering with the DAV root for clients that start there.

## Sharing

- Invitation notifications for the CalendarServer sharing protocol
  (accept/decline), and `DAV:share-resource`.
- Optional read-only public links.
- Scheduling on behalf of the owner for read-write sharees (delegation).

## Security and operations

- Per-user quotas and a configurable request-size limit.
- App passwords (per-device credentials) and optional two-factor sign-in for
  the admin UI; password reset by email.
- Setting to hide a user's free-busy from others.
- Metrics endpoint and structured access log.
- A `version` command and release builds.
- Verify on MySQL 8 (development used MariaDB) and with multiple instances
  sharing one database.

## Not planned

- SQLite or Postgres as the server's database (SQLite remains only as a
  Baikal import source).
- Built-in TLS or ACME: use a reverse proxy.
- A Docker image (deployment is a static binary plus MariaDB).
