// Package server wires the stores and handlers together and runs the HTTP
// listeners.
package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bitflipp/whitegopher/internal/admin"
	"github.com/bitflipp/whitegopher/internal/auth"
	"github.com/bitflipp/whitegopher/internal/config"
	"github.com/bitflipp/whitegopher/internal/dav"
	"github.com/bitflipp/whitegopher/internal/netx"
	"github.com/bitflipp/whitegopher/internal/obs"
	"github.com/bitflipp/whitegopher/internal/sched"
	"github.com/bitflipp/whitegopher/internal/sharing"
	"github.com/bitflipp/whitegopher/internal/store"
)

// Run serves DAV, the admin UI and metrics until SIGINT/SIGTERM or a listener
// fails. version is reported on the metrics endpoint.
func Run(cfg config.Config, st *store.Store, version string) error {
	basePath := strings.TrimRight(cfg.BasePath, "/")
	proxies, err := cfg.TrustedProxies()
	if err != nil {
		return err
	}
	limiter := netx.NewLimiter(cfg.LoginLimit.Failures, cfg.LoginLimit.Window)
	authn := &auth.Authenticator{Store: st, Realm: cfg.Realm, Proxies: netx.Proxies(proxies), Limiter: limiter}
	var mailer sched.Mailer
	var textMailer admin.TextMailer
	if cfg.SMTP.Host != "" {
		smtp := &sched.SMTP{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Username: cfg.SMTP.Username, Password: cfg.SMTP.Password, From: cfg.SMTP.From}
		mailer, textMailer = smtp, smtp
	}
	shares := &sharing.Service{Store: st, BasePath: basePath, RequireAccept: cfg.Sharing.RequireAccept}
	schedSvc := sched.New(st, basePath, mailer)
	schedSvc.HideAttendeesDefault, schedSvc.HideAttendeesForce = cfg.Scheduling.HideAttendees, cfg.Scheduling.ForceHideAttendees
	davSrv := &dav.Server{Store: st, Auth: authn, BasePath: basePath, Sharing: shares, MaxBody: cfg.Limits.MaxRequestMiB << 20, DefaultQuota: cfg.Limits.DefaultQuotaMiB << 20, Sched: schedSvc}

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
		ui, err := (&admin.Server{Store: st, Auth: authn, DAVPath: basePath, TrustedProxies: netx.Proxies(proxies), Limiter: limiter, Mailer: textMailer, BaseURL: cfg.Admin.BaseURL, Invites: cfg.Admin.Invites, Sharing: shares,
			HideAttendeesDefault: cfg.Scheduling.HideAttendees, HideAttendeesForce: cfg.Scheduling.ForceHideAttendees, LegalNotice: cfg.Admin.LegalNotice, PrivacyPolicy: cfg.Admin.PrivacyPolicy}).Handler()
		if err != nil {
			return err
		}
		mux.Handle("/admin/", ui)
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		})
	}
	metrics := obs.New()
	var accessLog *slog.Logger
	if cfg.Log.Access {
		accessLog = slog.Default()
	}
	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           obs.Middleware(metrics, accessLog, netx.Proxies(proxies).ClientIP, mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	// Shut down gracefully on SIGINT/SIGTERM so in-flight writes finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if days := cfg.Scheduling.InboxRetentionDays; days > 0 {
		go pruneInbox(ctx, st, days)
	}
	if sc := cfg.Sharing; sc.PendingExpiryDays > 0 || sc.DeclinedRetentionDays > 0 || sc.NotificationRetentionDays > 0 {
		go maintainShares(ctx, st, shares, sc)
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	if cfg.Metrics.Listen != "" {
		mm := http.NewServeMux()
		mm.Handle("GET /metrics", metrics.Handler(st.DB, version))
		ms := &http.Server{Addr: cfg.Metrics.Listen, Handler: mm, ReadHeaderTimeout: 10 * time.Second}
		go func() { errc <- ms.ListenAndServe() }()
		defer ms.Close()
		slog.Info("metrics", "addr", cfg.Metrics.Listen)
	}
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

// maintainShares expires and cleans up share invitations and their
// notifications at startup and daily.
func maintainShares(ctx context.Context, st *store.Store, shares *sharing.Service, sc config.Sharing) {
	for {
		if days := sc.PendingExpiryDays; days > 0 {
			if n, err := shares.ExpirePending(days); err != nil {
				slog.Error("expire pending shares", "err", err)
			} else if n > 0 {
				slog.Info("withdrew unanswered share invitations", "invitations", n, "older_than_days", days)
			}
		}
		if days := sc.DeclinedRetentionDays; days > 0 {
			if n, err := shares.RemoveFinished(days); err != nil {
				slog.Error("remove finished shares", "err", err)
			} else if n > 0 {
				slog.Info("removed declined and expired shares", "shares", n, "older_than_days", days)
			}
		}
		if days := sc.NotificationRetentionDays; days > 0 {
			if n, err := st.PruneNotifications(days); err != nil {
				slog.Error("prune notifications", "err", err)
			} else if n > 0 {
				slog.Info("pruned sharing notifications", "items", n, "older_than_days", days)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

// pruneInbox removes old scheduling-inbox items at startup and daily.
func pruneInbox(ctx context.Context, st *store.Store, days int) {
	for {
		if n, err := st.PruneInbox(days); err != nil {
			slog.Error("prune inbox", "err", err)
		} else if n > 0 {
			slog.Info("pruned scheduling inbox", "items", n, "older_than_days", days)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
