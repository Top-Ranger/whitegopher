# Features and limits

What galdav does today, area by area, each with its known limits. Planned work
is in [roadmap.md](roadmap.md); options are in [configuration.md](configuration.md).

> **Status:** everything below is covered by automated tests against MariaDB,
> but galdav has **not yet been tried with real clients** (DAVx5, iOS/macOS
> Calendar and Contacts, Thunderbird, Evolution). Expect client-specific
> quirks until that has been done; see [client-testing.md](client-testing.md).

## Protocols and discovery

- WebDAV, CalDAV (RFC 4791) and CardDAV (RFC 6352): `PROPFIND` (Depth 0/1),
  `PROPPATCH`, `REPORT`, `MKCALENDAR`, extended `MKCOL`, `GET`/`HEAD`, `PUT`,
  `DELETE`, `POST`, `OPTIONS`.
- Discovery: `/.well-known/caldav` and `/.well-known/carddav` redirect to
  `/dav/`; `current-user-principal`, `calendar-home-set`,
  `addressbook-home-set`.
- Tree layout: `/dav/principals/<user>/`, `/dav/calendars/<user>/<name>/`,
  `/dav/addressbooks/<user>/<name>/`. `/` redirects to the admin UI.

Limits: no `Depth: infinity`; no WebDAV locking, `COPY`/`MOVE` or ACL
methods; `PROPFIND` on `/` is not answered (clients should use `.well-known`
or the `/dav/` URL); objects are limited by `limits.max_request_mib` (10 MiB by default).

## Authentication

- HTTP Basic only (use TLS). Passwords are stored as argon2id. Digest is not
  supported. A password hash imported from Baikal is kept only until that
  user's first login, which replaces it with argon2id.
- Users only reach their own namespace (and collections shared with them).

- Failed logins, on DAV and in the admin UI, are logged (`login failed` with
  `user`, `ip` and `via`) and rate limited per client address (`login_limit`,
  default 10 per 15 minutes; `429` with `Retry-After`). The address is the
  rightmost `X-Forwarded-For` entry that is not a trusted proxy.

Limits: the rate limit is in memory and per process. No LDAP/OIDC, no app passwords, no two-factor authentication.

## Calendars and contacts

- Storage quotas: `limits.default_quota_mib`, overridable per user in the admin
  UI. A `PUT` that would exceed the owner's quota gets `507` with
  `DAV:quota-not-exceeded`; calendar and address book home sets report
  `quota-used-bytes` / `quota-available-bytes` (RFC 4331) when a quota applies.
  Shared collections count against their owner.

- Calendars (events, tasks, journals) and address books, with name,
  description, colour, order and allowed component types.
- Objects: `PUT` with `If-Match` / `If-None-Match`, ETags, validation
  (iCalendar 2.0, vCard 2.1/3.0/4.0, one UID per object, allowed component
  types) and UID-conflict detection (`no-uid-conflict`).
- Reports: `calendar-multiget`, `calendar-query` (component, property,
  parameter and text-match filters; time-range with recurrence expansion),
  `addressbook-multiget`, `addressbook-query` (`anyof`/`allof`, `contains`,
  `equals`, `starts-with`, `ends-with`), `sync-collection` (RFC 6578),
  `free-busy-query`.

Limits: quotas count object data only, are checked just before a write (so
concurrent uploads can overshoot slightly) and do not stop scheduling from
delivering invitations into a full mailbox; no partial `calendar-data` retrieval or `expand`; a time-range query
matches a moved recurrence instance at its original time too (rare false
positive); times with non-IANA `TZID`s (e.g. Windows zone names) are treated as
unindexed, which keeps queries correct but slower; no `Schedule-Tag` /
`If-Schedule-Tag-Match`; only `text/calendar` and `text/vcard` are served.

## Scheduling (RFC 6638 / iTIP)

Advertised as `calendar-auto-schedule`; processing is server-side ("implicit").

- Every user has a scheduling **inbox** (`/dav/calendars/<user>/inbox/`, read
  only except for deleting items) and an **outbox** (`.../outbox/`, POST only).
  The names `inbox` and `outbox` are reserved.
- Users are matched by account **email** (case-insensitive, unique) or
  principal URL; both appear in `calendar-user-address-set`.
