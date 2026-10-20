/* WARNING: Script requires that SQLITE_DBCONFIG_DEFENSIVE be disabled */
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE agent_ui_panels (
 session_id TEXT NOT NULL, id TEXT NOT NULL, title TEXT NOT NULL,
 html TEXT NOT NULL, revision INTEGER NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(session_id,id)
);
CREATE TABLE agent_ui_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL,
 panel_id TEXT NOT NULL, revision INTEGER NOT NULL, action TEXT NOT NULL,
 payload TEXT NOT NULL, created_at TEXT NOT NULL,
 FOREIGN KEY(session_id,panel_id) REFERENCES agent_ui_panels(session_id,id) ON DELETE CASCADE
);
CREATE TABLE session_meta (
	session_id   TEXT PRIMARY KEY,
	notes_md     TEXT DEFAULT '',
	auto_summary TEXT DEFAULT '',
	is_user_edited INTEGER DEFAULT 0,
	display_name TEXT,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);
CREATE TABLE tags (
	id    INTEGER PRIMARY KEY AUTOINCREMENT,
	name  TEXT UNIQUE NOT NULL,
	color TEXT NOT NULL DEFAULT '#58a6ff'
);
CREATE TABLE session_tags (
	session_id TEXT NOT NULL,
	tag_id     INTEGER NOT NULL,
	PRIMARY KEY (session_id, tag_id),
	FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
);
CREATE TABLE folder_tags (
	folder_name TEXT NOT NULL,
	tag_id      INTEGER NOT NULL,
	PRIMARY KEY (folder_name, tag_id),
	FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
);
CREATE TABLE pinned_folders (
	path      TEXT PRIMARY KEY,
	pinned_at TEXT NOT NULL
);
CREATE TABLE session_index (
	session_id      TEXT PRIMARY KEY,
	source_type     TEXT NOT NULL,
	source_file     TEXT NOT NULL,
	first_timestamp TEXT,
	last_timestamp  TEXT,
	message_count   INTEGER DEFAULT 0,
	display_summary TEXT DEFAULT '',
	indexed_at      TEXT NOT NULL,
	file_mtime      REAL NOT NULL
, agent_name TEXT, display_name TEXT);
CREATE TABLE IF NOT EXISTS 'session_fts_data'(id INTEGER PRIMARY KEY, block BLOB);
INSERT INTO session_fts_data VALUES(1,X'');
INSERT INTO session_fts_data VALUES(10,X'00000000000000');
CREATE TABLE IF NOT EXISTS 'session_fts_idx'(segid, term, pgno, PRIMARY KEY(segid, term)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS 'session_fts_content'(id INTEGER PRIMARY KEY, c0, c1);
CREATE TABLE IF NOT EXISTS 'session_fts_docsize'(id INTEGER PRIMARY KEY, sz BLOB);
CREATE TABLE IF NOT EXISTS 'session_fts_config'(k PRIMARY KEY, v) WITHOUT ROWID;
INSERT INTO session_fts_config VALUES('version',4);
CREATE TABLE summarizer_queue (
	session_id   TEXT PRIMARY KEY,
	status       TEXT NOT NULL DEFAULT 'pending',
	attempted_at TEXT,
	error_msg    TEXT
);
CREATE TABLE git_snapshots (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name        TEXT NOT NULL,
	agent_type        TEXT NOT NULL,
	working_directory TEXT NOT NULL,
	branch            TEXT NOT NULL,
	commit_hash       TEXT NOT NULL,
	commit_subject    TEXT DEFAULT '',
	commit_timestamp  TEXT,
	session_id        TEXT,
	remote_url        TEXT,
	recorded_at       TEXT NOT NULL, pr_number INTEGER, is_worktree INTEGER DEFAULT 0, base_branch TEXT DEFAULT '', ahead INTEGER DEFAULT 0, behind INTEGER DEFAULT 0, dirty_count INTEGER DEFAULT 0,
	UNIQUE(session_id, commit_hash)
);
CREATE TABLE agent_tasks (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name  TEXT NOT NULL,
	session_id  TEXT,
	title       TEXT NOT NULL,
	completed   INTEGER DEFAULT 0,
	sort_order  INTEGER DEFAULT 0,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
, started_at TEXT, completed_at TEXT, cost_usd REAL NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0, display_name TEXT, body TEXT, priority TEXT DEFAULT 'medium', completion_message TEXT);
CREATE TABLE agent_notes (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name  TEXT NOT NULL,
	session_id  TEXT,
	content     TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);
CREATE TABLE agent_events (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name  TEXT NOT NULL,
	session_id  TEXT,
	event_type  TEXT NOT NULL,
	tool_name   TEXT,
	summary     TEXT NOT NULL,
	detail_json TEXT,
	created_at  TEXT NOT NULL,
	duration_ms INTEGER,
	ref_id      TEXT
);
CREATE TABLE live_sessions (
	session_id    TEXT PRIMARY KEY,
	agent_type    TEXT NOT NULL,
	agent_name    TEXT NOT NULL,
	working_dir   TEXT NOT NULL,
	display_name  TEXT,
	resume_from_id TEXT,
	flags         TEXT,
	is_job        INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT NOT NULL
, prompt TEXT, board_name TEXT, board_server TEXT, backend TEXT DEFAULT 'tmux', icon TEXT, is_sleeping INTEGER NOT NULL DEFAULT 0, board_type TEXT, git_diff_mode TEXT, capabilities TEXT, model TEXT, pid INTEGER NOT NULL DEFAULT 0, tools TEXT, mcp_servers TEXT, worktree_path TEXT, worktree_repo TEXT, team_id INTEGER REFERENCES teams(id) ON DELETE SET NULL, status TEXT NOT NULL DEFAULT 'active', context_window INTEGER NOT NULL DEFAULT 0, stopped_at TEXT, name_color TEXT, is_archived INTEGER NOT NULL DEFAULT 0, sleep_reason TEXT, auto_accept INTEGER NOT NULL DEFAULT 0, auto_accept_until INTEGER, auto_accept_source TEXT, auto_accept_paused_code TEXT, cli_version TEXT, hooks_rev INTEGER);
INSERT INTO live_sessions VALUES('00000001-aaaa-4000-8000-000000000001','claude','fixture-agent-1','/fixture/work','Fixture Agent 1',NULL,NULL,0,'2025-01-01T00:00:00Z',NULL,NULL,NULL,'tmux',NULL,0,NULL,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,'active',0,NULL,NULL,0,NULL,0,NULL,NULL,NULL,NULL,NULL);
INSERT INTO live_sessions VALUES('00000002-aaaa-4000-8000-000000000002','claude','fixture-agent-2','/fixture/work','Fixture Agent 2',NULL,NULL,0,'2025-01-01T00:00:00Z',NULL,NULL,NULL,'tmux',NULL,0,NULL,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,'active',0,NULL,NULL,0,NULL,0,NULL,NULL,NULL,NULL,NULL);
INSERT INTO live_sessions VALUES('00000003-aaaa-4000-8000-000000000003','claude','fixture-agent-3','/fixture/work','Fixture Agent 3',NULL,NULL,0,'2025-01-01T00:00:00Z',NULL,NULL,NULL,'tmux',NULL,0,NULL,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,'active',0,NULL,NULL,0,NULL,0,NULL,NULL,NULL,NULL,NULL);
CREATE TABLE store_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
INSERT INTO store_meta VALUES('agent_events_detail_json_fixed','2026-10-20T10:09:35Z');
INSERT INTO store_meta VALUES('remote_url_credentials_scrubbed_2','2026-10-20T10:09:35Z');
CREATE TABLE user_settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
INSERT INTO user_settings VALUES('fixture_setting_1','value-1');
INSERT INTO user_settings VALUES('fixture_setting_2','value-2');
CREATE TABLE scheduled_jobs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	name            TEXT NOT NULL,
	description     TEXT DEFAULT '',
	cron_expr       TEXT NOT NULL,
	timezone        TEXT NOT NULL DEFAULT 'UTC',
	agent_type      TEXT NOT NULL DEFAULT 'claude',
	repo_path       TEXT NOT NULL,
	base_branch     TEXT DEFAULT 'main',
	prompt          TEXT NOT NULL,
	enabled         INTEGER NOT NULL DEFAULT 1,
	max_duration_s  INTEGER NOT NULL DEFAULT 3600,
	cleanup_worktree INTEGER NOT NULL DEFAULT 1,
	flags           TEXT DEFAULT '',
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL
, job_type TEXT DEFAULT 'prompt', workflow_id INTEGER, paused_reason TEXT DEFAULT '', resumed_at TEXT DEFAULT '');
CREATE TABLE scheduled_runs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id          INTEGER NOT NULL REFERENCES scheduled_jobs(id) ON DELETE CASCADE,
	session_id      TEXT,
	worktree_path   TEXT,
	status          TEXT NOT NULL DEFAULT 'pending',
	scheduled_at    TEXT NOT NULL,
	started_at      TEXT,
	finished_at     TEXT,
	exit_reason     TEXT,
	error_msg       TEXT,
	created_at      TEXT NOT NULL
