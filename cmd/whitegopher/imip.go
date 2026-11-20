package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Top-Ranger/whitegopher/internal/config"
	"github.com/Top-Ranger/whitegopher/internal/sched"
	"github.com/Top-Ranger/whitegopher/internal/store"
)

// imipReceive processes one iMIP email (read from FILE or stdin), for use as
// a mail-server pipe/alias target.
func imipReceive(cfg config.Config, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("imip-receive", flag.ContinueOnError)
	insecure := fs.Bool("no-auth-check", false, "accept the message without an Authentication-Results check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var in io.Reader = os.Stdin
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: imip-receive [-no-auth-check] [FILE]")
	}
	if fs.NArg() == 1 {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	svc := sched.New(st, strings.TrimRight(cfg.BasePath, "/"), nil)
	res, err := svc.ReceiveMail(io.LimitReader(in, 8<<20), cfg.Scheduling.IMIPRequireAuth && !*insecure)
	switch {
	case errors.Is(err, sched.ErrNotITIP):
		// Not an invitation or reply: nothing for us to do, and not an error
		// worth bouncing the mail for.
		slog.Info("imip: no calendar part, ignored")
		return nil
	case err != nil:
		return err
	case res.Ignored != "":
		slog.Warn("imip: ignored", "reason", res.Ignored, "method", res.Method, "uid", res.UID, "from", res.Sender)
	default:
		slog.Info("imip: delivered", "method", res.Method, "uid", res.UID, "from", res.Sender, "users", res.Delivered)
	}
	return nil
}
