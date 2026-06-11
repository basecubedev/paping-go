package main

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"fmt"
	"html/template"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed report_template.html
var reportTemplateHTML string

const (
	maxRecentRows     = 200
	maxReportFailures = 100
	maxChartPoints    = 2000
)

type ReportRow struct {
	Timestamp time.Time
	Target    string
	Port      int
	Success   bool
	LatencyMS float64
	Error     string
}

type ReportStats struct {
	Total                int
	Successful           int
	Failed               int
	LossPercent          float64
	MinLatencyMS         float64
	AvgLatencyMS         float64
	MedianLatencyMS      float64
	P95LatencyMS         float64
	P99LatencyMS         float64
	MaxLatencyMS         float64
	LatencyCount         int
	HasLatency           bool
	LongestSuccessStreak int
	LongestFailureStreak int
	FirstTimestamp       time.Time
	LastTimestamp        time.Time
	Duration             time.Duration
	Targets              []string
	Ports                []int
}

type reportViewData struct {
	GeneratedAt    string
	Stats          ReportStats
	Chart          template.HTML
	ChartInfo      ChartInfo
	Failures       []ReportRow
	RecentRows     []ReportRow
	RecentRowLimit int
	FailureLimit   int
}

type ChartInfo struct {
	OriginalRows int
	ShownRows    int
	Downsampled  bool
}

func runReport(args []string) int {
	usage := func() {
		fmt.Fprintf(os.Stderr, "Usage: paping-go report <csv-file> -o <report.html>\n")
	}

	csvPath, outputPath, help, err := parseReportArgs(args)
	if help {
		usage()
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		usage()
		return 2
	}
	if strings.TrimSpace(csvPath) == "" {
		fmt.Fprintln(os.Stderr, "Error: missing CSV input file.")
		usage()
		return 2
	}
	if strings.TrimSpace(outputPath) == "" {
		fmt.Fprintln(os.Stderr, "Error: missing output file. Use -o report.html.")
		return 2
	}

	rows, err := readReportCSVFile(csvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to read CSV: %v\n", err)
		return 2
	}
	html, err := renderReportHTML(rows, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to generate report: %v\n", err)
		return 2
	}
	if err := os.WriteFile(outputPath, []byte(html), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to write report: %v\n", err)
		return 2
	}

	fmt.Fprintf(os.Stderr, "Wrote report to %s\n", outputPath)
	return 0
}

func parseReportArgs(args []string) (csvPath, outputPath string, help bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return "", "", true, nil
		case arg == "-o" || arg == "--output":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", "", false, fmt.Errorf("missing output file. Use -o report.html.")
			}
			i++
			outputPath = args[i]
		case strings.HasPrefix(arg, "--output="):
			outputPath = strings.TrimPrefix(arg, "--output=")
			if strings.TrimSpace(outputPath) == "" {
				return "", "", false, fmt.Errorf("missing output file. Use -o report.html.")
			}
		case strings.HasPrefix(arg, "-"):
			return "", "", false, fmt.Errorf("unknown report option %q", arg)
		default:
			if csvPath != "" {
				return "", "", false, fmt.Errorf("report accepts exactly one CSV input file")
			}
			csvPath = arg
		}
	}
	return csvPath, outputPath, false, nil
}

func readReportCSVFile(path string) ([]ReportRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseReportCSV(f)
}

func parseReportCSV(r io.Reader) ([]ReportRow, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("empty CSV")
	}
	if err != nil {
		return nil, err
	}
	columns := map[string]int{}
	for i, h := range header {
		columns[normalizeCSVHeader(h)] = i
	}

	required := []string{"timestamp", "host", "port", "status", "latency_ms"}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("missing required column %q", name)
		}
	}

	var rows []ReportRow
	line := 1
	for {
		record, err := reader.Read()
		line++
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if isBlankCSVRecord(record) {
			continue
		}

		row, err := parseReportCSVRecord(columns, record)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("empty CSV/no usable rows")
	}
	return rows, nil
}

func normalizeCSVHeader(header string) string {
	return strings.ToLower(strings.TrimSpace(header))
}

func isBlankCSVRecord(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

func parseReportCSVRecord(columns map[string]int, record []string) (ReportRow, error) {
	timestampText := csvField(columns, record, "timestamp")
	timestamp, err := time.Parse(time.RFC3339Nano, timestampText)
	if err != nil {
		return ReportRow{}, fmt.Errorf("invalid timestamp %q", timestampText)
	}

	portText := csvField(columns, record, "port")
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return ReportRow{}, fmt.Errorf("invalid port %q", portText)
	}

	status := csvField(columns, record, "status")
	success := strings.EqualFold(status, "ok") || strings.EqualFold(status, "success")

	latencyText := csvField(columns, record, "latency_ms")
	var latency float64
	if strings.TrimSpace(latencyText) != "" {
		latency, err = strconv.ParseFloat(latencyText, 64)
		if err != nil || latency < 0 {
			return ReportRow{}, fmt.Errorf("invalid latency_ms %q", latencyText)
		}
	} else if success {
		return ReportRow{}, fmt.Errorf("successful row missing latency_ms")
	}

	row := ReportRow{
		Timestamp: timestamp,
		Target:    csvField(columns, record, "host"),
		Port:      port,
		Success:   success,
		LatencyMS: latency,
	}
	if strings.TrimSpace(row.Target) == "" {
		return ReportRow{}, fmt.Errorf("missing host")
	}
	if !success {
		row.Error = status
	}
	return row, nil
}

