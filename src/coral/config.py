"""Centralized tuning parameters for the Coral server.

All intervals, timeouts, and limits that affect server performance
are collected here so they can be adjusted in one place.
"""

import os
from pathlib import Path


def get_data_dir() -> Path:
    """Return the Coral data directory.

    Resolution: CORAL_DATA_DIR env var > ~/.coral (default).
    This is a function (not a constant) so it reads the env var at call time,
    after CLI flags have had a chance to set it.
    """
    return Path(os.environ.get("CORAL_DATA_DIR", str(Path.home() / ".coral")))


# ── Database ─────────────────────────────────────────────────────────────
DB_BUSY_TIMEOUT_MS = 5000          # SQLite busy_timeout (ms) before "database is locked"

# ── Startup ──────────────────────────────────────────────────────────────
DEFERRED_STARTUP_DELAY_S = 2       # Seconds before resuming sessions after server bind
INDEXER_STARTUP_DELAY_S = 30       # Seconds before first indexer pass

# ── Background task intervals (seconds) ──────────────────────────────────
INDEXER_INTERVAL_S = 120           # Session indexer re-scan interval
GIT_POLLER_INTERVAL_S = 120        # Git snapshot polling interval
WEBHOOK_DISPATCHER_INTERVAL_S = 15 # Webhook delivery retry interval
IDLE_DETECTOR_INTERVAL_S = 60      # Idle agent detection interval
BOARD_NOTIFIER_INTERVAL_S = 5      # Message board notification interval
REMOTE_POLLER_INTERVAL_S = 5       # Remote board polling interval

# ── WebSocket ────────────────────────────────────────────────────────────
WS_POLL_INTERVAL_S = 5            # Dashboard WebSocket refresh interval

# ── Message board (frontend) ────────────────────────────────────────────
BOARD_PAGE_SIZE = 50               # Messages per page in board UI
BOARD_MAX_LIMIT = 500              # Server-side cap on message list queries
BOARD_POLL_INTERVAL_S = 5          # Frontend polling interval for new messages
BOARD_SUBSCRIBER_POLL_MULTIPLIER = 3  # Refresh subscribers every N poll cycles
