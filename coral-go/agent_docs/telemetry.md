# Telemetry

Coral sends a small, fixed set of anonymous usage events. This page lists all of them. It is the canonical description of what Coral collects — the first-run disclosure in the dashboard is generated from the same source.

**There is no opt-out toggle.** What is true instead is stated under [No opt-out](#no-opt-out), and every claim there is checkable.

---

## Events

Every event Coral can send. There are no others.

| Event | When it fires | Additional properties |
|-------|---------------|-----------------------|
| `install` | The first time Coral runs on this machine | — |
| `upgrade` | The first run after Coral's version changes | — |
| `app_opened` | Every time the Coral server starts | — |
| `session_launched` | Every time you launch a single agent | — |
| `team_launched` | Every time you launch a team | `agent_count` |
| `first_agent_launched` | Once ever: the first agent you launch | — |
| `first_team_launched` | Once ever: the first team you launch | `agent_count` |
| `first_task_completed` | Once ever: the first message-board task marked complete | — |
| `returned_24h` | Once ever: the first time you open Coral more than 24 hours after your first open | — |
| `supporter_checkout_clicked` | Every time you click a link to the supporter store | `surface`, `campaign`, `source`, `medium` |
| `license_activated` | Every time a license key is activated successfully | `product_name`, `variant_name` |

The `first_*` events and `returned_24h` fire at most once per install. Their state lives in `<coralDir>/.milestones.json`, which holds event names and timestamps only.

## Properties

Every event carries exactly four properties:

| Property | Value |
|----------|-------|
| `version` | The Coral version you are running |
| `edition` | The build tier (`prod`, `beta`, `dev`) |
| `os` | Your operating system (`darwin`, `linux`, `windows`) |
| `arch` | Your CPU architecture (`amd64`, `arm64`) |

Three events carry a little more, listed in the table above. `license_activated` carries the product and variant name only — **never** the license key, your name, or your email.

## Identifier

Events are attached to a random UUID stored at `<coralDir>/.install_id`, generated on first run. It is not derived from your hardware, hostname, username, or email address. Delete the file and you become a new, unlinked install.

## Never collected

There is no code path that sends any of the following:

- Your prompts
- Your source code
- Repository, branch, and file names
- Agent output
- Your name, email address, or IP-derived location
- Your license key

## No opt-out

There is no runtime toggle that disables telemetry. Rather than soften that, here is what is true instead:

- **Coral is Apache 2.0.** The entire implementation is `internal/tracking/`. You can read every event before trusting the list above.
- **Builds compiled from source send nothing.** The analytics key is injected at build time via ldflags (`internal/config/config.go`). A build from source has no key, so `TrackEvent` and `TrackInstallAsync` return immediately and send nothing. Such a build also does not consume its one-time `first_*` events, so installing a release later still produces a correct funnel.
- **Downloaded release builds do send these events by default.** Both halves are stated deliberately.
- **Failed deliveries are recorded locally** at `<coralDir>/tracking-failures.log`, so you can see what Coral tried to send and could not. The log holds a timestamp, event name, HTTP status, and error detail — never event properties or your install ID. It is capped at 64 KB.

## The browser endpoint

One event originates in the dashboard UI rather than the server: `supporter_checkout_clicked`, because a link click can only be observed in the browser. Coral therefore exposes `POST /api/tracking/event`.

It is a strict allowlist, not a general event pipe:

- One permitted event name (`supporter_checkout_clicked`)
- Four permitted property keys (`surface`, `campaign`, `source`, `medium`)
- Values must match `^[A-Za-z0-9_.-]{1,64}$`

Anything else is dropped or rejected with `400`. Free-form text cannot be smuggled through it.

## Your AI agents

Separately from telemetry, and a question people usually ask in the same breath:

Coral has no API keys of its own and never calls a model on your behalf. It runs the CLI agents you have already installed, using your credentials. To count tokens and costs, Coral can proxy those agents' API traffic locally — the traffic goes to the same provider it always did, and nothing is sent to us.

---

## API

### Get the disclosure

```
GET /api/system/telemetry
```

Returns everything the first-run disclosure renders. The event list is generated from the tracking package's own definitions, so the UI cannot describe a different set of events than the one Coral sends.

**Response:**
```json
{
  "enabled": true,
  "acknowledged": false,
  "events": [
    { "name": "install", "when": "The first time Coral runs on this machine." },
    { "name": "team_launched", "when": "Every time you launch a team.", "extra": "agent_count — how many agents were in the team" }
  ],
  "properties": ["version — the Coral version you are running", "..."],
  "never_collected": ["Your prompts", "..."],
  "install_id_path": "/Users/you/.coral/.install_id",
  "failure_log": "/Users/you/.coral/tracking-failures.log"
}
```

| Field | Description |
|-------|-------------|
| `enabled` | `false` for builds compiled from source, which have no analytics key. The disclosure is not shown when this is `false`. |
| `acknowledged` | Whether the user has dismissed the disclosure on this install |

### Acknowledge the disclosure

```
POST /api/system/telemetry/acknowledge
```

Records the acknowledgement at `<coralDir>/.telemetry_disclosed` so the disclosure does not appear again. Idempotent.

**Response:**
```json
{ "ok": true, "acknowledged": true }
```

### Report a browser-observed event

```
POST /api/tracking/event
```

**Request:**
```json
{
  "event": "supporter_checkout_clicked",
  "props": { "surface": "settings_tier_badge" }
}
```

**Response:** `{ "ok": true }`, or `400` for an event name outside the allowlist. Property keys outside the allowlist, and values that do not match `^[A-Za-z0-9_.-]{1,64}$`, are silently dropped.
