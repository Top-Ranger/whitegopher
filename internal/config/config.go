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
}

type DB struct {
	Driver string `yaml:"driver"` // only "sqlite" for now
	DSN    string `yaml:"dsn"`
}

func Default() Config {
	return Config{
		Listen:   ":8080",
		Realm:    "BaikalDAV",
		BasePath: "/dav",
		DB:       DB{Driver: "sqlite", DSN: "galdav.db"},
	}
}

// Load reads path over the defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = yaml.Unmarshal(b, &c)
	return c, err
}
