package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

	"github.com/bitflipp/whitegopher/internal/importer"
	"github.com/bitflipp/whitegopher/internal/store"
	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

func importBaikal(st *store.Store, args []string) error {
	fs := flag.NewFlagSet("import-baikal", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "read and validate only; write nothing")
	mysqlDSN := fs.String("mysql", "", "read from a Baikal MySQL/MariaDB database (go-sql-driver DSN, e.g. user:pass@tcp(host:3306)/baikal) instead of a SQLite file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var src *sql.DB
	var err error
	switch {
	case *mysqlDSN != "" && fs.NArg() == 0:
		src, err = sql.Open("mysql", *mysqlDSN)
		if err == nil {
			err = src.Ping()
		}
	case *mysqlDSN == "" && fs.NArg() == 1:
		if _, err = os.Stat(fs.Arg(0)); err == nil {
			src, err = sql.Open("sqlite", "file:"+fs.Arg(0)+"?mode=ro")
		}
	default:
		return fmt.Errorf("usage: import-baikal [-dry-run] (SQLITE_FILE | -mysql DSN)")
	}
	if err != nil {
		return err
	}
	defer src.Close()
	res, err := importer.ImportBaikal(src, st, importer.Options{DryRun: *dry})
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	fmt.Printf("users %d, calendars %d, events %d, address books %d, cards %d (dry-run: %v)\n",
		res.Users, res.Calendars, res.Events, res.AddressBooks, res.Cards, *dry)
	return err
}
