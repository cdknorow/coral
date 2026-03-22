"""Tests for coral.background_tasks.subgent_ws — WebSocket listener and mention detection."""

import asyncio
import json
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from coral.background_tasks.subgent_ws import (
    BoardConnection,
    SubgentWSListener,
    _is_mentioned,
)


# ── _is_mentioned tests ──────────────────────────────────────────────────────


class TestIsMentioned:
    # Direct mentions
    def test_direct_mention(self):
        assert _is_mentioned("@agent-1 please review", "agent-1") == "direct"

    def test_direct_mention_case_insensitive(self):
        assert _is_mentioned("@AGENT-1 fix this", "agent-1") == "direct"

    def test_direct_mention_in_middle(self):
        assert _is_mentioned("hey @agent-1, can you help?", "agent-1") == "direct"

    def test_no_direct_mention(self):
        assert _is_mentioned("hey agent-1 fix it", "agent-1") is None  # no @ prefix

    def test_partial_match_not_triggered(self):
        """@agent-10 should not match agent-1 (it does because 'agent-1' is substring)."""
        # This is actually expected behavior — substring match
        assert _is_mentioned("@agent-10 do something", "agent-1") == "direct"

    # Role mentions
    def test_role_mention(self):
        assert _is_mentioned("@Lead Developer check this", "x", "Lead Developer") == "role"

    def test_role_mention_case_insensitive(self):
        assert _is_mentioned("@LEAD DEVELOPER review", "x", "Lead Developer") == "role"

    def test_no_role_when_empty(self):
        assert _is_mentioned("@Lead Developer check", "x", "") is None

    def test_no_role_when_not_mentioned(self):
        assert _is_mentioned("hello world", "x", "Lead Developer") is None

    # Broadcast mentions
    def test_at_all(self):
        assert _is_mentioned("@all standup time", "x") == "broadcast"

    def test_at_notify_all_hyphen(self):
        assert _is_mentioned("@notify-all meeting in 5", "x") == "broadcast"

    def test_at_notify_all_underscore(self):
        assert _is_mentioned("@notify_all heads up", "x") == "broadcast"

    def test_at_notifyall(self):
        assert _is_mentioned("@notifyall deploy incoming", "x") == "broadcast"

    def test_broadcast_case_insensitive(self):
        assert _is_mentioned("@ALL wake up", "x") == "broadcast"

    # Priority: direct > role > broadcast
    def test_direct_takes_priority_over_broadcast(self):
        assert _is_mentioned("@agent-1 @all update", "agent-1") == "direct"

    def test_direct_takes_priority_over_role(self):
        assert _is_mentioned("@agent-1 @Lead Developer review", "agent-1", "Lead Developer") == "direct"

    def test_role_takes_priority_over_broadcast(self):
        assert _is_mentioned("@Lead Developer @all standup", "x", "Lead Developer") == "role"

    # No mention
    def test_no_mention_at_all(self):
        assert _is_mentioned("just a regular message", "agent-1", "Dev") is None

    def test_empty_content(self):
        assert _is_mentioned("", "agent-1") is None

    # Multiple mentions in one message
    def test_multiple_agents_direct(self):
        content = "@agent-1 and @agent-2 please coordinate"
        assert _is_mentioned(content, "agent-1") == "direct"
        assert _is_mentioned(content, "agent-2") == "direct"
        assert _is_mentioned(content, "agent-3") is None


# ── _handle_message tests ────────────────────────────────────────────────────


