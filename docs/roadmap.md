# Roadmap

Ordered by what I would do next. See [features.md](features.md) for what
exists and for the limits that motivate these items.

## Next: prove it with real clients

White Gopher has only been tested against its own test suite. Before adding more
features, run through DAVx5 (Android), iOS/macOS Calendar and Contacts,
Thunderbird and Evolution: account setup via `.well-known`, create/edit/delete,
sync after offline edits, invitations (accept/decline), sharing, free-busy.
The checklist, test data and seed script are in
[client-testing.md](client-testing.md). Fix whatever surfaces. This is the biggest open risk (scheduling and Apple
sharing especially), and the results decide the order of everything below.

## Public calendars should have a more readable URL

For example by using the combination of random words. This must not reduce the security promise that an URL is not guessable.

## Data export

Allow the export of data for a given user. This includes all data belonging to a user excluding where the user is appearing in an other users calendar (e.g., as an attendee). Sensitive data like password hash can be left out, but it must be mentioned that they are left out (e.g., by writing "[blinded]"). The data must be in a format that is theoretically transferrable and – at the same time – understandable to a user.

The goal is to make GDPR requests more easy.

The data must be exportable by calling the binary on the cmd (e.g., ```./whitegopher export [user]```). If feasible, this should also be doable by the user themselves in the admin UI.

## Cache public calendars

On access, cache a generated public calendar file in the database (if not already cached). Any change to the calendar will invalidate the cache.

If the cache is not accessed a lot (e.g., cache is discarded with less than 3 Downloads 5 times in a row) temporarily disable caching for that calendar since it is not worth it (e.g., for a month).

##  Also open

- Verify on MySQL 8 (development and the test suite use MariaDB).

## Not planned

- SQLite or Postgres as the server's database.
- Built-in TLS or ACME: use a reverse proxy.
- A Docker image (deployment is a static binary plus MariaDB).
