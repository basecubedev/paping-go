package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseReportArgsMatrix(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantCSV   string
		wantOut   string
		wantHelp  bool
		wantError string
	}{
		{name: "short help", args: []string{"-h"}, wantHelp: true},
		{name: "long help", args: []string{"--help"}, wantHelp: true},
		{name: "short output", args: []string{"input.csv", "-o", "out.html"}, wantCSV: "input.csv", wantOut: "out.html"},
		{name: "long output", args: []string{"input.csv", "--output", "out.html"}, wantCSV: "input.csv", wantOut: "out.html"},
		{name: "long output equals", args: []string{"input.csv", "--output=out.html"}, wantCSV: "input.csv", wantOut: "out.html"},
		{name: "missing short output value", args: []string{"input.csv", "-o"}, wantError: "missing output"},
		{name: "missing equals output value", args: []string{"input.csv", "--output="}, wantError: "missing output"},
		{name: "unknown option", args: []string{"input.csv", "--bad"}, wantError: "unknown report option"},
		{name: "two inputs", args: []string{"a.csv", "b.csv", "-o", "out.html"}, wantError: "exactly one CSV"},
		{name: "no input", args: nil},
		{name: "no output", args: []string{"input.csv"}, wantCSV: "input.csv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCSV, gotOut, _, gotHelp, err := parseReportArgs(tt.args)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotCSV != tt.wantCSV || gotOut != tt.wantOut || gotHelp != tt.wantHelp {
				t.Fatalf("got csv=%q out=%q help=%v, want csv=%q out=%q help=%v",
					gotCSV, gotOut, gotHelp, tt.wantCSV, tt.wantOut, tt.wantHelp)
			}
		})
	}
}