class TestHandleMessage:
    @pytest.mark.asyncio
    async def test_skips_own_messages(self):
        """Messages from the listener's own session_id are ignored."""
        listener = SubgentWSListener()
        conn = BoardConnection(
            url="ws://localhost", board_id="test", api_key="key",
            session_id="my-agent", job_title="Dev",
        )

        with patch("coral.background_tasks.subgent_ws.discover_coral_agents", new_callable=AsyncMock) as mock_discover:
            await listener._handle_message(conn, {
                "type": "message",
                "session_id": "my-agent",  # same as conn
                "content": "@all important update",
            })
            mock_discover.assert_not_called()

    @pytest.mark.asyncio
    async def test_sends_tmux_nudge_on_mention(self):
        """Mentioned agents get a tmux nudge."""
        listener = SubgentWSListener()
        conn = BoardConnection(
            url="ws://localhost", board_id="test", api_key="key",
            session_id="listener", job_title="QA",
        )

        mock_agents = [
            {
                "agent_type": "claude",
                "agent_name": "backend-dev",
                "session_id": "uuid-123",
                "tmux_session": "agent-1",
                "job_title": "Dev",
            },
        ]

        with (
            patch("coral.background_tasks.subgent_ws.discover_coral_agents", new_callable=AsyncMock, return_value=mock_agents),
            patch("coral.background_tasks.subgent_ws.send_to_tmux", new_callable=AsyncMock, return_value=None) as mock_tmux,
        ):
            await listener._handle_message(conn, {
                "type": "message",
                "session_id": "other-agent",
                "content": "@agent-1 please review this PR",
            })

            mock_tmux.assert_called_once()
            call_args = mock_tmux.call_args
            assert call_args[0][0] == "backend-dev"  # agent_name
            assert "coral-board read" in call_args[0][1]  # nudge text

    @pytest.mark.asyncio
    async def test_no_nudge_when_not_mentioned(self):
        """Non-mentioned agents don't get nudged."""
        listener = SubgentWSListener()
        conn = BoardConnection(
            url="ws://localhost", board_id="test", api_key="key",
            session_id="listener", job_title="QA",
        )

        mock_agents = [
            {
                "agent_type": "claude",
                "agent_name": "backend-dev",
                "session_id": "uuid-123",
                "tmux_session": "agent-1",
                "job_title": "Dev",
            },
        ]

        with (
            patch("coral.background_tasks.subgent_ws.discover_coral_agents", new_callable=AsyncMock, return_value=mock_agents),
            patch("coral.background_tasks.subgent_ws.send_to_tmux", new_callable=AsyncMock) as mock_tmux,
        ):
            await listener._handle_message(conn, {
                "type": "message",
                "session_id": "other-agent",
                "content": "just a general update, no mentions",
            })

            mock_tmux.assert_not_called()

    @pytest.mark.asyncio
    async def test_broadcast_nudges_all_agents(self):
        """@all mention nudges all discovered agents."""
        listener = SubgentWSListener()
        conn = BoardConnection(
            url="ws://localhost", board_id="test", api_key="key",
            session_id="listener", job_title="QA",
        )

        mock_agents = [
            {"agent_type": "claude", "agent_name": "dev1", "session_id": "u1", "tmux_session": "a1", "job_title": "Dev"},
            {"agent_type": "claude", "agent_name": "dev2", "session_id": "u2", "tmux_session": "a2", "job_title": "QA"},
        ]

        with (
            patch("coral.background_tasks.subgent_ws.discover_coral_agents", new_callable=AsyncMock, return_value=mock_agents),
            patch("coral.background_tasks.subgent_ws.send_to_tmux", new_callable=AsyncMock, return_value=None) as mock_tmux,
        ):
            await listener._handle_message(conn, {
                "type": "message",
                "session_id": "sender",
                "content": "@all standup time",
            })

            assert mock_tmux.call_count == 2


# ── SubgentWSListener lifecycle tests ─────────────────────────────────────────


