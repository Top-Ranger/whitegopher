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

	"github.com/bitflipp/galdav/internal/admin"
	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/config"
	"github.com/bitflipp/galdav/internal/dav"
	"github.com/bitflipp/galdav/internal/importer"
	"github.com/bitflipp/galdav/internal/store"
	_ "github.com/go-sql-driver/mysql"
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
		fmt.Fprintln(os.Stderr, "usage: galdav [-config file] <serve|useradd [-admin] NAME|import-baikal [-dry-run] SQLITE_FILE|-mysql DSN>")
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
	if err := cfg.Validate(); err != nil {
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
		return useradd(cfg, st, fs.Args()[1:])
	}
	return fmt.Errorf("unknown command %q", fs.Arg(0))
}

func serve(cfg config.Config, st *store.Store) error {
	basePath := strings.TrimRight(cfg.BasePath, "/")
	authn := &auth.Authenticator{Store: st, Realm: cfg.Realm}
	davSrv := &dav.Server{Store: st, Auth: authn, BasePath: basePath}

	mux := http.NewServeMux()
	mux.Handle("/", davSrv.Handler())
	if cfg.Admin.Enabled {
		ui, err := (&admin.Server{Store: st, Auth: authn, DAVPath: basePath}).Handler()
		if err != nil {
			return err
		}
		mux.Handle("/admin/", ui)
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		})
	}
	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("listening", "addr", cfg.Listen, "admin", cfg.Admin.Enabled)
	return hs.ListenAndServe()
}

func useradd(cfg config.Config, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("useradd", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "make the user an administrator")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: useradd [-admin] NAME (password read from stdin)")
	}
	name := fs.Arg(0)
	if !auth.ValidUsername(name) {
		return fmt.Errorf("invalid username %q: use letters, digits and . _ @ - (max 64)", name)
	}
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
		IsAdmin:      *admin,
	})
	return err
}

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
