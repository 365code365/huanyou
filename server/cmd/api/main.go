// Command api is the control-plane server: accounts, login, node list and client profiles.
//
//	api -config api.json                         # serve
//	api -config api.json user disable a@b.com    # admin: disable / enable / rotate
//	api -config api.json user expire a@b.com 2026-12-31   # or "never"
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"vpnplatform/server/internal/api"
	"vpnplatform/server/internal/auth"
	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/monitor"
	"vpnplatform/server/internal/store"
)

func main() {
	cfgPath := flag.String("config", "api.json", "path to config file")
	flag.Parse()

	cfg, err := config.LoadAPI(*cfgPath)
	if err != nil {
		fatal("load config", err)
	}
	// A restore uploaded in the admin panel is staged next to the database
	// and swapped in here, before anything opens it.
	if applied, err := store.ApplyPendingRestore(cfg.Database); err != nil {
		fatal("apply restore", err)
	} else if applied {
		slog.Warn("restored database from backup (previous copy kept as .bak-pre-restore-*)")
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		fatal("open database", err)
	}
	defer st.Close()

	cat, err := loadCatalog(st, cfg)
	if err != nil {
		fatal("load catalog", err)
	}

	if flag.Arg(0) == "catalog" && flag.Arg(1) == "import" {
		seed := cfg.Catalog
		if err := seed.Normalize(); err != nil {
			fatal("catalog import", err)
		}
		if err := st.SaveCatalog(context.Background(), &seed); err != nil {
			fatal("catalog import", err)
		}
		fmt.Println("catalog imported from", *cfgPath, "- restart the API to apply")
		return
	}
	if flag.NArg() > 0 {
		if err := adminCmd(cat, st, flag.Args()); err != nil {
			fatal("command failed", err)
		}
		return
	}

	if err := st.EnsureRefCodes(context.Background()); err != nil {
		fatal("backfill invite codes", err)
	}

	issuer, err := auth.NewIssuer(cfg.JWTSecret, time.Duration(cfg.TokenTTLHours)*time.Hour)
	if err != nil {
		fatal("jwt", err)
	}
	live := config.NewLiveCatalog(cat)
	mon := monitor.New(live)
	apiServer, err := api.New(cfg, live, st, issuer, mon)
	if err != nil {
		fatal("payments", err)
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go mon.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if cfg.TLS.Domain != "" {
		serveTLS(ctx, srv, cfg)
		return
	}
	slog.Info("api listening", "addr", cfg.Listen, "nodes", len(cat.Nodes), "admin", cfg.Admin.Password != "")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("serve", err)
	}
}

// serveTLS terminates HTTPS on :443 with an automatic Let's Encrypt
// certificate for cfg.TLS.Domain; :80 answers ACME challenges and redirects
// everything else to HTTPS. Used by cloned deployments without nginx.
func serveTLS(ctx context.Context, srv *http.Server, cfg *config.API) {
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.TLS.Domain),
		Cache:      autocert.DirCache(cfg.TLS.CacheDir),
		Email:      cfg.TLS.Email,
	}
	srv.Addr = ":443"
	srv.TLSConfig = m.TLSConfig()
	redirect := &http.Server{
		Addr:              ":80",
		Handler:           m.HTTPHandler(nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := redirect.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http :80", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		_ = redirect.Close()
	}()
	slog.Info("api listening (tls)", "domain", cfg.TLS.Domain)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("serve tls", err)
	}
}

func adminCmd(cfg *config.Catalog, st *store.Store, args []string) error {
	ctx := context.Background()
	const usage = `usage:
  user disable|enable|rotate|reset-traffic <email>
  user plan <email> <plan_id> <days>      grant / extend a paid plan
  user expire <email> <YYYY-MM-DD>        set paid plan expiry`
	if len(args) < 3 || args[0] != "user" {
		return errors.New(usage)
	}
	email := args[2]
	switch args[1] {
	case "disable":
		return st.SetDisabled(ctx, email, true)
	case "enable":
		return st.SetDisabled(ctx, email, false)
	case "rotate":
		return st.RotateUUID(ctx, email)
	case "reset-traffic":
		return st.ResetTraffic(ctx, email)
	case "plan":
		if len(args) < 5 {
			return errors.New(usage)
		}
		p := cfg.Plan(args[3])
		if p == nil || p.ID == cfg.FreePlan {
			return fmt.Errorf("unknown paid plan %q", args[3])
		}
		days, err := strconv.Atoi(args[4])
		if err != nil || days <= 0 {
			return errors.New("days must be a positive integer")
		}
		return st.GrantPlan(ctx, email, p.ID, days)
	case "expire":
		if len(args) < 4 {
			return errors.New(usage)
		}
		t, err := time.ParseInLocation("2006-01-02", args[3], time.Local)
		if err != nil {
			return err
		}
		t = t.Add(24*time.Hour - time.Second) // end of that day
		return st.SetExpiry(ctx, email, &t)
	}
	return fmt.Errorf("unknown user command %q", args[1])
}

// loadCatalog returns the database copy of the catalog. On the first start
// (or after upgrading from a version without one) it is seeded from api.json.
// From then on the admin panel owns it: edits to plans / nodes / apps in
// api.json are ignored unless you run "catalog import".
func loadCatalog(st *store.Store, cfg *config.API) (*config.Catalog, error) {
	ctx := context.Background()
	c, err := st.LoadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if c == nil {
		seed := cfg.Catalog
		if err := seed.Normalize(); err != nil {
			return nil, err
		}
		if err := st.SaveCatalog(ctx, &seed); err != nil {
			return nil, err
		}
		slog.Info("catalog seeded from config file", "plans", len(seed.Plans), "nodes", len(seed.Nodes))
		return &seed, nil
	}
	if err := c.Normalize(); err != nil {
		return nil, fmt.Errorf("stored catalog invalid: %w", err)
	}
	return c, nil
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
