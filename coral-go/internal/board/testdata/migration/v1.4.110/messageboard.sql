PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE board_subscribers (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project       TEXT NOT NULL,
			session_id    TEXT NOT NULL,
			job_title     TEXT NOT NULL,
			webhook_url   TEXT,
			origin_server TEXT,
			receive_mode  TEXT NOT NULL DEFAULT 'mentions',
			last_read_id  INTEGER NOT NULL DEFAULT 0,
			subscribed_at TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1, can_peek INTEGER NOT NULL DEFAULT 0, subscriber_id TEXT, session_name TEXT, session_joined_at TEXT,
			UNIQUE(project, session_id)
		);
INSERT INTO board_subscribers VALUES(1,'fixture-board','fixture-sub-1','Fixture Role 1',NULL,NULL,'mentions',0,'fixture-board_subscribers-subscribed_at-1',1,0,NULL,NULL,NULL);
INSERT INTO board_subscribers VALUES(2,'fixture-board','fixture-sub-2','Fixture Role 2',NULL,NULL,'mentions',0,'fixture-board_subscribers-subscribed_at-2',1,0,NULL,NULL,NULL);
CREATE TABLE board_messages (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project     TEXT NOT NULL,
			session_id  TEXT NOT NULL,
			content     TEXT NOT NULL,
			created_at  TEXT NOT NULL
		, target_group_id TEXT, subscriber_id TEXT, external_json TEXT, origin TEXT, is_system INTEGER NOT NULL DEFAULT 0, hub_user TEXT);
INSERT INTO board_messages VALUES(1,'fixture-board','fixture-board_messages-session_id-1','fixture message 1','2025-01-01T00:00:00Z',NULL,'Fixture Role 1',NULL,NULL,0,NULL);
INSERT INTO board_messages VALUES(2,'fixture-board','fixture-board_messages-session_id-2','fixture message 2','2025-01-01T00:00:00Z',NULL,'Fixture Role 1',NULL,NULL,0,NULL);
INSERT INTO board_messages VALUES(3,'fixture-board','fixture-board_messages-session_id-3','fixture message 3','2025-01-01T00:00:00Z',NULL,'Fixture Role 1',NULL,NULL,0,NULL);
INSERT INTO board_messages VALUES(4,'fixture-board','fixture-board_messages-session_id-4','fixture message 4','2025-01-01T00:00:00Z',NULL,'Fixture Role 1',NULL,NULL,0,NULL);
CREATE TABLE board_reminders (
			project TEXT NOT NULL,
			subscriber_id TEXT NOT NULL,
			message TEXT NOT NULL,
			interval_seconds INTEGER NOT NULL,
			PRIMARY KEY(project, subscriber_id)
		);
CREATE TABLE board_groups (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			project    TEXT NOT NULL,
			group_id   TEXT NOT NULL,
			session_id TEXT NOT NULL, subscriber_id TEXT,
			UNIQUE(project, group_id, session_id)
		);
CREATE TABLE board_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			revision INTEGER NOT NULL DEFAULT 1,
			board_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT,
			status TEXT NOT NULL DEFAULT 'pending'
				CHECK (status IN ('pending', 'in_progress', 'completed', 'skipped', 'blocked', 'draft', 'review_pending')),
			priority TEXT NOT NULL DEFAULT 'medium'
				CHECK (priority IN ('critical', 'high', 'medium', 'low')),
			created_by TEXT NOT NULL,
			assigned_to TEXT,
			completed_by TEXT,
			completion_message TEXT,
			created_at TEXT NOT NULL,
			claimed_at TEXT,
			completed_at TEXT,
			last_activity_at TEXT,
			idle_snoozed_until TEXT
		, session_id TEXT, cost_usd REAL, input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER, cache_write_tokens INTEGER, parked_reason TEXT, parked_until TEXT, remind_at TEXT, reminded_at TEXT);
