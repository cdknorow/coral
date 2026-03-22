"""Tests for configurable receive modes on the message board.

Covers:
- Default receive_mode is 'mentions' (backward-compatible)
- receive_mode='all' counts ALL messages from others as unread
- receive_mode='none' always returns 0 unread
- receive_mode='<group-id>' counts only messages from group members
- get_all_unread_counts() respects receive_mode per subscriber
- Group CRUD: add_to_group, remove_from_group, list_group_members, list_groups
- subscribe() stores and returns receive_mode
- Orchestrator auto-subscribes with receive_mode='all'
"""

import pytest
import pytest_asyncio

from coral.messageboard.store import MessageBoardStore


@pytest_asyncio.fixture
async def store(tmp_path):
    s = MessageBoardStore(db_path=tmp_path / "test_board.db")
    yield s
    await s.close()


# ── Subscribe receive_mode ──────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_subscribe_default_receive_mode(store):
    """Default subscribe should set receive_mode to 'mentions'."""
    sub = await store.subscribe("proj", "agent-1", "Dev")
    assert sub["receive_mode"] == "mentions"


@pytest.mark.asyncio
async def test_subscribe_with_receive_mode_all(store):
    """Subscribe with receive_mode='all' should persist."""
    sub = await store.subscribe("proj", "agent-1", "Orchestrator", receive_mode="all")
    assert sub["receive_mode"] == "all"


@pytest.mark.asyncio
async def test_subscribe_with_receive_mode_none(store):
    """Subscribe with receive_mode='none' should persist."""
    sub = await store.subscribe("proj", "agent-1", "Silent", receive_mode="none")
    assert sub["receive_mode"] == "none"


@pytest.mark.asyncio
async def test_subscribe_with_receive_mode_group(store):
    """Subscribe with a group-id receive_mode should persist."""
    sub = await store.subscribe("proj", "agent-1", "Dev", receive_mode="backend-team")
    assert sub["receive_mode"] == "backend-team"


@pytest.mark.asyncio
async def test_resubscribe_updates_receive_mode(store):
    """Re-subscribing should update receive_mode."""
    await store.subscribe("proj", "agent-1", "Dev", receive_mode="mentions")
    sub = await store.subscribe("proj", "agent-1", "Dev", receive_mode="all")
    assert sub["receive_mode"] == "all"


# ── check_unread with receive_mode='mentions' (default) ─────────────────────


@pytest.mark.asyncio
async def test_check_unread_mentions_mode_only_counts_mentions(store):
    """With receive_mode='mentions', only @mentioned messages count."""
    await store.subscribe("proj", "agent-1", "Backend Dev", receive_mode="mentions")
    await store.subscribe("proj", "agent-2", "Frontend")

    await store.post_message("proj", "agent-2", "general update no mention")
    await store.post_message("proj", "agent-2", "@Backend Dev please review")
    await store.post_message("proj", "agent-2", "@notify-all heads up")

    count = await store.check_unread("proj", "agent-1")
    assert count == 2  # @Backend Dev + @notify-all, not the general one


# ── check_unread with receive_mode='all' ─────────────────────────────────────


@pytest.mark.asyncio
async def test_check_unread_all_mode_counts_everything(store):
    """With receive_mode='all', ALL messages from others count as unread."""
    await store.subscribe("proj", "agent-1", "Orchestrator", receive_mode="all")
    await store.subscribe("proj", "agent-2", "Worker")

    await store.post_message("proj", "agent-2", "general update")
    await store.post_message("proj", "agent-2", "another update")
    await store.post_message("proj", "agent-2", "third message")

    count = await store.check_unread("proj", "agent-1")
    assert count == 3


@pytest.mark.asyncio
async def test_check_unread_all_mode_excludes_own_messages(store):
    """With receive_mode='all', own messages are still excluded."""
    await store.subscribe("proj", "agent-1", "Orchestrator", receive_mode="all")
    await store.subscribe("proj", "agent-2", "Worker")

    await store.post_message("proj", "agent-1", "my own message")
    await store.post_message("proj", "agent-2", "other's message")

    count = await store.check_unread("proj", "agent-1")
    assert count == 1


