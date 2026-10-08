# Themes API

Coral supports custom color themes stored as JSON files in `~/.coral/themes/`. Three bundled themes (GhostV3, Obsidian, Dark) are auto-seeded on first startup.

Theme names are sanitized to alphanumeric characters, hyphens, underscores, and spaces.

## List Themes

```
GET /api/themes
```

Returns all available themes with metadata.

**Response:**
```json
{
  "themes": [
    {
      "name": "GhostV3",
      "description": "Dark indigo theme with soft pastels and cool blue accents",
      "base": "dark"
    }
  ]
}
```

## Get Theme

```
GET /api/themes/{name}
```

Returns a single theme with all CSS variables.

**Parameters:**
| Name | In | Description |
|------|-----|-------------|
| `name` | path | Theme name |

**Response:**
```json
{
  "name": "GhostV3",
  "theme": {
    "description": "Dark indigo theme with soft pastels and cool blue accents",
    "base": "dark",
    "variables": {
      "--bg-primary": "#0a0e27",
      "--bg-secondary": "#1a1f3a"
    }
  }
}
```

**Errors:**
- `400` — Invalid theme name
- `404` — Theme not found

## Save/Update Theme

```
PUT /api/themes/{name}
```

Creates or updates a theme.

**Parameters:**
| Name | In | Description |
|------|-----|-------------|
| `name` | path | Theme name |

**Request Body:**
```json
{
  "description": "Theme description",
  "base": "dark",
  "variables": {
    "--bg-primary": "#0a0e27",
    "--bg-secondary": "#1a1f3a"
  }
}
```

**Response:**
```json
{
  "ok": true,
  "name": "MyTheme"
}
```

## Delete Theme

```
DELETE /api/themes/{name}
```

Deletes a theme file.

**Parameters:**
| Name | In | Description |
|------|-----|-------------|
| `name` | path | Theme name |

**Response:**
```json
{
  "ok": true
}
```

## Import Theme

```
POST /api/themes/import
```

Imports a theme from an uploaded JSON file (max 10 MB). The theme name is extracted from the JSON `name` field, falling back to the filename.

**Request:** `multipart/form-data` with a `file` field containing the JSON theme file.

**Response:**
```json
{
  "ok": true,
  "name": "ImportedTheme"
}
```

**Errors:**
- `400` — No file provided, invalid JSON, or cannot determine theme name

## Get Theme Variables

```
GET /api/themes/variables
```

Returns the available CSS variable groups and their human-readable labels. Useful for building a theme editor UI.

**Response:**
```json
{
  "groups": {
    "Surface / Background": {
      "--bg-primary": "Primary background",
      "--bg-secondary": "Secondary background",
      "--bg-tertiary": "Tertiary background",
      "--bg-hover": "Hover background",
      "--bg-elevated": "Elevated surface",
      "--topbar-bg": "Top bar background",
      "--topbar-border": "Top bar border"
    },
    "Borders": {
      "--border": "Border",
      "--border-light": "Light border"
    },
    "Text": {
      "--text-primary": "Primary text",
      "--text-secondary": "Secondary text",
      "--text-muted": "Muted text"
    },
    "Accent / Brand": {
      "--accent": "Accent",
      "--accent-dim": "Accent dim"
    },
    "Semantic Status": {
      "--success": "Success",
      "--warning": "Warning",
      "--error": "Error"
    },
    "Agent Rail": {
      "--rail-tile-bg": "Rail tile background",
      "--rail-tile-bg-hover": "Rail tile hover background",
      "--rail-tile-border": "Rail tile border",
      "--rail-tile-text": "Rail tile text",
      "--rail-tile-active-bg": "Selected rail tile background",
      "--rail-tile-active-border": "Selected rail tile border",
      "--rail-chip-text": "Rail team/folder chip text",
      "--rail-badge-unread": "Rail unread badge",
      "--rail-badge-done": "Rail done badge",
      "--rail-badge-text": "Rail badge text"
    },
    "Agent Badges": {
      "--badge-claude": "Claude badge",
      "--badge-agy": "Antigravity badge",
      "--badge-gemini": "Gemini badge"
    },
    "Syntax Highlighting": { "...": "..." },
    "Diff": { "...": "..." },
    "Tool / Event Colors": { "...": "..." },
    "Chat": { "...": "..." },
    "Terminal (xterm)": { "...": "..." },
    "Message Board": { "...": "..." }
  }
}
```

## Generate Theme with LLM

```
POST /api/themes/generate
```

Uses an LLM (Claude, Antigravity, or Codex) to generate theme colors from a text description. Requires at least one CLI tool installed (`claude`, `agy`, or `codex`).

**Request Body:**
```json
{
  "description": "A cozy warm theme with earthy tones",
  "base": "dark",
  "agent_type": "claude"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `description` | string | yes | Text description of the desired theme |
| `base` | string | no | `"dark"` (default) or `"light"` |
| `agent_type` | string | no | Preferred LLM: `"claude"`, `"agy"`, or `"codex"`. Falls back to any available CLI. |

**Response:**
```json
{
  "ok": true,
  "variables": {
    "--bg-primary": "#2d1b0e",
    "--bg-secondary": "#3a2515"
  },
  "name": "Cozy Earth"
}
```

**Errors:**
- `400` — Description is required
- `500` — No LLM CLI found, or LLM output could not be parsed as JSON
