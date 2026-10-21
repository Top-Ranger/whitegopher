// Package store is the persistence layer, backed by MySQL/MariaDB (pure Go,
// no cgo).
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var ErrNotFound = errors.New("store: not found")

// forUpdate is appended to SELECTs inside write transactions to lock rows.
const forUpdate = " FOR UPDATE"

func isUnique(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

type Store struct {
	DB *sql.DB
}

// Open opens (and migrates) the database named by a go-sql-driver DSN such
// as "user:pass@tcp(127.0.0.1:3306)/galdav". The database must exist.
func Open(dsn string) (*Store, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("mysql dsn: %w", err)
	}
	// Case-sensitive comparisons for usernames, slugs and URIs.
	cfg.Collation = "utf8mb4_bin"
	cfg.MultiStatements = false
	cfg.ParseTime = false
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(20)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("mysql: %w", err)
	}
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name VARCHAR(255) NOT NULL PRIMARY KEY)`); err != nil {
		return err
	}
	const dir = "migrations"
	entries, err := fs.ReadDir(migrationFS, dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var done int
		if err := s.DB.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			continue
		}
		body, err := migrationFS.ReadFile(dir + "/" + name)
		if err != nil {
			return err
		}
		// MySQL commits DDL implicitly, so a transaction gives no atomicity
		// there; the migration is recorded only after all statements ran.
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		for _, stmt := range splitStatements(string(body)) {
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %s: %w", name, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// splitStatements splits a migration file into statements. Statements end
// with ";" at the end of a line; "--" comment lines are dropped.
func splitStatements(src string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		cur.WriteString(line + "\n")
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
	}
	if rest := strings.TrimSpace(cur.String()); rest != "" {
		out = append(out, rest)
	}
	return out
}
