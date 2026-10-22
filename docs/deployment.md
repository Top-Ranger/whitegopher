# Deployment guide

A production whitegopher is three things: the **whitegopher binary**, a **MariaDB/MySQL
database**, and a **reverse proxy** that terminates TLS. This guide sets that up
on a Linux host with systemd. Commands assume Debian/Ubuntu-style paths; adapt
as needed. The whitegopher parts (build, database user with the stated grant,
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

- Linux host; whitegopher is a single static binary, no other runtime.
- MariaDB (developed and tested on 13.0) or MySQL (8.x expected to work,
  *untested*), utf8mb4. On the same host or reachable over a private network.
- A DNS name and a reverse proxy with a TLS certificate (Caddy gets one
  automatically). Calendar and contact clients send passwords with every
  request: **never expose whitegopher over plain HTTP.**
- To build: Go 1.26 or newer. (Or build elsewhere and copy the binary.)

## 1. Build and install

```sh
git clone <your whitegopher repository> && cd whitegopher
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o whitegopher ./cmd/whitegopher
sudo install -m 0755 whitegopher /usr/local/bin/whitegopher
```

`make build` does the same with the version stamped in; `whitegopher version`
shows it. Tagged releases publish ready-made archives (`make release`).

The result is a ~15 MB statically linked executable with the web UI embedded.
Cross-compile with `GOOS`/`GOARCH` if you build on another machine.

## 2. Database

Create a database and a dedicated user (replace the password; generate one
with `openssl rand -hex 24`, avoiding `/` and `@`, which break the DSN):

```sql
CREATE DATABASE whitegopher CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE USER 'whitegopher'@'127.0.0.1' IDENTIFIED BY 'CHANGE-ME';
GRANT ALL PRIVILEGES ON whitegopher.* TO 'whitegopher'@'127.0.0.1';
```

`ALL` on that one database is needed because whitegopher creates and alters its own
tables at startup and on upgrades. Grant nothing outside it.

Uploads can reach 10 MiB, so make sure `max_allowed_packet` is at least `16M`
(the default of MariaDB and MySQL 8 already is). Keep the database listening on
localhost or a private interface only.

## 3. Service user and configuration

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin whitegopher
sudo install -d -m 0750 -o root -g whitegopher /etc/whitegopher
sudo install -m 0640 -o root -g whitegopher deploy/config.example.yaml /etc/whitegopher/config.yaml
sudo install -m 0600 -o root -g root     deploy/whitegopher.env.example  /etc/whitegopher/whitegopher.env
sudoedit /etc/whitegopher/whitegopher.env      # put the real database password into WHITEGOPHER_DB_DSN
```

Review `/etc/whitegopher/config.yaml`; the defaults in `deploy/config.example.yaml`
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
sudo systemd-run --pty --wait --collect --uid=whitegopher --property=EnvironmentFile=/etc/whitegopher/whitegopher.env \
  /usr/local/bin/whitegopher -config /etc/whitegopher/config.yaml useradd -admin -email you@example.com alice
```

It prompts for the password twice without echo (minimum 8 characters). The
`-email` is what lets other users invite this account to events. Further users
are created in the admin UI. (Without `systemd-run`: `export` the variables from
the env file in a shell and run the same command as the `whitegopher` user. Do not
`source` the env file; its DSN contains parentheses.)

## 5. Run it with systemd

```sh
sudo install -m 0644 deploy/whitegopher.service /etc/systemd/system/whitegopher.service
sudo systemctl daemon-reload
sudo systemctl enable --now whitegopher
systemctl status whitegopher
curl -s http://127.0.0.1:8080/healthz      # prints "ok"
```

The unit runs as the unprivileged `whitegopher` user with a locked-down sandbox,
restarts on failure and gives a graceful shutdown of up to 15 seconds. Logs go
to the journal: `journalctl -u whitegopher -f`. On first start whitegopher creates its
tables; a database error appears there (and the service retries).

## 6. Put a TLS proxy in front

whitegopher speaks plain HTTP and trusts `X-Forwarded-For`/`-Proto` only from the
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

1. Install whitegopher as above, but set `realm: "BaikalDAV"` in the config (unless
   you changed Baikal's realm) **before** importing, so imported password hashes
   keep working. Do not create users first.
2. Stop clients from writing to Baikal (or accept that later changes are
   lost), and take a backup of the Baikal database.
3. Preview, then import. For Baikal's default SQLite database (usually
   `Specific/db/db.sqlite`; readable by the user running the command):

   ```sh
   sudo systemd-run --pty --wait --collect --uid=whitegopher --property=EnvironmentFile=/etc/whitegopher/whitegopher.env \
     /usr/local/bin/whitegopher -config /etc/whitegopher/config.yaml import-baikal -dry-run /path/to/db.sqlite
   # review the counts and warnings, then run again without -dry-run
   ```

   For Baikal on MySQL/MariaDB use `import-baikal -mysql 'user:pass@tcp(host:3306)/baikal'`.
   The import can be repeated safely.
4. Imported users keep their passwords. Give them email addresses (admin UI →
   Users) so scheduling works, and make at least one an administrator.
5. Point clients at whitegopher. To keep existing client configurations, let the
   proxy rewrite Baikal's URL prefix (*untested*): in nginx,
   `rewrite ^/dav\.php/(.*)$ /dav/$1 last;`. Otherwise re-add the accounts.
   Either way clients do a full resync once.

Not imported: shared calendars, Baikal's pending scheduling inbox, and its
settings. A calendar named `inbox` or `outbox` is renamed (reserved names).

## Inbound email (optional)

Attendees outside whitegopher answer an invitation by email, and external organizers
invite your users by email. whitegopher does not fetch mail; your mail server hands
each relevant message to `whitegopher imip-receive`, which reads it from standard
input. Typical wiring (Postfix, Sieve or procmail): for messages that contain a
`text/calendar` part and are addressed to a whitegopher user, **copy** them to

```sh
/usr/local/bin/whitegopher -config /etc/whitegopher/config.yaml imip-receive
```

run as the `whitegopher` user (e.g. a Postfix `pipe` transport with `user=whitegopher`,
or a Sieve/procmail pipe action). The command exits `0` for anything it
understood or deliberately ignored (it logs why, at warn level) and non-zero for
real errors such as an unreachable database, so the mail is retried.

Email can be forged, so whitegopher acts on a message only if all of these hold:
the calendar's `ORGANIZER`/`ATTENDEE` is the message's `From`, the topmost
`Authentication-Results` header (added by **your** mail server, e.g. via
OpenDKIM/OpenDMARC/Rspamd) shows `dkim=pass`, `spf=pass` or `dmarc=pass`, and
the message would not overwrite an event the user organizes. Leave
`scheduling.imip_require_auth` on unless your mail path is fully trusted
(`-no-auth-check` skips the check for a single run, e.g. when testing).

Users need their **email address** set in their account: that is how a message
is matched to them.

## Operations

**Backups.** All state is in the database; config and the env file are small.

```sh
mysqldump --single-transaction whitegopher | zstd > whitegopher-$(date +%F).sql.zst
```

Back up `/etc/whitegopher/` as well. Test a restore occasionally:
load the dump into an empty database, point a second instance at it, and sign
in. The `sessions` table can be skipped; users just sign in again.

**Upgrades.** Take a backup, build or download the new binary, then:

```sh
sudo install -m 0755 whitegopher /usr/local/bin/whitegopher
sudo systemctl restart whitegopher
```

Schema migrations run automatically at startup and are forward-only, so the
backup is also your way back. Run **one** instance during an upgrade.

**Monitoring.** Poll `GET /healthz` (`200 ok`, or `503` when the database is
unreachable). Watch `journalctl -u whitegopher` for errors. For Prometheus, set
`metrics.listen` to a loopback or private address and scrape `/metrics`. Disk growth is the
database: object data plus the change log used for sync.

**Maintenance.** Scheduling-inbox items older than
`scheduling.inbox_retention_days` (90 by default; 0 keeps them) are deleted at
startup and daily; clients usually delete processed items sooner. Deleting a user removes their collections,
shares and sessions.

**Logs and privacy.** By default whitegopher logs startup, shutdown, errors and
failed logins, not requests. Set `log.access: true` for one structured line per
request (path without query string, never bodies or passwords), or use the
proxy's access log.

## Security checklist

- TLS only; HSTS at the proxy is a good idea once it works.
- The database listens on localhost/private network and the `whitegopher` database
  user has no rights beyond its own database.
- Create the first admin from the CLI on exposed hosts (section 4).
- `proxy.trusted` lists only your proxy. If it is empty or wrong, rate limits
  and cookie flags are wrong too.
- Failed logins (DAV and admin UI) are rate limited per client address
  (`login_limit`) and logged as `login failed ... ip=...` in the journal, with
  the real client address when `proxy.trusted` is right. `fail2ban` can watch
  that line (`journalctl -u whitegopher`) to block repeat offenders at the firewall.
- Public links (`/public/<token>`) are readable by anyone who has the address.
  Treat them like a password; replace or turn off a link in the collection's
  page if it leaks.
- Anyone with an account can query any user's free-busy times (no details)
  unless that user hides it (Account → Privacy). Do not create accounts for
  people you do not want to see that.
- Keep `/etc/whitegopher/whitegopher.env` at mode 0600 and out of backups that are shared.
- Never change `realm` after users exist.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Service exits at start with `no database configured` | `WHITEGOPHER_DB_DSN` missing in the env file, or the unit lacks `EnvironmentFile`. |
| `mysql: ... connection refused` / `Access denied` | wrong host/port/password, or the user was created for `localhost` while the DSN uses `127.0.0.1` (the host part must match). |
| Clients keep asking for the password | wrong password, or the realm changed since the user was created (reset the password). |
| Admin login says "too many failed attempts" for everyone | the proxy's address is not in `proxy.trusted`, so all users share one client address. |
| Admin cookies/CSRF problems behind the proxy | the proxy does not send `Host` and `X-Forwarded-Proto` (see section 6). |
| `413` on large uploads | proxy body-size limit below 10 MiB. |
| `/.well-known/caldav` does not redirect | the proxy is not forwarding that path to whitegopher. |
| Invitations to colleagues do not arrive | they have no email address on their account, or the address differs from the one invited (matching is case-insensitive and exact). |

Quick manual checks (replace host and credentials):

```sh
curl -u alice -X PROPFIND -H 'Depth: 1' https://cal.example.com/dav/calendars/alice/
curl -u alice -X REPORT  -H 'Depth: 1' --data '<d:sync-collection xmlns:d="DAV:"><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>' https://cal.example.com/dav/calendars/alice/NAME/
```
