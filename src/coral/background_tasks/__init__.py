"""Background tasks that run on a recurring interval during server lifespan.

- **SessionIndexer**: Scans Claude + Gemini history files, indexes into SQLite with FTS5.
- **BatchSummarizer**: Polls summarizer queue, generates AI summaries via Claude CLI.
- **GitPoller**: Periodically queries git state for live agents.
"""

from coral.background_tasks.session_indexer import SessionIndexer, BatchSummarizer
from coral.background_tasks.git_poller import GitPoller
from coral.background_tasks.auto_summarizer import AutoSummarizer
from coral.background_tasks.scheduler import JobScheduler
from coral.background_tasks.webhook_dispatcher import WebhookDispatcher
from coral.background_tasks.idle_detector import IdleDetector
from coral.background_tasks.board_notifier import MessageBoardNotifier
from coral.background_tasks.remote_board_poller import RemoteBoardPoller
__all__ = ["SessionIndexer", "BatchSummarizer", "GitPoller", "AutoSummarizer",
           "JobScheduler", "WebhookDispatcher", "IdleDetector", "MessageBoardNotifier",
           "RemoteBoardPoller"]
