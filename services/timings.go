package services

import (
	"fmt"
	"os"
	"time"
)

// phaseTimer logs FullScan phase walls when timings env is set, else no-ops.
func phaseTimer() func(string) {
	if os.Getenv("SRCINSIGHT_RESOLVE_TIMINGS") != "1" {
		return func(string) {}
	}
	start := time.Now()
	last := start
	return func(name string) {
		now := time.Now()
		fmt.Fprintf(os.Stderr, "[phase] %s wall=%s (total %s)\n", name, now.Sub(last).Round(time.Millisecond), now.Sub(start).Round(time.Millisecond))
		last = now
	}
}
