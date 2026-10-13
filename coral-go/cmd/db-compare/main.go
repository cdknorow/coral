// Command db-compare compares two SQLite databases for parity testing.
//
// Usage: db-compare <python-db> <go-db> [board-py-db] [board-go-db]
//
// It compares table lists, column schemas, row counts, and normalized
// row data (ignoring timestamps and UUIDs). Outputs a pass/fail report.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// columns that contain timestamps or UUIDs to normalize during comparison
var normalizeColumns = map[string]bool{
	"created_at":       true,
	"updated_at":       true,
	"recorded_at":      true,
	"scheduled_at":     true,
	"started_at":       true,
	"finished_at":      true,
	"attempted_at":     true,
	"delivered_at":     true,
	"next_retry_at":    true,
	"indexed_at":       true,
	"subscribed_at":    true,
	"session_id":       true,
	"resume_from_id":   true,
	"commit_timestamp": true,
}

// tables to skip in comparison (internal/virtual tables)
var skipTables = map[string]bool{
	"session_fts":         true,
	"session_fts_data":    true,
	"session_fts_idx":     true,
	"session_fts_config":  true,
	"session_fts_docsize": true,
	"session_fts_content": true,
	"sqlite_stat1":        true,
}

// knownAcceptableDiffs lists tables or columns that are known to differ
// between Python and Go due to dead features or background-service-only data.
// Format: "table" for whole-table exclusion, "table.column" for column exclusion.
var knownAcceptableDiffs = map[string]string{
	"board_groups":                   "Python-only table (dead feature, not referenced in source)",
	"board_subscribers.receive_mode": "Python-only column (dead feature, not referenced in source)",
	"git_snapshots":                  "Populated by background git poller only (not API-testable)",
	"git_changed_files":              "Populated by background git poller only (not API-testable)",
	"agent_live_state":               "Populated by live session parser (not API-testable)",
}

var uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

type result struct {
	table   string
	status  string // "PASS", "FAIL", "SKIP", "WARN"
	details []string
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: db-compare <python-db> <go-db> [board-py-db] [board-go-db]\n")
		os.Exit(1)
	}

	pyDB := os.Args[1]
	goDB := os.Args[2]

	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println("  Coral DB Parity Comparison")
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Printf("  Python DB: %s\n", pyDB)
	fmt.Printf("  Go DB:     %s\n", goDB)
	fmt.Println("───────────────────────────────────────────────────────")

	results := compareDatabases(pyDB, goDB, "main")

	// Board databases (optional)
	if len(os.Args) >= 5 {
		boardPy := os.Args[3]
		boardGo := os.Args[4]
		fmt.Println()
		fmt.Println("───────────────────────────────────────────────────────")
		fmt.Printf("  Board Python DB: %s\n", boardPy)
		fmt.Printf("  Board Go DB:     %s\n", boardGo)
		fmt.Println("───────────────────────────────────────────────────────")
		boardResults := compareDatabases(boardPy, boardGo, "board")
		results = append(results, boardResults...)
	}

	// Print summary
	printSummary(results)
}

