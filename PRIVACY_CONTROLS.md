# Privacy controls

**Implemented controls:** `telemetry_enabled` and `remote_access_enabled` are persisted in Coral's existing `user_settings` table and exposed through the authenticated settings API.

- Both settings default to enabled when absent, preserving existing behavior.
- `telemetry_enabled=false` takes effect immediately. PostHog initialization, capture, identify-equivalent event paths, queued tracking entry points, and browser funnel forwarding become no-ops. The setting is applied during startup before install tracking; startup settings-read failure fails closed for telemetry.
- `remote_access_enabled=false` takes effect on restart. Startup reads the setting before binding and changes the listener to `127.0.0.1`, preserving local desktop access while preventing direct LAN/mobile TCP connections, login, API, and WebSocket access. Existing remote connections are not forcibly disconnected before restart.
- `GET /api/system/privacy` reports saved values, `remote_access_effective`, `remote_access_restart_required`, and the effective bind host so a UI can distinguish a pending saved change from the running server.
- `PUT /api/settings` remains behind the existing API-key/session authority for nonlocal callers; localhost follows Coral's existing local operator authority. No forwarded headers or Host values are used to classify a client as local.

The loopback bind covers Coral's supported direct listener. A separately configured reverse proxy, SSH tunnel, port forward, or native helper that already has local access can still expose a loopback listener; Coral does not control those external ingress paths. Operators must disable those paths separately. The repository's LLM proxy is an outbound agent upstream proxy, not an additional public Coral listener.

These changes do not claim network isolation, revoke already established connections, or change sandbox/tool approval policy. A restart is required for remote access changes; telemetry changes are live. No telemetry is sent by tests.
