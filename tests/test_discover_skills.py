"""Tests for skill discovery in coral.agents.base."""

import json
import pytest
from pathlib import Path

from coral.agents.base import discover_skills, _try_add_skill, _scan_skills_dir


# ── _try_add_skill ────────────────────────────────────────────────────────


def test_try_add_skill_with_name_in_frontmatter(tmp_path):
    """Skill with 'name' in frontmatter uses it directly."""
    md = tmp_path / "my-skill.md"
    md.write_text("---\nname: refine\ndescription: Refine code\n---\nBody")
    seen, results = set(), []
    _try_add_skill(md, seen, results)
    assert len(results) == 1
    assert results[0]["name"] == "refine"
    assert results[0]["command"] == "/refine"
    assert results[0]["description"] == "Refine code"


def test_try_add_skill_falls_back_to_stem(tmp_path):
    """When no 'name' in frontmatter, uses filename stem."""
    md = tmp_path / "feature-dev.md"
    md.write_text("---\ndescription: Develop features\nargument-hint: feature\n---\nBody")
    seen, results = set(), []
    _try_add_skill(md, seen, results)
    assert len(results) == 1
    assert results[0]["name"] == "feature-dev"
    assert results[0]["command"] == "/feature-dev"


def test_try_add_skill_falls_back_to_parent_for_skill_md(tmp_path):
    """SKILL.md files use parent directory name as fallback."""
    skill_dir = tmp_path / "code-review"
    skill_dir.mkdir()
    md = skill_dir / "SKILL.md"
    md.write_text("---\ndescription: Review code\n---\nBody")
    seen, results = set(), []
    _try_add_skill(md, seen, results)
    assert len(results) == 1
    assert results[0]["name"] == "code-review"


def test_try_add_skill_deduplicates(tmp_path):
    """Second skill with same name is skipped."""
    md = tmp_path / "dup.md"
    md.write_text("---\nname: dup\ndescription: first\n---\n")
    seen, results = set(), []
    _try_add_skill(md, seen, results)
    _try_add_skill(md, seen, results)
    assert len(results) == 1


def test_try_add_skill_no_frontmatter(tmp_path):
    """File with no frontmatter uses filename stem."""
    md = tmp_path / "plain.md"
    md.write_text("No frontmatter here, just markdown.")
    seen, results = set(), []
    _try_add_skill(md, seen, results)
    assert len(results) == 1
    assert results[0]["name"] == "plain"


# ── discover_skills – v2 plugin format ────────────────────────────────────


def _make_plugin(plugin_dir, subdirs):
    """Create a fake plugin directory with skills/commands/agents subdirs."""
    for subdir_name, files in subdirs.items():
        d = plugin_dir / subdir_name
        d.mkdir(parents=True, exist_ok=True)
        for filename, content in files.items():
            (d / filename).write_text(content)


def test_discover_skills_v2_format(tmp_path, monkeypatch):
    """discover_skills parses v2 installed_plugins.json correctly."""
    # Set up fake home
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    monkeypatch.setattr(Path, "home", staticmethod(lambda: fake_home))

    # Create user skills dir (empty)
    (fake_home / ".claude" / "skills").mkdir(parents=True)

    # Create plugin dir with skills, commands, and agents
    plugin_dir = tmp_path / "plugins" / "feature-dev"
    _make_plugin(plugin_dir, {
        "skills": {
            "refine.md": "---\nname: refine\ndescription: Refine output\n---\n",
        },
        "commands": {
            "feature-dev.md": "---\ndescription: Develop features\n---\n",
        },
        "agents": {
            "code-architect.md": "---\ndescription: Design architecture\n---\n",
            "code-explorer.md": "---\nname: code-explorer\ndescription: Explore code\n---\n",
        },
    })

    # Write v2 installed_plugins.json
    plugins_dir = fake_home / ".claude" / "plugins"
    plugins_dir.mkdir(parents=True)
    plugins_json = {
        "version": 2,
        "plugins": {
            "feature-dev@claude-plugins-official": [
                {"installPath": str(plugin_dir), "otherField": "ignored"}
            ]
        }
    }
    (plugins_dir / "installed_plugins.json").write_text(json.dumps(plugins_json))

    results = discover_skills(working_dir=None)
    names = {r["name"] for r in results}
    assert "refine" in names, f"Expected 'refine' in {names}"
    assert "feature-dev" in names, f"Expected 'feature-dev' in {names}"
    assert "code-architect" in names, f"Expected 'code-architect' in {names}"
    assert "code-explorer" in names, f"Expected 'code-explorer' in {names}"


