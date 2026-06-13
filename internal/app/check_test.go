package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type checkRun struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
	dials  []dialCall
	waits  []time.Duration
	files  map[string]*memoryWriteCloser
	modes  map[string]os.FileMode
}

type dialCall struct {
	network string
	address string
	timeout time.Duration
}

func newCheckRun() *checkRun {
	return &checkRun{
		files: map[string]*memoryWriteCloser{},
		modes: map[string]os.FileMode{},
	}
}

func (r *checkRun) deps(addrs []net.IPAddr, dialResults ...dialResult) checkDeps {
	results := append([]dialResult(nil), dialResults...)
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	return checkDeps{
		stdout: &r.stdout,
		stderr: &r.stderr,
		lookupIPAddr: func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return addrs, nil
		},
		dialTarget: func(network, address string, timeout time.Duration) (float64, error) {
			r.dials = append(r.dials, dialCall{network: network, address: address, timeout: timeout})
			if len(results) == 0 {
				return 1.25, nil
			}
			result := results[0]
			results = results[1:]
			return result.ms, result.err
		},
		now: func() time.Time {
			return now
		},
		after: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- now
			return ch
		},
		createFile: func(name string, mode os.FileMode, noClobber bool) (io.WriteCloser, error) {
			f := &memoryWriteCloser{}
			r.files[name] = f
			r.modes[name] = mode
			return f, nil
		},
		enableConsoleColors: func() {},
	}
}

type dialResult struct {
	ms  float64
	err error
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type memoryWriteCloser struct {
	bytes.Buffer
	closeErr error
	closed   bool
}

func (w *memoryWriteCloser) Close() error {
	w.closed = true
	return w.closeErr
}

type failingWriteCloser struct {
	writeCalls int
	failAfter  int
}

func (w *failingWriteCloser) Write(p []byte) (int, error) {
	w.writeCalls++
	if w.writeCalls > w.failAfter {
		return 0, errors.New("forced write failure")
	}
	return len(p), nil
}

func (w *failingWriteCloser) Close() error { return nil }

func TestRunCheckWithDepsSuccessfulSingleCheck(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, dialResult{ms: 12.34})

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "example.com"}, "dev", deps)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, r.stdout.String(), r.stderr.String())
	}
	for _, want := range []string{
		"paping-go dev",
		"Connecting to example.com [93.184.216.34]",
		"Connected to 93.184.216.34: time=12.34ms",
		"Attempted = 1, Connected = 1, Failed = 0 (0.00%)",
	} {
		if !strings.Contains(r.stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, r.stdout.String())
		}
	}
	if len(r.dials) != 1 || r.dials[0].network != "tcp4" || r.dials[0].address != "93.184.216.34:443" {
		t.Fatalf("dial calls = %#v", r.dials)
	}
}

func TestRunCheckWithDepsFailedCheck(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, dialResult{err: errors.New("connection refused")})

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "example.com"}, "dev", deps)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		"Connection failed: connection refused",
		"Attempted = 1, Connected = 0, Failed = 1 (100.00%)",
	} {
		if !strings.Contains(r.stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, r.stdout.String())
		}
	}
}

func TestRunCheckWithDepsTimeoutCheck(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, dialResult{err: timeoutErr{}})

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "-o", "results.csv", "example.com"}, "dev", deps)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(r.stdout.String(), "Connection timed out") {
		t.Fatalf("stdout missing timeout:\n%s", r.stdout.String())
	}
	gotCSV := r.files["results.csv"].String()
	if !strings.Contains(gotCSV, ",example.com,93.184.216.34,443,timeout,\n") {
		t.Fatalf("CSV missing timeout row:\n%s", gotCSV)
	}
}

func TestRunCheckWithDepsDNSFailureDoesNotDial(t *testing.T) {
	r := newCheckRun()
	deps := r.deps(nil)
	deps.lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return nil, errors.New("no such host")
	}

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "example.invalid"}, "dev", deps)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(r.dials) != 0 {
		t.Fatalf("dial calls = %#v, want none", r.dials)
	}
	if !strings.Contains(r.stderr.String(), "Host lookup failed") {
		t.Fatalf("stderr missing lookup failure:\n%s", r.stderr.String())
	}
}

