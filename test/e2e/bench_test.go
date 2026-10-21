package e2e

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestLatencyComparison compares direct (one-shot) and daemon mode over 20
// sequential executes. It is skipped unless MCLI_BENCH=1.
//
// Against the in-process fake server it measures client overhead only. Set
// MCLI_BENCH_URL and MCLI_BENCH_API_KEY to measure a real MetaMCP endpoint,
// where the per-session server cost (fresh server instance, upstream binding)
// shows up in direct mode.
func TestLatencyComparison(t *testing.T) {
	if os.Getenv("MCLI_BENCH") != "1" {
		t.Skip("set MCLI_BENCH=1 to run")
	}
	e := newEnv(t)
	if url := os.Getenv("MCLI_BENCH_URL"); url != "" {
		e.vars = append(e.vars, "MCLI_URL="+url, "MCLI_API_KEY="+os.Getenv("MCLI_BENCH_API_KEY"))
	}
	const n = 20
	measure := func(args ...string) []time.Duration {
		var ds []time.Duration
		for i := 0; i < n; i++ {
			cmd := exec.Command(e.bin, args...)
			cmd.Env = append(os.Environ(), e.vars...)
			cmd.Stdin = strings.NewReader("return 1;")
			start := time.Now()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
			ds = append(ds, time.Since(start))
		}
		return ds
	}
	report := func(name string, ds []time.Duration) {
		var total time.Duration
		for _, d := range ds {
			total += d
		}
		s := slices.Clone(ds)
		slices.Sort(s)
		t.Logf("%-8s total=%v p50=%v p95=%v first=%v", name, total.Round(time.Millisecond),
			s[len(s)/2].Round(time.Millisecond), s[len(s)*95/100].Round(time.Millisecond), ds[0].Round(time.Millisecond))
	}
	report("direct", measure("--no-daemon", "execute", "-"))
	report("daemon", measure("execute", "-"))
}
