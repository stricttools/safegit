//go:build linux

package procutil

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/testutil"
)

func TestParseStartTicksOrdinaryComm(t *testing.T) {
	line := "1234 (bash) S 1 1234 1234 0 -1 4194304 6766 33040 0 2 2 1 8 10 20 0 1 0 736936472 238305280 991 18446744073709551615\n"
	ticks, err := parseStartTicks(line)
	if err != nil {
		t.Fatalf("parseStartTicks: %v", err)
	}
	if ticks != 736936472 {
		t.Errorf("ticks = %d, want 736936472", ticks)
	}
}

// TestParseStartTicksPathologicalComm pins the parsing trap: the kernel does
// not escape the comm field, so a process name containing spaces and
// parentheses shifts every field for a naive whitespace split.
func TestParseStartTicksPathologicalComm(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{
			name: "spaces and parens",
			line: "95175 (sg (weird) name) S 95172 95172 94906 0 -1 4194304 133 0 0 0 0 0 0 0 20 0 1 0 736936933 235884544 498 0",
		},
		{
			name: "trailing paren in comm",
			line: "95175 (weird)) S 95172 95172 94906 0 -1 4194304 133 0 0 0 0 0 0 0 20 0 1 0 736936933 235884544 498 0",
		},
		{
			name: "digits in comm",
			line: "95175 (12345 67890) S 95172 95172 94906 0 -1 4194304 133 0 0 0 0 0 0 0 20 0 1 0 736936933 235884544 498 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticks, err := parseStartTicks(tc.line)
			if err != nil {
				t.Fatalf("parseStartTicks: %v", err)
			}
			if ticks != 736936933 {
				t.Errorf("ticks = %d, want 736936933", ticks)
			}
		})
	}
}

func TestParseStartTicksRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"no closing paren": "1234 (bash S 1 1234",
		"too few fields":   "1234 (bash) S 1 1234 1234 0 -1",
		"non-numeric":      "1234 (bash) S 1 1234 1234 0 -1 4194304 6766 33040 0 2 2 1 8 10 20 0 1 0 notanumber 238305280",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseStartTicks(line); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

// TestStartTimeSelf reads the running test process's own start time from the
// real /proc file: it must be stable across reads and land in the past.
func TestStartTimeSelf(t *testing.T) {
	first, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime: %v", err)
	}
	if first.Ticks == 0 {
		t.Error("ticks = 0, want the process's start offset since boot")
	}
	second, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime (second read): %v", err)
	}
	if first.Ticks != second.Ticks {
		t.Errorf("ticks changed between reads: %d then %d", first.Ticks, second.Ticks)
	}
	if first.Wall.IsZero() {
		t.Fatal("Wall is zero; boot time conversion failed")
	}
	if age := time.Since(first.Wall); age < 0 || age > 365*24*time.Hour {
		t.Errorf("Wall = %v, which is %v ago -- implausible for the running test", first.Wall, age)
	}
}

// TestWallFromTicksSurvivesLongUptime pins the tick-to-wall-clock conversion
// against int64 overflow. Multiplying the tick count by time.Second before
// dividing by userHZ wraps once the count passes about 9.22e9 -- roughly 2.9
// years of host uptime -- and reports an instant that is not merely imprecise
// but wildly wrong, often before boot.
func TestWallFromTicksSurvivesLongUptime(t *testing.T) {
	boot := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	const year = 365 * 24 * 3600

	cases := []struct {
		name   string
		ticks  uint64
		uptime time.Duration
	}{
		{"zero", 0, 0},
		{"one second", userHZ, time.Second},
		{"sub-second remainder", 250, 2500 * time.Millisecond},
		{"just under the overflow point", 9_000_000_000, 90_000_000 * time.Second},
		{"three years of uptime", 3 * year * userHZ, 3 * year * time.Second},
		{"ten years of uptime", 10 * year * userHZ, 10 * year * time.Second},
		{"a century of uptime", 100 * year * userHZ, 100 * year * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wallFromTicks(boot, tc.ticks)
			want := boot.Add(tc.uptime)
			if !got.Equal(want) {
				t.Errorf("wallFromTicks(boot, %d) = %v, want %v", tc.ticks, got, want)
			}
			if got.Before(boot) {
				t.Errorf("wallFromTicks(boot, %d) = %v, which precedes boot %v -- the arithmetic wrapped", tc.ticks, got, boot)
			}
		})
	}
}

func TestStartTimeMissingProcess(t *testing.T) {
	_, err := StartTime(999999999)
	if !errors.Is(err, ErrNoStartTime) {
		t.Errorf("err = %v, want ErrNoStartTime", err)
	}
}

// TestStartTimeRealProcessWithPathologicalName exercises the parser against a
// real /proc file whose comm contains spaces and parentheses.
func TestStartTimeRealProcessWithPathologicalName(t *testing.T) {
	proc := testutil.SpawnPathologicalNameSleeper(t)
	if proc.Comm != testutil.PathologicalCommName {
		t.Fatalf("comm = %q, want %q -- the test is no longer exercising the trap", proc.Comm, testutil.PathologicalCommName)
	}
	got, err := StartTime(proc.Pid)
	if err != nil {
		t.Fatalf("StartTime for pid %d (comm %q): %v", proc.Pid, proc.Comm, err)
	}
	if got.Ticks == 0 {
		t.Error("ticks = 0 for a live process")
	}
}
