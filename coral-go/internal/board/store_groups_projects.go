// Groups, message deletion and projects (moved out of store.go, #4998; no behaviour change).

package board

import "context"

// AddToGroup adds a subscriber to a board group.
func (s *Store) AddToGroup(ctx context.Context, project, groupID, subscriberID string) error {
	s.unreadCounts.invalidate() // before and after, as for message deletes
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO board_groups (project, group_id, session_id, subscriber_id) VALUES (?, ?, ?, ?)
		 ON CONFLICT(project, group_id, session_id) DO NOTHING`,
		project, groupID, subscriberID, subscriberID)
	s.unreadCounts.invalidate()
	return err
}

// RemoveFromGroup removes a subscriber from a board group. Returns true if removed.
func (s *Store) RemoveFromGroup(ctx context.Context, project, groupID, subscriberID string) (bool, error) {
	s.unreadCounts.invalidate() // before and after, as for message deletes
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM board_groups WHERE project = ? AND group_id = ? AND subscriber_id = ?",
		project, groupID, subscriberID)
	s.unreadCounts.invalidate()
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// ListGroupMembers returns subscriber_ids in a group.
func (s *Store) ListGroupMembers(ctx context.Context, project, groupID string) ([]string, error) {
	var members []string
	err := s.reader().SelectContext(ctx, &members,
		"SELECT subscriber_id FROM board_groups WHERE project = ? AND group_id = ? ORDER BY subscriber_id",
		project, groupID)
	if err != nil {
		return []string{}, nil
	}
	return members, nil
}

// ListGroups returns all groups for a project with member counts.
func (s *Store) ListGroups(ctx context.Context, project string) ([]GroupInfo, error) {
	var groups []GroupInfo
	err := s.reader().SelectContext(ctx, &groups,
		`SELECT group_id, COUNT(*) as member_count
		 FROM board_groups WHERE project = ?
		 GROUP BY group_id ORDER BY group_id`,
		project)
	if err != nil {
		return []GroupInfo{}, nil
	}
	return groups, nil
}

// DeleteMessage deletes a single message by ID.
// DeleteMessage deletes one message of a board: an id from another board deletes nothing
// (#3668, the board authorization audit's finding 1).
func (s *Store) DeleteMessage(ctx context.Context, project string, messageID int64) (bool, error) {
	s.unreadCounts.invalidate()
	// The index rows and the message go in one transaction: a delete that fails leaves the message indexed (before, the
	// rows went first and a failed delete left a message nobody could find by its mentions; Security's #4523 INFO).
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	forgetMentionTokens(ctx, tx, "id = ? AND project = ?", messageID, project)
	result, err := tx.ExecContext(ctx, "DELETE FROM board_messages WHERE id = ? AND project = ?", messageID, project)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.unreadCounts.invalidate()
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// ListProjects returns all known projects with subscriber and message counts.
func (s *Store) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	var projects []ProjectInfo
	err := s.reader().SelectContext(ctx, &projects,
		`SELECT project,
		        COALESCE((SELECT b.created_at FROM boards b WHERE b.id = p.project), '') as created_at,
		        (SELECT COUNT(*) FROM board_subscribers s WHERE s.project = p.project AND s.is_active = 1) as subscriber_count,
		        (SELECT COUNT(*) FROM board_messages m WHERE m.project = p.project) as message_count,
		        (SELECT COUNT(*) FROM board_goals g WHERE g.board_id = p.project AND g.state IN ('proposed', 'active', 'blocked')) as goals_open,
		        COALESCE((SELECT seq FROM board_goal_meta gm WHERE gm.board_id = p.project), 0) as goals_seq
		 FROM (
		     SELECT DISTINCT project FROM board_subscribers WHERE is_active = 1
		     UNION
		     SELECT DISTINCT project FROM board_messages
		 ) p ORDER BY project`)
	if err == nil {
		ids := make([]string, len(projects))
		for i := range projects {
			ids[i] = projects[i].Project
		}
		labels := s.BoardLabels(ctx, ids)
		for i := range projects {
			projects[i].BoardLabel = labels[projects[i].Project]
			projects[i].BoardID = projects[i].Project
			projects[i].Goals = &GoalsBrief{Open: projects[i].GoalsOpen, Seq: projects[i].GoalsSeq}
		}
	}
	return projects, err
}

// EnrichedProject holds project info with timestamps and participant names.
type EnrichedProject struct {
	Project          string  `db:"project" json:"project"`
	BoardLabel       string  `db:"-" json:"board_label"` // the board's display name (#3536)
	SubscriberCount  int     `db:"subscriber_count" json:"subscriber_count"`
	MessageCount     int     `db:"message_count" json:"message_count"`
	FirstMessageAt   *string `db:"first_message_at" json:"first_message_at"`
	LastMessageAt    *string `db:"last_message_at" json:"last_message_at"`
	ParticipantNames *string `db:"participant_names" json:"participant_names"`
}

// ListProjectsEnriched returns board projects with timestamps, subscriber info, and participant names.
func (s *Store) ListProjectsEnriched(ctx context.Context) ([]EnrichedProject, error) {
	var projects []EnrichedProject
	err := s.reader().SelectContext(ctx, &projects,
		`SELECT
		     p.project,
		     (SELECT COUNT(*) FROM board_subscribers s WHERE s.project = p.project AND s.is_active = 1) as subscriber_count,
		     (SELECT COUNT(*) FROM board_messages m WHERE m.project = p.project) as message_count,
		     -- The first and last rows by id: a seek on (project, id) instead of
		     -- scanning every message's created_at. Messages are stamped at
		     -- insert, so id order is time order.
		     (SELECT created_at FROM board_messages m WHERE m.project = p.project ORDER BY m.id ASC LIMIT 1) as first_message_at,
		     (SELECT created_at FROM board_messages m WHERE m.project = p.project ORDER BY m.id DESC LIMIT 1) as last_message_at,
		     (SELECT GROUP_CONCAT(s.job_title, ', ')
		      FROM board_subscribers s WHERE s.project = p.project AND s.is_active = 1
		      ORDER BY s.subscribed_at, s.id LIMIT 5) as participant_names
		 FROM (
		     SELECT DISTINCT project FROM board_subscribers WHERE is_active = 1
		     UNION
		     SELECT DISTINCT project FROM board_messages
		 ) p
		 ORDER BY last_message_at DESC, p.project`)
	if err == nil {
		ids := make([]string, len(projects))
		for i := range projects {
			ids[i] = projects[i].Project
		}
		labels := s.BoardLabels(ctx, ids)
		for i := range projects {
			projects[i].BoardLabel = labels[projects[i].Project]
		}
	}
	return projects, err
}

// SearchMessages returns project names that have messages matching the query (LIKE search).
func (s *Store) SearchMessages(ctx context.Context, query string) ([]string, error) {
	var projects []string
	// Each board stops at its first match (#5354): a term that is common is found in the first rows of every board instead of scanning all of them.
	// The boards themselves are found by skipping through the (project, id) index, one seek per board, not by reading every row's entry.
	err := s.reader().SelectContext(ctx, &projects,
		`WITH RECURSIVE boards(project) AS (
		     SELECT MIN(project) FROM board_messages
		     UNION ALL
		     SELECT (SELECT MIN(project) FROM board_messages WHERE project > boards.project) FROM boards WHERE boards.project IS NOT NULL
		 )
		 SELECT b.project FROM boards b
		 WHERE b.project IS NOT NULL AND EXISTS (SELECT 1 FROM board_messages m WHERE m.project = b.project AND m.content LIKE ? COLLATE NOCASE)`,
		"%"+query+"%")
	if err != nil {
		return []string{}, nil
	}
	return projects, nil
}

// DeleteProject deletes a board entirely, in one transaction (#4966): its messages, subscribers, groups, reminders and waits, its tasks with everything
// that hangs off them (amendments, workflow data, dependency rules, ready notices, and the dependency links to and from them: a task on another
// board that was blocked by one of these is no longer blocked), its presets, working mode, settings (the pause, the health check, cross-team),
// health counters and items, and its row in the boards table. Two audit trails stay: hub_operator_actions (the Hub's record of what its operators did) and
// health_actions (what Coral's health rules did): a record of what happened is not part of the board. A board made again under the same name starts empty.
func (s *Store) DeleteProject(ctx context.Context, project string) error {
	_, err := s.deleteProject(ctx, project, false)
	return err
}

// DeleteProjectIfUnused is DeleteProject for a board whose only maker is taking it back (a team launch that started nobody, #5762): it
// deletes the board only when, in the same transaction, it holds no message, task, member, group, reminder or wait. A board that something else
// has used meanwhile (another launch, an agent that joined, a message posted) is kept, with what it holds. deleted says which.
func (s *Store) DeleteProjectIfUnused(ctx context.Context, project string) (deleted bool, err error) {
	return s.deleteProject(ctx, project, true)
}

func (s *Store) deleteProject(ctx context.Context, project string, onlyIfUnused bool) (bool, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if onlyIfUnused {
		for _, table := range []struct{ name, col string }{{"board_messages", "project"}, {"board_tasks", "board_id"}, {"board_subscribers", "project"}, {"board_groups", "project"},
			{"board_reminders", "project"}, {"board_waits", "project"}, {"board_goals", "board_id"}} {
			var n int
			if err := tx.GetContext(ctx, &n, "SELECT COUNT(*) FROM "+table.name+" WHERE "+table.col+" = ?", project); err != nil {
				return false, err
			}
			if n > 0 {
				return false, nil
			}
		}
	}
	s.unreadCounts.invalidate()
	forgetMentionTokens(ctx, tx, "project = ?", project)
	const mine = `(SELECT id FROM board_tasks WHERE board_id = ?)`
	for _, q := range []struct {
		sql  string
		args int // how many times the project is bound
	}{
		{"DELETE FROM task_amendments WHERE task_id IN " + mine, 1},
		{"DELETE FROM task_dependencies WHERE task_id IN " + mine + " OR blocked_by_task_id IN " + mine, 2},
		{"DELETE FROM task_workflows WHERE task_id IN " + mine, 1},
		{"DELETE FROM task_dependency_rules WHERE task_id IN " + mine + " OR upstream_id IN " + mine, 2},
		{"DELETE FROM task_ready_notifications WHERE task_id IN " + mine, 1},
		{"DELETE FROM board_tasks WHERE board_id = ?", 1},
		{"DELETE FROM board_goals WHERE board_id = ?", 1},
		{"DELETE FROM board_goal_meta WHERE board_id = ?", 1},
		{"DELETE FROM board_messages WHERE project = ?", 1},
		{"DELETE FROM board_subscribers WHERE project = ?", 1},
		{"DELETE FROM board_reminders WHERE project = ?", 1},
		{"DELETE FROM board_workflow_presets WHERE board_id = ?", 1},
		{"DELETE FROM board_working_modes WHERE board_id = ?", 1},
		{"DELETE FROM board_groups WHERE project = ?", 1},
		{"DELETE FROM board_waits WHERE project = ?", 1},
		{"DELETE FROM board_settings WHERE project = ?", 1},
		{"DELETE FROM board_settings_log WHERE board_id = ?", 1},
		{"DELETE FROM health_counters WHERE project = ?", 1},
		{"DELETE FROM health_items WHERE project = ?", 1},
		{"DELETE FROM boards WHERE id = ?", 1},
	} {
		args := make([]any, q.args)
		for i := range args {
			args[i] = project
		}
		if _, err := tx.ExecContext(ctx, q.sql, args...); err != nil {
			return false, err
		}
	}
	err = tx.Commit()
	s.unreadCounts.invalidate()
	s.subscribersChanged()
	return err == nil, err
}