func compareDatabases(pyPath, goPath, label string) []result {
	pyConn, err := sql.Open("sqlite", pyPath+"?mode=ro")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening Python DB: %v\n", err)
		os.Exit(1)
	}
	defer pyConn.Close()

	goConn, err := sql.Open("sqlite", goPath+"?mode=ro")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening Go DB: %v\n", err)
		os.Exit(1)
	}
	defer goConn.Close()

	pyTables := listTables(pyConn)
	goTables := listTables(goConn)

	// 1. Compare table lists
	fmt.Printf("\n[%s] Tables in Python: %d\n", label, len(pyTables))
	fmt.Printf("[%s] Tables in Go:     %d\n", label, len(goTables))

	pySet := toSet(pyTables)
	goSet := toSet(goTables)

	var results []result

	// Tables only in Python
	for _, t := range pyTables {
		if !goSet[t] && !skipTables[t] {
			if reason, ok := knownAcceptableDiffs[t]; ok {
				r := result{table: t, status: "SKIP", details: []string{"only in Python (known: " + reason + ")"}}
				results = append(results, r)
				fmt.Printf("  ○ %-30s  SKIP (known: %s)\n", t, reason)
			} else {
				r := result{table: t, status: "WARN", details: []string{"only in Python DB"}}
				results = append(results, r)
				fmt.Printf("  ⚠ %s — only in Python\n", t)
			}
		}
	}
	// Tables only in Go
	for _, t := range goTables {
		if !pySet[t] && !skipTables[t] {
			if reason, ok := knownAcceptableDiffs[t]; ok {
				r := result{table: t, status: "SKIP", details: []string{"only in Go (known: " + reason + ")"}}
				results = append(results, r)
				fmt.Printf("  ○ %-30s  SKIP (known: %s)\n", t, reason)
			} else {
				r := result{table: t, status: "WARN", details: []string{"only in Go DB"}}
				results = append(results, r)
				fmt.Printf("  ⚠ %s — only in Go\n", t)
			}
		}
	}

	// 2. Compare shared tables
	var sharedTables []string
	for _, t := range pyTables {
		if goSet[t] && !skipTables[t] {
			sharedTables = append(sharedTables, t)
		}
	}

	for _, table := range sharedTables {
		r := compareTable(pyConn, goConn, table)
		results = append(results, r)
	}

	return results
}

func compareTable(pyConn, goConn *sql.DB, table string) result {
	r := result{table: table}

	// Skip data comparison for tables with known acceptable differences
	if reason, ok := knownAcceptableDiffs[table]; ok {
		r.status = "SKIP"
		fmt.Printf("  ○ %-30s  SKIP (known: %s)\n", table, reason)
		return r
	}

	// Compare schemas
	pyCols := getColumns(pyConn, table)
	goCols := getColumns(goConn, table)

	pyColSet := toSet(colNames(pyCols))
	goColSet := toSet(colNames(goCols))

	for _, c := range colNames(pyCols) {
		if !goColSet[c] {
			key := table + "." + c
			if reason, ok := knownAcceptableDiffs[key]; ok {
				fmt.Printf("    ○ column %q only in Python (known: %s)\n", c, reason)
			} else {
				r.details = append(r.details, fmt.Sprintf("column %q only in Python", c))
			}
		}
	}
	for _, c := range colNames(goCols) {
		if !pyColSet[c] {
			key := table + "." + c
			if reason, ok := knownAcceptableDiffs[key]; ok {
				fmt.Printf("    ○ column %q only in Go (known: %s)\n", c, reason)
			} else {
				r.details = append(r.details, fmt.Sprintf("column %q only in Go", c))
			}
		}
	}

	// Compare row counts
	pyCount := rowCount(pyConn, table)
	goCount := rowCount(goConn, table)

	if pyCount != goCount {
		r.details = append(r.details, fmt.Sprintf("row count: Python=%d, Go=%d", pyCount, goCount))
	}

	// Compare data (normalized)
	if pyCount > 0 || goCount > 0 {
		// Use shared columns for comparison
		sharedCols := sharedColumns(pyCols, goCols)
		dataDiffs := compareData(pyConn, goConn, table, sharedCols)
		r.details = append(r.details, dataDiffs...)
	}

	if len(r.details) == 0 {
		r.status = "PASS"
		fmt.Printf("  ✓ %-30s  rows: %d/%d  PASS\n", table, pyCount, goCount)
	} else {
		r.status = "FAIL"
		fmt.Printf("  ✗ %-30s  rows: %d/%d  FAIL\n", table, pyCount, goCount)
		for _, d := range r.details {
			fmt.Printf("      → %s\n", d)
		}
	}

	return r
}

