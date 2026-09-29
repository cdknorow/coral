package coordination

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// FetchFromAPI queries the Coral server HTTP endpoints in read-only mode.
func FetchFromAPI(serverURL, board string) ([]Task, []Message, *BoardStatus, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	cleanServer := strings.TrimRight(serverURL, "/")

	// 1. Fetch tasks
	tasksURL := fmt.Sprintf("%s/api/board/%s/tasks", cleanServer, url.PathEscape(board))
	resp, err := client.Get(tasksURL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to fetch tasks from %s: %w", tasksURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, nil, fmt.Errorf("tasks endpoint returned HTTP %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read tasks body: %w", err)
	}
	var tasks []Task
	if err := json.Unmarshal(bodyBytes, &tasks); err != nil {
		var wrapper struct {
			Tasks []Task `json:"tasks"`
		}
		if err2 := json.Unmarshal(bodyBytes, &wrapper); err2 != nil {
			return nil, nil, nil, fmt.Errorf("failed to decode tasks JSON: %w", err)
		}
		tasks = wrapper.Tasks
	}

	// 2. Fetch messages
	msgsURL := fmt.Sprintf("%s/api/board/%s/messages/all?limit=500", cleanServer, url.PathEscape(board))
	resp2, err := client.Get(msgsURL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to fetch messages from %s: %w", msgsURL, err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return nil, nil, nil, fmt.Errorf("messages endpoint returned HTTP %d", resp2.StatusCode)
	}
	var messages []Message
	if err := json.NewDecoder(resp2.Body).Decode(&messages); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to decode messages JSON: %w", err)
	}

	// 3. Fetch board status / availability
	statusURL := fmt.Sprintf("%s/api/board/%s/status", cleanServer, url.PathEscape(board))
	resp3, err := client.Get(statusURL)
	var status *BoardStatus
	if err == nil && resp3.StatusCode == http.StatusOK {
		var s BoardStatus
		if err := json.NewDecoder(resp3.Body).Decode(&s); err == nil {
			status = &s
		}
		resp3.Body.Close()
	}

	return tasks, messages, status, nil
}

// FetchFromDB reads tasks, messages, and subscribers directly from SQLite in read-only mode.
func FetchFromDB(dbPath, board string) ([]Task, []Message, *BoardStatus, error) {
	// Connect in strict read-only mode with query pragma
	dsn := fmt.Sprintf("file:%s?mode=ro&_query_only=true", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open database at %s: %w", dbPath, err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Query Tasks
	taskRows, err := db.QueryContext(ctx, `SELECT id, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at FROM board_tasks WHERE board_id=? ORDER BY id ASC`, board)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to query tasks: %w", err)
	}
	defer taskRows.Close()

	var tasks []Task
	for taskRows.Next() {
		var t Task
		var body, assignedTo, completedBy, compMsg, claimedAt, completedAt sql.NullString
		if err := taskRows.Scan(&t.ID, &t.BoardID, &t.Title, &body, &t.Status, &t.Priority, &t.CreatedBy, &assignedTo, &completedBy, &compMsg, &t.CreatedAt, &claimedAt, &completedAt); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to scan task: %w", err)
		}
		if body.Valid {
			t.Body = &body.String
		}
		if assignedTo.Valid {
			t.AssignedTo = &assignedTo.String
		}
		if completedBy.Valid {
			t.CompletedBy = &completedBy.String
		}
		if compMsg.Valid {
			t.CompletionMessage = &compMsg.String
		}
		if claimedAt.Valid {
			t.ClaimedAt = &claimedAt.String
		}
		if completedAt.Valid {
			t.CompletedAt = &completedAt.String
		}

		// Read dependencies
		depRows, err := db.QueryContext(ctx, `SELECT blocked_by_task_id, blocked_by_board_id FROM task_dependencies WHERE task_id=?`, t.ID)
		if err == nil {
			for depRows.Next() {
				var dep TaskDep
				if err := depRows.Scan(&dep.TaskID, &dep.BoardID); err == nil {
					t.BlockedBy = append(t.BlockedBy, dep)
				}
			}
			depRows.Close()
		}

		// Read workflow if available
		var wfData sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT data FROM task_workflows WHERE task_id=?`, t.ID).Scan(&wfData); err == nil && wfData.Valid {
			var wf TaskWorkflow
			if err := json.Unmarshal([]byte(wfData.String), &wf); err == nil {
				t.Workflow = &wf
			}
		}

		tasks = append(tasks, t)
	}

	// 2. Query Messages
	msgRows, err := db.QueryContext(ctx, `SELECT id, project, subscriber_id, content, created_at FROM board_messages WHERE project=? ORDER BY id ASC`, board)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer msgRows.Close()

	var messages []Message
	for msgRows.Next() {
		var m Message
		var subID sql.NullString
		if err := msgRows.Scan(&m.ID, &m.Project, &subID, &m.Content, &m.CreatedAt); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to scan message: %w", err)
		}
		if subID.Valid {
			m.SubscriberID = subID.String
		}
		messages = append(messages, m)
	}

	// 3. Query Subscribers for Status
	subRows, err := db.QueryContext(ctx, `SELECT subscriber_id, job_title, is_active FROM board_subscribers WHERE project=?`, board)
	var agents []SubscriberStatus
	if err == nil {
		defer subRows.Close()
		for subRows.Next() {
			var subID, role string
			var isActive int
			if err := subRows.Scan(&subID, &role, &isActive); err == nil {
				avail := "available"
				if isActive == 0 {
					avail = "offline"
				}
				agents = append(agents, SubscriberStatus{
					SubscriberID: subID,
					Name:         subID,
					Role:         role,
					Availability: avail,
					Available:    isActive == 1,
				})
			}
		}
	}

	status := &BoardStatus{
		Board:      board,
		Agents:     agents,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}

	return tasks, messages, status, nil
}