, trigger_type TEXT DEFAULT 'cron', webhook_url TEXT, display_name TEXT);
CREATE TABLE git_changed_files (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name        TEXT NOT NULL,
	session_id        TEXT,
	working_directory TEXT NOT NULL,
	filepath          TEXT NOT NULL,
	additions         INTEGER NOT NULL DEFAULT 0,
	deletions         INTEGER NOT NULL DEFAULT 0,
	status            TEXT NOT NULL DEFAULT 'M',
	recorded_at       TEXT NOT NULL
, diff_mode TEXT DEFAULT '');
CREATE TABLE workflows (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	name               TEXT NOT NULL UNIQUE,
	description        TEXT DEFAULT '',
	steps_json         TEXT NOT NULL DEFAULT '[]',
	default_agent_json TEXT DEFAULT '',
	repo_path          TEXT DEFAULT '',
	base_branch        TEXT DEFAULT 'main',
	max_duration_s     INTEGER NOT NULL DEFAULT 3600,
	cleanup_worktree   INTEGER NOT NULL DEFAULT 1,
	enabled            INTEGER NOT NULL DEFAULT 1,
	created_at         TEXT NOT NULL,
	updated_at         TEXT NOT NULL
);
CREATE TABLE workflow_runs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	workflow_id     INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
	trigger_type    TEXT NOT NULL DEFAULT 'api',
	trigger_context TEXT,
	status          TEXT NOT NULL DEFAULT 'pending',
	current_step    INTEGER NOT NULL DEFAULT 0,
	step_results    TEXT NOT NULL DEFAULT '[]',
	worktree_path   TEXT DEFAULT '',
	started_at      TEXT,
	finished_at     TEXT,
	error_msg       TEXT,
	created_at      TEXT NOT NULL
);
CREATE TABLE remote_board_subscriptions (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id           TEXT NOT NULL,
    remote_server        TEXT NOT NULL,
    project              TEXT NOT NULL,
    job_title            TEXT NOT NULL,
    last_notified_unread INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    UNIQUE(session_id, remote_server, project)
);
CREATE TABLE remote_servers (
    id            TEXT PRIMARY KEY,
    label         TEXT NOT NULL,
    url           TEXT NOT NULL,
    api_key       TEXT NOT NULL,
    allow_private INTEGER NOT NULL DEFAULT 0,
    status        TEXT NOT NULL DEFAULT 'unknown',
    created_at    TEXT NOT NULL,
    last_seen     TEXT,
    last_error    TEXT
);
CREATE TABLE cloud_connection (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    hub_url    TEXT NOT NULL,
    server_id  TEXT NOT NULL,
    label      TEXT NOT NULL,
    token      TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    last_error TEXT
, route_key TEXT, hub_owner TEXT);
CREATE TABLE cloud_enrollment (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    hub_url          TEXT NOT NULL,
    device_code      TEXT NOT NULL DEFAULT '',
    user_code        TEXT NOT NULL DEFAULT '',
    verification_url TEXT NOT NULL DEFAULT '',
    interval_s       INTEGER NOT NULL DEFAULT 5,
    expires_at       TEXT NOT NULL DEFAULT '',
    next_start_at    TEXT NOT NULL DEFAULT '',
    failures         INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE artifact_keep (
    hex     TEXT PRIMARY KEY,
    kept_at TEXT NOT NULL
);
CREATE TABLE custom_views (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	prompt     TEXT NOT NULL DEFAULT '',
	html       TEXT NOT NULL DEFAULT '',
	tab_order  INTEGER NOT NULL DEFAULT 0,
	scope      TEXT NOT NULL DEFAULT 'global',
	created_via TEXT NOT NULL DEFAULT 'api',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE teams (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    config_json     TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'running'
        CHECK (status IN ('running', 'sleeping', 'stopped')),
    working_dir     TEXT NOT NULL,
    is_worktree     INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    stopped_at      TEXT
);
CREATE TABLE team_members (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    team_id           INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    agent_name        TEXT NOT NULL,
    agent_config_json TEXT NOT NULL,
    session_id        TEXT,
    status            TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'sleeping', 'stopped')),
    created_at        TEXT NOT NULL,
    stopped_at        TEXT
);
CREATE TABLE subagents (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT NOT NULL REFERENCES live_sessions(session_id) ON DELETE CASCADE,
    subagent_id        TEXT NOT NULL,
    subagent_type      TEXT,
    description        TEXT,
    tool_use_id        TEXT,
    model              TEXT,
    spawn_depth        INTEGER NOT NULL DEFAULT 0,
    api_calls          INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL NOT NULL DEFAULT 0,
    finished           INTEGER NOT NULL DEFAULT 0,
    transcript_path    TEXT,
    started_at         TEXT,
    last_activity_at   TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    UNIQUE(session_id, subagent_id)
);
CREATE TABLE token_usage (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT NOT NULL,
    agent_name      TEXT NOT NULL,
    agent_type      TEXT NOT NULL DEFAULT 'claude',
    team_id         INTEGER,
    board_name      TEXT,
    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    total_tokens    INTEGER NOT NULL DEFAULT 0,
    cost_usd        REAL NOT NULL DEFAULT 0,
    num_turns       INTEGER NOT NULL DEFAULT 0,
    recorded_at     TEXT NOT NULL
, cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0, session_start_at TEXT, last_activity_at TEXT, source TEXT NOT NULL DEFAULT 'jsonl', model TEXT);
CREATE TABLE call_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    call_type TEXT NOT NULL,
    operation TEXT NOT NULL,
    agent_name TEXT,
    session_id TEXT,
    team_id INTEGER,
    board_name TEXT,
    method TEXT,
    status_code INTEGER,
    duration_ms INTEGER,
    is_error INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
