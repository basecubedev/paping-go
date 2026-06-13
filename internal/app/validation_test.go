package app

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestValidatePort(t *testing.T) {
	tests := []struct {
		port    int
		wantErr bool
	}{
		{-1, true},
		{0, true},
		{1, false},
		{80, false},
		{65535, false},
		{65536, true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.port), func(t *testing.T) {
			err := validatePort(tt.port)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateTimeout(t *testing.T) {
	tests := []struct {
		timeout int
		wantErr bool
	}{
		{-1, true},
		{0, true},
		{1, false},
		{1000, false},
		{600000, false},
		{600001, true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.timeout), func(t *testing.T) {
			err := validateTimeout(tt.timeout)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFlagWasSet(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	count := fs.Int("c", -1, "")
	port := fs.Int("p", 0, "")
	if err := fs.Parse([]string{"-c", "3"}); err != nil {
		t.Fatal(err)
	}
	if !flagWasSet(fs, "c") {
		t.Fatal("expected -c to be set")
	}
	if flagWasSet(fs, "p") {
		t.Fatal("did not expect -p to be set")
	}
	if *count != 3 || *port != 0 {
		t.Fatalf("parsed values = count %d port %d", *count, *port)
	}

	defaults := flag.NewFlagSet("defaults", flag.ContinueOnError)
	defaults.Int("c", -1, "")
	if err := defaults.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if flagWasSet(defaults, "c") {
		t.Fatal("default -c should not count as set")
	}
}

func TestCol(t *testing.T) {
	old := useColor
	t.Cleanup(func() { useColor = old })

	useColor = false
	if got := col(colorRed, "text"); got != "text" {
		t.Fatalf("col without color = %q", got)
	}

	useColor = true
	got := col(colorRed, "text")
	if !strings.HasPrefix(got, colorRed) || !strings.Contains(got, "text") || !strings.HasSuffix(got, colorReset) {
		t.Fatalf("col with color = %q", got)
	}
}

func TestStatsPrintTo(t *testing.T) {
	old := useColor
	useColor = false
	t.Cleanup(func() { useColor = old })

	tests := []struct {
		name string
		st   stats
		want []string
	}{
		{
			name: "no attempts",
			st:   stats{},
			want: []string{"Attempted = 0, Connected = 0, Failed = 0 (0.00%)", "Average = 0.00ms"},
		},
		{
			name: "only success",
			st:   stats{attempts: 2, connects: 2, min: 10, max: 20, total: 30},
			want: []string{"Attempted = 2, Connected = 2, Failed = 0 (0.00%)", "Minimum = 10.00ms, Maximum = 20.00ms, Average = 15.00ms"},
		},
		{
			name: "only failure",
			st:   stats{attempts: 2, failures: 2},
			want: []string{"Attempted = 2, Connected = 0, Failed = 2 (100.00%)", "Average = 0.00ms"},
		},
		{
			name: "mixed",
			st:   stats{attempts: 2, connects: 1, failures: 1, min: 5, max: 5, total: 5},
			want: []string{"Attempted = 2, Connected = 1, Failed = 1 (50.00%)", "Average = 5.00ms"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tt.st.printTo(&buf)
			got := buf.String()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("stats output missing %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestFillCheckDepsProvidesDefaults(t *testing.T) {
	deps := fillCheckDeps(checkDeps{})
	if deps.stdout == nil ||
		deps.stderr == nil ||
		deps.lookupIPAddr == nil ||
		deps.dialTarget == nil ||
		deps.now == nil ||
		deps.after == nil ||
		deps.createFile == nil ||
		deps.enableConsoleColors == nil ||
		deps.setupInterrupt == nil {
		t.Fatalf("fillCheckDeps did not populate all defaults: %#v", deps)
	}
}

func TestFillCheckDepsPreservesExplicitValues(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	deps := checkDeps{
		stdout: stdout,
		stderr: stderr,
		lookupIPAddr: func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return nil, nil
		},
		dialTarget: func(network, address string, timeout time.Duration) (float64, error) {
			return 0, nil
		},
		now: func() time.Time {
			return time.Unix(1, 0)
		},
		after: func(d time.Duration) <-chan time.Time {
			return make(chan time.Time)
		},
		createFile: func(name string, mode os.FileMode) (io.WriteCloser, error) {
			return nil, nil
		},
		enableConsoleColors: func() {},
		setupInterrupt: func() (<-chan os.Signal, func()) {
			return nil, nil
		},
	}
	got := fillCheckDeps(deps)
	if got.stdout != stdout || got.stderr != stderr {
		t.Fatal("explicit writers were not preserved")
	}
}

func TestMedianBoundaries(t *testing.T) {
	tests := []struct {
		name string
		in   []float64
		want float64
	}{
		{"empty", nil, 0},
		{"one", []float64{3}, 3},
		{"two", []float64{3, 5}, 4},
		{"odd", []float64{1, 3, 9}, 3},
		{"even", []float64{1, 3, 9, 11}, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := median(tt.in); got != tt.want {
				t.Fatalf("median(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestPercentileNearestRankBoundaries(t *testing.T) {
	values := []float64{10, 20, 30, 40, 50}
	tests := []struct {
		percentile float64
		want       float64
	}{
		{0, 10},
		{1, 10},
		{50, 30},
		{95, 50},
		{99, 50},
		{100, 50},
		{200, 50},
	}
	if got := percentileNearestRank(nil, 95); got != 0 {
		t.Fatalf("empty percentile = %v, want 0", got)
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("p%.0f", tt.percentile), func(t *testing.T) {
			if got := percentileNearestRank(values, tt.percentile); got != tt.want {
				t.Fatalf("percentile = %v, want %v", got, tt.want)
			}
		})
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}
