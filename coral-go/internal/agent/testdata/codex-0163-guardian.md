# #6081 observed Codex 0.163 guardian format (before fix)

A real isolated `codex exec --ignore-user-config --ignore-rules --approve-for-me --skip-git-repo-check --json` run on 0.163.0 wrote one disposable file outside its workspace. HOME and CODEX_HOME were private test folders, only existing CLI authentication was copied privately; no operator sessions were opened. The run completed successfully and produced TWO rollout files.

Parent session metadata: `source: "exec"`, `thread_source: "user"`.
Guardian child session metadata: `type: "session_meta"`, `payload.cli_version: "0.163.0"`, `payload.thread_source: "guardian_review"`, `payload.source: {"subagent":{"other":"guardian"}}`, `payload.parent_thread_id: <parent>`, `payload.session_id: <parent>`, `payload.id: <child>`.

The guardian's verdict is a NORMAL `response_item` / `payload.type: "message"` / `payload.role: "assistant"`, with output_text content. It is not a distinctive assistant role. Thus role/content-only filtering cannot safely distinguish it from chat; filter using session metadata. This child emitted no legacy event_msg.agent_message in this run. Its user/developer messages are the review input, not human chat. The parent emitted normal user/assistant response_item messages and remains a real chat. Live chat resolves the parent's linked rollout; no explicit guardian predicate exists in the current reader/indexer.

Proposed shared predicate: only session_meta declaring thread_source guardian_review; only when thread_source is absent, source.subagent.other guardian. Leave other subagents and messages mentioning guardian intact. Exclude the guardian rollout from History indexing/list/search and the chat transcript reader; purge already-indexed metadata/search entries for that source, even when its mtime is unchanged. Preserve rollout files and terminal/raw-file paths. Check Board/Inbox call paths for reuse of chat parsing.

The adjacent JSONL files are selected actual metadata and message records from this run. Redactions: thread IDs replaced by stable synthetic UUIDs; private test-root paths replaced. Session metadata retains only provenance/classification fields; account/user identifiers, base instructions, model/provider settings, token usage, tool records and guardian review-input messages are omitted. Assistant verdict text is unchanged. No auth/config files are included.
