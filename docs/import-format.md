# Import format

Version 1 · read by `whitegopher import DIR`

The import format is a plain directory tree. It is independent of any source
server: to migrate from another CalDAV/CardDAV server (or from files on disk),
write a small converter that produces this tree, then run the importer on it.
White Gopher itself ships no converters; ready-made ones live in
[whitegopher-contrib](https://github.com/Top-Ranger/whitegopher-contrib).

## Quick start

```sh
whitegopher import -dry-run ./tree   # validate, print counts and warnings, write nothing
whitegopher import ./tree            # import
```

A run prints one line of counts and any warnings on stderr. Exit status is
non-zero on a fatal error (malformed manifest or JSON, database failure).

## Layout

```
whitegopher-import.json                          manifest (required)
users/
  <user>/
    user.json                                    optional
    calendars/
      <slug>/
        collection.json                          optional
        objects/
          <uri>                                  one iCalendar file per object
    addressbooks/
      <slug>/
        collection.json                          optional
        objects/
          <uri>                                  one vCard file per object
```

Every `<user>`, `<slug>` and `<uri>` is a single path segment holding the
**percent-encoded** name (RFC 3986). The importer decodes it, so `a b.ics` is
stored as `a%20b.ics`; a literal `%` must be written `%25`. Names containing
`/` or NUL cannot be represented. Missing directories and files are treated
as empty. Anything else in the tree is ignored.

## Manifest: `whitegopher-import.json`

```json
{"format": "whitegopher-import", "version": 1, "generator": "my-converter 0.3"}
```

| Field | Required | Meaning |
|---|---|---|
| `format` | yes | Must be `"whitegopher-import"`. |
| `version` | yes | Must be `1`. A different number is refused. |
| `generator` | no | Free text naming the converter; not interpreted. |

## `user.json`

| Field | Type | Meaning |
|---|---|---|
| `display_name` | string | Shown in the admin UI and used for scheduling. |
| `email` | string | Calendar-user address; stored lower-case. Needed for scheduling. |
| `digest_ha1` | string | Optional legacy password hash, `md5(user:realm:password)` as lower-case hex. It verifies the user's old password once, only if the server's `realm` equals the realm the hash was made with; the first successful login replaces it with argon2id. Omit it and set passwords in the admin UI instead. |

The username is the directory name. Users are never made administrators by
an import. An existing user with that name is reused and **not** modified
(its `user.json` is ignored), so re-running never overwrites passwords.

## `collection.json`

All fields are optional.

| Field | Type | Applies to | Meaning |
|---|---|---|---|
| `display_name` | string | both | Name shown to clients. |
| `description` | string | both | Description. |
| `color` | string | calendars | e.g. `"#00ff00"`. |
| `sort_order` | integer | both | Clients sort ascending. Default 0. |
| `components` | string | calendars | Comma-separated component types the calendar accepts, e.g. `"VEVENT,VTODO"`. Default `"VEVENT,VTODO"`. |
| `timezone` | string | calendars | A `VTIMEZONE` component as text, the calendar's default time zone. |
| `transparent` | boolean | calendars | Leave the calendar out of free-busy. |
| `etags` | object | both | Map from object URI (decoded) to the ETag the source server served, so clients need not re-download unchanged objects. Surrounding quotes are stripped. Objects not listed get a content-derived ETag. |

Fields that do not apply to the collection kind are ignored. Existing
collections (same user, kind and slug) are reused and **not** modified.

The slug must match `[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}` and must not be
`inbox`, `outbox` or the notification collection's name; collections with an
invalid or reserved slug are skipped with a warning. Rename them in the
converter.

## Objects

Each file in `objects/` is the complete body of one object, as a client would
`PUT` it:

- calendars: an iCalendar (RFC 5545) `VCALENDAR` with one UID, containing
  the event/task and any recurrence overrides;
- address books: a single vCard (RFC 6350 / RFC 2426 v3.0).

Requirements and behavior:

- **UTF-8 only.** A file that is not valid UTF-8 is skipped with a warning.
  Converters must transcode legacy charsets and decode non-standard encodings
  (e.g. quoted-printable vCard 2.1) themselves.
- The importer stores bytes verbatim; it does not rewrite line endings.
- Files that fail iCalendar/vCard validation are still imported, without
  index data (no time-range or UID search), and a warning is printed. Fix them
  in the converter where possible.
- The object URI is the decoded file name (1–255 bytes, no `/`, not `.` or
  `..`). Keep source URIs if you want existing client configurations to keep
  working.
- Several objects may share a UID.

## Behavior of the importer

- **Idempotent.** Existing users and collections are reused; objects are
  overwritten by URI. Repeat runs are safe.
- **Not atomic.** An error stops the run; objects imported so far remain.
  Re-run after fixing the cause.
- **`-dry-run`** parses and validates everything (including the iCalendar and
  vCard checks) and writes nothing. Counts are of what a real first run
  would create; in a dry run every user directory counts as a user.
- Sync tokens are not part of the format; clients do one full resync after a
  migration.
- Sharing, scheduling inboxes, app passwords and two-factor settings are not
  part of version 1.

## Writing a converter

1. Create the root directory and `whitegopher-import.json`.
2. For each user, create `users/<name>/user.json`.
3. For each collection, write `collection.json` and one file per object under
   `objects/`, percent-encoding names.
4. Run `import -dry-run`, fix warnings, then import for real.

Minimal complete example:

```
tree/whitegopher-import.json
tree/users/alice/user.json
tree/users/alice/calendars/work/collection.json
tree/users/alice/calendars/work/objects/standup.ics
tree/users/alice/addressbooks/main/objects/bob.vcf
```

A converter should be a standalone program or script, in any language, that
reads the source system and writes this tree; it needs no White Gopher code.
Changes to the format bump `version`; version 1 files remain readable by
later releases.
