package app

import (
	"encoding/csv"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestParseRate(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    float64
		wantErr bool
	}{
		{name: "minimum", value: "0.001", want: 0.001},
		{name: "normal", value: "2.5", want: 2.5},
		{name: "maximum", value: "1000", want: 1000},
		{name: "too small", value: "0.0009", wantErr: true},
		{name: "zero", value: "0", wantErr: true},
		{name: "too large", value: "1000.1", wantErr: true},
		{name: "not numeric", value: "fast", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRate(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseRate(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestValidateIPModeRejectsConflict(t *testing.T) {
	if err := validateIPMode(true, true); err == nil {
		t.Fatal("expected IPv4/IPv6 conflict error")
	}
	if err := validateIPMode(true, false); err != nil {
		t.Fatalf("unexpected IPv4-only error: %v", err)
	}
	if err := validateIPMode(false, true); err != nil {
		t.Fatalf("unexpected IPv6-only error: %v", err)
	}
}

func TestValidateCount(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		wantErr bool
	}{
		{name: "infinite", count: -1},
		{name: "positive one", count: 1},
		{name: "positive ten", count: 10},
		{name: "zero", count: 0, wantErr: true},
		{name: "below infinite sentinel", count: -2, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCount(tt.count)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateRunLimits(t *testing.T) {
	tests := []struct {
		name      string
		count     int
		duration  string
		countSet  bool
		want      time.Duration
		wantError bool
	}{
		{name: "infinite count", count: -1},
		{name: "positive count", count: 5},
		{name: "duration alone accepted", count: -1, duration: "30s", want: 30 * time.Second},
		{name: "invalid duration", count: -1, duration: "nope", wantError: true},
		{name: "zero duration", count: -1, duration: "0s", wantError: true},
		{name: "count and duration", count: 5, duration: "30s", countSet: true, wantError: true},
		{name: "invalid count", count: 0, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateRunLimits(tt.count, tt.duration, tt.countSet)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("duration = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatsAverage(t *testing.T) {
	st := &stats{}
	if got := st.average(); got != 0 {
		t.Fatalf("empty average = %v, want 0", got)
	}

	st.recordSuccess(10)
	st.recordSuccess(30)

	if st.connects != 2 {
		t.Fatalf("connects = %d, want 2", st.connects)
	}
	if st.min != 10 || st.max != 30 {
		t.Fatalf("min/max = %v/%v, want 10/30", st.min, st.max)
	}
	if got := st.average(); got != 20 {
		t.Fatalf("average = %v, want 20", got)
	}
}

func TestCSVResultRow(t *testing.T) {
	ts := time.Date(2026, 6, 11, 12, 30, 0, 123, time.UTC)
	got := csvResultRow(ts, "example.com", "93.184.216.34", 443, "ok", "12.345")
	want := []string{
		"2026-06-11T12:30:00.000000123Z",
		"example.com",
		"93.184.216.34",
		"443",
		"ok",
		"12.345",
	}

	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("csvResultRow = %#v, want %#v", got, want)
	}
}

func TestWriteCSVRowReturnsFlushError(t *testing.T) {
	writer := csv.NewWriter(failingWriter{})
	err := writeCSVRow(writer, []string{"timestamp", "host"})
	if err == nil {
		t.Fatal("expected write error")
	}
}

func TestDialTargetWithLocalListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot open localhost listener: %v", err)
	}
	defer listener.Close()

	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		accepted <- conn.Close()
	}()

	ms, err := dialTarget("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dialTarget failed: %v", err)
	}
	if ms < 0 {
		t.Fatalf("latency = %v, want non-negative", ms)
	}

	select {
	case err := <-accepted:
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			t.Fatalf("listener accept failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not accept connection")
	}
}

func TestCLIHelpSmoke(t *testing.T) {
	output, err := runMainForTest("--help")
	if err != nil {
		t.Fatalf("paping-go --help failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "paping-go [options] <host>") ||
		!strings.Contains(output, "paping-go report <csv-file> -o <report.html>") ||
		!strings.Contains(output, "-c N        number of checks (-1 = infinite, default: -1)") ||
		!strings.Contains(output, "-d DURATION run for duration; cannot be combined with -c") {
		t.Fatalf("help output missing usage:\n%s", output)
	}
}

func TestCLIVersionSmoke(t *testing.T) {
	output, err := runMainForTest("--version")
	if err != nil {
		t.Fatalf("paping-go --version failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "paping-go dev") {
		t.Fatalf("version output missing version:\n%s", output)
	}
}

func TestValidateDestination(t *testing.T) {
	got, err := validateDestination([]string{"example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "example.com" {
		t.Fatalf("destination = %q, want example.com", got)
	}

	for _, args := range [][]string{nil, {""}, {"one", "two"}} {
		if _, err := validateDestination(args); err == nil {
			t.Fatalf("validateDestination(%#v) returned nil error", args)
		}
	}
}

func runMainForTest(args ...string) (string, error) {
	cmdArgs := append([]string{"-test.run=TestHelperProcess", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(Run(os.Args[i+1:], "dev"))
		}
	}
	os.Exit(2)
}
