// Command whitegopher is a CalDAV/CardDAV server.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/Top-Ranger/whitegopher/internal/buildinfo"
	"github.com/Top-Ranger/whitegopher/internal/config"
	"github.com/Top-Ranger/whitegopher/internal/server"
	"github.com/Top-Ranger/whitegopher/internal/store"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = ""

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "whitegopher:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("whitegopher", flag.ContinueOnError)
	cfgPath := fs.String("config", "config.yaml", "path to config file")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: whitegopher [-config file] <serve|version|imip-receive|useradd [-admin] [-email ADDRESS] NAME|import [-dry-run] DIR>")
	}
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion || (fs.NArg() == 1 && fs.Arg(0) == "version") {
		fmt.Println("whitegopher", buildinfo.Version(version), runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
		return nil
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
	setupLogging(cfg.Log)
	st, err := store.Open(cfg.DB.DSN)
	if err != nil {
		return err
	}
	defer st.Close()

	switch fs.Arg(0) {
	case "serve":
		return server.Run(cfg, st, buildinfo.Version(version))
	case "import":
		return importTree(st, fs.Args()[1:])
	case "useradd":
		return useradd(cfg, st, fs.Args()[1:])
	case "imip-receive":
		return imipReceive(cfg, st, fs.Args()[1:])
	}
	return fmt.Errorf("unknown command %q", fs.Arg(0))
}

func setupLogging(c config.Log) {
	var h slog.Handler
	if c.Format == "json" {
		h = slog.NewJSONHandler(os.Stderr, nil)
	} else {
		h = slog.NewTextHandler(os.Stderr, nil)
	}
	slog.SetDefault(slog.New(h))
}