INSERT INTO call_metrics VALUES(1,'api','/api/fixture',NULL,NULL,NULL,NULL,NULL,NULL,NULL,0,'2025-01-01T00:00:00Z');
INSERT INTO call_metrics VALUES(2,'api','/api/fixture',NULL,NULL,NULL,NULL,NULL,NULL,NULL,0,'2025-01-01T00:00:00Z');
INSERT INTO call_metrics VALUES(3,'api','/api/fixture',NULL,NULL,NULL,NULL,NULL,NULL,NULL,0,'2025-01-01T00:00:00Z');
INSERT INTO call_metrics VALUES(4,'api','/api/fixture',NULL,NULL,NULL,NULL,NULL,NULL,NULL,0,'2025-01-01T00:00:00Z');
INSERT INTO call_metrics VALUES(5,'api','/api/fixture',NULL,NULL,NULL,NULL,NULL,NULL,NULL,0,'2025-01-01T00:00:00Z');
CREATE TABLE call_metrics_hourly (
    hour TEXT NOT NULL,
    call_type TEXT NOT NULL,
    operation TEXT NOT NULL,
    agent_name TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    board_name TEXT NOT NULL DEFAULT '',
    calls INTEGER NOT NULL,
    errors INTEGER NOT NULL DEFAULT 0,
    duration_sum_ms INTEGER NOT NULL DEFAULT 0,
    duration_n INTEGER NOT NULL DEFAULT 0,
    p50_ms INTEGER NOT NULL DEFAULT 0,
    p95_ms INTEGER NOT NULL DEFAULT 0,
    UNIQUE(hour, call_type, operation, agent_name, session_id, board_name)
);
CREATE TABLE goal_generations (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT NOT NULL,
    agent_name         TEXT NOT NULL DEFAULT '',
    agent_type         TEXT NOT NULL DEFAULT '',
    trigger            TEXT NOT NULL,
    outcome            TEXT NOT NULL,
    goal               TEXT NOT NULL DEFAULT '',
    error              TEXT NOT NULL DEFAULT '',
    duration_ms        INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL NOT NULL DEFAULT 0,
    transcript_bytes   INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL
);
CREATE TABLE session_tokens (
    token_hash   TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL,
    session_name TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    revoked_at   TEXT
);
CREATE TABLE browser_sessions (
    id_hash     TEXT PRIMARY KEY,
    label       TEXT NOT NULL DEFAULT '',
    method      TEXT NOT NULL DEFAULT '',
    persistent  INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
, hub_grant TEXT NOT NULL DEFAULT '', hub_account TEXT NOT NULL DEFAULT '', hub_display TEXT NOT NULL DEFAULT '', hub_last_ok INTEGER NOT NULL DEFAULT 0);
CREATE TABLE token_usage_hourly (
	hour               TEXT NOT NULL, -- the first 13 characters of the rows' recorded_at
	id                 INTEGER,       -- the newest raw row's id
	session_id         TEXT NOT NULL,
	agent_name         TEXT NOT NULL,
	agent_type         TEXT NOT NULL,
	team_id            INTEGER,
	board_name         TEXT,
	input_tokens       INTEGER NOT NULL,
	output_tokens      INTEGER NOT NULL,
	total_tokens       INTEGER NOT NULL,
	cost_usd           REAL NOT NULL,
	num_turns          INTEGER NOT NULL,
	recorded_at        TEXT NOT NULL, -- the newest raw row's
	cache_read_tokens  INTEGER NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	session_start_at   TEXT,
	last_activity_at   TEXT,
	source             TEXT NOT NULL,
	model              TEXT,
	n_rows             INTEGER NOT NULL,
	ctx_tokens         INTEGER NOT NULL
);
CREATE TABLE token_usage_pruning (on_since TEXT);
CREATE TABLE token_usage_rollup (
	session_id         TEXT NOT NULL,
	source             TEXT NOT NULL,
	input_tokens       INTEGER NOT NULL,
	output_tokens      INTEGER NOT NULL,
	cache_read_tokens  INTEGER NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	total_tokens       INTEGER NOT NULL,
	cost_usd           REAL NOT NULL,
	num_turns          INTEGER NOT NULL,
	max_id             INTEGER,
	agent_name         TEXT,
	agent_type         TEXT,
	team_id            INTEGER,
	board_name         TEXT,
	recorded_at        TEXT,
	latest_context     INTEGER NOT NULL,
	PRIMARY KEY (session_id, source)
);
CREATE TABLE agent_event_files (
			session_id TEXT NOT NULL,
			file_path  TEXT NOT NULL,
			n          INTEGER NOT NULL,
			PRIMARY KEY (session_id, file_path)
		) WITHOUT ROWID;
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
CREATE TABLE board_messages (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project     TEXT NOT NULL,
			session_id  TEXT NOT NULL,
			content     TEXT NOT NULL,
			created_at  TEXT NOT NULL
		, target_group_id TEXT, subscriber_id TEXT, external_json TEXT, origin TEXT, is_system INTEGER NOT NULL DEFAULT 0, hub_user TEXT);
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
PRAGMA writable_schema=ON;
INSERT INTO sqlite_schema(type,name,tbl_name,rootpage,sql)VALUES('table','session_fts','session_fts',0,'CREATE VIRTUAL TABLE session_fts
	USING fts5(session_id, body, tokenize=''porter'')');
INSERT INTO sqlite_sequence VALUES('board_tasks',0);
INSERT INTO sqlite_sequence VALUES('call_metrics',5);
CREATE VIEW token_usage_all AS
	SELECT id, session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens,
	       total_tokens, cost_usd, num_turns, recorded_at, cache_read_tokens, cache_write_tokens,
	       session_start_at, last_activity_at, source, model, 1 AS n_rows,
	       input_tokens + cache_read_tokens + cache_write_tokens AS ctx_tokens
	FROM token_usage
	UNION ALL
	SELECT id, session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens,
	       total_tokens, cost_usd, num_turns, recorded_at, cache_read_tokens, cache_write_tokens,
	       session_start_at, last_activity_at, source, model, n_rows, ctx_tokens
	FROM token_usage_hourly;
CREATE TRIGGER token_usage_skip_rolled_up BEFORE INSERT ON token_usage
WHEN EXISTS (SELECT 1 FROM token_usage_hourly h WHERE h.session_id = NEW.session_id AND h.hour = substr(NEW.recorded_at, 1, 13))
BEGIN
	SELECT RAISE(IGNORE);
END;
CREATE TRIGGER token_usage_rollup_insert AFTER INSERT ON token_usage BEGIN
	INSERT INTO token_usage_rollup VALUES (
		NEW.session_id, COALESCE(NEW.source, 'jsonl'),
		NEW.input_tokens, NEW.output_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens,
		NEW.total_tokens, NEW.cost_usd, 1, NEW.id, NEW.agent_name, NEW.agent_type,
		NEW.team_id, NEW.board_name, NEW.recorded_at,
		NEW.input_tokens + NEW.cache_read_tokens + NEW.cache_write_tokens)
	ON CONFLICT (session_id, source) DO UPDATE SET
		input_tokens       = input_tokens + excluded.input_tokens,
		output_tokens      = output_tokens + excluded.output_tokens,
		cache_read_tokens  = cache_read_tokens + excluded.cache_read_tokens,
		cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
		total_tokens       = total_tokens + excluded.total_tokens,
		cost_usd           = cost_usd + excluded.cost_usd,
		num_turns          = num_turns + 1,
		max_id             = CASE WHEN max_id IS NULL OR excluded.max_id > max_id THEN excluded.max_id ELSE max_id END,
		agent_name         = CASE WHEN agent_name IS NULL OR excluded.agent_name > agent_name THEN excluded.agent_name ELSE agent_name END,
		agent_type         = CASE WHEN agent_type IS NULL OR excluded.agent_type > agent_type THEN excluded.agent_type ELSE agent_type END,
		team_id            = CASE WHEN team_id IS NULL OR excluded.team_id > team_id THEN excluded.team_id ELSE team_id END,
		board_name         = CASE WHEN board_name IS NULL OR excluded.board_name > board_name THEN excluded.board_name ELSE board_name END,
		latest_context     = CASE WHEN recorded_at IS NULL OR excluded.recorded_at > recorded_at THEN excluded.latest_context ELSE latest_context END,
		recorded_at        = CASE WHEN recorded_at IS NULL OR excluded.recorded_at > recorded_at THEN excluded.recorded_at ELSE recorded_at END;
END;
CREATE TRIGGER token_usage_rollup_update_delta AFTER UPDATE ON token_usage
WHEN NEW.session_id IS OLD.session_id AND COALESCE(NEW.source, 'jsonl') IS COALESCE(OLD.source, 'jsonl')
 AND NEW.id IS OLD.id AND NEW.recorded_at IS OLD.recorded_at AND NEW.agent_name IS OLD.agent_name
 AND NEW.agent_type IS OLD.agent_type AND NEW.team_id IS OLD.team_id AND NEW.board_name IS OLD.board_name
BEGIN
	UPDATE token_usage_rollup SET
		input_tokens       = input_tokens + NEW.input_tokens - OLD.input_tokens,
		output_tokens      = output_tokens + NEW.output_tokens - OLD.output_tokens,
		cache_read_tokens  = cache_read_tokens + NEW.cache_read_tokens - OLD.cache_read_tokens,
		cache_write_tokens = cache_write_tokens + NEW.cache_write_tokens - OLD.cache_write_tokens,
		total_tokens       = total_tokens + NEW.total_tokens - OLD.total_tokens,
		cost_usd           = cost_usd + NEW.cost_usd - OLD.cost_usd,
		latest_context     = CASE WHEN recorded_at = NEW.recorded_at
		                          THEN NEW.input_tokens + NEW.cache_read_tokens + NEW.cache_write_tokens
		                          ELSE latest_context END
	WHERE session_id = NEW.session_id AND source = COALESCE(NEW.source, 'jsonl');
END;
CREATE TRIGGER token_usage_rollup_update_rebuild AFTER UPDATE ON token_usage
WHEN NOT (NEW.session_id IS OLD.session_id AND COALESCE(NEW.source, 'jsonl') IS COALESCE(OLD.source, 'jsonl')
 AND NEW.id IS OLD.id AND NEW.recorded_at IS OLD.recorded_at AND NEW.agent_name IS OLD.agent_name
 AND NEW.agent_type IS OLD.agent_type AND NEW.team_id IS OLD.team_id AND NEW.board_name IS OLD.board_name)
BEGIN
	DELETE FROM token_usage_rollup WHERE session_id = OLD.session_id AND source = COALESCE(OLD.source, 'jsonl');
	DELETE FROM token_usage_rollup WHERE session_id = NEW.session_id AND source = COALESCE(NEW.source, 'jsonl');
	INSERT INTO token_usage_rollup
	SELECT g.*, COALESCE((SELECT x.ctx_tokens FROM token_usage_all x
	             WHERE x.session_id = g.session_id AND x.recorded_at = g.recorded_at LIMIT 1), 0)
	FROM (SELECT session_id, COALESCE(source, 'jsonl') AS source,
	             SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens),
	             SUM(total_tokens), SUM(cost_usd), SUM(n_rows), MAX(id), MAX(agent_name), MAX(agent_type),
	             MAX(team_id), MAX(board_name), MAX(recorded_at) AS recorded_at
	      FROM token_usage_all WHERE session_id = OLD.session_id AND COALESCE(source, 'jsonl') = COALESCE(OLD.source, 'jsonl') GROUP BY 1, 2) g;
	INSERT INTO token_usage_rollup
	SELECT g.*, COALESCE((SELECT x.ctx_tokens FROM token_usage_all x
	             WHERE x.session_id = g.session_id AND x.recorded_at = g.recorded_at LIMIT 1), 0)
	FROM (SELECT session_id, COALESCE(source, 'jsonl') AS source,
	             SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens),
	             SUM(total_tokens), SUM(cost_usd), SUM(n_rows), MAX(id), MAX(agent_name), MAX(agent_type),
	             MAX(team_id), MAX(board_name), MAX(recorded_at) AS recorded_at
	      FROM token_usage_all WHERE session_id = NEW.session_id AND COALESCE(source, 'jsonl') = COALESCE(NEW.source, 'jsonl')
	  AND NOT (NEW.session_id IS OLD.session_id AND COALESCE(NEW.source, 'jsonl') IS COALESCE(OLD.source, 'jsonl')) GROUP BY 1, 2) g;
