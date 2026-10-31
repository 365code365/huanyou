package api

import (
	"log/slog"
	"os"
	"syscall"
	"time"
)

// restartSoon ends this process gracefully shortly after the current reply
// is sent: requests in flight finish, buffers are flushed and the database is
// closed cleanly (its WAL checkpointed) before systemd (Restart=always)
// starts it again. A plain os.Exit skipped all of that.
func restartSoon() {
	go func() {
		time.Sleep(800 * time.Millisecond)
		if p, err := os.FindProcess(os.Getpid()); err == nil && p.Signal(syscall.SIGTERM) == nil {
			return
		}
		slog.Warn("graceful restart unavailable, exiting")
		os.Exit(0)
	}()
}
