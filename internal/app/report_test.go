package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseReportCSVValid(t *testing.T) {
	rows, err := parseReportCSV(strings.NewReader(strings.Join([]string{
		"timestamp,host,ip,port,status,latency_ms",
		"2026-06-11T10:00:00Z,example.com,93.184.216.34,443,ok,12.345",
		"2026-06-11T10:00:01Z,example.com,93.184.216.34,443,timeout,",
	}, "\n")))
	if err != nil {
		t.Fatalf("parseReportCSV failed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if !rows[0].Success || rows[0].LatencyMS != 12.345 {
		t.Fatalf("success row = %#v, want ok with latency", rows[0])
	}
	if rows[1].Success || rows[1].Error != "timeout" {
		t.Fatalf("failure row = %#v, want timeout failure", rows[1])
	}
}

func TestParseReportCSVMissingHeader(t *testing.T) {
	_, err := parseReportCSV(strings.NewReader(strings.Join([]string{
		"timestamp,host,ip,status,latency_ms",
		"2026-06-11T10:00:00Z,example.com,93.184.216.34,ok,12.345",
	}, "\n")))
	if err == nil || !strings.Contains(err.Error(), `missing required column "port"`) {
		t.Fatalf("error = %v, want missing port header", err)
	}
}

func TestParseReportCSVInvalidLatency(t *testing.T) {
	_, err := parseReportCSV(strings.NewReader(strings.Join([]string{
		"timestamp,host,ip,port,status,latency_ms",
		"2026-06-11T10:00:00Z,example.com,93.184.216.34,443,ok,nope",
	}, "\n")))
	if err == nil || !strings.Contains(err.Error(), "invalid latency_ms") {
		t.Fatalf("error = %v, want invalid latency", err)
	}
}

func TestParseReportCSVAcceptsFailedRowWithoutLatency(t *testing.T) {
	rows, err := parseReportCSV(strings.NewReader(strings.Join([]string{
		"timestamp,host,ip,port,status,latency_ms",
		"2026-06-11T10:00:00Z,example.com,93.184.216.34,443,connection refused,",
	}, "\n")))
	if err != nil {
		t.Fatalf("parseReportCSV failed: %v", err)
	}
	if rows[0].Success {
		t.Fatalf("row = %#v, want failure", rows[0])
	}
}

func TestParseReportCSVEmpty(t *testing.T) {
	if _, err := parseReportCSV(strings.NewReader("")); err == nil {
		t.Fatal("expected empty CSV error")
	}
	if _, err := parseReportCSV(strings.NewReader("timestamp,host,ip,port,status,latency_ms\n")); err == nil {
		t.Fatal("expected no usable rows error")
	}
}

func TestComputeReportStats(t *testing.T) {
	t0 := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	rows := []ReportRow{
		{Timestamp: t0.Add(3 * time.Second), Target: "b.example", Port: 443, Success: true, LatencyMS: 10},
		{Timestamp: t0.Add(1 * time.Second), Target: "a.example", Port: 80, Success: false, Error: "timeout"},
		{Timestamp: t0.Add(2 * time.Second), Target: "a.example", Port: 443, Success: false, Error: "refused"},
		{Timestamp: t0, Target: "b.example", Port: 443, Success: true, LatencyMS: 30},
		{Timestamp: t0.Add(4 * time.Second), Target: "b.example", Port: 443, Success: true, LatencyMS: 20},
	}

	stats := computeReportStats(rows)
	if stats.Total != 5 || stats.Successful != 3 || stats.Failed != 2 {
		t.Fatalf("counts = %d/%d/%d, want 5/3/2", stats.Total, stats.Successful, stats.Failed)
	}
	if stats.LossPercent != 40 {
		t.Fatalf("loss = %v, want 40", stats.LossPercent)
	}
	if stats.MinLatencyMS != 10 || stats.AvgLatencyMS != 20 || stats.MedianLatencyMS != 20 || stats.P95LatencyMS != 30 || stats.P99LatencyMS != 30 || stats.MaxLatencyMS != 30 {
		t.Fatalf("latency stats = min %v avg %v median %v p95 %v p99 %v max %v",
			stats.MinLatencyMS, stats.AvgLatencyMS, stats.MedianLatencyMS, stats.P95LatencyMS, stats.P99LatencyMS, stats.MaxLatencyMS)
	}
	if stats.LongestSuccessStreak != 2 || stats.LongestFailureStreak != 2 {
		t.Fatalf("streaks = %d/%d, want 2/2", stats.LongestSuccessStreak, stats.LongestFailureStreak)
	}
	if !stats.FirstTimestamp.Equal(t0) || !stats.LastTimestamp.Equal(t0.Add(4*time.Second)) || stats.Duration != 4*time.Second {
		t.Fatalf("time range = %s %s %s", stats.FirstTimestamp, stats.LastTimestamp, stats.Duration)
	}
	if strings.Join(stats.Targets, ",") != "a.example,b.example" {
		t.Fatalf("targets = %#v", stats.Targets)
	}
	if len(stats.Ports) != 2 || stats.Ports[0] != 80 || stats.Ports[1] != 443 {
		t.Fatalf("ports = %#v", stats.Ports)
	}
}

func TestComputeReportStatsAllFailed(t *testing.T) {
	t0 := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	stats := computeReportStats([]ReportRow{
		{Timestamp: t0, Target: "example.com", Port: 443, Success: false, Error: "timeout"},
		{Timestamp: t0.Add(time.Second), Target: "example.com", Port: 443, Success: false, Error: "timeout"},
	})
	if stats.HasLatency {
		t.Fatal("all-failed stats should not have latency")
	}
	if stats.LongestFailureStreak != 2 || stats.LongestSuccessStreak != 0 {
		t.Fatalf("streaks = %d/%d, want 0/2", stats.LongestSuccessStreak, stats.LongestFailureStreak)
	}
}

func TestRenderReportHTMLEscapesCSVValues(t *testing.T) {
	html, err := renderReportHTML([]ReportRow{
		{
			Timestamp: time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC),
			Target:    `<script>alert("x")</script>`,
			Port:      443,
			Success:   false,
			Error:     `<b>boom</b>`,
		},
	}, time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("renderReportHTML failed: %v", err)
	}
	if !strings.Contains(html, "paping-go report") || !strings.Contains(html, "Total checks") {
		t.Fatalf("report HTML missing expected summary:\n%s", html)
	}
	if strings.Contains(html, `<script>alert("x")</script>`) || strings.Contains(html, "<b>boom</b>") {
		t.Fatalf("report contains unescaped CSV values:\n%s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") || !strings.Contains(html, "&lt;b&gt;boom&lt;/b&gt;") {
		t.Fatalf("report missing escaped CSV values:\n%s", html)
	}
	if !strings.Contains(html, `\u003cb\u003eboom\u003c/b\u003e`) {
		t.Fatalf("report missing safely escaped JSON chart values:\n%s", html)
	}
}

func TestRenderReportHTMLIncludesInteractiveChartAssets(t *testing.T) {
	html, err := renderReportHTML(makeReportRows(12, 5), time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("renderReportHTML failed: %v", err)
	}
	for _, want := range []string{
		"const chartData = [",
		`id="chart-reset"`,
		`id="chart-zoom-in"`,
		`id="chart-zoom-out"`,
		`id="toggle-latency-line"`,
		`id="toggle-data-points"`,
		`id="toggle-failed-checks"`,
		"Latency line",
		"Data points",
		"Failed checks",
		`title=`,
		"line breaks at failed checks",
		"individual successful checks",
		"failed TCP checks",
		"chartData.length <= 300",
		"currentSegment",
		"No chart layers selected.",
		"Wheel to zoom",
		"Chart contains all 12 CSV rows.",
		"Interactive chart zoom and display toggles require JavaScript.",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("interactive report missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"Blue line and points show successful checks.",
		"Use the display options to show or hide",
		"downsampled",
		"sampled points",
	} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("interactive report should not contain noisy/incorrect text %q", unwanted)
		}
	}
	if strings.Contains(html, "Failure markers") {
		t.Fatal("interactive report should use user-friendly Failed checks wording")
	}
}

