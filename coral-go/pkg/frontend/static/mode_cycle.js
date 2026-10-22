/* Claude Code's permission modes and Shift+Tab cycle, pure (no DOM): what the Mode button (controls.js) and its test use. */

// Claude Code's permission modes cycle with Shift+Tab (BTab in tmux). Verified in Claude Code 2.1.307 (Codex Test, an isolated scratch HOME: four single presses from Manual gave accept edits, plan, auto,
// and the footer said so each time) and in 2.1.308's own next-mode function: Manual -> Accept edits -> Plan -> Bypass Permissions (only when the session has it available) -> Auto (only when auto
// mode is available) -> Manual; from Auto the first press goes to Manual; Don't Ask goes to Manual. Which of Bypass and Auto follow Plan depends on the session, so only the steps that always hold are
// named ahead (Manual -> Accept edits -> Plan, Auto -> Manual); after Plan the button says "the next mode" and the footer shows which.
// Each state is named as Claude names it, from its footer: "manual mode on", "accept edits on", "plan mode on", "auto mode on", "bypass permissions on" (Claude's Auto is Auto, not Accept Edits).
export const MODES = {
    default: { label: "Manual", short: "Manual", next: "acceptEdits" }, // the mode a session starts in unless told otherwise (Coral's Settings call it Default)
    acceptEdits: { label: "Accept Edits", short: "Accept", next: "plan" },
    plan: { label: "Plan", short: "Plan", next: null },
    auto: { label: "Auto", short: "Auto", next: "default" },
    bypassPermissions: { label: "Bypass Permissions", short: "Bypass", next: null },
    dontAsk: { label: "Don't Ask", short: "Don't Ask", next: "default" },
};
export const MODE_CYCLE = ["default", "acceptEdits", "plan"]; // the steps that hold in every session

/** The mode a screen's mode line shows (the last lines of `text`), as a key of MODES, or null when there is no mode line to read. Only the screen's own line: this is what key presses are counted from. */
export function modeFromFooterText(text) {
    const t = String(text || "").trim().toLowerCase();
    if (!t || t === "no capture available") return null;
    const footer = t.split("\n").filter(l => l.trim()).slice(-8).join("\n");
    if (footer.includes("manual mode") || footer.includes("? for shortcuts")) return "default"; // 2.1.307 says "manual mode on"; an older Claude showed "? for shortcuts" in it
    if (footer.includes("plan mode")) return "plan";
    if (footer.includes("auto mode")) return "auto";
    if (footer.includes("bypass permissions")) return "bypassPermissions";
    if (footer.includes("don't ask")) return "dontAsk";
    if (footer.includes("accept edits") || footer.includes("auto-accept")) return "acceptEdits"; // "auto-accept edits" is an older Claude's words
    return null;
}

/**
 * How many Shift+Tab presses take `current` (the mode the SCREEN shows) to `target`, counted only over the steps that hold in every session (Default, Accept edits, Plan); null when either is not one
 * of them or there is no current mode. The agent's own report of its mode is never an input: it could be wrong or forged, and a wrong start lands on a more permissive mode than the person chose.
 */
export function pressesFor(current, target) {
    const from = MODE_CYCLE.indexOf(current), to = MODE_CYCLE.indexOf(target);
    if (from < 0 || to < 0) return null;
    const n = (to - from + MODE_CYCLE.length) % MODE_CYCLE.length;
    return n === 0 ? MODE_CYCLE.length : n;
}
