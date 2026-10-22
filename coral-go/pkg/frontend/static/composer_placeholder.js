/* The composer's normal placeholder for the agent that is open ("Sending to: <name> ..."), written by the session render (sessions.js) and by every live update (websocket.js). Two states
 * replace the box's words while they hold: view only through Coral Hub (hub_access.js) and asleep (sleeping_view.js). A normal write must not undo them (#5856: the render that came after
 * the access answer replaced "View only through Coral Hub", so the box stayed disabled without saying why), but it is what comes back when the state ends, so it is kept for that. */

/** Set the normal placeholder of `input`; while a state that shows its own words holds, only remember it for when that state ends. */
export function setCommandPlaceholder(input, text) {
    if (!input) return;
    if (input.dataset.hubDisabled) { input.dataset.hubPlaceholder = text; return; }
    if (input.dataset.sleepDisabled) { input.dataset.awakePlaceholder = text; return; }
    if (input.placeholder !== text) input.placeholder = text;
}
