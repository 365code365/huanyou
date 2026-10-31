// Package memlimit gives the Go runtime a soft memory limit sized to the
// machine, so on a small VPS the garbage collector works harder as memory
// gets tight instead of the kernel's OOM killer ending the process.
package memlimit

import (
	"bufio"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// Set sets the limit to frac of the machine's RAM, unless GOMEMLIMIT is set
// (then the runtime already uses it). Linux only; elsewhere it does nothing.
func Set(frac float64) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	total := memTotal()
	if total <= 0 {
		return
	}
	limit := int64(float64(total) * frac)
	if limit < 64<<20 {
		limit = 64 << 20
	}
	debug.SetMemoryLimit(limit)
	slog.Debug("memory limit", "mb", limit>>20)
}

func memTotal() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			if err != nil {
				return 0
			}
			return kb << 10
		}
	}
	return 0
}
