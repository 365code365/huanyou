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
	"syscall"
	"time"

	"vpnplatform/server/internal/api"
	"vpnplatform/server/internal/auth"
	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/store"
)

func main() {
	cfgPath := flag.String("config", "api.json", "path to config file")
	flag.Parse()

	cfg, err := config.LoadAPI(*cfgPath)
	if err != nil {
		fatal("load config", err)
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		fatal("open database", err)
	}
	defer st.Close()

	if flag.NArg() > 0 {
		if err := adminCmd(st, flag.Args()); err != nil {
			fatal("command failed", err)
		}
		return
	}

	issuer, err := auth.NewIssuer(cfg.JWTSecret, time.Duration(cfg.TokenTTLHours)*time.Hour)
	if err != nil {
		fatal("jwt", err)
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(cfg, st, issuer).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("api listening", "addr", cfg.Listen, "nodes", len(cfg.Nodes))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("serve", err)
	}
}

func adminCmd(st *store.Store, args []string) error {
	ctx := context.Background()
	if len(args) < 3 || args[0] != "user" {
		return errors.New("usage: user <disable|enable|rotate> <email> | user expire <email> <YYYY-MM-DD|never>")
	}
	email := args[2]
	switch args[1] {
	case "disable":
		return st.SetDisabled(ctx, email, true)
	case "enable":
		return st.SetDisabled(ctx, email, false)
	case "rotate":
		return st.RotateUUID(ctx, email)
	case "expire":
		if len(args) < 4 {
			return errors.New("usage: user expire <email> <YYYY-MM-DD|never>")
		}
		if args[3] == "never" {
			return st.SetExpiry(ctx, email, nil)
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

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
