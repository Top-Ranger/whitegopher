# galdav

A CalDAV and CardDAV server in pure Go: one static binary plus MariaDB/MySQL.
MIT licensed. A from-scratch alternative to [Baikal](https://sabre.io/baikal/)
that can import an existing Baikal installation.

Clean-room: implemented from the RFCs and observed client behaviour, not from
the source of Baikal or sabre/dav.

**Status: not yet tested with real clients** (DAVx5, iOS/macOS, Thunderbird).
The automated suite is extensive, but expect client-specific quirks.
See [docs/features.md](docs/features.md) and the
[client testing checklist](docs/client-testing.md).

## What it does

Calendars (events, tasks) and address books over CalDAV/CardDAV with Basic auth;
sync (`sync-collection`), queries and free-busy; invitations and
replies between users, with optional email to outsiders (scheduling, RFC 6638);
sharing between users, read-only or read-write; a web admin UI; and a one-time
importer for Baikal databases.

## Documentation

| | |
|---|---|
| [docs/deployment.md](docs/deployment.md) | install, systemd, TLS proxy, clients, Baikal migration, backups, security, troubleshooting |
| [docs/configuration.md](docs/configuration.md) | every config option, CLI command, environment variable and endpoint |
| [docs/features.md](docs/features.md) | what works, and the known limits of each area |
| [docs/client-testing.md](docs/client-testing.md) | checklist and test data for trying DAVx5, iOS/macOS and Thunderbird |
| [docs/roadmap.md](docs/roadmap.md) | what comes next |
| [deploy/](deploy) | systemd unit, example config and env file, Caddy and nginx configs |

## Quick start (development)

Needs Go 1.26+ and a MariaDB/MySQL database you can write to:

```sh
mysql -e "CREATE DATABASE galdav CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
export GALDAV_DB_DSN='root@tcp(127.0.0.1:3306)/galdav'
go run ./cmd/galdav useradd -admin -email me@example.com alice   # prompts for a password
go run ./cmd/galdav serve                                        # http://localhost:8080/admin/
```

For a real installation follow [docs/deployment.md](docs/deployment.md).

## Development

```
cmd/galdav/         CLI: serve, useradd, import-baikal
internal/config/    YAML config and environment overrides
internal/store/     MySQL/MariaDB store, embedded migrations
internal/auth/      password hashing, Basic auth middleware
internal/netx/      trusted proxies, client address, login rate limiter
internal/obs/       access log and Prometheus metrics
internal/dav/       WebDAV/CalDAV/CardDAV handlers, REPORTs, sharing
internal/sched/     free-busy, iTIP scheduling, iMIP email
internal/index/     object validation and occurrence indexing
internal/admin/     web UI (templates and static assets embedded)
internal/importer/  one-time Baikal import
internal/storetest/ throwaway test databases
deploy/             deployment files
docs/               documentation
```

Tests need a MySQL/MariaDB server where they may create and drop
`galdav_test_*` databases. Without it, database tests **fail** with an
explanatory message, so a green run always means they ran:

```sh
GALDAV_TEST_MYSQL_DSN='root@tcp(127.0.0.1:3306)/' go test -p 1 ./...
```

(`GALDAV_TEST_SKIP_DB=1` skips them on purpose; the pure unit tests in
`internal/index` and `internal/sched` need no database.) Use `-race` when
touching concurrency.

The server stores everything in MySQL/MariaDB. SQLite is supported only as a
read-only source for the Baikal importer.

## Dependencies and licences

galdav is MIT. Direct dependencies are MIT/BSD/Apache-2.0 licensed except
`github.com/go-sql-driver/mysql`, which is MPL-2.0 (file-level copyleft: it
only affects modifications to that library's own files, not galdav).
