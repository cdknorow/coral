// Database security and privacy status (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/dbcrypt"
	"github.com/subgentic/coral/internal/inputlog"
	setreg "github.com/subgentic/coral/internal/settings"
)

// DatabaseSecurityStatus is intentionally metadata-only. Passwords and key
// material never cross this API boundary.
func (h *SystemHandler) DatabaseSecurityStatus(w http.ResponseWriter, r *http.Request) {
	b, err := dbcrypt.Load(h.cfg.CoralDir())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	saved := b.DatabaseEncryption
	if saved == "" {
		saved = "disabled"
	}
	effective := dbcrypt.EffectiveMode()
	migrationCommand := ""
	if dbcrypt.FeatureAvailable() {
		migrationCommand = "CORAL_DB_ENCRYPTION_MIGRATE=1 coral"
	}
	available := dbcrypt.FeatureAvailable() && (saved != "password" || dbcrypt.UnlockSurface() != "headless")
	migrationRequired := false
	if saved != "disabled" {
		for _, path := range []string{h.cfg.DBPath, filepath.Join(h.cfg.CoralDir(), "messageboard.db")} {
			if plain, probeErr := dbcrypt.EncryptedFile(path); probeErr == nil && !plain && fileExists(path) {
				migrationRequired = true
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"saved_mode":          saved,
		"effective_mode":      effective,
		"restart_required":    saved != effective,
		"feature_available":   available,
		"encryption_compiled": dbcrypt.FeatureAvailable(),
		"build_variant":       dbcrypt.BuildVariant(),
		"unlock_surface":      dbcrypt.UnlockSurface(),
		"key_file_configured": b.KeyFile != "" || fileExists(filepath.Join(h.cfg.CoralDir(), dbcrypt.KeyName)),
		"migration_required":  migrationRequired,
		"migration_command":   migrationCommand,
	})
}

// SetDatabaseSecurity changes only the saved mode. It never accepts a
// password/key and takes effect after a successful restart/unlock.
func (h *SystemHandler) SetDatabaseSecurity(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	for key := range raw {
		if key != "mode" {
			errBadRequest(w, "only mode may be configured; credentials are never accepted")
			return
		}
	}
	var mode string
	if value, ok := raw["mode"]; !ok || json.Unmarshal(value, &mode) != nil {
		errBadRequest(w, "mode is required")
		return
	}
	if mode != "disabled" && mode != "key_file" && mode != "password" {
		errBadRequest(w, "unsupported database encryption mode")
		return
	}
	if mode != "disabled" && !dbcrypt.FeatureAvailable() {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":      "experimental database encryption is not included in this standard build",
			"error_code": "encryption_unavailable",
		})
		return
	}
	if effective := dbcrypt.EffectiveMode(); effective != "disabled" && mode != effective {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "changing encryption mode requires the explicit offline migration command",
			"error_code": "migration_required",
		})
		return
	}
	current, err := dbcrypt.Load(h.cfg.CoralDir())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !dbcrypt.FeatureAvailable() && current.DatabaseEncryption != "" && current.DatabaseEncryption != "disabled" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "saved encryption settings require an encryption-enabled build; refusing to replace them",
			"error_code": "encryption_unavailable",
		})
		return
	}
	current.DatabaseEncryption = mode
	if err := dbcrypt.Save(h.cfg.CoralDir(), current); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.DatabaseSecurityStatus(w, r)
}

// GetPrivacyStatus reports persisted privacy settings and the effective
// running bind state. Remote access changes are restart-applied.
// GET /api/system/privacy
func (h *SystemHandler) GetPrivacyStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := h.ss.GetSettings(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	telemetryEnabled := setreg.Bool(settings, setreg.TelemetryEnabled)
	// Remote access is opt-in: only an explicit saved "true" enables it.
	configured := setreg.Has(settings, setreg.RemoteAccessEnabled)
	remoteEnabled := setreg.Bool(settings, setreg.RemoteAccessEnabled)
	effectiveRemote := !config.IsLoopbackHost(h.cfg.Host)
	status := "disabled"
	switch {
	case remoteEnabled && effectiveRemote:
		status = "enabled"
	case remoteEnabled:
		status = "enable_pending_restart"
	case effectiveRemote:
		status = "disable_pending_restart"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"telemetry_enabled":              telemetryEnabled,
		"remote_access_enabled":          remoteEnabled,
		"remote_access_configured":       configured,
		"remote_access_effective":        effectiveRemote,
		"remote_access_restart_required": remoteEnabled != effectiveRemote,
		"remote_access_status":           status,
		// TLS (#3355): whether the port also serves HTTPS. Remote access without it is
		// unencrypted, and the dashboard says so.
		"remote_access_tls": h.cfg.TLS,
		"effective_host":    h.cfg.Host,
		"debug_input_log":   inputlog.State(),
		// H5: who besides the system and administrators can change the folder of the hooks every agent
		// runs (a portable Coral in the user's profile); empty for an install in Program Files.
		"tools_folder": toolsFolderStatus(),
	})
}

func toolsFolderStatus() map[string]any {
	dir, who := agent.ToolsFolderWriters()
	if who == nil {
		who = []string{}
	}
	return map[string]any{"path": dir, "writable_by": who}
}
