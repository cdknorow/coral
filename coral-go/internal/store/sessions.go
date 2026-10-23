package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// SessionMeta holds notes and summary metadata for a session.
type SessionMeta struct {
	SessionID    string  `db:"session_id" json:"session_id"`
	NotesMD      string  `db:"notes_md" json:"notes_md"`
	AutoSummary  string  `db:"auto_summary" json:"auto_summary"`
	IsUserEdited bool    `db:"is_user_edited" json:"is_user_edited"`
	UpdatedAt    *string `db:"updated_at" json:"updated_at,omitempty"`
}

// Tag represents a user-defined tag.
type Tag struct {
	ID    int64  `db:"id" json:"id"`
	Name  string `db:"name" json:"name"`
	Color string `db:"color" json:"color"`
}

// SessionIndex holds indexed session info for search and listing.
type SessionIndex struct {
	SessionID      string  `db:"session_id" json:"session_id"`
	SourceType     string  `db:"source_type" json:"source_type"`
	SourceFile     string  `db:"source_file" json:"source_file"`
	FirstTimestamp *string `db:"first_timestamp" json:"first_timestamp"`
	LastTimestamp  *string `db:"last_timestamp" json:"last_timestamp"`
	MessageCount   int     `db:"message_count" json:"message_count"`
	DisplaySummary string  `db:"display_summary" json:"summary"`
	// FirstUserMessage is the first thing the person typed in the session ("" for a row indexed before it was kept), the lead of its History title (#6207).
	FirstUserMessage string  `db:"first_user_message" json:"first_user_message,omitempty"`
	AgentName        string  `db:"agent_name" json:"agent_name,omitempty"`
	DisplayName      string  `db:"display_name" json:"display_name,omitempty"`
	IndexedAt        string  `db:"indexed_at" json:"-"`
	FileMtime        float64 `db:"file_mtime" json:"-"`
	IndexVersion     int     `db:"index_version" json:"-"`
}

// LiveSession represents a persistent live session record.
type LiveSession struct {
	SessionID    string  `db:"session_id" json:"session_id"`
	AgentType    string  `db:"agent_type" json:"agent_type"`
	AgentName    string  `db:"agent_name" json:"agent_name"`
	WorkingDir   string  `db:"working_dir" json:"working_dir"`
	DisplayName  *string `db:"display_name" json:"display_name,omitempty"`
	ResumeFromID *string `db:"resume_from_id" json:"resume_from_id,omitempty"`
	Flags        *string `db:"flags" json:"flags,omitempty"`
	IsJob        int     `db:"is_job" json:"is_job"`
	Prompt       *string `db:"prompt" json:"prompt,omitempty"`
	BoardName    *string `db:"board_name" json:"board_name,omitempty"`
	BoardServer  *string `db:"board_server" json:"board_server,omitempty"`
	Backend      *string `db:"backend" json:"launch_path,omitempty"` // the launch path ("pty" means through the TerminalBackend, tmux's included), not the terminal the agent runs on (#6146)
	Icon         *string `db:"icon" json:"icon,omitempty"`
	NameColor    *string `db:"name_color" json:"name_color,omitempty"`
	IsSleeping   int     `db:"is_sleeping" json:"is_sleeping"`
	IsArchived   int     `db:"is_archived" json:"is_archived"`
	SleepReason  *string `db:"sleep_reason" json:"sleep_reason,omitempty"`
	BoardType    *string `db:"board_type" json:"board_type,omitempty"`
	GitDiffMode  *string `db:"git_diff_mode" json:"git_diff_mode,omitempty"`
	Capabilities *string `db:"capabilities" json:"capabilities,omitempty"`
	Model        *string `db:"model" json:"model,omitempty"`
	// PermissionMode is the effective permission mode the agent was launched with (#5188).
	PermissionMode *string `db:"permission_mode" json:"permission_mode,omitempty"`
	// Thinking is the thinking (reasoning effort) level the agent was started with: "" or NULL is the CLI's own default (#4969).
	Thinking      *string `db:"thinking" json:"thinking,omitempty"`
	ContextWindow int     `db:"context_window" json:"context_window,omitempty"`
	// ObservedModel is the model a hook last reported (agy); display only, never a launch setting.
	ObservedModel *string `db:"observed_model" json:"-"`
	Tools         *string `db:"tools" json:"tools,omitempty"`
	PID           int     `db:"pid" json:"pid,omitempty"`
	WorktreePath  *string `db:"worktree_path" json:"worktree_path,omitempty"`
	WorktreeRepo  *string `db:"worktree_repo" json:"worktree_repo,omitempty"`
	TeamID        *int64  `db:"team_id" json:"team_id,omitempty"`
	CreatedAt     string  `db:"created_at" json:"created_at"`
	StoppedAt     *string `db:"stopped_at" json:"stopped_at,omitempty"`
	Status        string  `db:"status" json:"status,omitempty"`
}

// UserSetting is a key-value pair.
type UserSetting struct {
	Key   string `db:"key" json:"key"`
	Value string `db:"value" json:"value"`
}

// SessionStore provides session-related database operations.
type SessionStore struct {
	db *DB
}

// NewSessionStore creates a new SessionStore.
func NewSessionStore(db *DB) *SessionStore {
	return &SessionStore{db: db}
}

// DB returns the underlying database connection.
func (s *SessionStore) DB() *DB {
	return s.db
}

func (s *SessionStore) reader() *sqlx.DB {
	return s.db.Reader()
}

// ── Live Session Flags Helper ──────────────────────────────────────────

// MarshalFlags serializes a flags slice to JSON.
func MarshalFlags(flags []string) *string {
	if len(flags) == 0 {
		return nil
	}
	b, err := json.Marshal(flags)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// MarshalCapabilities serializes capabilities to a JSON string pointer for DB storage.
func MarshalCapabilities(caps any) *string {
	if caps == nil {
		return nil
	}
	b, err := json.Marshal(caps)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// UnmarshalFlags deserializes a JSON flags string to a slice.
func UnmarshalFlags(flags *string) []string {
	if flags == nil || *flags == "" {
		return nil
	}
	var result []string
	if err := json.Unmarshal([]byte(*flags), &result); err != nil {
		return nil
	}
	return result
}

// ── Helpers ────────────────────────────────────────────────────────────

var headerRE = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)

func extractFirstHeader(text string) string {
	if text == "" {
		return ""
	}
	m := headerRE.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

var tsFormats = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
}

func parseTimestamp(ts string) (time.Time, error) {
	for _, fmt := range tsFormats {
		if t, err := time.Parse(fmt, ts); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp format: %s", ts)
}

func computeDuration(firstTS, lastTS *string) *int {
	if firstTS == nil || lastTS == nil || *firstTS == "" || *lastTS == "" {
		return nil
	}

	a, err := parseTimestamp(*firstTS)
	if err != nil {
		return nil
	}
	b, err := parseTimestamp(*lastTS)
	if err != nil {
		return nil
	}

	delta := int(b.Sub(a).Seconds())
	if delta < 0 {
		delta = 0
	}
	return &delta
}

// ISOFormat matches Python's datetime.isoformat() which includes microseconds and uses +00:00 instead of Z for UTC.
const ISOFormat = "2006-01-02T15:04:05.000000+00:00"

func nowUTC() string {
	return time.Now().UTC().Format(ISOFormat)
}

// NowUTC returns the current time as an ISO 8601 UTC string (exported for use by route handlers).
func NowUTC() string {
	return nowUTC()
}