END;
CREATE TRIGGER token_usage_rollup_delete AFTER DELETE ON token_usage
WHEN NOT EXISTS (SELECT 1 FROM token_usage_pruning)
BEGIN
	DELETE FROM token_usage_rollup WHERE session_id = OLD.session_id AND source = COALESCE(OLD.source, 'jsonl');
	INSERT INTO token_usage_rollup
	SELECT g.*, COALESCE((SELECT x.ctx_tokens FROM token_usage_all x
	             WHERE x.session_id = g.session_id AND x.recorded_at = g.recorded_at LIMIT 1), 0)
	FROM (SELECT session_id, COALESCE(source, 'jsonl') AS source,
	             SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens),
	             SUM(total_tokens), SUM(cost_usd), SUM(n_rows), MAX(id), MAX(agent_name), MAX(agent_type),
	             MAX(team_id), MAX(board_name), MAX(recorded_at) AS recorded_at
	      FROM token_usage_all WHERE session_id = OLD.session_id AND COALESCE(source, 'jsonl') = COALESCE(OLD.source, 'jsonl') GROUP BY 1, 2) g;
END;
CREATE TRIGGER agent_event_files_insert AFTER INSERT ON agent_events
			WHEN NEW.event_type = 'tool_use' AND NEW.tool_name IN ('Write', 'Edit')
		AND NEW.session_id IS NOT NULL AND NEW.detail_json IS NOT NULL AND json_valid(NEW.detail_json)
		AND json_extract(NEW.detail_json, '$.file_path') IS NOT NULL BEGIN INSERT INTO agent_event_files (session_id, file_path, n)
		VALUES (NEW.session_id, json_extract(NEW.detail_json, '$.file_path'), 1)
		ON CONFLICT (session_id, file_path) DO UPDATE SET n = n + 1; END;
