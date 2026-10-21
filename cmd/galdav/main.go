// Command galdav is a CalDAV/CardDAV server.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bitflipp/galdav/internal/admin"
	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/config"
	"github.com/bitflipp/galdav/internal/dav"
	"github.com/bitflipp/galdav/internal/importer"
	"github.com/bitflipp/galdav/internal/sched"
	"github.com/bitflipp/galdav/internal/store"
	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/term"
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
		fmt.Fprintln(os.Stderr, "usage: galdav [-config file] <serve|useradd [-admin] [-email ADDRESS] NAME|import-baikal [-dry-run] SQLITE_FILE|-mysql DSN>")
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
	var mailer sched.Mailer
	if cfg.SMTP.Host != "" {
		mailer = &sched.SMTP{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Username: cfg.SMTP.Username, Password: cfg.SMTP.Password, From: cfg.SMTP.From}
	}
	davSrv := &dav.Server{Store: st, Auth: authn, BasePath: basePath, Sched: sched.New(st, basePath, mailer)}

	mux := http.NewServeMux()
	mux.Handle("/", davSrv.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := st.DB.PingContext(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, "ok\n")
	})
	if cfg.Admin.Enabled {
		proxies, err := cfg.TrustedProxies()
		if err != nil {
			return err
		}
		ui, err := (&admin.Server{Store: st, Auth: authn, DAVPath: basePath, TrustedProxies: proxies}).Handler()
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
		IdleTimeout:       2 * time.Minute,
	}
	// Shut down gracefully on SIGINT/SIGTERM so in-flight writes finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	slog.Info("listening", "addr", cfg.Listen, "admin", cfg.Admin.Enabled)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return hs.Shutdown(shutdownCtx)
	}
}

func useradd(cfg config.Config, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("useradd", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "make the user an administrator")
	email := fs.String("email", "", "email address (used to match invitation attendees)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: useradd [-admin] [-email ADDRESS] NAME (password is prompted for, or read from stdin)")
	}
	name := fs.Arg(0)
	if !auth.ValidUsername(name) {
		return fmt.Errorf("invalid username %q: use letters, digits and . _ @ - (max 64)", name)
	}
	pw, err := readPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = st.CreateUser(store.User{
		Username:     name,
		Email:        *email,
		PasswordHash: hash,
		DigestHA1:    auth.HA1(name, cfg.Realm, pw),
		IsAdmin:      *admin,
	})
	if errors.Is(err, store.ErrExists) {
		return fmt.Errorf("user %q already exists", name)
	}
	return err
}

// readPassword prompts (without echo, twice) on a terminal, or reads one
// line from a pipe.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	var pw string
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(os.Stderr, "Repeat password: ")
		b2, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(b) != string(b2) {
			return "", errors.New("passwords do not match")
		}
		pw = string(b)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if len(pw) < 8 {
		return "", errors.New("password must be at least 8 characters")
	}
	return pw, nil
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