def test_discover_skills_v1_format(tmp_path, monkeypatch):
    """discover_skills still works with v1 list format."""
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    monkeypatch.setattr(Path, "home", staticmethod(lambda: fake_home))
    (fake_home / ".claude" / "skills").mkdir(parents=True)

    plugin_dir = tmp_path / "plugins" / "old-plugin"
    _make_plugin(plugin_dir, {
        "commands": {
            "old-cmd.md": "---\nname: old-cmd\ndescription: Legacy\n---\n",
        },
    })

    plugins_dir = fake_home / ".claude" / "plugins"
    plugins_dir.mkdir(parents=True)
    v1_json = [{"installPath": str(plugin_dir)}]
    (plugins_dir / "installed_plugins.json").write_text(json.dumps(v1_json))

    results = discover_skills(working_dir=None)
    names = {r["name"] for r in results}
    assert "old-cmd" in names


def test_discover_skills_project_overrides_plugin(tmp_path, monkeypatch):
    """Project-level skill with same name takes precedence over plugin."""
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    monkeypatch.setattr(Path, "home", staticmethod(lambda: fake_home))
    (fake_home / ".claude" / "skills").mkdir(parents=True)

    # Project skill
    project_dir = tmp_path / "project"
    project_skills = project_dir / ".claude" / "skills"
    project_skills.mkdir(parents=True)
    (project_skills / "refine.md").write_text(
        "---\nname: refine\ndescription: Project version\n---\n"
    )

    # Plugin with same name
    plugin_dir = tmp_path / "plugins" / "p1"
    _make_plugin(plugin_dir, {
        "skills": {
            "refine.md": "---\nname: refine\ndescription: Plugin version\n---\n",
        },
    })
    plugins_dir = fake_home / ".claude" / "plugins"
    plugins_dir.mkdir(parents=True)
    (plugins_dir / "installed_plugins.json").write_text(json.dumps({
        "version": 2,
        "plugins": {"p1@src": [{"installPath": str(plugin_dir)}]}
    }))

    results = discover_skills(working_dir=str(project_dir))
    refine = [r for r in results if r["name"] == "refine"]
    assert len(refine) == 1
    assert refine[0]["description"] == "Project version"


def test_discover_skills_multiple_plugins(tmp_path, monkeypatch):
    """Multiple plugins in v2 format are all scanned."""
    fake_home = tmp_path / "home"
    fake_home.mkdir()
    monkeypatch.setattr(Path, "home", staticmethod(lambda: fake_home))
    (fake_home / ".claude" / "skills").mkdir(parents=True)

    plugin_a = tmp_path / "plugins" / "a"
    _make_plugin(plugin_a, {
        "commands": {"cmd-a.md": "---\nname: cmd-a\ndescription: A\n---\n"},
    })
    plugin_b = tmp_path / "plugins" / "b"
    _make_plugin(plugin_b, {
        "agents": {"agent-b.md": "---\nname: agent-b\ndescription: B\n---\n"},
    })

    plugins_dir = fake_home / ".claude" / "plugins"
    plugins_dir.mkdir(parents=True)
    (plugins_dir / "installed_plugins.json").write_text(json.dumps({
        "version": 2,
        "plugins": {
            "plugin-a@src": [{"installPath": str(plugin_a)}],
            "plugin-b@src": [{"installPath": str(plugin_b)}],
        }
    }))

    results = discover_skills(working_dir=None)
    names = {r["name"] for r in results}
    assert "cmd-a" in names
    assert "agent-b" in names