CREATE TRIGGER agent_event_files_delete AFTER DELETE ON agent_events
			WHEN OLD.event_type = 'tool_use' AND OLD.tool_name IN ('Write', 'Edit')
		AND OLD.session_id IS NOT NULL AND OLD.detail_json IS NOT NULL AND json_valid(OLD.detail_json)
		AND json_extract(OLD.detail_json, '$.file_path') IS NOT NULL BEGIN UPDATE agent_event_files SET n = n - 1
		WHERE session_id = OLD.session_id AND file_path = json_extract(OLD.detail_json, '$.file_path');
		DELETE FROM agent_event_files
		WHERE session_id = OLD.session_id AND file_path = json_extract(OLD.detail_json, '$.file_path') AND n <= 0; END;
CREATE TRIGGER agent_event_files_update_old AFTER UPDATE OF session_id, event_type, tool_name, detail_json ON agent_events
			WHEN OLD.event_type = 'tool_use' AND OLD.tool_name IN ('Write', 'Edit')
		AND OLD.session_id IS NOT NULL AND OLD.detail_json IS NOT NULL AND json_valid(OLD.detail_json)
		AND json_extract(OLD.detail_json, '$.file_path') IS NOT NULL BEGIN UPDATE agent_event_files SET n = n - 1
		WHERE session_id = OLD.session_id AND file_path = json_extract(OLD.detail_json, '$.file_path');
		DELETE FROM agent_event_files
		WHERE session_id = OLD.session_id AND file_path = json_extract(OLD.detail_json, '$.file_path') AND n <= 0; END;