- An organizer saving or deleting an event sends REQUEST or CANCEL to the
  attendees; an attendee changing their answer (or deleting the event) sends a
  REPLY. Local recipients get the iTIP message in their inbox **and** the event
  in their default calendar (chosen in the admin UI or with
  `schedule-default-calendar-URL`). Replies update the organizer's copy, and
  delivery results are recorded as `SCHEDULE-STATUS`. Attendees with
  `SCHEDULE-AGENT=CLIENT` are left to the client.
- Attendees without an account get an iMIP email when `smtp` is configured;
  otherwise they are marked `5.3` (no scheduling support).
- Explicit scheduling: iTIP messages and free-busy requests POSTed to the outbox.

Limits: a REQUEST is sent on every organizer change, not only "significant"
ones; an attendee's reply to one instance of a series does not create the
override in the organizer's copy; the inbox is never pruned; replies by email
(inbound iMIP) are not processed; edits made through a read-write share send no
scheduling messages. The `SCHEDULE-STATUS` codes follow my reading of the RFCs
and should be checked against real clients.

## Free-busy

- `free-busy-query` on a calendar, and VFREEBUSY requests POSTed to the outbox
  (per-recipient `schedule-response`).
- Busy time comes from all of a user's calendars that are not marked
  transparent (`schedule-calendar-transp`), with recurrences expanded, overrides
  and cancelled instances honoured, `TRANSP` and `STATUS` respected, tentative
  time reported separately and declined invitations ignored.

By default **any authenticated user can query anyone's free-busy** (times
only, no details). A user can hide theirs under *Account → Privacy*; others then
get `3.8` (no authority) for that user, while the user can still query their
own. Limits: the setting is all-or-nothing (no per-person exceptions). Shared calendars do not count
towards the sharee's availability.

## Sharing

- A calendar or address book can be shared with other local users, read-only or
  read-write. Shares are accepted automatically: the collection appears in the
  sharee's home set (named `<owner>-<name>` if the name is taken) and syncs like
  any other. Each sharee keeps their own name, colour and order.
- `current-user-privilege-set` reflects the access level. Deleting a mounted
  collection leaves the share; the owner deleting it removes it for everyone.
- Managed in the admin UI, or by Apple clients through the CalendarServer
  sharing protocol (`CS:share` POST, `CS:invite`, `CS:allowed-sharing-modes`).

Limits: no invitation notifications (shares are accepted at once) and the Apple
protocol is untested with real clients; no `DAV:share-resource`, public/
read-only links or per-object permissions; edits through a read-write share
are plain writes.

## Admin web UI (`/admin/`)

- First-run setup page, login with server-side sessions (24 h).
- Users manage their calendars, address books, shares, default calendar for
  invitations and their password; administrators also create, edit, reset and
  delete users (the last administrator is protected).
- Protections: HttpOnly SameSite=Lax cookies (Secure behind HTTPS), CSRF
  tokens, cross-origin POST rejection, strict content-security policy, login
  rate limiting per client address (proxy-aware).

Limits: no password reset by email, no audit log, no
administrator view of other users' collections, English only.

## Importing from Baikal

`galdav import-baikal` copies users (including password hashes), calendars,
address books and objects from a Baikal SQLite file or a Baikal MySQL/MariaDB
database. It is idempotent and has a `-dry-run`. Sync tokens are not carried
over, so clients do one full resync. Shared calendars, the sabre scheduling
inbox and Baikal's own settings are not imported.

## Operations

- MySQL/MariaDB only; the schema is created and migrated automatically
  (forward-only). Pure Go: a static binary built with `CGO_ENABLED=0`.
- `GET /healthz` for monitoring, graceful shutdown on SIGTERM/SIGINT, logs on
  stderr (journald-friendly), as text or JSON.
- Optional structured access log (`log.access`) and Prometheus metrics on a
  separate listener (`metrics.listen`).
- `galdav version`; `make release` builds static archives for Linux
  (amd64/arm64/arm), macOS and FreeBSD with checksums.
- Trusted-proxy handling for `X-Forwarded-For` / `X-Forwarded-Proto`.

Limits: no built-in TLS (use a reverse proxy), no clustering guarantees beyond what the database gives
(several instances behind one database should work, but are untested).