func compareData(pyConn, goConn *sql.DB, table string, cols []columnInfo) []string {
	if len(cols) == 0 {
		return nil
	}

	// Build column list for SELECT, excluding normalized columns
	var selectCols []string
	for _, c := range cols {
		selectCols = append(selectCols, c.name)
	}
	colList := strings.Join(selectCols, ", ")

	// Determine order column (prefer id, then first column)
	orderCol := selectCols[0]
	for _, c := range selectCols {
		if c == "id" || strings.HasSuffix(c, "_id") {
			orderCol = c
			break
		}
	}

	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", colList, table, orderCol)

	pyRows := fetchNormalized(pyConn, query, selectCols)
	goRows := fetchNormalized(goConn, query, selectCols)

	var diffs []string
	maxCheck := len(pyRows)
	if len(goRows) > maxCheck {
		maxCheck = len(goRows)
	}
	if maxCheck > 50 {
		maxCheck = 50 // Limit detailed comparison
	}

	for i := 0; i < maxCheck; i++ {
		if i >= len(pyRows) {
			diffs = append(diffs, fmt.Sprintf("row %d: exists in Go but not Python", i+1))
			continue
		}
		if i >= len(goRows) {
			diffs = append(diffs, fmt.Sprintf("row %d: exists in Python but not Go", i+1))
			continue
		}
		for j, col := range selectCols {
			if normalizeColumns[col] {
				continue
			}
			pyVal := pyRows[i][j]
			goVal := goRows[i][j]
			if pyVal != goVal {
				diffs = append(diffs, fmt.Sprintf("row %d, col %q: Python=%q, Go=%q", i+1, col, pyVal, goVal))
			}
		}
	}

	if len(diffs) > 10 {
		truncated := diffs[:10]
		truncated = append(truncated, fmt.Sprintf("... and %d more differences", len(diffs)-10))
		return truncated
	}
	return diffs
}

func fetchNormalized(db *sql.DB, query string, cols []string) [][]string {
	rows, err := db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result [][]string
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			s := fmt.Sprintf("%v", v)
			if normalizeColumns[cols[i]] {
				s = normalizeValue(s)
			}
			row[i] = s
		}
		result = append(result, row)
	}
	return result
}

func normalizeValue(s string) string {
	// Replace UUIDs with placeholder
	s = uuidRe.ReplaceAllString(s, "<UUID>")
	// Replace ISO timestamps with placeholder
	if len(s) >= 19 && (s[4] == '-' || s[10] == 'T') {
		return "<TIMESTAMP>"
	}
	return s
}

// ── Helpers ──────────────────────────────────────────────────────────

type columnInfo struct {
	name    string
	colType string
}

func listTables(db *sql.DB) []string {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables = append(tables, name)
	}
	return tables
}

func getColumns(db *sql.DB, table string) []columnInfo {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var cols []columnInfo
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt interface{}
		rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk)
		cols = append(cols, columnInfo{name: name, colType: colType})
	}
	return cols
}

func colNames(cols []columnInfo) []string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	return names
}

func sharedColumns(pyCols, goCols []columnInfo) []columnInfo {
	goSet := make(map[string]bool)
	for _, c := range goCols {
		goSet[c.name] = true
	}
	var shared []columnInfo
	for _, c := range pyCols {
		if goSet[c.name] {
			shared = append(shared, c)
		}
	}
	return shared
}

func rowCount(db *sql.DB, table string) int {
	var count int
	db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
	return count
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

func printSummary(results []result) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println("  SUMMARY")
	fmt.Println("═══════════════════════════════════════════════════════")

	pass, fail, warn, skip := 0, 0, 0, 0
	var failedTables []string
	for _, r := range results {
		switch r.status {
		case "PASS":
			pass++
		case "FAIL":
			fail++
			failedTables = append(failedTables, r.table)
		case "WARN":
			warn++
		case "SKIP":
			skip++
		}
	}

	sort.Strings(failedTables)

	fmt.Printf("  PASS: %d  FAIL: %d  WARN: %d  SKIP: %d (known acceptable)\n", pass, fail, warn, skip)
	if len(failedTables) > 0 {
		fmt.Printf("  Failed tables: %s\n", strings.Join(failedTables, ", "))
	}
	fmt.Println("═══════════════════════════════════════════════════════")

	if fail > 0 {
		os.Exit(1)
	}
}
