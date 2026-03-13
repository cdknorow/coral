"""API routes for custom theme management."""

from __future__ import annotations

import json
import logging
from pathlib import Path

import shutil

from fastapi import APIRouter, UploadFile, File

log = logging.getLogger(__name__)

router = APIRouter()

THEMES_DIR = Path.home() / ".corral" / "themes"


def _ensure_dir():
    THEMES_DIR.mkdir(parents=True, exist_ok=True)


def _theme_path(name: str) -> Path:
    # Sanitize name to prevent path traversal
    safe = "".join(c for c in name if c.isalnum() or c in "-_ ").strip()
    if not safe:
        raise ValueError("Invalid theme name")
    return THEMES_DIR / f"{safe}.json"


# ── Default variable definitions (used to populate the editor) ────────────

THEME_VARIABLES = {
    "Surface / Background": {
        "--bg-primary": "Primary background",
        "--bg-secondary": "Secondary background",
        "--bg-tertiary": "Tertiary background",
        "--bg-hover": "Hover background",
        "--bg-elevated": "Elevated surface",
        "--topbar-bg": "Top bar background",
        "--topbar-border": "Top bar border",
    },
    "Borders": {
        "--border": "Border",
        "--border-light": "Light border",
    },
    "Text": {
        "--text-primary": "Primary text",
        "--text-secondary": "Secondary text",
        "--text-muted": "Muted text",
    },
    "Accent / Brand": {
        "--accent": "Accent",
        "--accent-dim": "Accent dim",
    },
    "Semantic Status": {
        "--success": "Success",
        "--warning": "Warning",
        "--error": "Error",
    },
    "Agent Badges": {
        "--badge-claude": "Claude badge",
        "--badge-gemini": "Gemini badge",
    },
    "Syntax Highlighting": {
        "--sh-keyword": "Keyword",
        "--sh-string": "String",
        "--sh-comment": "Comment",
        "--sh-number": "Number",
        "--sh-builtin": "Builtin",
        "--sh-decorator": "Decorator",
    },
    "Diff": {
        "--diff-add-bg": "Addition background",
        "--diff-add-color": "Addition text",
        "--diff-del-bg": "Deletion background",
        "--diff-del-color": "Deletion text",
    },
    "Tool / Event Colors": {
        "--color-tool-read": "Read tool",
        "--color-tool-write": "Write tool",
        "--color-tool-edit": "Edit tool",
        "--color-tool-bash": "Bash tool",
        "--color-tool-grep": "Grep tool",
        "--color-tool-web": "Web tool",
        "--color-tool-status": "Status event",
        "--color-tool-goal": "Goal event",
        "--color-tool-stop": "Stop event",
    },
    "Chat": {
        "--chat-human-bg": "Human message background",
        "--chat-human-color": "Human message text",
    },
    "Terminal (xterm)": {
        "--xterm-background": "Background",
        "--xterm-foreground": "Foreground",
        "--xterm-cursor": "Cursor",
        "--xterm-selection-background": "Selection background",
        "--xterm-black": "Black",
        "--xterm-red": "Red",
        "--xterm-green": "Green",
        "--xterm-yellow": "Yellow",
        "--xterm-blue": "Blue",
        "--xterm-magenta": "Magenta",
        "--xterm-cyan": "Cyan",
        "--xterm-white": "White",
        "--xterm-bright-black": "Bright black",
        "--xterm-bright-red": "Bright red",
        "--xterm-bright-green": "Bright green",
        "--xterm-bright-yellow": "Bright yellow",
        "--xterm-bright-blue": "Bright blue",
        "--xterm-bright-magenta": "Bright magenta",
        "--xterm-bright-cyan": "Bright cyan",
        "--xterm-bright-white": "Bright white",
    },
    "Diff Viewer (diff2html)": {
        "--d2h-code-bg": "Code background",
        "--d2h-gutter-bg": "Gutter background",
        "--d2h-ins-bg": "Insertion background",
        "--d2h-ins-gutter-bg": "Insertion gutter",
        "--d2h-ins-highlight": "Insertion highlight",
        "--d2h-del-bg": "Deletion background",
        "--d2h-del-gutter-bg": "Deletion gutter",
        "--d2h-del-highlight": "Deletion highlight",
        "--d2h-empty-bg": "Empty placeholder",
        "--d2h-hunk-bg": "Hunk header",
    },
}


@router.get("/api/themes/variables")
async def get_theme_variables():
    """Return the theme variable definitions grouped by category."""
    return {"groups": THEME_VARIABLES}


