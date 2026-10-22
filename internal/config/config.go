// Package config loads the server configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"

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
	// Proxy lists reverse proxies whose X-Forwarded-* headers are believed.
	Proxy Proxy `yaml:"proxy"`
}

type Proxy struct {
	// Trusted holds IP addresses or CIDR ranges. Only requests arriving from
	// these peers may set the client address (X-Forwarded-For) and scheme
	// (X-Forwarded-Proto) that galdav acts on. Default: loopback.
	Trusted []string `yaml:"trusted"`
}

type SMTP struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	// Password may also come from GALDAV_SMTP_PASSWORD, which takes precedence.
	Password string `yaml:"password"`
	// From is the sender, e.g. "galdav <noreply@example.com>".
	From string `yaml:"from"`
}

type Admin struct {
	// Enabled serves the web UI under /admin/.
	Enabled bool `yaml:"enabled"`
}

type DB struct {
	// Driver is kept for old configs; only "mysql" (the default, and also
	// right for MariaDB) is supported.
	Driver string `yaml:"driver"`
	// DSN is a go-sql-driver DSN, e.g. "user:pass@tcp(127.0.0.1:3306)/galdav".
	// The database must exist. The GALDAV_DB_DSN environment variable takes
	// precedence, to keep passwords out of the file.
	DSN string `yaml:"dsn"`
}

func Default() Config {
	return Config{
		Listen:   ":8080",
		Realm:    "BaikalDAV",
		BasePath: "/dav",
		Admin:    Admin{Enabled: true},
		SMTP:     SMTP{Port: 587},
		Proxy:    Proxy{Trusted: []string{"127.0.0.1", "::1"}},
		DB:       DB{Driver: "mysql"},
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
	DSNEnv          = "GALDAV_DB_DSN"
	SMTPPasswordEnv = "GALDAV_SMTP_PASSWORD"
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
	if c.DB.DSN == "" {
		return fmt.Errorf("no database configured: set db.dsn (or %s), e.g. \"galdav:secret@tcp(127.0.0.1:3306)/galdav\"", DSNEnv)
	}
	return nil
}
