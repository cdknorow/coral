/* The words of Settings > Network and Hub > Coral Hub that are decisions, not layout (#6555, #6579): a pure module, node-tested.
 *   morePermsWords: the summary of the closed "More Coral Hub permissions": which permissive switches are ON (named), and a warning when the protective one is OFF.
 *   operatorLine:   where the Operator switch's state is said outside the closed disclosure (the most consequential of them, Security's #6559).
 *   spellCode:      a one-time code as a screen reader should say it ("W X Y Z, 1 2 3 4"): a code read as a word is useless. */

const list = items => items.length < 2 ? items.join('') : items.slice(0, -1).join(', ') + ' and ' + items[items.length - 1];

/** { text, warn }: "More Coral Hub permissions: may change cross-team settings and act as the Operator" or ": none on"; warn is the sentence for tokens that are visible to view-only hub users, or ''.
 *  `st` is the connector status, `operatorOn` whether the Operator switch is on (true, false, or null when this page cannot tell). */
export function morePermsWords(st, operatorOn) {
    const on = [];
    if (st && st.hub_cross_team_settings !== false) on.push('change cross-team settings');
    if (operatorOn === true) on.push('act as the Operator');
    if (st && st.hub_relaunch_updates === true) on.push('relaunch Coral to install updates');
    return {
        text: 'More Coral Hub permissions: ' + (on.length ? 'may ' + list(on) : 'none on'),
        warn: st && st.redact_tokens === false ? 'Tokens are visible to view-only hub users' : '',
    };
}

/** The Operator's state as a sentence for the status area, or '' when it does not apply (not linked, or this page cannot tell). */
export function operatorLine(operatorOn) {
    if (operatorOn === true) return 'Coral Hub may act as the Operator.';
    if (operatorOn === false) return 'Coral Hub may not act as the Operator.';
    return '';
}

/** "WXYZ-1234" as "W X Y Z, 1 2 3 4": the letters and digits said apart, the dash a pause. */
export function spellCode(code) {
    return String(code == null ? '' : code).split('-').map(part => [...part].join(' ')).filter(Boolean).join(', ');
}