func TestRunCheckWithDepsIPSelection(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		addrs       []net.IPAddr
		wantNetwork string
		wantAddress string
		wantCode    int
		wantDial    bool
	}{
		{
			name:        "force IPv4",
			args:        []string{"-nocolor", "-4", "-p", "443", "-c", "1", "example.com"},
			addrs:       []net.IPAddr{{IP: net.ParseIP("::1")}, {IP: net.ParseIP("192.0.2.10")}},
			wantNetwork: "tcp4",
			wantAddress: "192.0.2.10:443",
			wantDial:    true,
		},
		{
			name:        "force IPv6",
			args:        []string{"-nocolor", "-6", "-p", "443", "-c", "1", "example.com"},
			addrs:       []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}, {IP: net.ParseIP("::1")}},
			wantNetwork: "tcp6",
			wantAddress: "[::1]:443",
			wantDial:    true,
		},
		{
			name:     "no matching IP mode",
			args:     []string{"-nocolor", "-6", "-p", "443", "-c", "1", "example.com"},
			addrs:    []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}},
			wantCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCheckRun()
			deps := r.deps(tt.addrs, dialResult{ms: 1.5})
			code := runCheckWithDeps(tt.args, "dev", deps)
			if tt.wantCode == 0 && code != 0 {
				t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, r.stdout.String(), r.stderr.String())
			}
			if tt.wantCode != 0 && code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !tt.wantDial {
				if len(r.dials) != 0 {
					t.Fatalf("dial calls = %#v, want none", r.dials)
				}
				if !strings.Contains(r.stdout.String(), "No matching address found") {
					t.Fatalf("stdout missing no matching address:\n%s", r.stdout.String())
				}
				return
			}
			if len(r.dials) != 1 {
				t.Fatalf("dial calls = %#v, want one", r.dials)
			}
			if r.dials[0].network != tt.wantNetwork || r.dials[0].address != tt.wantAddress {
				t.Fatalf("dial call = %#v, want %s %s", r.dials[0], tt.wantNetwork, tt.wantAddress)
			}
		})
	}
}

func TestResolvedTargetsSelectsAllMatchingIPs(t *testing.T) {
	addrs := []net.IPAddr{
		{IP: net.ParseIP("192.0.2.10")},
		{IP: net.ParseIP("2001:db8::10")},
		{IP: net.ParseIP("192.0.2.11")},
	}
	targets := resolvedTargets(addrs, "4", 443, true)
	if len(targets) != 2 {
		t.Fatalf("targets = %#v, want two IPv4 targets", targets)
	}
	if targets[0].DialNet != "tcp4" || targets[0].Address != "192.0.2.10:443" ||
		targets[1].DialNet != "tcp4" || targets[1].Address != "192.0.2.11:443" {
		t.Fatalf("targets = %#v", targets)
	}

	first := resolvedTargets(addrs, "", 443, false)
	if len(first) != 1 || first[0].IPStr != "192.0.2.10" {
		t.Fatalf("first target = %#v", first)
	}
}

func TestRunCheckWithDepsAllIPsDialsEveryResolvedAddress(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{
		{IP: net.ParseIP("192.0.2.10")},
		{IP: net.ParseIP("192.0.2.11")},
	}, dialResult{ms: 1.25}, dialResult{ms: 2.5})

	code := runCheckWithDeps([]string{"-nocolor", "-all-ips", "-p", "443", "-c", "1", "-o", "results.csv", "example.com"}, "dev", deps)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, r.stdout.String(), r.stderr.String())
	}
	if len(r.dials) != 2 {
		t.Fatalf("dial calls = %#v, want two", r.dials)
	}
	if r.dials[0].address != "192.0.2.10:443" || r.dials[1].address != "192.0.2.11:443" {
		t.Fatalf("dial calls = %#v", r.dials)
	}
	if !strings.Contains(r.stdout.String(), "Connecting to example.com [192.0.2.10, 192.0.2.11]") {
		t.Fatalf("stdout missing all IP display:\n%s", r.stdout.String())
	}
	gotCSV := r.files["results.csv"].String()
	for _, want := range []string{
		",example.com,192.0.2.10,443,ok,1.250\n",
		",example.com,192.0.2.11,443,ok,2.500\n",
	} {
		if !strings.Contains(gotCSV, want) {
			t.Fatalf("CSV missing %q:\n%s", want, gotCSV)
		}
	}
}