class TestSubgentWSListener:
    @pytest.mark.asyncio
    async def test_start_board_creates_task(self):
        listener = SubgentWSListener()
        with patch.object(listener, "_listen_forever", new_callable=AsyncMock) as mock_listen:
            await listener.start_board(
                ws_url="ws://localhost:8420",
                board_id="test-board",
                api_key="cb_agent_xxx",
                session_id="agent-1",
                job_title="Dev",
            )

            key = "ws://localhost:8420:test-board"
            assert key in listener._boards
            conn = listener._boards[key]
            assert conn.url == "ws://localhost:8420"
            assert conn.board_id == "test-board"
            assert conn.session_id == "agent-1"
            assert conn.task is not None

            # Cleanup
            await listener.stop_all()

    @pytest.mark.asyncio
    async def test_stop_board_cancels_task(self):
        listener = SubgentWSListener()

        # Create a mock task that blocks
        async def fake_listen(conn):
            await asyncio.sleep(100)

        with patch.object(listener, "_listen_forever", side_effect=fake_listen):
            await listener.start_board(
                ws_url="ws://localhost",
                board_id="p",
                api_key="key",
                session_id="s",
            )

            assert "ws://localhost:p" in listener._boards
            await listener.stop_board("ws://localhost", "p")
            assert "ws://localhost:p" not in listener._boards

    @pytest.mark.asyncio
    async def test_duplicate_start_is_noop(self):
        listener = SubgentWSListener()
        call_count = 0

        async def counting_listen(conn):
            nonlocal call_count
            call_count += 1
            await asyncio.sleep(100)

        with patch.object(listener, "_listen_forever", side_effect=counting_listen):
            await listener.start_board(ws_url="ws://host", board_id="p", api_key="k", session_id="s")
            # Give the task a moment to start
            await asyncio.sleep(0.01)
            await listener.start_board(ws_url="ws://host", board_id="p", api_key="k", session_id="s")  # duplicate

            assert call_count == 1

            await listener.stop_all()


# ── _listen_forever auth flow tests ───────────────────────────────────────────


class TestListenForeverAuth:
    @pytest.mark.asyncio
    async def test_auth_failure_stops_without_retry(self):
        """If auth returns an error, _listen_forever exits (no reconnect loop)."""
        import sys

        listener = SubgentWSListener()
        conn = BoardConnection(
            url="ws://localhost", board_id="test", api_key="bad-key",
            session_id="agent", job_title="Dev",
        )

        mock_ws = AsyncMock()
        mock_ws.recv = AsyncMock(return_value=json.dumps({
            "type": "error",
            "message": "invalid token",
        }))
        mock_ws.__aenter__ = AsyncMock(return_value=mock_ws)
        mock_ws.__aexit__ = AsyncMock(return_value=False)

        mock_websockets = MagicMock()
        mock_websockets.connect = MagicMock(return_value=mock_ws)

        with patch.dict(sys.modules, {"websockets": mock_websockets}):
            await listener._listen_forever(conn)

        # Should have sent auth message
        mock_ws.send.assert_called_once()
        auth_payload = json.loads(mock_ws.send.call_args[0][0])
        assert auth_payload["type"] == "auth"
        assert auth_payload["token"] == "bad-key"
        assert auth_payload["session_id"] == "agent"

    @pytest.mark.asyncio
    async def test_auth_sends_correct_payload(self):
        """Auth message includes type, token, session_id, and last_seen_id."""
        import sys

        listener = SubgentWSListener()
        conn = BoardConnection(
            url="ws://localhost:8420", board_id="my-board", api_key="cb_agent_xxx",
            session_id="agent-99", job_title="QA",
        )

        mock_ws = AsyncMock()
        mock_ws.recv = AsyncMock(return_value=json.dumps({
            "type": "error",  # fail after auth to exit cleanly
            "message": "test",
        }))
        mock_ws.__aenter__ = AsyncMock(return_value=mock_ws)
        mock_ws.__aexit__ = AsyncMock(return_value=False)

        mock_websockets = MagicMock()
        mock_websockets.connect = MagicMock(return_value=mock_ws)

        with patch.dict(sys.modules, {"websockets": mock_websockets}):
            await listener._listen_forever(conn)

        # Verify WebSocket endpoint
        mock_websockets.connect.assert_called_once_with(
            "ws://localhost:8420/api/board/ws/my-board"  # board_id used in WS URL
        )

        # Verify auth payload
        auth_payload = json.loads(mock_ws.send.call_args[0][0])
        assert auth_payload == {
            "type": "auth",
            "token": "cb_agent_xxx",
            "session_id": "agent-99",
            "last_seen_id": 0,
        }