CREATE TRIGGER agent_event_files_update_new AFTER UPDATE OF session_id, event_type, tool_name, detail_json ON agent_events
			WHEN NEW.event_type = 'tool_use' AND NEW.tool_name IN ('Write', 'Edit')
		AND NEW.session_id IS NOT NULL AND NEW.detail_json IS NOT NULL AND json_valid(NEW.detail_json)
		AND json_extract(NEW.detail_json, '$.file_path') IS NOT NULL BEGIN INSERT INTO agent_event_files (session_id, file_path, n)
		VALUES (NEW.session_id, json_extract(NEW.detail_json, '$.file_path'), 1)
		ON CONFLICT (session_id, file_path) DO UPDATE SET n = n + 1; END;
CREATE TRIGGER personal_task_insert AFTER INSERT ON board_tasks BEGIN
 INSERT OR IGNORE INTO agent_tasks(id,agent_name,session_id,title,body,priority,completed,sort_order,created_at,updated_at)
 VALUES(NEW.id,json_extract(NEW.board_id,'$[0]'),json_extract(NEW.board_id,'$[1]'),NEW.title,NEW.body,NEW.priority,
 CASE NEW.status WHEN 'completed' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'skipped' THEN 3 WHEN 'blocked' THEN 4 WHEN 'draft' THEN 5 WHEN 'review_pending' THEN 6 ELSE 0 END,
 (SELECT COALESCE(MAX(sort_order),-1)+1 FROM agent_tasks WHERE agent_name=json_extract(NEW.board_id,'$[0]') AND session_id IS json_extract(NEW.board_id,'$[1]')),NEW.created_at,NEW.created_at);
 END;