func TestRunCheckWithDepsCountModeCallsDialerExactlyN(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}},
		dialResult{ms: 1},
		dialResult{ms: 2},
		dialResult{ms: 3},
	)

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "3", "example.com"}, "dev", deps)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if len(r.dials) != 3 {
		t.Fatalf("dial calls = %d, want 3", len(r.dials))
	}
	if !strings.Contains(r.stdout.String(), "Attempted = 3, Connected = 3, Failed = 0") {
		t.Fatalf("stdout missing count stats:\n%s", r.stdout.String())
	}
}

func TestRunCheckWithDepsDurationModeStopsAtDeadline(t *testing.T) {
	r := newCheckRun()
	current := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}})
	deps.now = func() time.Time {
		return current
	}
	deps.after = func(wait time.Duration) <-chan time.Time {
		r.waits = append(r.waits, wait)
		current = current.Add(wait)
		ch := make(chan time.Time, 1)
		ch <- current
		return ch
	}

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-d", "3s", "-r", "1", "example.com"}, "dev", deps)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, r.stdout.String(), r.stderr.String())
	}
	if len(r.dials) != 4 {
		t.Fatalf("dial calls = %d, want 4", len(r.dials))
	}
	if !strings.Contains(r.stdout.String(), "Attempted = 4, Connected = 4, Failed = 0") {
		t.Fatalf("stdout missing duration stats:\n%s", r.stdout.String())
	}
}

func TestRunCheckWithDepsInterruptDuringWaitPrintsStatsAndClosesCSV(t *testing.T) {
	r := newCheckRun()
	sig := make(chan os.Signal, 1)
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, dialResult{ms: 1})
	deps.setupInterrupt = func() (<-chan os.Signal, func()) {
		return sig, func() { close(sig) }
	}
	deps.after = func(wait time.Duration) <-chan time.Time {
		sig <- os.Interrupt
		return make(chan time.Time)
	}

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "3", "-o", "results.csv", "example.com"}, "dev", deps)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, r.stdout.String(), r.stderr.String())
	}
	if len(r.dials) != 1 {
		t.Fatalf("dial calls = %d, want 1", len(r.dials))
	}
	if !strings.Contains(r.stdout.String(), "Attempted = 1, Connected = 1, Failed = 0") {
		t.Fatalf("stdout missing interrupt stats:\n%s", r.stdout.String())
	}
	if !r.files["results.csv"].closed {
		t.Fatal("CSV file was not closed after interrupt")
	}
}

func TestRunCheckWithDepsCSVOutputSuccessAndFailure(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}},
		dialResult{ms: 12.34},
		dialResult{err: errors.New("connection refused")},
	)

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "2", "-o", "results.csv", "example.com"}, "dev", deps)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	got := r.files["results.csv"].String()
	for _, want := range []string{
		"timestamp,host,ip,port,status,latency_ms",
		",example.com,93.184.216.34,443,ok,12.340",
		",example.com,93.184.216.34,443,connection refused,",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("CSV missing %q:\n%s", want, got)
		}
	}
	if !r.files["results.csv"].closed {
		t.Fatal("CSV file was not closed")
	}
	if r.modes["results.csv"] != defaultOutputMode {
		t.Fatalf("CSV mode = %v, want %v", r.modes["results.csv"], defaultOutputMode)
	}
}

func TestRunCheckWithDepsCSVOutputModeOption(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, dialResult{ms: 1})

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "-o", "results.csv", "--output-mode", "0644", "example.com"}, "dev", deps)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, r.stdout.String(), r.stderr.String())
	}
	if r.modes["results.csv"] != sharedOutputMode {
		t.Fatalf("CSV mode = %v, want %v", r.modes["results.csv"], sharedOutputMode)
	}
}