func csvField(columns map[string]int, record []string, name string) string {
	idx := columns[name]
	if idx >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[idx])
}

func computeReportStats(rows []ReportRow) ReportStats {
	stats := ReportStats{Total: len(rows)}
	if len(rows) == 0 {
		return stats
	}

	targetSet := map[string]struct{}{}
	portSet := map[int]struct{}{}
	var latencies []float64

	for i, row := range rows {
		if i == 0 || row.Timestamp.Before(stats.FirstTimestamp) {
			stats.FirstTimestamp = row.Timestamp
		}
		if i == 0 || row.Timestamp.After(stats.LastTimestamp) {
			stats.LastTimestamp = row.Timestamp
		}
		targetSet[row.Target] = struct{}{}
		portSet[row.Port] = struct{}{}

		if row.Success {
			stats.Successful++
			latencies = append(latencies, row.LatencyMS)
		} else {
			stats.Failed++
		}
	}

	stats.LossPercent = float64(stats.Failed) / float64(stats.Total) * 100
	stats.Duration = stats.LastTimestamp.Sub(stats.FirstTimestamp)
	stats.Targets = sortedStringKeys(targetSet)
	stats.Ports = sortedIntKeys(portSet)
	stats.LongestSuccessStreak, stats.LongestFailureStreak = longestReportStreaks(rows)

	if len(latencies) == 0 {
		return stats
	}
	sort.Float64s(latencies)
	stats.LatencyCount = len(latencies)
	stats.HasLatency = true
	stats.MinLatencyMS = latencies[0]
	stats.MaxLatencyMS = latencies[len(latencies)-1]
	stats.MedianLatencyMS = median(latencies)
	stats.P95LatencyMS = percentileNearestRank(latencies, 95)
	stats.P99LatencyMS = percentileNearestRank(latencies, 99)
	for _, latency := range latencies {
		stats.AvgLatencyMS += latency
	}
	stats.AvgLatencyMS /= float64(len(latencies))
	return stats
}

func sortedStringKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedIntKeys(values map[int]struct{}) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}

func longestReportStreaks(rows []ReportRow) (successStreak, failureStreak int) {
	currentSuccess := 0
	currentFailure := 0
	for _, row := range rows {
		if row.Success {
			currentSuccess++
			currentFailure = 0
			if currentSuccess > successStreak {
				successStreak = currentSuccess
			}
		} else {
			currentFailure++
			currentSuccess = 0
			if currentFailure > failureStreak {
				failureStreak = currentFailure
			}
		}
	}
	return successStreak, failureStreak
}

func median(sortedValues []float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	mid := len(sortedValues) / 2
	if len(sortedValues)%2 == 1 {
		return sortedValues[mid]
	}
	return (sortedValues[mid-1] + sortedValues[mid]) / 2
}

