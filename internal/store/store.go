// Package store is the persistence layer, backed by SQLite or MySQL/MariaDB
// (both pure Go, no cgo).
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
	_ "modernc.org/sqlite"
)

//go:embed migrations
var migrationFS embed.FS

var ErrNotFound = errors.New("store: not found")

// Supported database drivers.
const (
	DriverSQLite = "sqlite"
	DriverMySQL  = "mysql"
)

// dialect holds the SQL that differs between databases.
type dialect struct {
	name string
	// forUpdate is appended to SELECTs inside write transactions to lock rows.
	forUpdate string
	// upsertObject is the INSERT for PutObject.
	upsertObject string
	// insertUserIfNone inserts a user only while the table is empty.
	insertUserIfNone string
	isUnique         func(error) bool
}

var sqliteDialect = dialect{
	name: DriverSQLite,
	upsertObject: `INSERT INTO objects (collection_id, uri, uid, etag, data, component, first_occ, last_occ)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (collection_id, uri) DO UPDATE SET
		   uid = excluded.uid, etag = excluded.etag, data = excluded.data, component = excluded.component,
		   first_occ = excluded.first_occ, last_occ = excluded.last_occ, updated_at = CURRENT_TIMESTAMP`,
	insertUserIfNone: `INSERT INTO users (username, display_name, email, digest_ha1, password_hash, is_admin)
		 SELECT ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`,
	isUnique: func(err error) bool { return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") },
}

var mysqlDialect = dialect{
	name:      DriverMySQL,
	forUpdate: " FOR UPDATE",
	upsertObject: `INSERT INTO objects (collection_id, uri, uid, etag, data, component, first_occ, last_occ)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   uid = VALUES(uid), etag = VALUES(etag), data = VALUES(data), component = VALUES(component),
		   first_occ = VALUES(first_occ), last_occ = VALUES(last_occ), updated_at = CURRENT_TIMESTAMP`,
	insertUserIfNone: `INSERT INTO users (username, display_name, email, digest_ha1, password_hash, is_admin)
		 SELECT ?, ?, ?, ?, ?, ? FROM DUAL WHERE NOT EXISTS (SELECT 1 FROM users)`,
	isUnique: func(err error) bool {
		var me *mysql.MySQLError
		return errors.As(err, &me) && me.Number == 1062
	},
}

type Store struct {
	DB *sql.DB
	d  dialect
}

// Open opens (and migrates) a SQLite database at dsn.
func Open(dsn string) (*Store, error) { return OpenDriver(DriverSQLite, dsn) }

// OpenDriver opens (and migrates) a database. For "mysql" the DSN is a
// go-sql-driver DSN such as "user:pass@tcp(127.0.0.1:3306)/galdav"; the
// database must already exist.
func OpenDriver(driver, dsn string) (*Store, error) {
	var s *Store
	switch driver {
	case DriverSQLite, "":
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		// SQLite allows one writer; a single connection avoids SQLITE_BUSY
		// and keeps :memory: databases coherent.
		db.SetMaxOpenConns(1)
		for _, p := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL", "PRAGMA busy_timeout = 5000"} {
			if _, err := db.Exec(p); err != nil {
				db.Close()
				return nil, err
			}
		}
		s = &Store{DB: db, d: sqliteDialect}
	case DriverMySQL:
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			return nil, fmt.Errorf("mysql dsn: %w", err)
		}
		// Case-sensitive comparisons, like SQLite; strict handling of oversized values.
		cfg.Collation = "utf8mb4_bin"
		cfg.MultiStatements = false
		cfg.ParseTime = false
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, err
		}
		db := sql.OpenDB(connector)
		db.SetMaxOpenConns(20)
		db.SetConnMaxLifetime(0)
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, fmt.Errorf("mysql: %w", err)
		}
		s = &Store{DB: db, d: mysqlDialect}
	default:
		return nil, fmt.Errorf("unknown database driver %q (want %q or %q)", driver, DriverSQLite, DriverMySQL)
	}
	if err := s.migrate(); err != nil {
		s.DB.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name VARCHAR(255) NOT NULL PRIMARY KEY)`); err != nil {
		return err
	}
	dir := "migrations/" + s.d.name
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