func TestRunCheckWithDepsRejectsInvalidOutputMode(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}})

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "-o", "results.csv", "--output-mode", "0666", "example.com"}, "dev", deps)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(r.stderr.String(), "output mode must be either 0600 or 0644") {
		t.Fatalf("stderr missing output mode error:\n%s", r.stderr.String())
	}
	if len(r.dials) != 0 {
		t.Fatalf("dial calls = %#v, want none", r.dials)
	}
}

func TestRunCheckCSVOutputFileModes(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows does not expose Unix file modes reliably")
	}
	tests := []struct {
		name      string
		existing  os.FileMode
		args      []string
		wantMode  os.FileMode
		wantValue string
	}{
		{
			name:      "default creates private file",
			args:      []string{"-nocolor", "-p", "443", "-c", "1", "-o"},
			wantMode:  defaultOutputMode,
			wantValue: "1.250",
		},
		{
			name:      "default hardens existing shared file",
			existing:  0o644,
			args:      []string{"-nocolor", "-p", "443", "-c", "1", "-o"},
			wantMode:  defaultOutputMode,
			wantValue: "1.250",
		},
		{
			name:      "shared mode creates shared file",
			args:      []string{"-nocolor", "-p", "443", "-c", "1", "-o"},
			wantMode:  sharedOutputMode,
			wantValue: "1.250",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "results.csv")
			if tt.existing != 0 {
				if err := os.WriteFile(path, []byte("old"), tt.existing); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, tt.existing); err != nil {
					t.Fatal(err)
				}
			}

			args := append([]string{}, tt.args...)
			args = append(args, path)
			if tt.wantMode == sharedOutputMode {
				args = append(args, "--output-mode", "0644")
			}
			args = append(args, "example.com")

			r := newCheckRun()
			deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, dialResult{ms: 1.25})
			deps.createFile = func(name string, mode os.FileMode, noClobber bool) (io.WriteCloser, error) {
				return createOutputFile(name, mode, noClobber)
			}
			code := runCheckWithDeps(args, "dev", deps)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, r.stdout.String(), r.stderr.String())
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat failed: %v", err)
			}
			if got := info.Mode().Perm(); got != tt.wantMode {
				t.Fatalf("mode = %v, want %v", got, tt.wantMode)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read failed: %v", err)
			}
			if !strings.Contains(string(content), tt.wantValue) {
				t.Fatalf("CSV missing value %q:\n%s", tt.wantValue, content)
			}
		})
	}
}

func TestRunCheckCSVNoClobberRejectsExistingFile(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows does not expose Unix file modes reliably")
	}
	path := filepath.Join(t.TempDir(), "results.csv")
	if err := os.WriteFile(path, []byte("old csv"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, dialResult{ms: 1.25})
	deps.createFile = func(name string, mode os.FileMode, noClobber bool) (io.WriteCloser, error) {
		return createOutputFile(name, mode, noClobber)
	}
	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "-o", path, "--no-clobber", "example.com"}, "dev", deps)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, r.stdout.String(), r.stderr.String())
	}
	if !strings.Contains(r.stderr.String(), "output file already exists: "+path) {
		t.Fatalf("stderr missing no-clobber error:\n%s", r.stderr.String())
	}
	if len(r.dials) != 0 {
		t.Fatalf("dial calls = %#v, want none", r.dials)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(content) != "old csv" {
		t.Fatalf("content = %q, want old csv", content)
	}
}

func TestRunCheckWithDepsCSVCreateFileError(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}})
	deps.createFile = func(name string, mode os.FileMode, noClobber bool) (io.WriteCloser, error) {
		return nil, errors.New("permission denied")
	}

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "-o", "results.csv", "example.com"}, "dev", deps)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(r.stderr.String(), "cannot create file") {
		t.Fatalf("stderr missing create error:\n%s", r.stderr.String())
	}
	if len(r.dials) != 0 {
		t.Fatalf("dial calls = %#v, want none", r.dials)
	}
}

