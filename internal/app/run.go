package app

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

var useColor = true

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[1;31m"
	colorGreen  = "\033[1;32m"
	colorYellow = "\033[1;33m"
	colorBlue   = "\033[1;34m"
)

func col(color, text string) string {
	if !useColor {
		return text
	}
	return color + text + colorReset
}

type stats struct {
	attempts int
	connects int
	failures int
	min      float64
	max      float64
	total    float64
}

func (s *stats) recordSuccess(ms float64) {
	s.connects++
	s.total += ms
	if s.connects == 1 || ms < s.min {
		s.min = ms
	}
	if s.connects == 1 || ms > s.max {
		s.max = ms
	}
}

func (s *stats) average() float64 {
	if s.connects == 0 {
		return 0
	}
	return s.total / float64(s.connects)
}

func (s *stats) printTo(w io.Writer) {
	pct := 0.0
	if s.attempts > 0 {
		pct = float64(s.failures) / float64(s.attempts) * 100
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "TCP check statistics:")
	fmt.Fprintf(w, "\tAttempted = %s, Connected = %s, Failed = %s (%s)\n",
		col(colorBlue, fmt.Sprintf("%d", s.attempts)),
		col(colorBlue, fmt.Sprintf("%d", s.connects)),
		col(colorBlue, fmt.Sprintf("%d", s.failures)),
		col(colorBlue, fmt.Sprintf("%.2f%%", pct)))
	fmt.Fprintln(w, "Connection latency summary:")
	fmt.Fprintf(w, "\tMinimum = %s, Maximum = %s, Average = %s\n",
		col(colorBlue, fmt.Sprintf("%.2fms", s.min)),
		col(colorBlue, fmt.Sprintf("%.2fms", s.max)),
		col(colorBlue, fmt.Sprintf("%.2fms", s.average())))
}

func validatePort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("valid port required (-p 1-65535)")
	}
	return nil
}

func validateTimeout(timeoutMS int) error {
	if timeoutMS <= 0 || timeoutMS > 600000 {
		return fmt.Errorf("timeout must be 1-600000 ms")
	}
	return nil
}

func parseRate(rateStr string) (float64, error) {
	rate, err := strconv.ParseFloat(rateStr, 64)
	if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0.001 || rate > 1000 {
		return 0, fmt.Errorf("rate must be a finite number between 0.001 and 1000")
	}
	return rate, nil
}

func validateIPMode(forceV4, forceV6 bool) error {
	if forceV4 && forceV6 {
		return fmt.Errorf("-4 and -6 cannot be used together")
	}
	return nil
}

func validateCount(count int) error {
	if count != -1 && count < 1 {
		return fmt.Errorf("-c must be -1 for continuous mode or greater than 0")
	}
	return nil
}

func validateRunLimits(count int, duration string, countSet bool) (time.Duration, error) {
	if err := validateCount(count); err != nil {
		return 0, err
	}
	if duration == "" {
		return 0, nil
	}
	if countSet {
		return 0, fmt.Errorf("-c and -d cannot be used together")
	}
	d, err := time.ParseDuration(duration)
	if err != nil {
		return 0, fmt.Errorf("invalid duration '%s' (use e.g. 30s, 5m, 1h)", duration)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration must be greater than zero")
	}
	return d, nil
}

func validateDestination(args []string) (string, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("destination required")
	}
	return args[0], nil
}

func csvResultRow(t time.Time, host, ip string, port int, status string, latencyMS string) []string {
	return []string{
		t.Format(time.RFC3339Nano),
		host,
		ip,
		fmt.Sprintf("%d", port),
		status,
		latencyMS,
	}
}

