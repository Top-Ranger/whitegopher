# Configuration reference

galdav reads a YAML file (default `config.yaml` in the working directory;
choose another with `-config`), then applies environment overrides. Unknown
keys are ignored. Example files are in [`deploy/`](../deploy).

```
galdav [-config FILE] <command>
```

| Command | Purpose |
|---|---|
| `serve` | run the server |
| `version` (or `-version`) | print the version and exit; needs no config or database |
| `useradd [-admin] [-email ADDRESS] NAME` | create a user; prompts for the password (hidden, twice), or reads one line from stdin when piped; at least 8 characters |
| `import-baikal [-dry-run] SQLITE_FILE` | import from a Baikal SQLite database |
| `import-baikal [-dry-run] -mysql DSN` | import from a Baikal MySQL/MariaDB database |

## File options

```yaml
listen: ":8080"          # address to listen on; use "127.0.0.1:8080" behind a proxy
realm: "BaikalDAV"       # HTTP auth realm (see below). Default BaikalDAV.
base_path: "/dav"        # URL prefix of the DAV tree

db:
  driver: mysql          # optional; "mysql" (MariaDB included) is the only value
  dsn: ""                # go-sql-driver DSN, e.g. "user:pass@tcp(127.0.0.1:3306)/galdav"

admin:
  enabled: true          # serve the web UI under /admin/
  base_url: ""           # public URL, e.g. "https://cal.example.com"; with smtp, enables password reset by email

proxy:
  trusted: ["127.0.0.1", "::1"]   # IPs/CIDRs of reverse proxies to believe

log:
  format: text           # "text" or "json"
  access: false          # one structured log line per HTTP request

metrics:
  listen: ""             # e.g. "127.0.0.1:9100" to serve /metrics there; empty = off

limits:
  max_request_mib: 10    # largest request body, hence the largest single object
  default_quota_mib: 0   # object data each user may own; 0 = unlimited

scheduling:
  inbox_retention_days: 90   # delete scheduling-inbox items older than this; 0 keeps them

login_limit:             # failed logins (DAV and admin UI) per client address
  failures: 10           # refuse further attempts after this many; 0 disables
  window: 15m

smtp:                    # optional iMIP email for attendees without an account
  host: ""               # empty disables email delivery
  port: 587              # STARTTLS is used when the server offers it
  username: ""
  password: ""
  from: ""               # required with host, e.g. "galdav <noreply@example.com>"
```

### Things worth knowing

- **`realm`** is the HTTP Basic realm. The default `BaikalDAV` is also what
  Baikal uses, which is what makes imported password hashes
  (`md5(user:realm:password)`) work: it must match until each imported user
  has signed in once. After that, or if you never import from Baikal, it can
  be changed freely.
- **`db.dsn`** must name an existing database; galdav creates and migrates the
  tables itself. Special characters (`/`, `@`) in the password break the DSN
  parser, so generate one with `openssl rand -hex 24`. Unix sockets work:
  `user:pass@unix(/run/mysqld/mysqld.sock)/galdav`.
- **`proxy.trusted`** matters whenever a reverse proxy is in front. Only
  requests from these peers may set the client address (`X-Forwarded-For`)
  and scheme (`X-Forwarded-Proto`); everyone else's headers are ignored. The
  address drives login rate limiting, failed-login logging and the `Secure`
  cookie flag. If your
  proxy is on another host, add its address; if it is missing, all users appear
  to come from the proxy and share one rate limit.
- **`admin.base_url`** is only used to build the link in password-reset emails
  (the `Host` header is never trusted for that). Password reset by email is on
  when both `smtp` and `admin.base_url` are set; users need an email address on
  their account.
- **`log.access`** writes one line per request (method, path without query,
  status, bytes, duration in ms, client address, user). `/healthz` is left out.
  Passwords and request bodies are never logged.
- **`metrics.listen`** starts a second, **unauthenticated** listener serving
  Prometheus metrics at `/metrics`: request counts by method and status class,
  a latency histogram, failed/blocked logins, database pool gauges, uptime and
  build info. Bind it to loopback or a private interface.
- **`base_path`** changes where clients must connect; `/.well-known/*` redirects
  follow it.

## Environment variables

| Variable | Effect |
|---|---|
| `GALDAV_DB_DSN` | overrides `db.dsn` (keeps the password out of the config file) |
| `GALDAV_SMTP_PASSWORD` | overrides `smtp.password` |

Variables used only by the test suite are described in the README
(`GALDAV_TEST_MYSQL_DSN`, `GALDAV_TEST_SKIP_DB`).

## Endpoints

| Path | Purpose |
|---|---|
| `/dav/` | CalDAV/CardDAV (Basic auth) |
| `/.well-known/caldav`, `/.well-known/carddav` | redirect to `/dav/` |
| `/admin/` | web UI (session login) |
| `/healthz` | `200 ok` when the database answers, else `503`; unauthenticated |
| `/metrics` | on the separate `metrics.listen` address only |
| `/` | redirects to `/admin/` |