CREATE TRIGGER personal_task_update AFTER UPDATE ON board_tasks BEGIN
 UPDATE agent_tasks SET title=NEW.title,body=NEW.body,priority=NEW.priority,
 completed=CASE NEW.status WHEN 'completed' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'skipped' THEN 3 WHEN 'blocked' THEN 4 WHEN 'draft' THEN 5 WHEN 'review_pending' THEN 6 ELSE 0 END,
 started_at=NEW.claimed_at,completed_at=NEW.completed_at,completion_message=NEW.completion_message,
 cost_usd=COALESCE(NEW.cost_usd,0),input_tokens=COALESCE(NEW.input_tokens,0),output_tokens=COALESCE(NEW.output_tokens,0),cache_read_tokens=COALESCE(NEW.cache_read_tokens,0),cache_write_tokens=COALESCE(NEW.cache_write_tokens,0),
 updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=NEW.id;
 END;
CREATE TRIGGER personal_task_delete AFTER DELETE ON board_tasks BEGIN
 DELETE FROM agent_tasks WHERE id=OLD.id;
 END;
CREATE INDEX idx_agent_ui_events ON agent_ui_events(session_id,panel_id,id);
CREATE INDEX idx_session_index_last_ts
	ON session_index(last_timestamp DESC);
CREATE INDEX idx_scheduled_jobs_enabled
	ON scheduled_jobs(enabled, id);
