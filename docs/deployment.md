# Deployment guide

A production galdav is three things: the **galdav binary**, a **MariaDB/MySQL
database**, and a **reverse proxy** that terminates TLS. This guide sets that up
on a Linux host with systemd. Commands assume Debian/Ubuntu-style paths; adapt
as needed. The galdav parts (build, database user with the stated grant,
configuration, user creation, serving, health check, proxy-header handling,
graceful shutdown) were rehearsed against the real binary and a MariaDB server;
the root-level steps (creating the system user, `systemd-run`, installing the
unit) were only syntax-checked (`systemd-analyze verify`, `caddy validate`), and
the nginx configuration and anything marked *untested* were not run.

Contents: [requirements](#requirements) · [build](#1-build-and-install) ·
[database](#2-database) · [service user and config](#3-service-user-and-configuration) ·
[first admin](#4-create-the-first-administrator) · [systemd](#5-run-it-with-systemd) ·
[TLS proxy](#6-put-a-tls-proxy-in-front) · [clients](#7-connect-clients) ·
[Baikal migration](#8-migrating-from-baikal) · [operations](#operations) ·
[security checklist](#security-checklist) · [troubleshooting](#troubleshooting)

## Requirements

- Linux host; galdav is a single static binary, no other runtime.
- MariaDB (developed and tested on 13.0) or MySQL (8.x expected to work,
  *untested*), utf8mb4. On the same host or reachable over a private network.
- A DNS name and a reverse proxy with a TLS certificate (Caddy gets one
  automatically). Calendar and contact clients send passwords with every
  request: **never expose galdav over plain HTTP.**
- To build: Go 1.26 or newer. (Or build elsewhere and copy the binary.)

## 1. Build and install

```sh
git clone <your galdav repository> && cd galdav
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o galdav ./cmd/galdav
sudo install -m 0755 galdav /usr/local/bin/galdav
```

The result is a ~15 MB statically linked executable with the web UI embedded.
Cross-compile with `GOOS`/`GOARCH` if you build on another machine.

## 2. Database

Create a database and a dedicated user (replace the password; generate one
with `openssl rand -hex 24`, avoiding `/` and `@`, which break the DSN):

```sql
CREATE DATABASE galdav CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE USER 'galdav'@'127.0.0.1' IDENTIFIED BY 'CHANGE-ME';
GRANT ALL PRIVILEGES ON galdav.* TO 'galdav'@'127.0.0.1';
```

`ALL` on that one database is needed because galdav creates and alters its own
tables at startup and on upgrades. Grant nothing outside it.

Uploads can reach 10 MiB, so make sure `max_allowed_packet` is at least `16M`
(the default of MariaDB and MySQL 8 already is). Keep the database listening on
localhost or a private interface only.

## 3. Service user and configuration

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin galdav
sudo install -d -m 0750 -o root -g galdav /etc/galdav
sudo install -m 0640 -o root -g galdav deploy/config.example.yaml /etc/galdav/config.yaml
sudo install -m 0600 -o root -g root     deploy/galdav.env.example  /etc/galdav/galdav.env
sudoedit /etc/galdav/galdav.env      # put the real database password into GALDAV_DB_DSN
```

Review `/etc/galdav/config.yaml`; the defaults in `deploy/config.example.yaml`
are right for a proxy on the same host. Two decisions to make **now**:

1. **`realm`** is the HTTP Basic realm. Pick a value (e.g. your domain). If
   you will import from Baikal, use `BaikalDAV` instead until the imported
   users have signed in once (see [section 8](#8-migrating-from-baikal)).
2. **`proxy.trusted`**: if the proxy runs on another host, add its address.

Full option list: [configuration.md](configuration.md).

## 4. Create the first administrator

The web UI's setup page creates the first administrator for whoever reaches it
first. On an Internet-facing host, create the account from the command line
**before** starting the service or opening the proxy:

```sh
sudo systemd-run --pty --wait --collect --uid=galdav --property=EnvironmentFile=/etc/galdav/galdav.env \
  /usr/local/bin/galdav -config /etc/galdav/config.yaml useradd -admin -email you@example.com alice
```

It prompts for the password twice without echo (minimum 8 characters). The
`-email` is what lets other users invite this account to events. Further users
are created in the admin UI. (Without `systemd-run`: `export` the variables from
the env file in a shell and run the same command as the `galdav` user. Do not
`source` the env file; its DSN contains parentheses.)

## 5. Run it with systemd

```sh
sudo install -m 0644 deploy/galdav.service /etc/systemd/system/galdav.service
sudo systemctl daemon-reload
sudo systemctl enable --now galdav
systemctl status galdav
curl -s http://127.0.0.1:8080/healthz      # prints "ok"
```

The unit runs as the unprivileged `galdav` user with a locked-down sandbox,
restarts on failure and gives a graceful shutdown of up to 15 seconds. Logs go
to the journal: `journalctl -u galdav -f`. On first start galdav creates its
tables; a database error appears there (and the service retries).

## 6. Put a TLS proxy in front

galdav speaks plain HTTP and trusts `X-Forwarded-For`/`-Proto` only from the
addresses in `proxy.trusted`. The proxy must forward the original `Host`
header, allow request bodies of at least 10 MiB, and use TLS.

**Caddy** (simplest; gets and renews the certificate itself):
[`deploy/Caddyfile`](../deploy/Caddyfile)

```
cal.example.com {
	encode zstd gzip
	request_body {
		max_size 12MB
	}
	reverse_proxy 127.0.0.1:8080
}
```

**nginx**: [`deploy/nginx.conf`](../deploy/nginx.conf) (*untested sketch*;
remember `client_max_body_size 12m` and the three `proxy_set_header` lines).

Then check it from outside: `curl -i https://cal.example.com/healthz` and
`curl -I https://cal.example.com/.well-known/caldav` (a `301` to `/dav/`).
Open `https://cal.example.com/admin/` and sign in.

If you do not want the web UI reachable from the Internet, restrict `/admin/`
in the proxy to your own address range, or set `admin: {enabled: false}` and
manage users with the command line only (calendars, sharing and the default
invitation calendar are then not manageable; keep the UI on for normal use).

## 7. Connect clients

Create the users first (admin UI → Users → New user). Give each user an
**email address**: it is how invitations find them.

| Client | Setup |
|---|---|
| **DAVx5** (Android) | Add account → *Login with URL and user name*: base URL `https://cal.example.com/dav/`, then user name and password. |
| **iOS / macOS** | Add account → Other → CalDAV (and CardDAV) account: server `cal.example.com`, user name, password. Discovery uses `/.well-known/`. |
| **Thunderbird** | Calendar → New calendar → *On the network* → CalDAV, location `https://cal.example.com/dav/calendars/<user>/<name>/`. For contacts use the CardDAV address-book option with `https://cal.example.com/dav/addressbooks/<user>/<name>/`. |
| Other | Principal URL: `https://cal.example.com/dav/principals/<user>/`. |

Create the first calendar and address book in the admin UI so the client has
something to sync. The client-testing status is stated in
[features.md](features.md); before relying on a new installation, work through
[client-testing.md](client-testing.md) and report anything that misbehaves.

## 8. Migrating from Baikal

1. Install galdav as above, but set `realm: "BaikalDAV"` in the config (unless
   you changed Baikal's realm) **before** importing, so imported password hashes
   keep working. Do not create users first.
2. Stop clients from writing to Baikal (or accept that later changes are
   lost), and take a backup of the Baikal database.
3. Preview, then import. For Baikal's default SQLite database (usually
   `Specific/db/db.sqlite`; readable by the user running the command):

   ```sh
   sudo systemd-run --pty --wait --collect --uid=galdav --property=EnvironmentFile=/etc/galdav/galdav.env \
     /usr/local/bin/galdav -config /etc/galdav/config.yaml import-baikal -dry-run /path/to/db.sqlite
   # review the counts and warnings, then run again without -dry-run
   ```

   For Baikal on MySQL/MariaDB use `import-baikal -mysql 'user:pass@tcp(host:3306)/baikal'`.
   The import can be repeated safely.
4. Imported users keep their passwords. Give them email addresses (admin UI →
   Users) so scheduling works, and make at least one an administrator.
5. Point clients at galdav. To keep existing client configurations, let the
   proxy rewrite Baikal's URL prefix (*untested*): in nginx,
   `rewrite ^/dav\.php/(.*)$ /dav/$1 last;`. Otherwise re-add the accounts.
   Either way clients do a full resync once.

Not imported: shared calendars, Baikal's pending scheduling inbox, and its
settings. A calendar named `inbox` or `outbox` is renamed (reserved names).

## Operations

**Backups.** All state is in the database; config and the env file are small.

```sh
mysqldump --single-transaction galdav | zstd > galdav-$(date +%F).sql.zst
```

Back up `/etc/galdav/` as well. Test a restore occasionally:
load the dump into an empty database, point a second instance at it, and sign
in. The `sessions` table can be skipped; users just sign in again.

**Upgrades.** Take a backup, build or download the new binary, then:

```sh
sudo install -m 0755 galdav /usr/local/bin/galdav
sudo systemctl restart galdav
```

Schema migrations run automatically at startup and are forward-only, so the
backup is also your way back. Run **one** instance during an upgrade.

**Monitoring.** Poll `GET /healthz` (`200 ok`, or `503` when the database is
unreachable). Watch `journalctl -u galdav` for errors. Disk growth is the
database: object data plus the change log used for sync.

**Maintenance.** The scheduling inbox is not pruned automatically; clients
usually delete processed items. Deleting a user removes their collections,
shares and sessions.

**Logs and privacy.** galdav logs startup, shutdown and errors, not requests;
use the proxy's access log if you need request logs.

## Security checklist

- TLS only; HSTS at the proxy is a good idea once it works.
- The database listens on localhost/private network and the `galdav` database
  user has no rights beyond its own database.
- Create the first admin from the CLI on exposed hosts (section 4).
- `proxy.trusted` lists only your proxy. If it is empty or wrong, rate limits
  and cookie flags are wrong too.
- DAV logins are **not** rate limited by galdav; add `fail2ban` on the proxy log
  (repeated `401`s on `/dav/`) or a proxy rate limit.
- Anyone with an account can query any user's free-busy times (no details).
  Do not create accounts for people you do not want to see that.
- Keep `/etc/galdav/galdav.env` at mode 0600 and out of backups that are shared.
- Never change `realm` after users exist.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Service exits at start with `no database configured` | `GALDAV_DB_DSN` missing in the env file, or the unit lacks `EnvironmentFile`. |
| `mysql: ... connection refused` / `Access denied` | wrong host/port/password, or the user was created for `localhost` while the DSN uses `127.0.0.1` (the host part must match). |
| Clients keep asking for the password | wrong password, or the realm changed since the user was created (reset the password). |
| Admin login says "too many failed attempts" for everyone | the proxy's address is not in `proxy.trusted`, so all users share one client address. |
| Admin cookies/CSRF problems behind the proxy | the proxy does not send `Host` and `X-Forwarded-Proto` (see section 6). |
| `413` on large uploads | proxy body-size limit below 10 MiB. |
| `/.well-known/caldav` does not redirect | the proxy is not forwarding that path to galdav. |
| Invitations to colleagues do not arrive | they have no email address on their account, or the address differs from the one invited (matching is case-insensitive and exact). |

Quick manual checks (replace host and credentials):

```sh
curl -u alice -X PROPFIND -H 'Depth: 1' https://cal.example.com/dav/calendars/alice/
curl -u alice -X REPORT  -H 'Depth: 1' --data '<d:sync-collection xmlns:d="DAV:"><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>' https://cal.example.com/dav/calendars/alice/NAME/
```
