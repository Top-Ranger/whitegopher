// Package storetest opens throwaway stores for tests.
//
// Tests need a MySQL/MariaDB server: set GALDAV_TEST_MYSQL_DSN to a
// go-sql-driver DSN with rights to create and drop databases, e.g.
// "root@tcp(127.0.0.1:3306)/". Each call creates a fresh database and drops
// it on cleanup. Without the variable, tests that need a store are skipped.
package storetest

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/bitflipp/galdav/internal/store"
)

// DSNEnv names the environment variable that selects the test server.
const DSNEnv = "GALDAV_TEST_MYSQL_DSN"

// New returns a fresh store that is closed when the test ends.
func New(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// DSN returns a DSN for a fresh empty database, or skips the test when no
// server is configured.
func DSN(t testing.TB) string {
	t.Helper()
	base := os.Getenv(DSNEnv)
	if base == "" {
		t.Skipf("set %s to run tests that need a database", DSNEnv)
	}
	return freshDatabase(t, base)
}

func freshDatabase(t testing.TB, base string) string {
	t.Helper()
	cfg, err := mysql.ParseDSN(base)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("galdav_test_%08x", rand.Uint32())
	if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec("DROP DATABASE " + name)
		admin.Close()
	})
	cfg.DBName = name
	return cfg.FormatDSN()
}
