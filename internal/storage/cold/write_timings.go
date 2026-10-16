package cold

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

var writeTimingsOn = sync.OnceValue(func() bool {
	return strings.TrimSpace(os.Getenv("SRCINSIGHT_WRITE_TIMINGS")) == "1"
})

type writeStage struct {
	build time.Duration
	exec  time.Duration
	calls int
}

type writeTimings struct {
	mu     sync.Mutex
	stages map[string]*writeStage
}

func (v *Vault) wtAdd(stage string, build, exec time.Duration, calls int) {
	if v == nil || !writeTimingsOn() {
		return
	}
	if v.wt == nil {
		v.wt = &writeTimings{stages: make(map[string]*writeStage)}
	}
	v.wt.mu.Lock()
	defer v.wt.mu.Unlock()
	s, ok := v.wt.stages[stage]
	if !ok {
		s = &writeStage{}
		v.wt.stages[stage] = s
	}
	s.build += build
	s.exec += exec
	s.calls += calls
}

func (v *Vault) wtDump(tag string) {
	if v == nil || !writeTimingsOn() || v.wt == nil {
		return
	}
	v.wt.mu.Lock()
	stages := v.wt.stages
	v.wt.stages = make(map[string]*writeStage)
	v.wt.mu.Unlock()
	names := make([]string, 0, len(stages))
	var total time.Duration
	for n, s := range stages {
		names = append(names, n)
		total += s.build + s.exec
	}
	sort.Strings(names)
	var sb strings.Builder
	fmt.Fprintf(&sb, "[write] %s total=%s", tag, total.Round(time.Millisecond))
	for _, n := range names {
		s := stages[n]
		fmt.Fprintf(&sb, " %s={build=%s exec=%s n=%d}", n,
			s.build.Round(time.Millisecond), s.exec.Round(time.Millisecond), s.calls)
	}
	fmt.Fprintln(os.Stderr, sb.String())
}
