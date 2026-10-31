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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/keys"
	"vpnplatform/server/internal/memlimit"
	"vpnplatform/server/internal/node"
)

func main() {
	cfgPath := flag.String("config", "node.json", "path to config file")
	flag.Parse()

	if flag.Arg(0) == "keygen" {
		keygen()
		return
	}

	reexecWithoutSplice()

	memlimit.Set(0.5)
	cfg, err := config.LoadNode(*cfgPath)
	if err != nil {
		fatal("load config", err)
	}
	st := node.NewStats()
	x, err := node.StartXray(cfg, st)
	if err != nil {
		fatal("xray", err)
	}
	defer x.Close()
	slog.Info("node started", "port", cfg.VisionPort, "xhttp", cfg.XHTTPPath != "")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Fair sharing under load; the API tells us the node's bandwidth (0 = off).
	fair := &node.FairShare{Stats: st}
	go fair.Run(ctx)
	(&node.Syncer{
		APIURL:   cfg.APIURL,
		Token:    cfg.Token,
		Interval: time.Duration(cfg.SyncIntervalSeconds) * time.Second,
		Xray:     x,
		Stats:    st,
		Fair:     fair,
		// systemd StateDirectory=vpn-node sets $STATE_DIRECTORY.
		CacheFile: cacheFile(),
	}).Run(ctx)
	slog.Info("shutting down")
}

// On Linux, Xray's freedom outbound can hand a Vision connection to the
// kernel (splice), which bypasses the per-chunk counters, so speed limits
// would not apply and traffic would only be counted when the connection
// closes. Xray reads this switch in a package init(), so it must be in the
// environment before the process starts: set it and re-exec once.
func reexecWithoutSplice() {
	const key = "XRAY_BUF_SPLICE"
	if os.Getenv(key) != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		fatal("re-exec", err)
	}
	env := append(os.Environ(), key+"=disable")
	fatal("re-exec", syscall.Exec(exe, os.Args, env))
}

func keygen() {
	priv, pub, sid, err := keys.RealityKeys()
	if err != nil {
		fatal("keygen", err)
	}
	tok, err := keys.RandomToken(32)
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

// cacheFile is where the last user list is kept: the systemd state directory,
// or next to nothing (no cache) when run by hand without one.
func cacheFile() string {
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		return filepath.Join(strings.Split(d, ":")[0], "users.json")
	}
	return ""
}
