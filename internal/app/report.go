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
	"path/filepath"
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
	defaultOutputMode = 0o600
	sharedOutputMode  = 0o644
)

type ReportRow struct {
	Timestamp time.Time
	Target    string
	IP        string
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
	IPs                  []string
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

type reportData struct {
	Stats        ReportStats
	ChartPoints  []ChartPoint
	Failures     []ReportRow
	RecentRows   []ReportRow
	OriginalRows int
}

type reportOptions struct {
	MaxChartPoints int
	FullChart      bool
	OutputMode     os.FileMode
	NoClobber      bool
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
		fmt.Fprintf(os.Stderr, "Usage: paping-go report <csv-file> -o <report.html> [--max-chart-points N] [--full-chart] [--output-mode 0600|0644] [--no-clobber]\n")
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		fmt.Fprintf(os.Stderr, "  --max-chart-points N  maximum embedded chart points; statistics still use the full CSV (default: %d)\n", defaultChartLimit)
		fmt.Fprintf(os.Stderr, "  --full-chart          embed every chart point; can create very large HTML reports for long measurements\n")
		fmt.Fprintf(os.Stderr, "  --output-mode MODE    output file permissions: 0600 or 0644 (default: 0600)\n")
		fmt.Fprintf(os.Stderr, "  --no-clobber          fail if the output file already exists\n")
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

	report, err := buildReportDataFromCSVFile(csvPath, options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to read CSV: %v\n", err)
		return 2
	}
	html, err := renderReportDataHTML(report, time.Now(), options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to generate report: %v\n", err)
		return 2
	}
	if err := writeOutputFileAtomically(outputPath, []byte(html), options.OutputMode, options.NoClobber); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to write report: %v\n", err)
		return 2
	}

	fmt.Fprintf(os.Stderr, "Wrote report to %s\n", outputPath)
	return 0
}

func defaultReportOptions() reportOptions {
	return reportOptions{MaxChartPoints: defaultChartLimit, OutputMode: defaultOutputMode}
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
		case arg == "--no-clobber":
			options.NoClobber = true
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
		case arg == "--output-mode":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", "", options, false, fmt.Errorf("missing value for --output-mode")
			}
			i++
			mode, parseErr := parseOutputMode(args[i])
			if parseErr != nil {
				return "", "", options, false, parseErr
			}
			options.OutputMode = mode
		case strings.HasPrefix(arg, "--output-mode="):
			mode, parseErr := parseOutputMode(strings.TrimPrefix(arg, "--output-mode="))
			if parseErr != nil {
				return "", "", options, false, parseErr
			}
			options.OutputMode = mode
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

func parseOutputMode(value string) (os.FileMode, error) {
	switch strings.TrimSpace(value) {
	case "0600":
		return defaultOutputMode, nil
	case "0644":
		return sharedOutputMode, nil
	default:
		return 0, fmt.Errorf("output mode must be either 0600 or 0644")
	}
}

func outputFileExistsError(path string) error {
	return fmt.Errorf("output file already exists: %s", path)
}

func createPrivateOutputFile(path string) (*os.File, error) {
	return createOutputFile(path, defaultOutputMode, false)
}