func TestParseReportArgsChartOptions(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      reportOptions
		wantError string
	}{
		{
			name: "defaults",
			args: []string{"input.csv", "-o", "out.html"},
			want: defaultReportOptions(),
		},
		{
			name: "max chart points",
			args: []string{"input.csv", "-o", "out.html", "--max-chart-points", "42"},
			want: reportOptions{MaxChartPoints: 42, OutputMode: defaultOutputMode},
		},
		{
			name: "max chart points equals",
			args: []string{"input.csv", "-o", "out.html", "--max-chart-points=43"},
			want: reportOptions{MaxChartPoints: 43, OutputMode: defaultOutputMode},
		},
		{
			name: "full chart",
			args: []string{"input.csv", "-o", "out.html", "--full-chart"},
			want: reportOptions{MaxChartPoints: defaultChartLimit, FullChart: true, OutputMode: defaultOutputMode},
		},
		{
			name: "output mode",
			args: []string{"input.csv", "-o", "out.html", "--output-mode", "0644"},
			want: reportOptions{MaxChartPoints: defaultChartLimit, OutputMode: sharedOutputMode},
		},
		{
			name: "output mode equals",
			args: []string{"input.csv", "-o", "out.html", "--output-mode=0644"},
			want: reportOptions{MaxChartPoints: defaultChartLimit, OutputMode: sharedOutputMode},
		},
		{
			name:      "missing max chart points",
			args:      []string{"input.csv", "-o", "out.html", "--max-chart-points"},
			wantError: "missing value",
		},
		{
			name:      "bad max chart points",
			args:      []string{"input.csv", "-o", "out.html", "--max-chart-points", "0"},
			wantError: "positive integer",
		},
		{
			name:      "missing output mode",
			args:      []string{"input.csv", "-o", "out.html", "--output-mode"},
			wantError: "missing value",
		},
		{
			name:      "bad output mode",
			args:      []string{"input.csv", "-o", "out.html", "--output-mode", "0666"},
			wantError: "output mode must be either 0600 or 0644",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, got, _, err := parseReportArgs(tt.args)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("options = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseOutputMode(t *testing.T) {
	valid := map[string]os.FileMode{
		"0600": defaultOutputMode,
		"0644": sharedOutputMode,
	}
	for input, want := range valid {
		t.Run(input, func(t *testing.T) {
			got, err := parseOutputMode(input)
			if err != nil {
				t.Fatalf("parseOutputMode failed: %v", err)
			}
			if got != want {
				t.Fatalf("mode = %v, want %v", got, want)
			}
		})
	}

	for _, input := range []string{"0666", "0777", "600", "644", "abc"} {
		t.Run(input, func(t *testing.T) {
			_, err := parseOutputMode(input)
			if err == nil || !strings.Contains(err.Error(), "output mode must be either 0600 or 0644") {
				t.Fatalf("error = %v, want output mode validation error", err)
			}
		})
	}
}

func TestRunReportErrorPaths(t *testing.T) {
	dir := t.TempDir()
	missingCSV := dir + "/missing.csv"
	invalidCSV := dir + "/invalid.csv"
	if err := os.WriteFile(invalidCSV, []byte("not,a,paping,csv\n1,2,3,4\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "help", args: []string{"--help"}, want: 0},
		{name: "missing csv path", args: []string{"-o", dir + "/out.html"}, want: 2},
		{name: "missing output", args: []string{invalidCSV}, want: 2},
		{name: "nonexistent csv", args: []string{missingCSV, "-o", dir + "/out.html"}, want: 2},
		{name: "invalid csv", args: []string{invalidCSV, "-o", dir + "/out.html"}, want: 2},
		{name: "unwritable output path", args: []string{validReportCSV(t, dir), "-o", dir}, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runReport(tt.args); got != tt.want {
				t.Fatalf("runReport exit = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunReportOutputFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix file modes reliably")
	}
	tests := []struct {
		name     string
		existing os.FileMode
		args     []string
		wantMode os.FileMode
	}{
		{
			name:     "shared mode creates shared report",
			args:     []string{"--output-mode", "0644"},
			wantMode: sharedOutputMode,
		},
		{
			name:     "shared mode hardens existing private report to requested mode",
			existing: defaultOutputMode,
			args:     []string{"--output-mode", "0644"},
			wantMode: sharedOutputMode,
		},
		{
			name:     "default hardens existing shared report",
			existing: sharedOutputMode,
			wantMode: defaultOutputMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			csvPath := validReportCSV(t, dir)
			htmlPath := filepath.Join(dir, "report.html")
			if tt.existing != 0 {
				if err := os.WriteFile(htmlPath, []byte("old"), tt.existing); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(htmlPath, tt.existing); err != nil {
					t.Fatal(err)
				}
			}

			args := []string{csvPath, "-o", htmlPath}
			args = append(args, tt.args...)
			if got := runReport(args); got != 0 {
				t.Fatalf("runReport exit = %d, want 0", got)
			}

			info, err := os.Stat(htmlPath)
			if err != nil {
				t.Fatalf("stat failed: %v", err)
			}
			if got := info.Mode().Perm(); got != tt.wantMode {
				t.Fatalf("mode = %v, want %v", got, tt.wantMode)
			}
			content, err := os.ReadFile(htmlPath)
			if err != nil {
				t.Fatalf("read failed: %v", err)
			}
			if !strings.Contains(string(content), "paping-go report") {
				t.Fatalf("report missing expected content:\n%s", content)
			}
			tmpFiles, err := filepath.Glob(filepath.Join(dir, ".report.html-*.tmp"))
			if err != nil {
				t.Fatalf("glob failed: %v", err)
			}
			if len(tmpFiles) != 0 {
				t.Fatalf("temporary files left behind: %#v", tmpFiles)
			}
		})
	}
}

func TestWriteOutputFileAtomicallyKeepsExistingFileOnWriteError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.html")
	if err := os.WriteFile(path, []byte("old report"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := writeOutputFileAtomicallyWithWriter(path, []byte("new report"), defaultOutputMode, func(f *os.File, data []byte) error {
		if _, writeErr := f.Write([]byte("partial")); writeErr != nil {
			t.Fatalf("setup write failed: %v", writeErr)
		}
		return errors.New("forced write failure")
	})
	if err == nil || !strings.Contains(err.Error(), "forced write failure") {
		t.Fatalf("error = %v, want forced write failure", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(content) != "old report" {
		t.Fatalf("content = %q, want old report", content)
	}
	tmpFiles, err := filepath.Glob(filepath.Join(dir, ".report.html-*.tmp"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(tmpFiles) != 0 {
		t.Fatalf("temporary files left behind: %#v", tmpFiles)
	}
}

func TestCreatePrivateOutputFileUsesPrivateMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix file modes reliably")
	}
	path := filepath.Join(t.TempDir(), "diagnostic-output.txt")
	f, err := createPrivateOutputFile(path)
	if err != nil {
		t.Fatalf("createPrivateOutputFile failed: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %v, want 0600", got)
	}
}

func TestCreatePrivateOutputFileHardensExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix file modes reliably")
	}
	path := filepath.Join(t.TempDir(), "diagnostic-output.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := createPrivateOutputFile(path)
	if err != nil {
		t.Fatalf("createPrivateOutputFile failed: %v", err)
	}
	if _, err := f.Write([]byte("new")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %v, want 0600", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("content = %q, want new", content)
	}
}

func TestCreatePrivateOutputFileReturnsOpenError(t *testing.T) {
	_, err := createPrivateOutputFile(filepath.Join(t.TempDir(), "missing", "diagnostic-output.txt"))
	if err == nil {
		t.Fatal("expected open error")
	}
}

func TestParseReportCSVBoundaryCases(t *testing.T) {
	t.Run("normalizes headers and accepts extra columns", func(t *testing.T) {
		rows, err := parseReportCSV(strings.NewReader(strings.Join([]string{
			" Timestamp , HOST , IP , Port , Status , Latency_MS , note",
			"2026-06-11T10:00:00.123456789Z, example.com , 93.184.216.34 , 65535 , OK , 12.345 , ignored",
			"2026-06-11T10:00:01Z, example.com , 93.184.216.34 , 443 , success , 13.5 , ignored",
		}, "\n")))
		if err != nil {
			t.Fatalf("parseReportCSV failed: %v", err)
		}
		if len(rows) != 2 || !rows[0].Success || rows[0].Port != 65535 || rows[1].LatencyMS != 13.5 {
			t.Fatalf("rows = %#v", rows)
		}
	})

	tests := []struct {
		name      string
		record    string
		wantError string
	}{
		{name: "successful row missing latency", record: "2026-06-11T10:00:00Z,example.com,93.184.216.34,443,ok", wantError: "missing latency_ms"},
		{name: "port zero", record: "2026-06-11T10:00:00Z,example.com,93.184.216.34,0,ok,1", wantError: "invalid port"},
		{name: "port too large", record: "2026-06-11T10:00:00Z,example.com,93.184.216.34,65536,ok,1", wantError: "invalid port"},
		{name: "port non numeric", record: "2026-06-11T10:00:00Z,example.com,93.184.216.34,abc,ok,1", wantError: "invalid port"},
		{name: "empty host", record: "2026-06-11T10:00:00Z,  ,93.184.216.34,443,ok,1", wantError: "missing host"},
		{name: "invalid timestamp", record: "not-a-time,example.com,93.184.216.34,443,ok,1", wantError: "invalid timestamp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseReportCSV(strings.NewReader("timestamp,host,ip,port,status,latency_ms\n" + tt.record + "\n"))
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantError)
			}
		})
	}

	t.Run("failure row may omit trailing latency field", func(t *testing.T) {
		rows, err := parseReportCSV(strings.NewReader("timestamp,host,ip,port,status,latency_ms\n2026-06-11T10:00:00Z,example.com,93.184.216.34,443,timeout\n"))
		if err != nil {
			t.Fatalf("parseReportCSV failed: %v", err)
		}
		if rows[0].Success || rows[0].Error != "timeout" {
			t.Fatalf("row = %#v, want timeout failure", rows[0])
		}
	})

	t.Run("status semantics", func(t *testing.T) {
		rows, err := parseReportCSV(strings.NewReader(strings.Join([]string{
			"timestamp,host,ip,port,status,latency_ms",
			"2026-06-11T10:00:00Z,example.com,93.184.216.34,443,ok,1",
			"2026-06-11T10:00:01Z,example.com,93.184.216.34,443,success,2",
			"2026-06-11T10:00:02Z,example.com,93.184.216.34,443,OK,3",
			"2026-06-11T10:00:03Z,example.com,93.184.216.34,443,timeout,",
			"2026-06-11T10:00:04Z,example.com,93.184.216.34,443,connection refused,",
		}, "\n")))
		if err != nil {
			t.Fatalf("parseReportCSV failed: %v", err)
		}
		for i := 0; i < 3; i++ {
			if !rows[i].Success {
				t.Fatalf("row %d should be success: %#v", i, rows[i])
			}
		}
		if rows[3].Success || rows[3].Error != "timeout" || rows[4].Success || rows[4].Error != "connection refused" {
			t.Fatalf("failure rows = %#v %#v", rows[3], rows[4])
		}
	})
}

func TestComputeReportStatsEmptyAndDurationBoundaries(t *testing.T) {
	if stats := computeReportStats(nil); stats.Total != 0 || stats.Duration != 0 || stats.HasLatency {
		t.Fatalf("empty stats = %#v", stats)
	}
	t0 := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	stats := computeReportStats([]ReportRow{
		{Timestamp: t0, Target: "b", Port: 443, Success: false, Error: "timeout"},
		{Timestamp: t0, Target: "a", Port: 80, Success: true, LatencyMS: 1.5},
		{Timestamp: t0, Target: "a", Port: 443, Success: true, LatencyMS: 2.5},
	})
	if stats.Duration != 0 || !stats.FirstTimestamp.Equal(t0) || !stats.LastTimestamp.Equal(t0) {
		t.Fatalf("time stats = first %s last %s duration %s", stats.FirstTimestamp, stats.LastTimestamp, stats.Duration)
	}
	if strings.Join(stats.Targets, ",") != "a,b" || fmt.Sprint(stats.Ports) != "[80 443]" {
		t.Fatalf("targets/ports = %#v %#v", stats.Targets, stats.Ports)
	}
}

func TestRenderReportHTMLChartJSONStructure(t *testing.T) {
	html, err := renderReportHTML([]ReportRow{
		{Timestamp: time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC), Target: "example.com", Port: 443, Success: true, LatencyMS: 12.34},
		{Timestamp: time.Date(2026, 6, 11, 10, 0, 1, 0, time.UTC), Target: "example.com", Port: 443, Success: false, Error: `bad "failure"`},
	}, time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("renderReportHTML failed: %v", err)
	}
	if strings.Contains(html, "cdn.plot.ly") {
		t.Fatal("report HTML should not reference Plotly CDN")
	}
	if strings.Count(html, "const chartData = ") != 1 {
		t.Fatalf("chartData assignment count = %d", strings.Count(html, "const chartData = "))
	}

	var points []ChartPoint
	if err := json.Unmarshal([]byte(extractChartJSON(t, html)), &points); err != nil {
		t.Fatalf("chart JSON did not unmarshal: %v", err)
	}
	if len(points) != 2 || !points[0].Success || points[1].Error != `bad "failure"` {
		t.Fatalf("chart points = %#v", points)
	}
	for _, id := range []string{
		`id="chart-reset"`,
		`id="chart-zoom-in"`,
		`id="chart-zoom-out"`,
		`id="chart-pan-left"`,
		`id="chart-pan-right"`,
		`id="toggle-latency-line"`,
		`id="toggle-data-points"`,
		`id="toggle-failed-checks"`,
	} {
		if !strings.Contains(html, id) {
			t.Fatalf("HTML missing %s", id)
		}
	}
}

func TestReportTemplateHelpers(t *testing.T) {
	funcs := reportTemplateFuncs()
	formatTimestamp := funcs["formatTimestamp"].(func(time.Time) string)
	formatDuration := funcs["formatDuration"].(func(time.Duration) string)
	joinStrings := funcs["joinStrings"].(func([]string) string)
	joinInts := funcs["joinInts"].(func([]int) string)
	statusText := funcs["statusText"].(func(ReportRow) string)

	ts := time.Date(2026, 6, 12, 10, 30, 0, 0, time.UTC)
	if got := formatTimestamp(time.Time{}); got != "n/a" {
		t.Fatalf("zero timestamp = %q", got)
	}
	if got := formatTimestamp(ts); got != "2026-06-12T10:30:00Z" {
		t.Fatalf("timestamp = %q", got)
	}
	for _, tt := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{2 * time.Second, "2s"},
		{-2 * time.Second, "2s"},
	} {
		if got := formatDuration(tt.in); got != tt.want {
			t.Fatalf("duration %s = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := joinStrings(nil); got != "n/a" {
		t.Fatalf("empty strings = %q", got)
	}
	if got := joinStrings([]string{"b", "a"}); got != "b, a" {
		t.Fatalf("strings = %q", got)
	}
	if got := joinInts(nil); got != "n/a" {
		t.Fatalf("empty ints = %q", got)
	}
	if got := joinInts([]int{443, 80}); got != "443, 80" {
		t.Fatalf("ints = %q", got)
	}
	if got := statusText(ReportRow{Success: true}); got != "ok" {
		t.Fatalf("success status = %q", got)
	}
	if got := statusText(ReportRow{Error: "timeout"}); got != "timeout" {
		t.Fatalf("failure status = %q", got)
	}
	if got := statusText(ReportRow{}); got != "" {
		t.Fatalf("unknown failure status = %q", got)
	}
}

func TestStandaloneViewerHTMLUsesLocalAssets(t *testing.T) {
	content, err := os.ReadFile(repoRootPath(t, "tools", "viewer.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(content)
	for _, want := range []string{
		`<script src="./vendor/plotly.min.js"`,
		`id="file-input"`,
		`id="filter"`,
		`id="show-line"`,
		`id="show-points"`,
		`id="show-failures"`,
		"CSV files are processed locally in your browser",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("viewer missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"cdn.plot.ly",
		"https://cdn.",
		"http://cdn.",
	} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("viewer should not contain external CDN reference %q", unwanted)
		}
	}
}

func repoRootPath(t *testing.T, parts ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(append([]string{root}, parts...)...)
}

func FuzzParseRate(f *testing.F) {
	for _, seed := range []string{"1", "0.001", "1000", "fast", "", "-1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got, err := parseRate(input)
		if err == nil && (got < 0.001 || got > 1000) {
			t.Fatalf("parseRate(%q) = %v outside valid range", input, got)
		}
	})
}

func FuzzParseReportCSV(f *testing.F) {
	f.Add("timestamp,host,ip,port,status,latency_ms\n2026-06-11T10:00:00Z,example.com,93.184.216.34,443,ok,1\n")
	f.Add("")
	f.Add("not,csv\n")
	f.Fuzz(func(t *testing.T, input string) {
		_, _ = parseReportCSV(strings.NewReader(input))
	})
}

func FuzzParseReportArgs(f *testing.F) {
	f.Add("")
	f.Add("-h")
	f.Add("input.csv -o out.html")
	f.Add("input.csv --output=out.html")
	f.Fuzz(func(t *testing.T, input string) {
		args := strings.Fields(input)
		if len(args) > 8 {
			args = args[:8]
		}
		_, _, _, _, _ = parseReportArgs(args)
	})
}

func validReportCSV(t *testing.T, dir string) string {
	t.Helper()
	path := dir + "/valid.csv"
	content := "timestamp,host,ip,port,status,latency_ms\n2026-06-11T10:00:00Z,example.com,93.184.216.34,443,ok,1\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func extractChartJSON(t *testing.T, html string) string {
	t.Helper()
	prefix := "const chartData = "
	start := strings.Index(html, prefix)
	if start < 0 {
		t.Fatal("missing chartData assignment")
	}
	start += len(prefix)
	end := strings.Index(html[start:], ";\n")
	if end < 0 {
		t.Fatal("missing chartData terminator")
	}
	return html[start : start+end]
}
