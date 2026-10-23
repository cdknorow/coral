/* The composer's normal placeholder for the agent that is open ("Sending to: <name> ..."), written by the session render (sessions.js) and by every live update (websocket.js). Two states
 * replace the box's words while they hold: view only through Coral Hub (hub_access.js) and asleep (sleeping_view.js). A normal write must not undo them (#5856: the render that came after
 * the access answer replaced "View only through Coral Hub", so the box stayed disabled without saying why), but it is what comes back when the state ends, so it is kept for that. */

/** Set the normal placeholder of `input`; while a state that shows its own words holds, only remember it for when that state ends. */
export function setCommandPlaceholder(input, text) {
    if (!input) return;
    if (input.dataset.hubDisabled) { input.dataset.hubPlaceholder = text; return; }
    if (input.dataset.sleepDisabled) { input.dataset.awakePlaceholder = text; return; }
    if (input.dataset.hooksPlaceholder) { input.dataset.normalPlaceholder = text; return; } // Codex's hooks question holds the box's words (hooks_review.js, #6409)
    if (input.placeholder !== text) input.placeholder = text;
}

/** Put the words of Codex's hooks question in the box (`text`), or take them off again (''): the normal placeholder, kept meanwhile, comes back. View only and asleep keep their own words over it. */
export function setHooksPlaceholder(input, text) {
    if (!input) return;
    if (text) {
        if (!input.dataset.hooksPlaceholder) input.dataset.normalPlaceholder = input.placeholder;
        input.dataset.hooksPlaceholder = text;
        if (!input.dataset.hubDisabled && !input.dataset.sleepDisabled && input.placeholder !== text) input.placeholder = text;
    } else if (input.dataset.hooksPlaceholder) {
        const normal = input.dataset.normalPlaceholder;
        delete input.dataset.hooksPlaceholder; delete input.dataset.normalPlaceholder;
        if (normal !== undefined && !input.dataset.hubDisabled && !input.dataset.sleepDisabled) input.placeholder = normal;
    }
}
