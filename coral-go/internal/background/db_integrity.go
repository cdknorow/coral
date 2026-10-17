package background

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// Database integrity check (#4157, item 2 of the #4151 report): a corrupt page in a table no
// start reads went unnoticed until something read it. A minute after start, PRAGMA quick_check
// runs once on each database's read pool (a read: it never blocks writes for long and changes
// nothing). Damage is logged and shown on the dashboard with what the user can do; Coral never
// repairs or deletes anything on its own.

// quickCheckLimit bounds the problems quick_check reports per database.
const quickCheckLimit = 5

// QuickCheck runs PRAGMA quick_check on db and returns the problems it reports: none when the
// database is sound.
func QuickCheck(ctx context.Context, db *sqlx.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA quick_check(%d)", quickCheckLimit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		// One row can hold several lines, headed by "*** in database main ***": the lines
		// after the header are the problems.
		for _, l := range strings.Split(line, "\n") {
			if l = strings.TrimSpace(l); l != "" && l != "ok" && !strings.HasPrefix(l, "*** in database") {
				problems = append(problems, l)
			}
		}
	}
	return problems, rows.Err()
}

// DBIntegrity is the one-time check.
type DBIntegrity struct {
	targets map[string]*sqlx.DB // by file name
	First   time.Duration
}

// NewDBIntegrity checks targets (file name to read pool) a minute after start.
func NewDBIntegrity(targets map[string]*sqlx.DB) *DBIntegrity {
	return &DBIntegrity{targets: targets, First: time.Minute}
}

// Run waits First, checks every database once and returns; ctx (Coral stopping) cancels both
// the wait and a check under way.
func (j *DBIntegrity) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(j.First):
	}
	j.RunOnce(ctx)
}

// RunOnce checks every database, in name order, and records what it finds.
func (j *DBIntegrity) RunOnce(ctx context.Context) {
	names := make([]string, 0, len(j.targets))
	for n := range j.targets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		start := time.Now()
		problems, err := QuickCheck(ctx, j.targets[name])
		switch {
		case ctx.Err() != nil:
			return
		case err != nil && isCorruption(err):
			problems = []string{err.Error()}
		case err != nil:
			log.Printf("[integrity] could not check %s: %v", name, err)
			continue
		}
		if len(problems) == 0 {
			log.Printf("[integrity] %s: ok (%s)", name, time.Since(start).Round(time.Millisecond))
			integrity.set(name, "")
			continue
		}
		log.Printf("[integrity] %s is damaged: %s. Coral has not changed it.", name, strings.Join(problems, "; "))
		integrity.set(name, problems[0])
	}
}

// isCorruption reports whether a failed check is itself the damage (SQLite stops with
// "database disk image is malformed" or "file is not a database").
func isCorruption(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "malformed") || strings.Contains(s, "not a database") || strings.Contains(s, "corrupt")
}

// integrity holds the problems found since start, by database file name.
var integrity = &integrityState{problems: map[string]string{}}

type integrityState struct {
	mu       sync.Mutex
	problems map[string]string
}

func (s *integrityState) set(name, problem string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if problem == "" {
		delete(s.problems, name)
	} else {
		s.problems[name] = problem
	}
}

// DatabaseWarning is the dashboard's notice when a check found damage, "" otherwise. It says
// what the user can do: Coral itself never repairs or deletes.
func DatabaseWarning() string {
	integrity.mu.Lock()
	defer integrity.mu.Unlock()
	if len(integrity.problems) == 0 {
		return ""
	}
	names := make([]string, 0, len(integrity.problems))
	for n := range integrity.problems {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "Coral found damage in %s (%s).", n, integrity.problems[n])
	}
	b.WriteString(" Coral has not changed anything. Copy the data folder somewhere safe, then stop Coral and run \"coral db compact\": it rebuilds sessions.db after writing a backup of it, and stops without changing anything when the damage is too deep to copy. If it stops, or the damage is in messageboard.db, restore that file from a backup (sqlite3's .recover can save what is still readable). The details are in Coral's log (coral.log, or tray.log when the tray started Coral).")
	return b.String()
}