func percentileNearestRank(sortedValues []float64, percentile float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	rank := int(math.Ceil(percentile / 100 * float64(len(sortedValues))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sortedValues) {
		rank = len(sortedValues)
	}
	return sortedValues[rank-1]
}

func renderReportHTML(rows []ReportRow, generatedAt time.Time) (string, error) {
	stats := computeReportStats(rows)
	chartRows := downsampleChartRows(rows, maxChartPoints)
	tmpl, err := template.New("report").Funcs(reportTemplateFuncs()).Parse(reportTemplateHTML)
	if err != nil {
		return "", err
	}

	data := reportViewData{
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Stats:       stats,
		Chart:       template.HTML(renderLatencySVG(chartRows, stats)),
		ChartInfo: ChartInfo{
			OriginalRows: len(rows),
			ShownRows:    len(chartRows),
			Downsampled:  len(chartRows) < len(rows),
		},
		Failures:       recentFailureRows(rows, maxReportFailures),
		RecentRows:     recentReportRows(rows, maxRecentRows),
		RecentRowLimit: maxRecentRows,
		FailureLimit:   maxReportFailures,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func reportTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"formatFloat": func(value float64) string {
			return fmt.Sprintf("%.2f", value)
		},
		"formatPercent": func(value float64) string {
			return fmt.Sprintf("%.2f%%", value)
		},
		"formatTimestamp": func(value time.Time) string {
			if value.IsZero() {
				return "n/a"
			}
			return value.Format(time.RFC3339)
		},
		"formatDuration": func(value time.Duration) string {
			if value < 0 {
				value = -value
			}
			return value.String()
		},
		"joinStrings": func(values []string) string {
			if len(values) == 0 {
				return "n/a"
			}
			return strings.Join(values, ", ")
		},
		"joinInts": func(values []int) string {
			if len(values) == 0 {
				return "n/a"
			}
			parts := make([]string, 0, len(values))
			for _, value := range values {
				parts = append(parts, strconv.Itoa(value))
			}
			return strings.Join(parts, ", ")
		},
		"statusText": func(row ReportRow) string {
			if row.Success {
				return "ok"
			}
			return row.Error
		},
	}
}

func recentFailureRows(rows []ReportRow, limit int) []ReportRow {
	failures := make([]ReportRow, 0)
	for _, row := range rows {
		if !row.Success {
			failures = append(failures, row)
		}
	}
	if len(failures) <= limit {
		return failures
	}
	return failures[len(failures)-limit:]
}

func recentReportRows(rows []ReportRow, limit int) []ReportRow {
	if len(rows) <= limit {
		return rows
	}
	return rows[len(rows)-limit:]
}

func downsampleChartRows(rows []ReportRow, limit int) []ReportRow {
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	if limit == 1 {
		return rows[:1]
	}

	sampled := make([]ReportRow, 0, limit)
	lastIndex := len(rows) - 1
	for i := 0; i < limit; i++ {
		idx := int(math.Round(float64(i) * float64(lastIndex) / float64(limit-1)))
		if i > 0 && i < limit-1 {
			start := int(math.Floor(float64(i) * float64(len(rows)) / float64(limit)))
			end := int(math.Floor(float64(i+1) * float64(len(rows)) / float64(limit)))
			if end <= start {
				end = start + 1
			}
			if end > len(rows) {
				end = len(rows)
			}
			for j := start; j < end; j++ {
				if !rows[j].Success {
					idx = j
					break
				}
			}
		}
		sampled = append(sampled, rows[idx])
	}
	return sampled
}

func renderLatencySVG(rows []ReportRow, stats ReportStats) string {
	const (
		width   = 960.0
		height  = 320.0
		left    = 58.0
		right   = 24.0
		top     = 24.0
		bottom  = 48.0
		pointR  = 3.0
		failTop = top + 10
	)

	plotWidth := width - left - right
	plotHeight := height - top - bottom
	maxLatency := stats.MaxLatencyMS
	if maxLatency <= 0 {
		maxLatency = 1
	}

	xFor := func(index int) float64 {
		if len(rows) <= 1 {
			return left + plotWidth/2
		}
		return left + (float64(index) / float64(len(rows)-1) * plotWidth)
	}
	yFor := func(latency float64) float64 {
		return top + plotHeight - (latency / maxLatency * plotHeight)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg class="chart" viewBox="0 0 %.0f %.0f" role="img" aria-label="Latency over attempts">`, width, height))
	b.WriteString(fmt.Sprintf(`<rect x="0" y="0" width="%.0f" height="%.0f" rx="8" fill="#f8fafc"/>`, width, height))
	b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#94a3b8"/>`, left, top+plotHeight, left+plotWidth, top+plotHeight))
	b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#94a3b8"/>`, left, top, left, top+plotHeight))
	for i := 0; i <= 4; i++ {
		y := top + plotHeight - float64(i)/4*plotHeight
		value := maxLatency * float64(i) / 4
		b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#e2e8f0"/>`, left, y, left+plotWidth, y))
		b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="end" font-size="11" fill="#475569">%.1f</text>`, left-8, y+4, value))
	}

	var path strings.Builder
	started := false
	for i, row := range rows {
		x := xFor(i)
		if !row.Success {
			b.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#dc2626" stroke-width="2"/>`, x, failTop, x, top+plotHeight))
			b.WriteString(fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="%.1f" fill="#dc2626"/>`, x, failTop, pointR))
			continue
		}
		y := yFor(row.LatencyMS)
		if !started {
			path.WriteString(fmt.Sprintf("M %.1f %.1f", x, y))
			started = true
		} else {
			path.WriteString(fmt.Sprintf(" L %.1f %.1f", x, y))
		}
	}
	if started {
		b.WriteString(fmt.Sprintf(`<path d="%s" fill="none" stroke="#2563eb" stroke-width="2.5"/>`, path.String()))
	}
	for i, row := range rows {
		if !row.Success {
			continue
		}
		x := xFor(i)
		y := yFor(row.LatencyMS)
		b.WriteString(fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="%.1f" fill="#2563eb"><title>Attempt %d: %.3f ms</title></circle>`, x, y, pointR, i+1, row.LatencyMS))
	}
	b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" font-size="12" fill="#475569">attempt</text>`, left+plotWidth-45, height-14))
	b.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" font-size="12" fill="#475569">latency ms</text>`, 8.0, top-8))
	b.WriteString(`</svg>`)
	return b.String()
}
