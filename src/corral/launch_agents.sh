#!/usr/bin/env bash
set -euo pipefail

TARGET_DIR="${1:-.}"
AGENT_TYPE="${2:-claude}"
LAUNCH_AGENTS="${3:-}"  # Pass "agents" as 3rd arg to auto-launch agents
LOG_DIR="${TMPDIR:-/tmp}"
LOG_DIR="${LOG_DIR%/}" # Remove trailing slash if present
MAX_AGENTS=5


TARGET_DIR="$(cd "$TARGET_DIR" && pwd)"

# Clean up all existing corral logs
rm -f "${LOG_DIR}/"*_corral_*.log

# Locate assets relative to this script
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROTOCOL_PATH="${SCRIPT_DIR}/PROTOCOL.md"
WEB_SERVER_PATH="${SCRIPT_DIR}/web_server.py"
WEB_SESSION="corral-web-server"
WEB_PORT="${CORRAL_PORT:-8420}"

# Launch the web server in a dedicated tmux session
launch_web_server() {
    local cmd="python3 $WEB_SERVER_PATH --port $WEB_PORT"

    # Prefer the installed entry point
    if command -v corral &>/dev/null; then
        cmd="corral --no-browser --port $WEB_PORT"
    elif [ ! -f "$WEB_SERVER_PATH" ]; then
        echo "  [!] Web server skip: web_server.py not found and corral not in PATH."
        return 1
    fi

    # Kill existing web server session if present
    tmux kill-session -t "$WEB_SESSION" 2>/dev/null || true

    echo "=== Launching Web Server (tmux: $WEB_SESSION) ==="
    tmux new-session -d -s "$WEB_SESSION"
    tmux send-keys -t "$WEB_SESSION" "$cmd" Enter
    echo "  [+] Web server started on http://localhost:$WEB_PORT"
    echo "      Attach  : tmux attach -t $WEB_SESSION"
    echo ""
}

# Skip web server launch if requested
if [ "${SKIP_WEB_SERVER:-0}" = "1" ]; then
    echo "  [~] Skipping web server launch (SKIP_WEB_SERVER=1)"
    launch_web_server() {
        return 0
    }
fi

# Open a new terminal window attached to a tmux session
open_agent_terminal() {
    local session="$1"
    local title="$2"
    # SSH_CONNECTION is set by sshd whenever the shell is reached via SSH.
    # Skip GUI window opening silently in that environment.
    if [ -n "${SSH_CONNECTION:-}" ]; then
        echo "  [~] SSH detected — skipping terminal window (use: tmux attach -t $session)"
        return 0
    fi
    if command -v osascript &>/dev/null; then
        if osascript -e 'tell application "iTerm2" to version' &>/dev/null 2>&1; then
            osascript << EOF
tell application "iTerm2"
    create window with default profile command "tmux attach -t ${session}"
end tell
EOF
        else
            osascript << EOF
tell application "Terminal"
    do script "tmux attach -t ${session}"
    set custom title of front window to "${title}"
end tell
EOF
        fi
    elif command -v x-terminal-emulator &>/dev/null; then
        x-terminal-emulator -e "tmux attach -t ${session}" &
    elif command -v gnome-terminal &>/dev/null; then
        gnome-terminal -- tmux attach -t "${session}" &
    elif command -v konsole &>/dev/null; then
        konsole -e "tmux attach -t ${session}" &
    elif command -v xfce4-terminal &>/dev/null; then
        xfce4-terminal -e "tmux attach -t ${session}" &
    else
        echo "  [~] No supported terminal emulator found (use: tmux attach -t $session)"
    fi
}

session_names=()

if [ "$LAUNCH_AGENTS" = "agents" ]; then
    # Collect valid subdirectories (up to MAX_AGENTS)
    worktrees=()
    for dir in "$TARGET_DIR"/*/; do
        [ -d "$dir" ] || continue
        worktrees+=("$dir")
        [ ${#worktrees[@]} -ge $MAX_AGENTS ] && break
    done

    if [ ${#worktrees[@]} -eq 0 ]; then
        echo "Error: No subdirectories found in $TARGET_DIR"
        exit 1
    fi

    echo "=== $AGENT_TYPE Corral Launcher (Independent Sessions) ==="
    echo "Target directory: $TARGET_DIR"
    echo "Found ${#worktrees[@]} workspace(s):"
    echo ""

    for dir in "${worktrees[@]}"; do
        folder_name="$(basename "$dir")"
        session_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
        session_name="${AGENT_TYPE}-${session_id}"
        log_file="${LOG_DIR}/${AGENT_TYPE}_corral_${session_id}.log"

        # Clear old log
        > "$log_file"

        # Create a new detached session rooted in the worktree
        tmux new-session -d -s "$session_name" -c "$dir"

        # Stream stdout to log file
        tmux pipe-pane -t "${session_name}" -o "cat >> '${log_file}'"

        # Pane 0: agent (with PROTOCOL.md piped in as system prompt if available)
        tmux send-keys -t "${session_name}.0" "printf '\033]2;${folder_name} — ${AGENT_TYPE}\033\\\\'" Enter
        if [ "$AGENT_TYPE" == "gemini" ]; then
            if [ -f "$PROTOCOL_PATH" ]; then
                tmux send-keys -t "${session_name}.0" "GEMINI_SYSTEM_MD=\"${PROTOCOL_PATH}\" gemini" Enter
            else
                tmux send-keys -t "${session_name}.0" "gemini" Enter
            fi
        else
            if [ -f "$PROTOCOL_PATH" ]; then
                tmux send-keys -t "${session_name}.0" "claude --session-id ${session_id} --append-system-prompt \"\$(cat '${PROTOCOL_PATH}')\"" Enter
            else
                tmux send-keys -t "${session_name}.0" "claude --session-id ${session_id}" Enter
            fi
        fi


        # Focus back on the agent pane
        tmux select-pane -t "${session_name}.0"

        # Open a new terminal window and attach to this session
        open_agent_terminal "$session_name" "${folder_name} — ${AGENT_TYPE}"

        echo "  [+] Session : $session_name"
        echo "      Dir     : $dir"
        echo "      Log     : $log_file"
        echo "      Attach  : tmux attach -t $session_name"
        echo ""

        session_names+=("$session_name")
    done
else
    echo "=== Corral Dashboard ==="
    echo "Starting web server only (launch agents from the dashboard)."
    echo "To also launch agents: launch-corral <path> <agent-type> agents"
    echo ""
fi

launch_web_server

# Open the dashboard in the default browser
if [ -z "${SSH_CONNECTION:-}" ]; then
    sleep 1
    if command -v open &>/dev/null; then
        open "http://localhost:$WEB_PORT"
    elif command -v xdg-open &>/dev/null; then
        xdg-open "http://localhost:$WEB_PORT" &>/dev/null &
    fi
fi

echo "=== All sessions launched ==="
echo ""
echo "Quick attach commands:"
for sn in "${session_names[@]}"; do
    echo "  tmux attach -t ${sn}"
done
echo ""
echo "Kill all agents:"
echo "  for sn in ${session_names[*]}; do tmux kill-session -t \$sn 2>/dev/null; done"
