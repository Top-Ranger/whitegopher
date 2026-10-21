// Command galdav is a CalDAV/CardDAV server.
package main

import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/config"
	"github.com/bitflipp/galdav/internal/dav"
	"github.com/bitflipp/galdav/internal/importer"
	"github.com/bitflipp/galdav/internal/store"
	_ "modernc.org/sqlite"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "galdav:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("galdav", flag.ContinueOnError)
	cfgPath := fs.String("config", "config.yaml", "path to config file")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: galdav [-config file] <serve|useradd NAME|import-baikal [-dry-run] SQLITE_FILE>")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("missing command")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DB.DSN)
	if err != nil {
		return err
	}
	defer st.Close()

	switch fs.Arg(0) {
	case "serve":
		return serve(cfg, st)
	case "import-baikal":
		return importBaikal(st, fs.Args()[1:])
	case "useradd":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: useradd NAME (password read from stdin)")
		}
		return useradd(cfg, st, fs.Arg(1))
	}
	return fmt.Errorf("unknown command %q", fs.Arg(0))
}

func serve(cfg config.Config, st *store.Store) error {
	srv := &dav.Server{
		Store:    st,
		Auth:     &auth.Authenticator{Store: st, Realm: cfg.Realm},
		BasePath: strings.TrimRight(cfg.BasePath, "/"),
	}
	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("listening", "addr", cfg.Listen)
	return hs.ListenAndServe()
}

func useradd(cfg config.Config, st *store.Store, name string) error {
	fmt.Fprint(os.Stderr, "password: ")
	pw, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && pw == "" {
		return err
	}
	pw = strings.TrimRight(pw, "\r\n")
	if pw == "" {
		return fmt.Errorf("empty password")
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = st.CreateUser(store.User{
		Username:     name,
		PasswordHash: hash,
		DigestHA1:    auth.HA1(name, cfg.Realm, pw),
	})
	return err
}

func importBaikal(st *store.Store, args []string) error {
	fs := flag.NewFlagSet("import-baikal", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "read and validate only; write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: import-baikal [-dry-run] SQLITE_FILE")
	}
	path := fs.Arg(0)
	if _, err := os.Stat(path); err != nil {
		return err
	}
	src, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
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
