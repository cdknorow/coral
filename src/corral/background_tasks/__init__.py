"""Background tasks that run on a recurring interval during server lifespan.

- **SessionIndexer**: Scans Claude + Gemini history files, indexes into SQLite with FTS5.
- **BatchSummarizer**: Polls summarizer queue, generates AI summaries via Claude CLI.
- **GitPoller**: Periodically queries git state for live agents.
"""

from corral.background_tasks.session_indexer import SessionIndexer, BatchSummarizer
from corral.background_tasks.git_poller import GitPoller
from corral.background_tasks.auto_summarizer import AutoSummarizer

__all__ = ["SessionIndexer", "BatchSummarizer", "GitPoller", "AutoSummarizer"]
