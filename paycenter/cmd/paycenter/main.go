// Command paycenter is the payment center between the VPN system and
// 信联pay: it serves the VPN "下单链接" protocol (checkout link, order query,
// paid notification), talks to 信联pay, and has an admin panel.
//
// Configuration (flags override environment variables):
//
//	-listen      PAYCENTER_LISTEN          default :8090
//	-db-driver   PAYCENTER_DB_DRIVER       sqlite | mysql, default sqlite
//	-db-dsn      PAYCENTER_DB_DSN          default data/paycenter.db
//	             (mysql: user:pass@tcp(host:3306)/paycenter?charset=utf8mb4&parseTime=true&loc=Local)
//	-public-url  PAYCENTER_PUBLIC_URL      external base URL; saved on first start, then edited in the admin panel
//	-trust-proxy PAYCENTER_TRUST_PROXY     1 = read the client IP from X-Real-IP / X-Forwarded-For
//	             PAYCENTER_ADMIN_USER      first-start admin user (default admin)
//	             PAYCENTER_ADMIN_PASSWORD  first-start admin password (random and logged if empty)
//
//	paycenter reset-password <new password>   reset the admin password
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
	"strings"
	"syscall"
	"time"

	"paycenter/internal/service"
	"paycenter/internal/store"
	"paycenter/internal/web"
)

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	listen := flag.String("listen", env("PAYCENTER_LISTEN", ":8090"), "listen address")
	driver := flag.String("db-driver", env("PAYCENTER_DB_DRIVER", "sqlite"), "sqlite | mysql")
	dsn := flag.String("db-dsn", env("PAYCENTER_DB_DSN", "data/paycenter.db"), "database DSN")
	publicURL := flag.String("public-url", env("PAYCENTER_PUBLIC_URL", ""), "external base URL, e.g. https://pay.example.com")
	trustProxy := flag.Bool("trust-proxy", env("PAYCENTER_TRUST_PROXY", "") == "1", "trust X-Real-IP / X-Forwarded-For")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	db, err := store.Open(*driver, *dsn)
	if err != nil {
		slog.Error("open database", "driver", *driver, "err", err)
		os.Exit(1)
	}

	if flag.Arg(0) == "reset-password" {
		if err := web.SetAdminPassword(db, flag.Arg(1)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("管理员密码已重置")
		return
	}

	created, pw, err := web.EnsureAdmin(db, env("PAYCENTER_ADMIN_USER", "admin"), os.Getenv("PAYCENTER_ADMIN_PASSWORD"))
	if err != nil {
		slog.Error("init admin", "err", err)
		os.Exit(1)
	}
	if created && os.Getenv("PAYCENTER_ADMIN_PASSWORD") == "" {
		slog.Warn("已创建管理员账号，请登录后立即修改密码", "user", env("PAYCENTER_ADMIN_USER", "admin"), "password", pw)
	}
	if u := strings.TrimRight(strings.TrimSpace(*publicURL), "/"); u != "" && store.GetSetting(db, service.SettingPublicURL) == "" {
		if err := store.SetSetting(db, service.SettingPublicURL, u); err != nil {
			slog.Error("save public url", "err", err)
		}
	}

	svc := service.New(db)
	srv, err := web.New(svc, *trustProxy)
	if err != nil {
		slog.Error("init web", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	svc.Run(ctx)

	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		slog.Info("paycenter listening", "addr", *listen, "db", db.Dialector.Name(), "public_url", svc.PublicURL())
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	slog.Info("paycenter stopped")
}
