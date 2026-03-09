"""Generic utilities and configuration for Corral."""

from __future__ import annotations

import asyncio
import os
import subprocess
from pathlib import Path
from typing import Tuple

# Configuration Constants
LOG_DIR = os.environ.get("TMPDIR", "/tmp").rstrip("/")
LOG_PATTERN = f"{LOG_DIR}/*_corral_*.log"

HISTORY_PATH = Path(os.environ.get("CLAUDE_PROJECTS_DIR", Path.home() / ".claude" / "projects"))
GEMINI_HISTORY_BASE = Path(os.environ.get("GEMINI_TMP_DIR", Path.home() / ".gemini" / "tmp"))


async def run_cmd(*args: str, timeout: float | None = None) -> Tuple[int, str, str]:
    """Execute a subprocess command asynchronously.

    Returns:
        Tuple of (returncode, stdout, stderr).
    """
    try:
        proc = await asyncio.create_subprocess_exec(
            *args,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        
        if timeout is not None:
            stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
        else:
            stdout, stderr = await proc.communicate()
            
        return proc.returncode or 0, stdout.decode().strip(), stderr.decode().strip()
    except asyncio.TimeoutError:
        # If timeout, try to terminate the process
        if proc:
            try:
                proc.terminate()
                await asyncio.wait_for(proc.wait(), timeout=1.0)
            except Exception:
                pass
        return -1, "", "Command timed out"
    except Exception as e:
        return -1, "", str(e)