func createOutputFile(path string, mode os.FileMode, noClobber bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if noClobber {
		flags = os.O_CREATE | os.O_WRONLY | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, mode)
	if err != nil {
		if noClobber && os.IsExist(err) {
			return nil, outputFileExistsError(path)
		}
		return nil, err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func writePrivateOutputFile(path string, data []byte) error {
	return writeOutputFile(path, data, defaultOutputMode)
}

func writeOutputFile(path string, data []byte, mode os.FileMode) error {
	f, err := createOutputFile(path, mode, false)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return closeErr
}

func writeOutputFileAtomically(path string, data []byte, mode os.FileMode, noClobber bool) error {
	return writeOutputFileAtomicallyWithWriter(path, data, mode, noClobber, writeAll)
}

func writeOutputFileAtomicallyWithWriter(path string, data []byte, mode os.FileMode, noClobber bool, write func(*os.File, []byte) error) error {
	if noClobber {
		if _, err := os.Stat(path); err == nil {
			return outputFileExistsError(path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := write(tmp, data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	if noClobber {
		if err := os.Link(tmpPath, path); err != nil {
			if os.IsExist(err) {
				return outputFileExistsError(path)
			}
			return err
		}
	} else if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func writeAll(f *os.File, data []byte) error {
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func readReportCSVFile(path string) ([]ReportRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseReportCSV(f)
}

func buildReportDataFromCSVFile(path string, options reportOptions) (reportData, error) {
	report, err := scanReportSummaryFile(path)
	if err != nil {
		return reportData{}, err
	}
	report.ChartPoints, err = selectReportChartPointsFromCSVFile(path, report.OriginalRows, options)
	if err != nil {
		return reportData{}, err
	}
	return report, nil
}

func scanReportSummaryFile(path string) (reportData, error) {
	f, err := os.Open(path)
	if err != nil {
		return reportData{}, err
	}
	defer f.Close()

	builder := newReportSummaryBuilder()
	if err := scanReportCSV(f, func(idx int, row ReportRow) error {
		builder.add(row)
		return nil
	}); err != nil {
		return reportData{}, err
	}
	return builder.finish(), nil
}

func selectReportChartPointsFromCSVFile(path string, totalRows int, options reportOptions) ([]ChartPoint, error) {
	indexes := reportChartSampleIndexes(totalRows, options)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	points := make([]ChartPoint, 0, len(indexes))
	nextSample := 0
	err = scanReportCSV(f, func(idx int, row ReportRow) error {
		if nextSample >= len(indexes) || idx != indexes[nextSample] {
			return nil
		}
		points = append(points, chartPointForRow(row, idx))
		nextSample++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(points) != len(indexes) {
		return nil, fmt.Errorf("selected %d chart points, expected %d", len(points), len(indexes))
	}
	return points, nil
}

func parseReportCSV(r io.Reader) ([]ReportRow, error) {
	var rows []ReportRow
	err := scanReportCSV(r, func(idx int, row ReportRow) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func scanReportCSV(r io.Reader, visit func(idx int, row ReportRow) error) error {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err == io.EOF {
		return fmt.Errorf("empty CSV")
	}
	if err != nil {
		return err
	}
	columns := map[string]int{}
	for i, h := range header {
		columns[normalizeCSVHeader(h)] = i
	}

	required := []string{"timestamp", "host", "port", "status", "latency_ms"}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return fmt.Errorf("missing required column %q", name)
		}
	}

	line := 1
	rowIndex := 0
	for {
		record, err := reader.Read()
		line++
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if isBlankCSVRecord(record) {
			continue
		}

		row, err := parseReportCSVRecord(columns, record)
		if err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if err := visit(rowIndex, row); err != nil {
			return err
		}
		rowIndex++
	}
	if rowIndex == 0 {
		return fmt.Errorf("empty CSV/no usable rows")
	}
	return nil
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
		IP:        csvFieldOptional(columns, record, "ip"),
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

func csvFieldOptional(columns map[string]int, record []string, name string) string {
	if _, ok := columns[name]; !ok {
		return ""
	}
	return csvField(columns, record, name)
}

type reportSummaryBuilder struct {
	stats          ReportStats
	targetSet      map[string]struct{}
	ipSet          map[string]struct{}
	portSet        map[int]struct{}
	latencies      []float64
	recentRows     []ReportRow
	recentFailures []ReportRow
	currentSuccess int
	currentFailure int
}

func newReportSummaryBuilder() *reportSummaryBuilder {
	return &reportSummaryBuilder{
		targetSet: map[string]struct{}{},
		ipSet:     map[string]struct{}{},
		portSet:   map[int]struct{}{},
	}
}

func (b *reportSummaryBuilder) add(row ReportRow) {
	if b.stats.Total == 0 || row.Timestamp.Before(b.stats.FirstTimestamp) {
		b.stats.FirstTimestamp = row.Timestamp
	}
	if b.stats.Total == 0 || row.Timestamp.After(b.stats.LastTimestamp) {
		b.stats.LastTimestamp = row.Timestamp
	}

	b.stats.Total++
	b.targetSet[row.Target] = struct{}{}
	if strings.TrimSpace(row.IP) != "" {
		b.ipSet[row.IP] = struct{}{}
	}
	b.portSet[row.Port] = struct{}{}

	if row.Success {
		b.stats.Successful++
		b.latencies = append(b.latencies, row.LatencyMS)
		b.currentSuccess++
		b.currentFailure = 0
		if b.currentSuccess > b.stats.LongestSuccessStreak {
			b.stats.LongestSuccessStreak = b.currentSuccess
		}
	} else {
		b.stats.Failed++
		b.currentFailure++
		b.currentSuccess = 0
		if b.currentFailure > b.stats.LongestFailureStreak {
			b.stats.LongestFailureStreak = b.currentFailure
		}
		b.recentFailures = appendLimitedReportRow(b.recentFailures, row, maxReportFailures)
	}

	b.recentRows = appendLimitedReportRow(b.recentRows, row, maxRecentRows)
}

func (b *reportSummaryBuilder) finish() reportData {
	stats := b.stats
	if stats.Total > 0 {
		stats.LossPercent = float64(stats.Failed) / float64(stats.Total) * 100
		stats.Duration = stats.LastTimestamp.Sub(stats.FirstTimestamp)
	}
	stats.Targets = sortedStringKeys(b.targetSet)
	stats.IPs = sortedStringKeys(b.ipSet)
	stats.Ports = sortedIntKeys(b.portSet)

	if len(b.latencies) > 0 {
		sort.Float64s(b.latencies)
		stats.LatencyCount = len(b.latencies)
		stats.HasLatency = true
		stats.MinLatencyMS = b.latencies[0]
		stats.MaxLatencyMS = b.latencies[len(b.latencies)-1]
		stats.MedianLatencyMS = median(b.latencies)
		stats.P95LatencyMS = percentileNearestRank(b.latencies, 95)
		stats.P99LatencyMS = percentileNearestRank(b.latencies, 99)
		for _, latency := range b.latencies {
			stats.AvgLatencyMS += latency
		}
		stats.AvgLatencyMS /= float64(len(b.latencies))
	}

	return reportData{
		Stats:        stats,
		Failures:     append([]ReportRow(nil), b.recentFailures...),
		RecentRows:   append([]ReportRow(nil), b.recentRows...),
		OriginalRows: stats.Total,
	}
}

func appendLimitedReportRow(rows []ReportRow, row ReportRow, limit int) []ReportRow {
	if limit <= 0 {
		return rows[:0]
	}
	rows = append(rows, row)
	if len(rows) > limit {
		copy(rows, rows[len(rows)-limit:])
		rows = rows[:limit]
	}
	return rows
}

func computeReportStats(rows []ReportRow) ReportStats {
	builder := newReportSummaryBuilder()
	for _, row := range rows {
		builder.add(row)
	}
	return builder.finish().Stats
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
	return renderReportDataHTML(buildReportDataFromRows(rows, options), generatedAt, options)
}

func buildReportDataFromRows(rows []ReportRow, options reportOptions) reportData {
	builder := newReportSummaryBuilder()
	for _, row := range rows {
		builder.add(row)
	}
	report := builder.finish()
	report.ChartPoints = buildChartPointsForOptions(rows, options)
	return report
}

func renderReportDataHTML(report reportData, generatedAt time.Time, options reportOptions) (string, error) {
	chartJSON, err := marshalChartJSON(report.ChartPoints)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New("report").Funcs(reportTemplateFuncs()).Parse(reportTemplateHTML)
	if err != nil {
		return "", err
	}

	data := reportViewData{
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Stats:       report.Stats,
		ChartJSON:   chartJSON,
		ChartInfo: ChartInfo{
			OriginalRows: report.OriginalRows,
			ChartPoints:  len(report.ChartPoints),
			Note:         chartDataNote(report.OriginalRows, len(report.ChartPoints), options),
		},
		Failures:       report.Failures,
		RecentRows:     report.RecentRows,
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
		return fmt.Sprintf("Chart data was downsampled from %d to %d representative points for browser performance. Summary statistics use all %d CSV rows.", rows, chartPoints, rows)
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
	indexes := reportChartSampleIndexes(len(rows), options)
	points := make([]ChartPoint, 0, len(indexes))
	for _, idx := range indexes {
		points = append(points, chartPointForRow(rows[idx], idx))
	}
	return points
}

func reportChartSampleIndexes(totalRows int, options reportOptions) []int {
	if totalRows <= 0 {
		return nil
	}
	if options.FullChart || options.MaxChartPoints <= 0 || totalRows <= options.MaxChartPoints {
		indexes := make([]int, totalRows)
		for i := range indexes {
			indexes[i] = i
		}
		return indexes
	}

	limit := options.MaxChartPoints
	if limit == 1 {
		return []int{0}
	}

	indexes := make([]int, 0, limit)
	lastIndex := totalRows - 1
	lastSample := limit - 1
	previous := -1
	for sample := 0; sample < limit; sample++ {
		idx := sample * lastIndex / lastSample
		if idx == previous {
			continue
		}
		indexes = append(indexes, idx)
		previous = idx
	}
	return indexes
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
