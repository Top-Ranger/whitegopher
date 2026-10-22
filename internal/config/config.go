// Package config loads the server configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen string `yaml:"listen"`
	// Realm is the HTTP Basic auth realm. It is also part of the HA1 hashes
	// imported from Baikal, so it must match the old server's realm when
	// importing its password hashes.
	Realm string `yaml:"realm"`
	DB    DB     `yaml:"db"`
	// BasePath is the URL prefix of the DAV tree.
	BasePath string `yaml:"base_path"`
	Admin    Admin  `yaml:"admin"`
	// SMTP, when Host is set, delivers invitations to attendees who have no
	// account here, as iMIP email.
	SMTP SMTP `yaml:"smtp"`
	// Log configures log output.
	Log Log `yaml:"log"`
	// Metrics exposes Prometheus metrics on a separate listener.
	Metrics Metrics `yaml:"metrics"`
	// Scheduling tunes invitation handling.
	Scheduling Scheduling `yaml:"scheduling"`
	// Sharing configures collection sharing between users.
	Sharing Sharing `yaml:"sharing"`
	// Limits bounds request sizes and per-user storage.
	Limits Limits `yaml:"limits"`
	// LoginLimit throttles failed logins (DAV and admin UI) per client address.
	LoginLimit LoginLimit `yaml:"login_limit"`
	// Proxy lists reverse proxies whose X-Forwarded-* headers are believed.
	Proxy Proxy `yaml:"proxy"`
}

type Log struct {
	// Format is "text" (default) or "json".
	Format string `yaml:"format"`
	// Access logs one line per HTTP request.
	Access bool `yaml:"access"`
}

type Metrics struct {
	// Listen is the address of the metrics listener, e.g. "127.0.0.1:9100".
	// Empty disables metrics. Keep it off public interfaces: it is not
	// authenticated.
	Listen string `yaml:"listen"`
}

type Scheduling struct {
	// InboxRetentionDays deletes scheduling-inbox items older than this;
	// 0 keeps them forever.
	InboxRetentionDays int `yaml:"inbox_retention_days"`
	// IMIPRequireAuth makes "whitegopher imip-receive" accept an email only when
	// the receiving mail server vouched for the sender (an
	// Authentication-Results header with dkim, spf or dmarc pass).
	IMIPRequireAuth bool `yaml:"imip_require_auth"`
}

type Sharing struct {
	// RequireAccept makes a new share wait until the other user accepts it
	// (in the admin UI, or from the notification in a CalendarServer-style
	// client); otherwise shares appear at once.
	RequireAccept bool `yaml:"require_accept"`
}

type Limits struct {
	// MaxRequestMiB caps a request body (and so the size of one object).
	MaxRequestMiB int64 `yaml:"max_request_mib"`
	// DefaultQuotaMiB caps the object data each user owns; 0 is unlimited.
	// Administrators can override it per user.
	DefaultQuotaMiB int64 `yaml:"default_quota_mib"`
}

type LoginLimit struct {
	// Failures is how many failed logins a client address may make within
	// Window before being refused. 0 disables the limit.
	Failures int           `yaml:"failures"`
	Window   time.Duration `yaml:"window"`
}

type Proxy struct {
	// Trusted holds IP addresses or CIDR ranges. Only requests arriving from
	// these peers may set the client address (X-Forwarded-For) and scheme
	// (X-Forwarded-Proto) that whitegopher acts on. Default: loopback.
	Trusted []string `yaml:"trusted"`
}

type SMTP struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	// Password may also come from WHITEGOPHER_SMTP_PASSWORD, which takes precedence.
	Password string `yaml:"password"`
	// From is the sender, e.g. "whitegopher <noreply@example.com>".
	From string `yaml:"from"`
}

type Admin struct {
	// Enabled serves the web UI under /admin/.
	Enabled bool `yaml:"enabled"`
	// BaseURL is the public address of this server, e.g.
	// "https://cal.example.com". With smtp configured it enables password
	// reset by email (links in the message point here).
	BaseURL string `yaml:"base_url"`
}