# ── check_unread with receive_mode='none' ────────────────────────────────────


@pytest.mark.asyncio
async def test_check_unread_none_mode_always_zero(store):
    """With receive_mode='none', unread count is always 0."""
    await store.subscribe("proj", "agent-1", "Silent Agent", receive_mode="none")
    await store.subscribe("proj", "agent-2", "Worker")

    await store.post_message("proj", "agent-2", "@notify-all important!")
    await store.post_message("proj", "agent-2", "@Silent Agent direct mention")
    await store.post_message("proj", "agent-2", "general message")

    count = await store.check_unread("proj", "agent-1")
    assert count == 0


# ── check_unread with receive_mode='<group-id>' ─────────────────────────────


@pytest.mark.asyncio
async def test_check_unread_group_mode_only_counts_group_members(store):
    """With receive_mode=group-id, only messages from group members count."""
    await store.subscribe("proj", "agent-1", "Dev", receive_mode="backend-team")
    await store.subscribe("proj", "agent-2", "Backend 2")
    await store.subscribe("proj", "agent-3", "Frontend")

    # Add agent-2 to backend-team group, agent-3 is NOT in the group
    await store.add_to_group("proj", "backend-team", "agent-2")

    await store.post_message("proj", "agent-2", "backend update")  # in group
    await store.post_message("proj", "agent-3", "frontend update")  # not in group

    count = await store.check_unread("proj", "agent-1")
    assert count == 1  # only agent-2's message


@pytest.mark.asyncio
async def test_check_unread_group_mode_empty_group_zero(store):
    """With a group-id that has no members, unread count is 0."""
    await store.subscribe("proj", "agent-1", "Dev", receive_mode="empty-group")
    await store.subscribe("proj", "agent-2", "Worker")

    await store.post_message("proj", "agent-2", "hello")

    count = await store.check_unread("proj", "agent-1")
    assert count == 0


# ── get_all_unread_counts with mixed receive_modes ──────────────────────────


@pytest.mark.asyncio
async def test_get_all_unread_counts_respects_receive_modes(store):
    """get_all_unread_counts should branch on each subscriber's receive_mode."""
    await store.subscribe("proj", "orch", "Orchestrator", receive_mode="all")
    await store.subscribe("proj", "worker", "Worker", receive_mode="mentions")
    await store.subscribe("proj", "silent", "Silent", receive_mode="none")
    await store.subscribe("proj", "poster", "Poster")

    await store.post_message("proj", "poster", "general message no mention")
    await store.post_message("proj", "poster", "@notify-all broadcast")

    counts = await store.get_all_unread_counts()

    # orch (all mode): sees both messages
    assert counts["orch"] == 2
    # worker (mentions mode): only sees the @notify-all one
    assert counts["worker"] == 1
    # silent (none mode): sees nothing
    assert counts["silent"] == 0
    # poster: own messages excluded
    assert counts["poster"] == 0


@pytest.mark.asyncio
async def test_get_all_unread_counts_with_group_mode(store):
    """get_all_unread_counts should support group-based receive_mode."""
    await store.subscribe("proj", "grouped", "Dev", receive_mode="my-team")
    await store.subscribe("proj", "teammate", "Dev2")
    await store.subscribe("proj", "outsider", "Other")

    await store.add_to_group("proj", "my-team", "teammate")

    await store.post_message("proj", "teammate", "team update")
    await store.post_message("proj", "outsider", "other update")

    counts = await store.get_all_unread_counts()
    assert counts["grouped"] == 1  # only teammate's message


# ── Group CRUD ───────────────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_add_to_group(store):
    """add_to_group should create a group membership."""
    await store.add_to_group("proj", "team-a", "agent-1")
    members = await store.list_group_members("proj", "team-a")
    member_ids = [m["session_id"] for m in members]
    assert "agent-1" in member_ids


