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

## Also open

- Verify on MySQL 8 (development and the test suite use MariaDB).

## Not planned

- SQLite or Postgres as the server's database (SQLite remains only as a
  Baikal import source).
- Built-in TLS or ACME: use a reverse proxy.
- A Docker image (deployment is a static binary plus MariaDB).
