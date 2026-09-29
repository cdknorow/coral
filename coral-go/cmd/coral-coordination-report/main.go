package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/coordination"
)

func main() {
	var (
		boardFlag       = flag.String("board", "", "Target board ID (e.g. death-or-trade-ai-auto)")
		sinceFlag       = flag.String("since", "", "Filter events created at or after this RFC3339 timestamp (e.g. 2026-09-29T06:11:53Z)")
		untilFlag       = flag.String("until", "", "Filter events created at or before this RFC3339 timestamp")
		formatFlag      = flag.String("format", "markdown", "Output format: markdown, json, or text")
		outputFlag      = flag.String("output", "", "Output file path (default: stdout)")
		serverFlag      = flag.String("server", "http://localhost:8420", "Coral HTTP server URL")
		dbFlag          = flag.String("db", "", "Path to messageboard.db for direct read-only SQLite queries")
		thresholdFlag   = flag.String("threshold", "15m", "Duration threshold for active-without-progress candidates (e.g. 15m, 30m)")
	)
	flag.Parse()

	board := strings.TrimSpace(*boardFlag)
	if board == "" && flag.NArg() > 0 {
		board = flag.Arg(0)
	}
	if board == "" {
		// Attempt to use CORAL_BOARD environment variable if available
		board = os.Getenv("CORAL_BOARD")
	}
	if board == "" {
		fmt.Fprintf(os.Stderr, "Error: --board is required (e.g. --board death-or-trade-ai-auto)\n\n")
		flag.Usage()
		os.Exit(1)
	}

	filter := coordination.ReportFilter{
		Board:        board,
		ServerURL:    *serverFlag,
		DatabasePath: *dbFlag,
	}

	if *sinceFlag != "" {
		t, err := time.Parse(time.RFC3339, *sinceFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --since timestamp format (must be RFC3339, e.g. 2026-09-29T06:11:53Z): %v\n", err)
			os.Exit(1)
		}
		filter.Since = &t
	}

	if *untilFlag != "" {
		t, err := time.Parse(time.RFC3339, *untilFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --until timestamp format (must be RFC3339): %v\n", err)
			os.Exit(1)
		}
		filter.Until = &t
	}

	if *thresholdFlag != "" {
		d, err := time.ParseDuration(*thresholdFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --threshold duration (e.g. 15m, 30m): %v\n", err)
			os.Exit(1)
		}
		filter.InactivityLimit = d
	}

	// Fetch data via read-only API or SQLite DB
	var tasks []coordination.Task
	var messages []coordination.Message
	var status *coordination.BoardStatus
	var fetchErr error

	if filter.DatabasePath != "" {
		tasks, messages, status, fetchErr = coordination.FetchFromDB(filter.DatabasePath, board)
	} else {
		tasks, messages, status, fetchErr = coordination.FetchFromAPI(filter.ServerURL, board)
	}

	if fetchErr != nil {
		fmt.Fprintf(os.Stderr, "Error fetching coordination data: %v\n", fetchErr)
		os.Exit(1)
	}

	// Run deterministic coordination analysis
	report := coordination.Analyze(tasks, messages, status, filter, time.Now())

	// Format output
	var outputContent string
	switch strings.ToLower(*formatFlag) {
	case "json":
		jsonStr, err := coordination.FormatJSON(report)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting JSON: %v\n", err)
			os.Exit(1)
		}
		outputContent = jsonStr
	case "text", "txt":
		outputContent = coordination.FormatMarkdown(report)
	case "markdown", "md":
		outputContent = coordination.FormatMarkdown(report)
	default:
		fmt.Fprintf(os.Stderr, "Error: unsupported format %q (choose markdown, json, or text)\n", *formatFlag)
		os.Exit(1)
	}

	// Write output
	if *outputFlag != "" && *outputFlag != "-" {
		err := os.WriteFile(*outputFlag, []byte(outputContent), 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output file %s: %v\n", *outputFlag, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Coordination report written to %s (%d bytes)\n", *outputFlag, len(outputContent))
	} else {
		io.WriteString(os.Stdout, outputContent)
	}
}
