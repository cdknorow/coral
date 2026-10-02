# Database encryption settings UI

The Settings modal exposes the non-secret database-encryption bootstrap state
through a dedicated API. The browser never receives or submits a password,
derived key, or key-file contents.

## Contract

`GET /api/system/database-security` returns:

```json
{
  "saved_mode": "disabled|key_file|password",
  "effective_mode": "disabled|key_file|password",
  "restart_required": false,
  "feature_available": true,
  "unlock_surface": "native|tty|headless",
  "key_file_configured": false,
  "migration_required": false,
  "migration_command": "CORAL_DB_ENCRYPTION_MIGRATE=1 coral"
}
```

`PUT /api/system/database-security` accepts only `{ "mode": "disabled" |
"key_file" | "password" }`. It updates bootstrap metadata and returns the
same status shape. It must reject password/key material and must not use the
ordinary `user_settings` table for bootstrap state.

The Settings UI renders saved versus effective mode, restart-required state,
feature availability, and the supported unlock surface. A missing endpoint is
shown as unavailable and disables the selector, preserving compatibility with
older servers. Selecting password mode only changes the mode; startup collects
the password through the supported CLI TTY prompt before opening either
database. The macOS tray provides a Cocoa secure text field before database open; other
tray/webview/headless entry points fail closed because no secure native
secret-input primitive is provided. `migration_required` and
`migration_command` give an actionable offline plaintext-to-encrypted migration
path when saved encryption is enabled while plaintext files remain. Encrypted
key rotation and encrypted-to-plaintext disable/decrypt are not implemented;
the API rejects those active-mode transitions rather than implying an unsafe
recovery flow. Headless limitations remain
a startup concern and are not implied to be solved by the browser UI.

Database encryption covers Coral SQLite databases only. It does not cover
transcripts, logs, artifacts, uploads, API keys, CA files, shell history, or
other files under `~/.coral`.
