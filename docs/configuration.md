# Configuration reference

White Gopher reads a YAML file (default `config.yaml` in the working directory;
choose another with `-config`), then applies environment overrides. Unknown
keys are ignored. Example files are in [`deploy/`](../deploy).

```
whitegopher [-config FILE] <command>
```

| Command | Purpose |
|---|---|
| `serve` | run the server |
| `imip-receive [-no-auth-check] [FILE]` | process one iMIP email (replies and invitations) from FILE or stdin |
| `version` (or `-version`) | print the version and exit; needs no config or database |
| `useradd [-admin] [-email ADDRESS] NAME` | create a user; prompts for the password (hidden, twice), or reads one line from stdin when piped; at least 8 characters |
| `import [-dry-run] DIR` | import a tree in the [import format](import-format.md) |

## File options

```yaml
listen: ":8080"          # address to listen on; use "127.0.0.1:8080" behind a proxy
realm: "whitegopher"     # HTTP auth realm (see below). Default whitegopher.
base_path: "/dav"        # URL prefix of the DAV tree

db:
  driver: mysql          # optional; "mysql" (MariaDB included) is the only value
  dsn: ""                # go-sql-driver DSN, e.g. "user:pass@tcp(127.0.0.1:3306)/whitegopher"

admin:
  enabled: true          # serve the web UI under /admin/
  base_url: ""           # public URL, e.g. "https://cal.example.com"; with smtp, enables password reset by email
  invites: admin         # who may create self-registration invite links: admin, everyone or off

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

sharing:
  require_accept: false  # new shares wait for the other user to accept
  pending_expiry_days: 0 # withdraw invitations unanswered for this long; 0 keeps them
  declined_retention_days: 0     # remove declined and expired shares after this long; 0 keeps them
  notification_retention_days: 90  # delete old sharing notifications (not open invitations); 0 keeps them

scheduling:
  inbox_retention_days: 90   # delete scheduling-inbox items older than this; 0 keeps them
  imip_require_auth: true    # imip-receive only trusts mail the receiving MTA authenticated

login_limit:             # failed logins (DAV and admin UI) per client address
  failures: 10           # refuse further attempts after this many; 0 disables
  window: 15m

smtp:                    # optional iMIP email for attendees without an account
  host: ""               # empty disables email delivery
  port: 587              # 465 = implicit TLS; otherwise STARTTLS when offered
  username: ""
  password: ""
  from: ""               # required with host, e.g. "whitegopher <noreply@example.com>"
```

### Things worth knowing

- **`realm`** is the HTTP Basic realm. Imported legacy password hashes
  (`md5(user:realm:password)`, see [import-format.md](import-format.md)) only
  work while it equals the realm they were made with, until each imported user
  has signed in once. Otherwise it can be changed freely.
- **`db.dsn`** must name an existing database; White Gopher creates and migrates
  the tables itself. Special characters (`/`, `@`) in the password break the DSN
  parser, so generate one with `openssl rand -hex 24`. Unix sockets work:
  `user:pass@unix(/run/mysqld/mysqld.sock)/whitegopher`.
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
- **`admin.invites`** controls invite links, which let one person register
  their own (non-admin) account. `admin` (default): only administrators can
  create them; `everyone`: any user can; `off`: the feature and its
  `/admin/register` page are disabled. Links are single-use, unguessable
  (256 random bits, stored hashed), expire after 7 days and are shown only
  once; creators can revoke open ones under **Invites**. The link is built from
  `admin.base_url` if set, else from the request.
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
| `WHITEGOPHER_DB_DSN` | overrides `db.dsn` (keeps the password out of the config file) |
| `WHITEGOPHER_SMTP_PASSWORD` | overrides `smtp.password` |

Variables used only by the test suite are described in the README
(`WHITEGOPHER_TEST_MYSQL_DSN`, `WHITEGOPHER_TEST_SKIP_DB`).

## Endpoints

| Path | Purpose |
|---|---|
| `/dav/` | CalDAV/CardDAV (Basic auth) |
| `/.well-known/caldav`, `/.well-known/carddav` | redirect to `/dav/` |
| `/admin/` | web UI (session login) |
| `/public/<token>.ics`, `.vcf` | read-only public link of a collection (no login) |
| `/healthz` | `200 ok` when the database answers, else `503`; unauthenticated |
| `/metrics` | on the separate `metrics.listen` address only |
| `/` | redirects to `/admin/` |