func TestRecentReportRowsReturnsMostRecentRows(t *testing.T) {
	rows := makeReportRows(300, 0)
	got := recentReportRows(rows, maxRecentRows)
	if len(got) != maxRecentRows {
		t.Fatalf("recent rows = %d, want %d", len(got), maxRecentRows)
	}
	if !got[0].Timestamp.Equal(rows[100].Timestamp) || !got[len(got)-1].Timestamp.Equal(rows[299].Timestamp) {
		t.Fatalf("recent rows range = %s to %s, want rows 101-300", got[0].Timestamp, got[len(got)-1].Timestamp)
	}

	all := recentReportRows(rows[:87], maxRecentRows)
	if len(all) != 87 {
		t.Fatalf("recent rows below limit = %d, want 87", len(all))
	}
}

func TestRecentFailureRowsReturnsMostRecentFailures(t *testing.T) {
	rows := makeReportRows(150, 1)
	got := recentFailureRows(rows, maxReportFailures)
	if len(got) != maxReportFailures {
		t.Fatalf("failure rows = %d, want %d", len(got), maxReportFailures)
	}
	if got[0].Error != "failure-50" || got[len(got)-1].Error != "failure-149" {
		t.Fatalf("failure range = %q to %q, want failure-50 to failure-149", got[0].Error, got[len(got)-1].Error)
	}

	all := recentFailureRows(rows[:42], maxReportFailures)
	if len(all) != 42 {
		t.Fatalf("failure rows below limit = %d, want 42", len(all))
	}
}

