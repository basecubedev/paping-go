package app

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"encoding/json"
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
	largeReportRows   = 10000
	veryLargeRows     = 100000
	defaultChartLimit = 20000
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
	ChartJSON      template.JS
	ChartInfo      ChartInfo
	Failures       []ReportRow
	RecentRows     []ReportRow
	RecentRowLimit int
	FailureLimit   int
}

type reportOptions struct {
	MaxChartPoints int
	FullChart      bool
}

type ChartInfo struct {
	OriginalRows int
	ChartPoints  int
	Note         string
}

type ChartPoint struct {
	Index     int     `json:"index"`
	Label     string  `json:"label"`
	Timestamp string  `json:"timestamp,omitempty"`
	LatencyMS float64 `json:"latencyMs,omitempty"`
	Success   bool    `json:"success"`
	Error     string  `json:"error,omitempty"`
}

func runReport(args []string) int {
	usage := func() {
		fmt.Fprintf(os.Stderr, "Usage: paping-go report <csv-file> -o <report.html> [--max-chart-points N] [--full-chart]\n")
	}

	csvPath, outputPath, options, help, err := parseReportArgs(args)
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
	html, err := renderReportHTMLWithOptions(rows, time.Now(), options)
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

func defaultReportOptions() reportOptions {
	return reportOptions{MaxChartPoints: defaultChartLimit}
}

func parseReportArgs(args []string) (csvPath, outputPath string, options reportOptions, help bool, err error) {
	options = defaultReportOptions()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return "", "", options, true, nil
		case arg == "-o" || arg == "--output":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", "", options, false, fmt.Errorf("missing output file. Use -o report.html.")
			}
			i++
			outputPath = args[i]
		case strings.HasPrefix(arg, "--output="):
			outputPath = strings.TrimPrefix(arg, "--output=")
			if strings.TrimSpace(outputPath) == "" {
				return "", "", options, false, fmt.Errorf("missing output file. Use -o report.html.")
			}
		case arg == "--full-chart":
			options.FullChart = true
		case arg == "--max-chart-points":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", "", options, false, fmt.Errorf("missing value for --max-chart-points")
			}
			i++
			limit, parseErr := parseMaxChartPoints(args[i])
			if parseErr != nil {
				return "", "", options, false, parseErr
			}
			options.MaxChartPoints = limit
		case strings.HasPrefix(arg, "--max-chart-points="):
			limit, parseErr := parseMaxChartPoints(strings.TrimPrefix(arg, "--max-chart-points="))
			if parseErr != nil {
				return "", "", options, false, parseErr
			}
			options.MaxChartPoints = limit
		case strings.HasPrefix(arg, "-"):
			return "", "", options, false, fmt.Errorf("unknown report option %q", arg)
		default:
			if csvPath != "" {
				return "", "", options, false, fmt.Errorf("report accepts exactly one CSV input file")
			}
			csvPath = arg
		}
	}
	return csvPath, outputPath, options, false, nil
}

func parseMaxChartPoints(value string) (int, error) {
	limit, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || limit < 1 {
		return 0, fmt.Errorf("--max-chart-points must be a positive integer")
	}
	return limit, nil
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
		if err != nil || math.IsNaN(latency) || math.IsInf(latency, 0) || latency < 0 {
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
	return renderReportHTMLWithOptions(rows, generatedAt, defaultReportOptions())
}

func renderReportHTMLWithOptions(rows []ReportRow, generatedAt time.Time, options reportOptions) (string, error) {
	stats := computeReportStats(rows)
	chartPoints := buildChartPointsForOptions(rows, options)
	chartJSON, err := marshalChartJSON(chartPoints)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New("report").Funcs(reportTemplateFuncs()).Parse(reportTemplateHTML)
	if err != nil {
		return "", err
	}

	data := reportViewData{
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Stats:       stats,
		ChartJSON:   chartJSON,
		ChartInfo: ChartInfo{
			OriginalRows: len(rows),
			ChartPoints:  len(chartPoints),
			Note:         chartDataNote(len(rows), len(chartPoints), options),
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

func marshalChartJSON(points []ChartPoint) (template.JS, error) {
	data, err := json.Marshal(points)
	if err != nil {
		return "", err
	}
	return template.JS(data), nil
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

func chartDataNote(rows, chartPoints int, options reportOptions) string {
	if chartPoints < rows {
		return fmt.Sprintf("Chart data was downsampled from %d to %d points for browser performance.", rows, chartPoints)
	}
	switch {
	case rows > veryLargeRows:
		if options.FullChart {
			return fmt.Sprintf("Chart contains all %d CSV rows because --full-chart was used. Very large reports can be slow in the browser; the CSV remains the canonical raw data source.", rows)
		}
		return fmt.Sprintf("Chart contains all %d CSV rows. Very large reports can be slow in the browser; the CSV remains the canonical raw data source.", rows)
	case rows > largeReportRows:
		return fmt.Sprintf("Chart contains all %d CSV rows. Large reports may take longer to open or interact with.", rows)
	default:
		return fmt.Sprintf("Chart contains all %d CSV rows.", rows)
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

func buildChartPoints(rows []ReportRow) []ChartPoint {
	points := make([]ChartPoint, 0, len(rows))
	for idx, row := range rows {
		points = append(points, chartPointForRow(row, idx))
	}
	return points
}

func buildChartPointsForOptions(rows []ReportRow, options reportOptions) []ChartPoint {
	if options.FullChart || options.MaxChartPoints <= 0 || len(rows) <= options.MaxChartPoints {
		return buildChartPoints(rows)
	}
	limit := options.MaxChartPoints
	points := make([]ChartPoint, 0, limit)
	if limit == 1 {
		return append(points, chartPointForRow(rows[0], 0))
	}
	lastIndex := len(rows) - 1
	lastSample := limit - 1
	for sample := 0; sample < limit; sample++ {
		idx := sample * lastIndex / lastSample
		points = append(points, chartPointForRow(rows[idx], idx))
	}
	return points
}

func chartPointForRow(row ReportRow, idx int) ChartPoint {
	point := ChartPoint{
		Index:     idx + 1,
		Label:     fmt.Sprintf("Attempt %d", idx+1),
		Timestamp: row.Timestamp.Format(time.RFC3339),
		Success:   row.Success,
	}
	if row.Success {
		point.LatencyMS = row.LatencyMS
	} else {
		point.Error = row.Error
	}
	return point
}
