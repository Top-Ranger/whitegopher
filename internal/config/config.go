// Package config loads the server configuration.
package config

import (
	"errors"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen string `yaml:"listen"`
	// Realm is used for HTTP auth. It is part of Digest HA1 hashes, so it
	// must match the old server's realm when importing its password hashes.
	Realm string `yaml:"realm"`
	DB    DB     `yaml:"db"`
	// BasePath is the URL prefix of the DAV tree.
	BasePath string `yaml:"base_path"`
	Admin    Admin  `yaml:"admin"`
}

type Admin struct {
	// Enabled serves the web UI under /admin/.
	Enabled bool `yaml:"enabled"`
}

type DB struct {
	// Driver is "sqlite" (DSN is a file path) or "mysql" (DSN is a
	// go-sql-driver DSN, e.g. "user:pass@tcp(127.0.0.1:3306)/galdav"; the
	// database must exist). MariaDB works as "mysql".
	Driver string `yaml:"driver"`
	// DSN may also be supplied through the GALDAV_DB_DSN environment
	// variable, which takes precedence, to keep passwords out of the file.
	DSN string `yaml:"dsn"`
}

func Default() Config {
	return Config{
		Listen:   ":8080",
		Realm:    "BaikalDAV",
		BasePath: "/dav",
		Admin:    Admin{Enabled: true},
		DB:       DB{Driver: "sqlite", DSN: "galdav.db"},
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

// DSNEnv overrides db.dsn when set.
const DSNEnv = "GALDAV_DB_DSN"

// ApplyEnv applies environment overrides.
func (c *Config) ApplyEnv() {
	if v := os.Getenv(DSNEnv); v != "" {
		c.DB.DSN = v
	}
}
