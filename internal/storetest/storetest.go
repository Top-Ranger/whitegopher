// Package storetest opens throwaway stores for tests.
//
// Tests need a MySQL/MariaDB server: set WHITEGOPHER_TEST_MYSQL_DSN to a
// go-sql-driver DSN with rights to create and drop databases, e.g.
// "root@tcp(127.0.0.1:3306)/". Each call creates a fresh database and drops
// it on cleanup. Without the variable such tests FAIL, so that a green run
// always means the database tests ran; set WHITEGOPHER_TEST_SKIP_DB=1 to skip
// them deliberately.
package storetest

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/Top-Ranger/whitegopher/internal/store"
)

// DSNEnv names the environment variable that selects the test server.
const DSNEnv = "WHITEGOPHER_TEST_MYSQL_DSN"

// SkipEnv, when set to 1, turns the missing-database failure into a skip.
const SkipEnv = "WHITEGOPHER_TEST_SKIP_DB"

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

// DSN returns a DSN for a fresh empty database. Without a configured server
// the test fails (or is skipped when SkipEnv is 1).
func DSN(t testing.TB) string {
	t.Helper()
	base := os.Getenv(DSNEnv)
	if base == "" {
		if os.Getenv(SkipEnv) == "1" {
			t.Skipf("%s=1: skipping test that needs a database", SkipEnv)
		}
		t.Fatalf("no test database: set %s to a MySQL/MariaDB DSN (or %s=1 to skip these tests)", DSNEnv, SkipEnv)
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
	name := fmt.Sprintf("whitegopher_test_%08x", rand.Uint32())
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