@router.get("/api/themes")
async def list_themes():
    """List all saved custom themes."""
    _ensure_dir()
    themes = []
    for f in sorted(THEMES_DIR.glob("*.json")):
        try:
            data = json.loads(f.read_text())
            themes.append({
                "name": f.stem,
                "description": data.get("description", ""),
                "base": data.get("base", "dark"),
            })
        except (json.JSONDecodeError, OSError):
            continue
    return {"themes": themes}


@router.get("/api/themes/{name}")
async def get_theme(name: str):
    """Get a specific theme by name."""
    try:
        path = _theme_path(name)
    except ValueError:
        return {"error": "Invalid theme name"}
    if not path.exists():
        return {"error": f"Theme '{name}' not found"}
    try:
        data = json.loads(path.read_text())
        return {"name": name, "theme": data}
    except (json.JSONDecodeError, OSError) as e:
        return {"error": str(e)}


@router.put("/api/themes/{name}")
async def save_theme(name: str, body: dict):
    """Save or update a custom theme."""
    _ensure_dir()
    try:
        path = _theme_path(name)
    except ValueError:
        return {"error": "Invalid theme name"}
    theme_data = {
        "description": body.get("description", ""),
        "base": body.get("base", "dark"),
        "variables": body.get("variables", {}),
    }
    path.write_text(json.dumps(theme_data, indent=2))
    return {"ok": True, "name": name}


@router.delete("/api/themes/{name}")
async def delete_theme(name: str):
    """Delete a custom theme."""
    try:
        path = _theme_path(name)
    except ValueError:
        return {"error": "Invalid theme name"}
    if path.exists():
        path.unlink()
    return {"ok": True}


@router.post("/api/themes/import")
async def import_theme(file: UploadFile = File(...)):
    """Import a theme from an uploaded JSON file."""
    _ensure_dir()
    try:
        content = await file.read()
        data = json.loads(content)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return {"error": "Invalid JSON file"}

    name = data.get("name") or file.filename.replace(".json", "")
    # Sanitize
    safe_name = "".join(c for c in name if c.isalnum() or c in "-_ ").strip()
    if not safe_name:
        return {"error": "Could not determine theme name"}

    theme_data = {
        "description": data.get("description", ""),
        "base": data.get("base", "dark"),
        "variables": data.get("variables", {}),
    }
    path = THEMES_DIR / f"{safe_name}.json"
    path.write_text(json.dumps(theme_data, indent=2))
    return {"ok": True, "name": safe_name}


GENERATE_PROMPT = """You are a UI theme designer. Given a description of a color theme, generate a complete set of CSS color values for a web dashboard.

You MUST respond with ONLY a valid JSON object — no markdown, no explanation, no code fences. The JSON must have this exact structure:

{
  "name": "A short creative name for this theme (2-4 words)",
  "variables": {
    "--css-variable-name": "#hexcolor",
    ...
  }
}

Here are the CSS variables you must provide values for, grouped by category:

"""


@router.post("/api/themes/generate")
async def generate_theme(body: dict):
    """Use an LLM to generate theme colors from a text description."""
    description = body.get("description", "").strip()
    if not description:
        return {"error": "Description is required"}

    base = body.get("base", "dark")

    # Build the variable list for the prompt
    var_list = ""
    for group_name, vars_dict in THEME_VARIABLES.items():
        var_list += f"\n{group_name}:\n"
        for css_var, label in vars_dict.items():
            var_list += f"  {css_var} — {label}\n"

    prompt = (
        f"{GENERATE_PROMPT}{var_list}\n"
        f"The theme should be based on a {base} color scheme.\n"
        f"User's description: {description}\n\n"
        f"Respond with ONLY the JSON object."
    )

    claude_path = shutil.which("claude")
    if not claude_path:
        return {"error": "Claude CLI not found — install Claude Code to use AI theme generation"}

    from corral.tools.utils import run_cmd

    rc, stdout, stderr = await run_cmd(
        claude_path,
        "--print",
        "--model", "haiku",
        "--no-session-persistence",
        prompt,
        timeout=60.0,
    )

    if rc != 0:
        err = stderr.strip() if stderr else "Unknown error"
        return {"error": f"Claude CLI failed: {err}"}

    # Parse the response — strip any markdown fences if present
    raw = (stdout or "").strip()
    if raw.startswith("```"):
        # Remove code fences
        lines = raw.split("\n")
        lines = [l for l in lines if not l.strip().startswith("```")]
        raw = "\n".join(lines).strip()

    try:
        result = json.loads(raw)
    except json.JSONDecodeError:
        return {"error": "Failed to parse LLM response as JSON", "raw": raw}

    variables = result.get("variables", result)
    if not isinstance(variables, dict):
        return {"error": "LLM response missing variables object", "raw": raw}

    name = result.get("name", "")
    return {"ok": True, "variables": variables, "name": name}
