// Settings shows one group at a time (#4566): a check that looks at every control of the dialog has to open each group in turn.
const GROUPS = ['general', 'agents', 'terminal', 'notifications', 'files', 'cleanup', 'network', 'security'];

/** Runs `expr` (a page expression returning an array) with each group open and returns the concatenated results; the dialog must be open. */
async function acrossGroups(ev, expr) {
    const out = [];
    for (const g of GROUPS) {
        await ev(`(() => { const t = document.getElementById('settings-tab-${g}'); if (t && !t.hidden) t.click(); return true; })()`);
        out.push(...await ev(expr));
    }
    return out;
}

module.exports = { GROUPS, acrossGroups };