func writeCSVRow(writer *csv.Writer, row []string) error {
	if err := writer.Write(row); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

func dialTarget(network, address string, timeout time.Duration) (float64, error) {
	start := time.Now()
	conn, err := net.DialTimeout(network, address, timeout)
	ms := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return ms, err
	}
	_ = conn.Close()
	return ms, nil
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func Run(args []string, buildVersion string) int {
	if len(args) > 0 && args[0] == "report" {
		return runReport(args[1:])
	}
	return runCheck(args, buildVersion)
}

func runCheck(args []string, buildVersion string) int {
	return runCheckWithDeps(args, buildVersion, realCheckDeps())
}

func runCheckWithDeps(args []string, buildVersion string, deps checkDeps) int {
	useColor = true
	deps = fillCheckDeps(deps)

	fs := flag.NewFlagSet("paping-go", flag.ContinueOnError)
	fs.SetOutput(deps.stderr)

	port := fs.Int("p", 0, "TCP port (required)")
	count := fs.Int("c", -1, "number of checks (default: infinite)")
	timeout := fs.Int("t", 1000, "timeout in milliseconds")
	nocolor := fs.Bool("nocolor", false, "disable color output")
	forceV4 := fs.Bool("4", false, "force IPv4")
	forceV6 := fs.Bool("6", false, "force IPv6")
	outFile := fs.String("o", "", "write results to CSV file")
	runDuration := fs.String("d", "", "run for duration (e.g. 30s, 5m, 1h)")
	rateStr := fs.String("r", "1", "requests per second (e.g. 0.5, 1, 5)")
	showVersion := fs.Bool("version", false, "print version and exit")

	fs.Usage = func() {
		fmt.Fprintf(deps.stderr, "Usage:\n")
		fmt.Fprintf(deps.stderr, "  paping-go [options] <host>\n")
		fmt.Fprintf(deps.stderr, "  paping-go report <csv-file> -o <report.html> [--max-chart-points N] [--full-chart]\n\n")
		fmt.Fprintf(deps.stderr, "Examples:\n")
		fmt.Fprintf(deps.stderr, "  paping-go -p 443 -c 5 example.com\n")
		fmt.Fprintf(deps.stderr, "  paping-go -p 443 -c 100 -o results.csv example.com\n")
		fmt.Fprintf(deps.stderr, "  paping-go report results.csv -o report.html\n")
		fmt.Fprintf(deps.stderr, "  paping-go report results.csv -o report.html --max-chart-points 50000\n\n")
		fmt.Fprintf(deps.stderr, "Options:\n")
		fmt.Fprintf(deps.stderr, "  -p N        TCP port (required)\n")
		fmt.Fprintf(deps.stderr, "  -c N        number of checks (-1 = infinite, default: -1)\n")
		fmt.Fprintf(deps.stderr, "  -d DURATION run for duration; cannot be combined with -c\n")
		fmt.Fprintf(deps.stderr, "  -r N        requests per second (default: 1, e.g. 0.5, 2, 10)\n")
		fmt.Fprintf(deps.stderr, "  -t N        timeout in milliseconds (default: 1000)\n")
		fmt.Fprintf(deps.stderr, "  -4          force IPv4\n")
		fmt.Fprintf(deps.stderr, "  -6          force IPv6\n")
		fmt.Fprintf(deps.stderr, "  -o FILE     write results to CSV file\n")
		fmt.Fprintf(deps.stderr, "  -nocolor    disable color output\n")
		fmt.Fprintf(deps.stderr, "  -version    print version and exit\n")
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if *nocolor {
		useColor = false
	}
	deps.enableConsoleColors()

	fmt.Fprintf(deps.stdout, "paping-go %s\n\n", buildVersion)
	if *showVersion {
		return 0
	}

	if err := validatePort(*port); err != nil {
		fmt.Fprintf(deps.stderr, "Error: %v\n", err)
		fs.Usage()
		return 2
	}
	if err := validateTimeout(*timeout); err != nil {
		fmt.Fprintf(deps.stderr, "Error: %v\n", err)
		return 2
	}
	if err := validateIPMode(*forceV4, *forceV6); err != nil {
		fmt.Fprintf(deps.stderr, "Error: %v.\n", err)
		fs.Usage()
		return 2
	}
	dest, err := validateDestination(fs.Args())
	if err != nil {
		fmt.Fprintf(deps.stderr, "Error: %v\n", err)
		fs.Usage()
		return 2
	}

	rate, rerr := parseRate(*rateStr)
	if rerr != nil {
		fmt.Fprintf(deps.stderr, "Error: %v\n", rerr)
		return 2
	}
	interval := time.Duration(float64(time.Second) / rate)
	dur := time.Duration(*timeout) * time.Millisecond
	runLimit, runLimitErr := validateRunLimits(*count, *runDuration, flagWasSet(fs, "c"))
	if runLimitErr != nil {
		fmt.Fprintf(deps.stderr, "Error: %v\n", runLimitErr)
		return 2
	}

	// Resolve hostname
	ipFilter := ""
	if *forceV4 {
		ipFilter = "4"
	} else if *forceV6 {
		ipFilter = "6"
	}

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	addrs, err := deps.lookupIPAddr(ctx, dest)
	cancel()
	if err != nil || len(addrs) == 0 {
		fmt.Fprintln(deps.stderr, col(colorRed, "Host lookup failed"))
		return 1
	}

	var ip net.IP
	for _, a := range addrs {
		isV4 := a.IP.To4() != nil
		switch ipFilter {
		case "4":
			if isV4 {
				ip = a.IP
			}
		case "6":
			if !isV4 {
				ip = a.IP
			}
		default:
			ip = a.IP
		}
		if ip != nil {
			break
		}
	}
	if ip == nil {
		fmt.Fprintln(deps.stdout, col(colorRed, "No matching address found for requested protocol"))
		return 1
	}

	ipStr := ip.String()
	dialNet := "tcp4"
	if ip.To4() == nil {
		dialNet = "tcp6"
	}
	addr := net.JoinHostPort(ipStr, fmt.Sprintf("%d", *port))

	// Print connection info
	if ipStr == dest {
		fmt.Fprintf(deps.stdout, "Connecting to %s on %s:\n\n",
			col(colorYellow, dest),
			col(colorYellow, fmt.Sprintf("TCP %d", *port)))
	} else {
		fmt.Fprintf(deps.stdout, "Connecting to %s [%s] on %s:\n\n",
			col(colorYellow, dest),
			col(colorYellow, ipStr),
			col(colorYellow, fmt.Sprintf("TCP %d", *port)))
	}

	st := &stats{}
	exitCode := 0
	continuous := *count < 0 && *runDuration == ""
	n := *count

	// Parse duration limit
	var deadline time.Time
	if *runDuration != "" {
		deadline = deps.now().Add(runLimit)
	}

	// CSV output
	var csvWriter *csv.Writer
	var csvFile io.WriteCloser
	if *outFile != "" {
		f, ferr := deps.createFile(*outFile)
		if ferr != nil {
			fmt.Fprintf(deps.stderr, "Error: cannot create file %s: %v\n", *outFile, ferr)
			return 2
		}
		csvFile = f
		csvWriter = csv.NewWriter(f)
		if err := writeCSVRow(csvWriter, []string{"timestamp", "host", "ip", "port", "status", "latency_ms"}); err != nil {
			fmt.Fprintf(deps.stderr, "CSV write error: %v\n", err)
			csvFile.Close()
			return 2
		}
	}

	// Ctrl+C handling
	stop := make(chan struct{})
	if deps.setupInterrupt != nil {
		sig, stopSignals := deps.setupInterrupt()
		if stopSignals != nil {
			defer stopSignals()
		}
		if sig != nil {
			go func() {
				_, ok := <-sig
				if ok {
					close(stop)
				}
			}()
		}
	}

	for i := 0; continuous || (!deadline.IsZero()) || i < n; i++ {
		select {
		case <-stop:
			goto done
		default:
		}

		// Check duration limit
		if !deadline.IsZero() && deps.now().After(deadline) {
			break
		}

		st.attempts++
		start := deps.now()
		ms, dialErr := deps.dialTarget(dialNet, addr, dur)

		if dialErr != nil {
			st.failures++
			exitCode = 1
			errMsg := ""
			if netErr, ok := dialErr.(net.Error); ok && netErr.Timeout() {
				errMsg = "timeout"
				fmt.Fprintln(deps.stdout, col(colorRed, "Connection timed out"))
			} else {
				errMsg = dialErr.Error()
				fmt.Fprintln(deps.stdout, col(colorRed, fmt.Sprintf("Connection failed: %v", dialErr)))
			}
			if csvWriter != nil {
				if err := writeCSVRow(csvWriter, csvResultRow(start, dest, ipStr, *port, errMsg, "")); err != nil {
					fmt.Fprintf(deps.stderr, "CSV write error: %v\n", err)
					exitCode = 1
					goto done
				}
			}
		} else {
			st.recordSuccess(ms)
			fmt.Fprintf(deps.stdout, "Connected to %s: time=%s protocol=%s port=%s\n",
				col(colorGreen, ipStr),
				col(colorGreen, fmt.Sprintf("%.2fms", ms)),
				col(colorGreen, "TCP"),
				col(colorGreen, fmt.Sprintf("%d", *port)))
			if csvWriter != nil {
				if err := writeCSVRow(csvWriter, csvResultRow(start, dest, ipStr, *port, "ok", fmt.Sprintf("%.3f", ms))); err != nil {
					fmt.Fprintf(deps.stderr, "CSV write error: %v\n", err)
					exitCode = 1
					goto done
				}
			}
		}

		// Wait between attempts
		if continuous || (!deadline.IsZero()) || i+1 < n {
			wait := interval - deps.now().Sub(start)
			if wait > 0 {
				select {
				case <-stop:
					goto done
				case <-deps.after(wait):
				}
			}
		}
	}

done:
	if csvFile != nil {
		if err := csvFile.Close(); err != nil {
			fmt.Fprintf(deps.stderr, "CSV close error: %v\n", err)
			exitCode = 1
		}
	}
	st.printTo(deps.stdout)
	return exitCode
}