@pytest.mark.asyncio
async def test_add_to_group_idempotent(store):
    """Adding same member twice should not duplicate."""
    await store.add_to_group("proj", "team-a", "agent-1")
    await store.add_to_group("proj", "team-a", "agent-1")
    members = await store.list_group_members("proj", "team-a")
    assert len(members) == 1


@pytest.mark.asyncio
async def test_remove_from_group(store):
    """remove_from_group should remove a membership."""
    await store.add_to_group("proj", "team-a", "agent-1")
    await store.remove_from_group("proj", "team-a", "agent-1")
    members = await store.list_group_members("proj", "team-a")
    member_ids = [m["session_id"] for m in members]
    assert "agent-1" not in member_ids


@pytest.mark.asyncio
async def test_list_group_members_multiple(store):
    """list_group_members returns all members of a group as objects."""
    await store.add_to_group("proj", "team-a", "agent-1")
    await store.add_to_group("proj", "team-a", "agent-2")
    await store.add_to_group("proj", "team-a", "agent-3")
    members = await store.list_group_members("proj", "team-a")
    member_ids = {m["session_id"] for m in members}
    assert member_ids == {"agent-1", "agent-2", "agent-3"}
    # Each member should have a job_title key
    assert all("job_title" in m for m in members)


@pytest.mark.asyncio
async def test_list_group_members_empty(store):
    """list_group_members returns empty list for nonexistent group."""
    members = await store.list_group_members("proj", "nonexistent")
    assert members == []


@pytest.mark.asyncio
async def test_list_groups(store):
    """list_groups returns all group IDs for a project with member counts."""
    await store.add_to_group("proj", "team-a", "agent-1")
    await store.add_to_group("proj", "team-b", "agent-2")
    await store.add_to_group("proj", "team-a", "agent-3")
    groups = await store.list_groups("proj")
    group_ids = {g["group_id"] for g in groups}
    assert group_ids == {"team-a", "team-b"}
    team_a = next(g for g in groups if g["group_id"] == "team-a")
    assert team_a["member_count"] == 2


@pytest.mark.asyncio
async def test_list_groups_empty(store):
    """list_groups returns empty list when no groups exist."""
    groups = await store.list_groups("proj")
    assert len(groups) == 0


@pytest.mark.asyncio
async def test_groups_are_project_scoped(store):
    """Groups in different projects are independent."""
    await store.add_to_group("proj1", "team-a", "agent-1")
    await store.add_to_group("proj2", "team-a", "agent-2")

    members1 = await store.list_group_members("proj1", "team-a")
    members2 = await store.list_group_members("proj2", "team-a")
    assert [m["session_id"] for m in members1] == ["agent-1"]
    assert [m["session_id"] for m in members2] == ["agent-2"]


# ── Backward compatibility ───────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_delete_project_cleans_up_groups(store):
    """delete_project should remove board_groups entries too."""
    await store.subscribe("proj", "agent-1", "Dev")
    await store.add_to_group("proj", "team-a", "agent-1")
    await store.add_to_group("proj", "team-a", "agent-2")

    await store.delete_project("proj")

    members = await store.list_group_members("proj", "team-a")
    assert members == []
    groups = await store.list_groups("proj")
    assert len(groups) == 0


@pytest.mark.asyncio
async def test_backward_compat_existing_subscribe_defaults_to_mentions(store):
    """Existing code calling subscribe() without receive_mode gets 'mentions'."""
    sub = await store.subscribe("proj", "agent-1", "Dev")
    assert sub["receive_mode"] == "mentions"

    # And check_unread should behave as mentions mode
    await store.subscribe("proj", "agent-2", "Other")
    await store.post_message("proj", "agent-2", "no mention here")
    count = await store.check_unread("proj", "agent-1")
    assert count == 0

    await store.post_message("proj", "agent-2", "@notify-all broadcast")
    count = await store.check_unread("proj", "agent-1")
    assert count == 1