INSERT INTO board_tasks VALUES(1,1,'fixture-board','Fixture task 1','fixture body','pending','medium','Fixture Role 1',NULL,NULL,NULL,'2025-01-01T00:00:00Z',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO board_tasks VALUES(2,1,'fixture-board','Fixture task 2','fixture body','pending','medium','Fixture Role 1',NULL,NULL,NULL,'2025-01-01T00:00:00Z',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO board_tasks VALUES(3,1,'fixture-board','Fixture task 3','fixture body','pending','medium','Fixture Role 1',NULL,NULL,NULL,'2025-01-01T00:00:00Z',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
CREATE TABLE task_amendments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL REFERENCES board_tasks(id),
			revision INTEGER NOT NULL,
			actor TEXT NOT NULL,
			created_at TEXT NOT NULL,
			reason TEXT NOT NULL,
			changes_json TEXT NOT NULL,
			previous_snapshot_json TEXT NOT NULL,
			effective_snapshot_json TEXT NOT NULL,
			UNIQUE(task_id, revision)
		);
CREATE TABLE task_dependencies (
			task_id INTEGER NOT NULL,
			blocked_by_task_id INTEGER NOT NULL,
			blocked_by_board_id TEXT NOT NULL,
			PRIMARY KEY (task_id, blocked_by_task_id),
			FOREIGN KEY (task_id) REFERENCES board_tasks(id),
			FOREIGN KEY (blocked_by_task_id) REFERENCES board_tasks(id)
		);
CREATE TABLE board_waits (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			project        TEXT NOT NULL,
			subscriber_id  TEXT NOT NULL,
			session_name   TEXT NOT NULL,
			wait_type      TEXT NOT NULL DEFAULT 'message',
			target_id      TEXT NOT NULL DEFAULT '',
			reason         TEXT NOT NULL DEFAULT '',
			status         TEXT NOT NULL DEFAULT 'active'
				CHECK (status IN ('active', 'resolved', 'cancelled', 'expired')),
			created_at     TEXT NOT NULL,
			resolved_at    TEXT,
			expires_at     TEXT NOT NULL
		);
CREATE TABLE board_settings (
		project TEXT NOT NULL,
		key     TEXT NOT NULL,
		value   TEXT NOT NULL,
		PRIMARY KEY (project, key)
	);
INSERT INTO board_settings VALUES('~store','migration:health_default_off','done');
CREATE TABLE board_relayed_messages (
		source_server     TEXT NOT NULL,
		message_id        TEXT NOT NULL,
		target_message_id INTEGER NOT NULL,
		created_at        TEXT NOT NULL,
		PRIMARY KEY (source_server, message_id)
	);
CREATE TABLE hub_operator_actions (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		at       TEXT NOT NULL,
		hub_user TEXT NOT NULL,
		method   TEXT NOT NULL,
		path     TEXT NOT NULL,
		project  TEXT NOT NULL,
		status   INTEGER NOT NULL
	);
CREATE TABLE health_actions (
	project TEXT NOT NULL,
	rule    TEXT NOT NULL,
	target  TEXT NOT NULL,
	at      TEXT NOT NULL
);
CREATE TABLE health_counters (
	project   TEXT NOT NULL,
	rule      TEXT NOT NULL,
	target    TEXT NOT NULL,
	subject   TEXT NOT NULL,
	count     INTEGER NOT NULL,
	last_at   TEXT NOT NULL,
	escalated INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (project, rule, target, subject)
);
CREATE TABLE health_items (
	project     TEXT NOT NULL,
	item        TEXT NOT NULL,
	state       TEXT NOT NULL,
	shown_at    TEXT NOT NULL,
	acked_state TEXT,
	acked_by    TEXT,
	acked_at    TEXT,
	PRIMARY KEY (project, item)
);
CREATE TABLE board_working_modes (board_id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE board_workflow_presets (board_id TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL, instructions TEXT NOT NULL, PRIMARY KEY(board_id,id));
CREATE TABLE task_workflows (
	 task_id INTEGER PRIMARY KEY REFERENCES board_tasks(id), data TEXT NOT NULL);
CREATE TABLE task_dependency_rules (
	 task_id INTEGER NOT NULL, upstream_id INTEGER NOT NULL,
	 condition TEXT NOT NULL, required_artifacts TEXT NOT NULL,
	 PRIMARY KEY(task_id, upstream_id));
CREATE TABLE task_ready_notifications (
	 task_id INTEGER PRIMARY KEY REFERENCES board_tasks(id));
CREATE TABLE boards (
			id         TEXT PRIMARY KEY,
			label      TEXT NOT NULL,
			created_at TEXT NOT NULL,
			created_by TEXT NOT NULL DEFAULT ''
		);
CREATE TABLE board_rekeys (
		old_id TEXT PRIMARY KEY, new_id TEXT NOT NULL, rekeyed_at TEXT NOT NULL);
CREATE TABLE board_message_mentions (
			token      TEXT NOT NULL,
			message_id INTEGER NOT NULL,
			PRIMARY KEY (token, message_id)
		) WITHOUT ROWID;
CREATE TABLE board_mention_index_state (
			id         INTEGER PRIMARY KEY CHECK (id = 1),
			indexed_to INTEGER NOT NULL
		);
INSERT INTO board_mention_index_state VALUES(1,0);
INSERT INTO sqlite_sequence VALUES('board_subscribers',2);
INSERT INTO sqlite_sequence VALUES('board_messages',4);
INSERT INTO sqlite_sequence VALUES('board_tasks',3);
CREATE INDEX idx_board_messages_project ON board_messages(project, id);
CREATE INDEX idx_board_groups_project_group ON board_groups(project, group_id);
CREATE INDEX idx_board_tasks_board_status ON board_tasks(board_id, status);
CREATE INDEX idx_board_tasks_assigned ON board_tasks(board_id, assigned_to);
CREATE INDEX idx_board_tasks_done ON board_tasks(board_id, completed_at, id)
			WHERE status IN ('completed', 'skipped');
CREATE INDEX idx_task_deps_blocked_by ON task_dependencies(blocked_by_task_id);
CREATE INDEX idx_board_subscribers_session_name ON board_subscribers(session_name);
CREATE INDEX idx_board_subscribers_subscriber ON board_subscribers(subscriber_id, project);
CREATE INDEX idx_board_waits_active ON board_waits(project, status);
CREATE INDEX idx_board_waits_subscriber ON board_waits(project, subscriber_id, status);
CREATE INDEX idx_health_actions ON health_actions(project, at);
CREATE INDEX idx_boards_label ON boards(label);
COMMIT;