func TestBuildChartPoints(t *testing.T) {
	rows := []ReportRow{
		{
			Timestamp: time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC),
			Target:    "127.0.0.1",
			Port:      8080,
			Success:   true,
			LatencyMS: 12.345,
		},
		{
			Timestamp: time.Date(2026, 6, 11, 10, 0, 1, 0, time.UTC),
			Target:    "127.0.0.1",
			Port:      8080,
			Success:   false,
			Error:     `timeout "quoted"`,
		},
	}
	points := buildChartPoints(rows)
	if len(points) != 2 {
		t.Fatalf("chart points = %d, want 2", len(points))
	}
	if points[0].Index != 1 || points[0].Label != "Attempt 1" || !points[0].Success || points[0].LatencyMS != 12.345 {
		t.Fatalf("success chart point = %#v", points[0])
	}
	if points[1].Index != 2 || points[1].Success || points[1].Error != `timeout "quoted"` {
		t.Fatalf("failure chart point = %#v", points[1])
	}
	if _, err := json.Marshal(points); err != nil {
		t.Fatalf("chart points must marshal to JSON: %v", err)
	}
}

func TestBuildChartPointsIncludesAllRows(t *testing.T) {
	rows := makeReportRows(10000, 100)
	points := buildChartPoints(rows)
	if len(points) != len(rows) {
		t.Fatalf("chart points = %d, want %d", len(points), len(rows))
	}
	if points[0].Index != 1 || points[len(points)-1].Index != len(rows) {
		t.Fatalf("chart points did not preserve first/last index: %d/%d", points[0].Index, points[len(points)-1].Index)
	}
}

func TestRenderReportHTMLLargeDatasetStaysCompact(t *testing.T) {
	rows := makeReportRows(10001, 10)
	html, err := renderReportHTML(rows, time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("renderReportHTML failed: %v", err)
	}
	for _, want := range []string{
		"Chart contains all 10001 CSV rows. Large reports may take longer to open or interact with.",
		"Showing the most recent 200 of 10001 raw rows.",
		"1001 failures recorded. Showing the most recent 100.",
		"<details",
		"Full data remains in the CSV file.",
		"const chartData = [",
		`id="chart-reset"`,
		`id="toggle-data-points"`,
		"Wheel to zoom",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("large report missing %q", want)
		}
	}
	for _, unwanted := range []string{"downsampled", "sampled points"} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("large report should not contain %q", unwanted)
		}
	}
	if strings.Count(html, "<tr>") > maxRecentRows+maxReportFailures+4 {
		t.Fatalf("large report rendered too many table rows: %d", strings.Count(html, "<tr>"))
	}
}

func TestChartFullDataNoteWarnsForVeryLargeReports(t *testing.T) {
	note := chartFullDataNote(100001)
	if !strings.Contains(note, "Chart contains all 100001 CSV rows.") || !strings.Contains(note, "Very large reports can be slow in the browser") {
		t.Fatalf("very large report note = %q", note)
	}
}

func TestRunReportCommand(t *testing.T) {
	dir := t.TempDir()
	csvPath := dir + "/results.csv"
	htmlPath := dir + "/report.html"
	csv := strings.Join([]string{
		"timestamp,host,ip,port,status,latency_ms",
		"2026-06-11T10:00:00Z,example.com,93.184.216.34,443,ok,12.345",
		"2026-06-11T10:00:01Z,example.com,93.184.216.34,443,timeout,",
	}, "\n")
	if err := os.WriteFile(csvPath, []byte(csv), 0644); err != nil {
		t.Fatal(err)
	}

	if code := Run([]string{"report", csvPath, "-o", htmlPath}, "dev"); code != 0 {
		t.Fatalf("run report exit code = %d, want 0", code)
	}
	content, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("reading report failed: %v", err)
	}
	if !strings.Contains(string(content), "paping-go report") || !strings.Contains(string(content), "12.35 ms") {
		t.Fatalf("report content missing expected values:\n%s", string(content))
	}
}

func makeReportRows(count int, failEvery int) []ReportRow {
	start := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	rows := make([]ReportRow, 0, count)
	for i := 0; i < count; i++ {
		row := ReportRow{
			Timestamp: start.Add(time.Duration(i) * time.Second),
			Target:    "127.0.0.1",
			Port:      8080,
			Success:   true,
			LatencyMS: 0.25 + float64(i%100)/100,
		}
		if failEvery > 0 && i%failEvery == 0 {
			row.Success = false
			row.LatencyMS = 0
			row.Error = fmt.Sprintf("failure-%d", i)
		}
		rows = append(rows, row)
	}
	return rows
}
