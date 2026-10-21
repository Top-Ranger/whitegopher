// Package storetest opens throwaway stores for tests.
//
// By default it returns an in-memory SQLite store. When
// GALDAV_TEST_MYSQL_DSN is set (a go-sql-driver DSN with rights to create
// databases, e.g. "root@tcp(127.0.0.1:3306)/"), every call creates a fresh
// MySQL/MariaDB database instead and drops it on cleanup, so the whole test
// suite can run against MySQL.
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

// DSNEnv names the environment variable that selects MySQL.
const DSNEnv = "GALDAV_TEST_MYSQL_DSN"

// New returns a fresh store that is closed when the test ends.
func New(t testing.TB) *store.Store {
	t.Helper()
	base := os.Getenv(DSNEnv)
	if base == "" {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
	return newMySQL(t, base)
}

// MySQLDSN returns a DSN for a fresh empty database, or skips the test when
// MySQL testing is not configured.
func MySQLDSN(t testing.TB) string {
	t.Helper()
	base := os.Getenv(DSNEnv)
	if base == "" {
		t.Skipf("set %s to run MySQL tests", DSNEnv)
	}
	return freshDatabase(t, base)
}

func newMySQL(t testing.TB, base string) *store.Store {
	t.Helper()
	st, err := store.OpenDriver(store.DriverMySQL, freshDatabase(t, base))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
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