CREATE INDEX idx_scheduled_runs_job
	ON scheduled_runs(job_id, scheduled_at DESC);
CREATE INDEX idx_scheduled_runs_session
	ON scheduled_runs(session_id);
CREATE INDEX idx_scheduled_runs_status
	ON scheduled_runs(status, scheduled_at DESC);
CREATE INDEX idx_git_changed_files_session
	ON git_changed_files(session_id);
CREATE INDEX idx_git_changed_files_agent
	ON git_changed_files(agent_name);
CREATE INDEX idx_workflow_runs_workflow
	ON workflow_runs(workflow_id, created_at DESC);
CREATE INDEX idx_workflow_runs_status
	ON workflow_runs(status);
CREATE INDEX idx_teams_status ON teams(status);
CREATE INDEX idx_team_members_team ON team_members(team_id, status);
CREATE INDEX idx_subagents_session ON subagents(session_id);
CREATE INDEX idx_call_metrics_created ON call_metrics(created_at);
CREATE UNIQUE INDEX idx_token_usage_session_time ON token_usage(session_id, recorded_at);
CREATE INDEX idx_token_usage_team ON token_usage(team_id);
CREATE INDEX idx_token_usage_time ON token_usage(recorded_at);
CREATE INDEX idx_goal_generations_time ON goal_generations(created_at);
CREATE INDEX idx_session_tokens_session ON session_tokens(session_id);
CREATE INDEX idx_browser_sessions_expires ON browser_sessions(expires_at);
CREATE INDEX idx_goal_generations_session ON goal_generations(session_id, created_at);
CREATE INDEX idx_git_snap_session ON git_snapshots(session_id);
CREATE INDEX idx_session_tags_tag_id ON session_tags(tag_id);
CREATE INDEX idx_folder_tags_tag_id ON folder_tags(tag_id);
CREATE INDEX idx_session_index_first_ts ON session_index(first_timestamp);
CREATE INDEX idx_agent_events_session_time ON agent_events(session_id, created_at);
CREATE INDEX idx_agent_events_session_type_time ON agent_events(session_id, event_type, created_at);
CREATE INDEX idx_agent_events_agent ON agent_events(agent_name, session_id);
CREATE INDEX idx_agent_events_agent_time ON agent_events(agent_name, created_at);
CREATE INDEX idx_agent_events_edited_files ON agent_events(session_id, json_extract(detail_json, '$.file_path'))
	WHERE event_type = 'tool_use' AND tool_name IN ('Write', 'Edit') AND detail_json IS NOT NULL AND json_valid(detail_json);
CREATE INDEX idx_git_snap_session_time ON git_snapshots(session_id, recorded_at DESC);
CREATE INDEX idx_git_snap_agent_time ON git_snapshots(agent_name, recorded_at DESC);
CREATE INDEX idx_agent_events_ref
		ON agent_events(session_id, event_type, ref_id) WHERE ref_id IS NOT NULL;
CREATE INDEX idx_token_usage_source ON token_usage(source, session_id);
CREATE INDEX idx_token_usage_hourly_session ON token_usage_hourly(session_id, hour);
CREATE INDEX idx_token_usage_hourly_team ON token_usage_hourly(team_id);
CREATE INDEX idx_token_usage_hourly_time ON token_usage_hourly(recorded_at);
CREATE INDEX idx_token_usage_hourly_source ON token_usage_hourly(source, session_id);
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
PRAGMA writable_schema=OFF;
COMMIT;
