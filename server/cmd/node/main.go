// Command node runs a proxy node: embedded Xray-core (VLESS + REALITY) whose
// users are pulled from the API server.
//
//	node -config node.json    # run
//	node keygen               # generate REALITY keys + node token
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/node"
)

func main() {
	cfgPath := flag.String("config", "node.json", "path to config file")
	flag.Parse()

	if flag.Arg(0) == "keygen" {
		keygen()
		return
	}

	cfg, err := config.LoadNode(*cfgPath)
	if err != nil {
		fatal("load config", err)
	}
	x, err := node.StartXray(cfg)
	if err != nil {
		fatal("xray", err)
	}
	defer x.Close()
	slog.Info("node started", "port", cfg.VisionPort, "xhttp", cfg.XHTTPPath != "")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	(&node.Syncer{
		APIURL:   cfg.APIURL,
		Token:    cfg.Token,
		Interval: time.Duration(cfg.SyncIntervalSeconds) * time.Second,
		Xray:     x,
	}).Run(ctx)
	slog.Info("shutting down")
}

func keygen() {
	priv, pub, sid, err := node.RealityKeys()
	if err != nil {
		fatal("keygen", err)
	}
	tok, err := node.RandomToken(32)
	if err != nil {
		fatal("keygen", err)
	}
	fmt.Printf(`# node.json (keep private_key secret)
"token": %q,
"reality": { "private_key": %q, "short_ids": [%q], ... }

# api.json -> nodes[] entry for this node
"token": %q,
"reality": { "public_key": %q, "short_id": %q, ... }
`, tok, priv, sid, tok, pub, sid)
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