type DB struct {
	// Driver is kept for old configs; only "mysql" (the default, and also
	// right for MariaDB) is supported.
	Driver string `yaml:"driver"`
	// DSN is a go-sql-driver DSN, e.g. "user:pass@tcp(127.0.0.1:3306)/whitegopher".
	// The database must exist. The WHITEGOPHER_DB_DSN environment variable takes
	// precedence, to keep passwords out of the file.
	DSN string `yaml:"dsn"`
}

func Default() Config {
	return Config{
		Listen:     ":8080",
		Realm:      "BaikalDAV",
		BasePath:   "/dav",
		Admin:      Admin{Enabled: true},
		SMTP:       SMTP{Port: 587},
		Limits:     Limits{MaxRequestMiB: 10},
		Scheduling: Scheduling{InboxRetentionDays: 90, IMIPRequireAuth: true},
		LoginLimit: LoginLimit{Failures: 10, Window: 15 * time.Minute},
		Proxy:      Proxy{Trusted: []string{"127.0.0.1", "::1"}},
		DB:         DB{Driver: "mysql"},
	}
}

// Load reads path over the defaults, then applies environment overrides. A
// missing file is not an error.
func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, err
	}
	if err == nil {
		if err := yaml.Unmarshal(b, &c); err != nil {
			return c, err
		}
	}
	c.ApplyEnv()
	return c, nil
}

// Environment variables that override file settings.
const (
	DSNEnv          = "WHITEGOPHER_DB_DSN"
	SMTPPasswordEnv = "WHITEGOPHER_SMTP_PASSWORD"
)

// ApplyEnv applies environment overrides.
func (c *Config) ApplyEnv() {
	if v := os.Getenv(DSNEnv); v != "" {
		c.DB.DSN = v
	}
	if v := os.Getenv(SMTPPasswordEnv); v != "" {
		c.SMTP.Password = v
	}
}

// TrustedProxies parses proxy.trusted into prefixes.
func (c Config) TrustedProxies() ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, v := range c.Proxy.Trusted {
		if p, err := netip.ParsePrefix(v); err == nil {
			out = append(out, p)
		} else if a, err := netip.ParseAddr(v); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		} else {
			return nil, fmt.Errorf("proxy.trusted: %q is neither an IP address nor a CIDR range", v)
		}
	}
	return out, nil
}

// Validate reports configuration errors.
func (c Config) Validate() error {
	if c.DB.Driver != "" && c.DB.Driver != "mysql" {
		return fmt.Errorf("db.driver %q is not supported: only \"mysql\" (MariaDB included) is", c.DB.Driver)
	}
	if c.SMTP.Host != "" && c.SMTP.From == "" {
		return fmt.Errorf("smtp.from is required when smtp.host is set")
	}
	if _, err := c.TrustedProxies(); err != nil {
		return err
	}
	if c.LoginLimit.Failures < 0 || (c.LoginLimit.Failures > 0 && c.LoginLimit.Window <= 0) {
		return fmt.Errorf("login_limit: failures must be >= 0 and window positive")
	}
	if c.Admin.BaseURL != "" {
		if u, err := url.Parse(c.Admin.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("admin.base_url %q must be an http(s) URL such as https://cal.example.com", c.Admin.BaseURL)
		}
	}
	if c.Log.Format != "" && c.Log.Format != "text" && c.Log.Format != "json" {
		return fmt.Errorf("log.format %q must be \"text\" or \"json\"", c.Log.Format)
	}
	if c.Scheduling.InboxRetentionDays < 0 {
		return fmt.Errorf("scheduling.inbox_retention_days must be >= 0")
	}
	if c.Limits.MaxRequestMiB < 1 || c.Limits.DefaultQuotaMiB < 0 {
		return fmt.Errorf("limits: max_request_mib must be >= 1 and default_quota_mib >= 0")
	}
	if c.DB.DSN == "" {
		return fmt.Errorf("no database configured: set db.dsn (or %s), e.g. \"whitegopher:secret@tcp(127.0.0.1:3306)/whitegopher\"", DSNEnv)
	}
	return nil
}