func TestRunCheckWithDepsCSVHeaderWriteError(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}})
	deps.createFile = func(name string, mode os.FileMode, noClobber bool) (io.WriteCloser, error) {
		return &failingWriteCloser{failAfter: 0}, nil
	}

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "-o", "results.csv", "example.com"}, "dev", deps)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(r.stderr.String(), "CSV write error") {
		t.Fatalf("stderr missing header write error:\n%s", r.stderr.String())
	}
	if len(r.dials) != 0 {
		t.Fatalf("dial calls = %#v, want none", r.dials)
	}
}

func TestRunCheckWithDepsCSVWriteError(t *testing.T) {
	r := newCheckRun()
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, dialResult{ms: 1})
	deps.createFile = func(name string, mode os.FileMode, noClobber bool) (io.WriteCloser, error) {
		return &failingWriteCloser{failAfter: 1}, nil
	}

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "-o", "results.csv", "example.com"}, "dev", deps)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(r.stderr.String(), "CSV write error") {
		t.Fatalf("stderr missing write error:\n%s", r.stderr.String())
	}
}

func TestRunCheckWithDepsCSVCloseError(t *testing.T) {
	r := newCheckRun()
	csvFile := &memoryWriteCloser{closeErr: errors.New("close failed")}
	deps := r.deps([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, dialResult{ms: 1})
	deps.createFile = func(name string, mode os.FileMode, noClobber bool) (io.WriteCloser, error) {
		r.files[name] = csvFile
		return csvFile, nil
	}

	code := runCheckWithDeps([]string{"-nocolor", "-p", "443", "-c", "1", "-o", "results.csv", "example.com"}, "dev", deps)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(r.stderr.String(), "CSV close error") {
		t.Fatalf("stderr missing close error:\n%s", r.stderr.String())
	}
}

func TestCLISmokeLocalListenerSuccess(t *testing.T) {
	addr, closeServer := startTCPServer(t)
	defer closeServer()
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}

	output, err := runMainForTest("-nocolor", "-p", portText, "-c", "2", "-r", "1000", "127.0.0.1")
	if err != nil {
		t.Fatalf("local CLI run failed: %v\n%s", err, output)
	}
	if strings.Contains(output, "\033[") {
		t.Fatalf("output contains ANSI escape codes:\n%s", output)
	}
	if got := strings.Count(output, "Connected to"); got != 2 {
		t.Fatalf("Connected lines = %d, want 2\n%s", got, output)
	}
	if !strings.Contains(output, "Attempted = 2, Connected = 2, Failed = 0") {
		t.Fatalf("output missing success stats:\n%s", output)
	}
}

func TestCLISmokeClosedLocalPortFails(t *testing.T) {
	port := freeLocalTCPPort(t)
	output, err := runMainForTest("-nocolor", "-p", strconv.Itoa(port), "-c", "1", "127.0.0.1")
	if err == nil {
		t.Fatalf("closed port run unexpectedly succeeded:\n%s", output)
	}
	if exitCode(err) != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", exitCode(err), output)
	}
	if !strings.Contains(output, "Connection failed") ||
		!strings.Contains(output, "Attempted = 1, Connected = 0, Failed = 1") {
		t.Fatalf("output missing failure details:\n%s", output)
	}
}

func TestCLIInvalidInvocationsExit2(t *testing.T) {
	tests := [][]string{
		nil,
		{"example.com"},
		{"-p", "0", "example.com"},
		{"-p", "65536", "example.com"},
		{"-p", "443", "-t", "0", "example.com"},
		{"-p", "443", "-4", "-6", "example.com"},
		{"-p", "443", "-c", "0", "example.com"},
		{"-p", "443", "-c", "1", "-d", "1s", "example.com"},
		{"-p", "443", "-r", "0", "example.com"},
		{"-p", "443", "-r", "1001", "example.com"},
		{"-unknown"},
	}

	for _, args := range tests {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			output, err := runMainForTest(args...)
			if err == nil {
				t.Fatalf("command unexpectedly succeeded:\n%s", output)
			}
			if exitCode(err) != 2 {
				t.Fatalf("exit code = %d, want 2\noutput:\n%s", exitCode(err), output)
			}
		})
	}
}

func startTCPServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().String(), func() {
		_ = ln.Close()
		<-done
	}
}

func freeLocalTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
