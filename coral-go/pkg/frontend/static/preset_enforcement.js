/* What a capability set makes each agent CLI enforce (#4545, Security's #4550 audit). Coral adds no
 * permission layer of its own: users govern agents through their CLI's own permissions, so the form says
 * what each CLI keeps and nothing more. Claude keeps the lists (Allow as pre-approvals, Deny as blocks);
 * Codex keeps only a sandbox and an approval mode; Antigravity (and "gemini", which launches it) only a
 * mode. codexEnforced and agyEnforced mirror internal/agent/permissions.go's translations, and
 * tests/frontend/preset_enforcement.test.mjs holds them to internal/agent/testdata/preset_enforcement.golden,
 * which the Go translation writes. */

function _set(list) { return new Set(list || []); }

/** Codex's sandbox and approval mode for caps, as codex.go emits them: null when caps are empty. */
export function codexEnforced(caps) {
    const allow = _set(caps?.allow);
    const hasDeny = (caps?.deny || []).length > 0;
    if (!allow.size && !hasDeny) return null;
    const search = allow.has('web_access');
    const out = (sandbox, approval) => ({ sandbox, approval, search });
    if (allow.has('shell') && allow.has('file_read') && allow.has('file_write') && allow.has('git_write') && !hasDeny) return out('none', 'never');
    if (allow.has('shell') && !hasDeny) return out('workspace-write', 'never');
    if (allow.has('shell')) return out('workspace-write', 'on-request');
    if (allow.has('file_read') && allow.has('file_write')) return out('workspace-write', 'on-request');
    if (allow.has('file_read')) return out('read-only', 'on-request');
    if (allow.has('web_access')) return out('read-only', 'on-request');
    return out('workspace-write', 'on-request');
}

/** Antigravity's mode for caps, as agy.go emits it. */
export function agyEnforced(caps) {
    const allow = _set(caps?.allow);
    const hasDeny = (caps?.deny || []).length > 0;
    if (!allow.size && !hasDeny) return 'default';
    if (allow.has('shell') && allow.has('file_write') && !hasDeny) return 'skip-permissions';
    if (allow.has('shell')) return 'accept-edits';
    if (allow.has('file_read') && !allow.has('file_write')) return 'plan';
    if (allow.has('file_read') && allow.has('file_write')) return 'accept-edits';
    return 'default';
}

const CODEX_SANDBOX = {
    'none': 'no sandbox',
    'workspace-write': 'a sandbox that can write the working folder',
    'read-only': 'a read-only sandbox',
};
const CODEX_APPROVAL = {
    'never': 'it never asks',
    'on-request': 'it asks only when the model requests it (sandboxed commands run without asking)',
};
const AGY_MODE = {
    'skip-permissions': 'nothing asks (--dangerously-skip-permissions)',
    'accept-edits': 'edits are approved without asking (--mode accept-edits)',
    'plan': 'read-only planning (--mode plan)',
    'default': 'it asks before acting (its default mode)',
};

const BYPASS_NOTE = 'Claude, in Bypass Permissions mode: nothing asks, so Allow restricts nothing; only Deny blocks.';

function _agentFamily(agentType) {
    if (agentType === 'agy' || agentType === 'antigravity' || agentType === 'gemini') return 'agy';
    return agentType || 'claude';
}

/**
 * What agentType enforces for caps under permMode (the form's Permission Mode).
 * @returns {{ text: string, denyEnforced: boolean, patternsEnforced: boolean, quiet?: boolean }} quiet: the plain Bypass line, which a mirror of this text elsewhere leaves out
 */
export function presetEnforcement(agentType, caps, permMode) {
    const family = _agentFamily(agentType);
    const allow = _set(caps?.allow);
    if (family === 'claude') {
        let text = 'Claude: Allow pre-approves these tools and others still run after it asks; Deny blocks.';
        if (permMode === 'bypassPermissions') text = BYPASS_NOTE;
        const quiet = text === BYPASS_NOTE; // the plain Bypass line is the default case: it is said on Permissions only, not again under the persona (#5595)
        if (allow.has('shell') && allow.has('git_write')) text += " Git Write's git patterns don't limit git while Shell is allowed.";
        return { text, denyEnforced: true, patternsEnforced: true, quiet };
    }
    if (family === 'codex') {
        const m = codexEnforced(caps);
        let text;
        if (!m) {
            text = 'Codex: no capabilities set, so the Permission Mode decides its sandbox and approvals.';
        } else {
            text = `Codex keeps only a sandbox and an approval mode: ${CODEX_SANDBOX[m.sandbox]}, and ${CODEX_APPROVAL[m.approval]}${m.search ? '; web search on' : ''}.`;
            if (m.sandbox === 'none') text += ' Shell, Write Files and Git Write together make it run without a sandbox.';
        }
        text += ' Deny entries and shell patterns are not enforced (a Deny entry only changes the mode).';
        return { text, denyEnforced: false, patternsEnforced: false };
    }
    if (family === 'agy') {
        const mode = agyEnforced(caps);
        let text = (caps?.allow?.length || caps?.deny?.length)
            ? `Antigravity keeps only a mode: ${AGY_MODE[mode]}.`
            : 'Antigravity: no capabilities set, so the Permission Mode decides its mode.';
        text += ' Deny entries and shell patterns are not enforced (a Deny entry only changes the mode).';
        return { text, denyEnforced: false, patternsEnforced: false };
    }
    if (family === 'terminal') return { text: '', denyEnforced: false, patternsEnforced: false };
    return { text: `${family === 'pi' ? 'Pi' : 'This agent'} does not use these permissions.`, denyEnforced: false, patternsEnforced: false };
}

/**
 * The permission mode a launch starts with when the person did not pick one (#5836): the mode they saved in Settings, else Auto for Claude (Claude's own safety checks stay on, and a new
 * user is not met with an agent that bypasses every prompt). The other CLIs keep what they had: Codex's Auto is a workspace-write sandbox that cannot reach the board on localhost, Antigravity
 * has no mode between its edits-only one and skip-permissions, and Pi has none. A saved mode is the person's choice and applies to every CLI as before.
 */
export function defaultPermissionMode(settings, agentType) {
    const saved = settings && settings.default_permission_mode;
    if (saved) return saved;
    return !agentType || agentType === 'claude' ? 'auto' : 'bypassPermissions';
}

/**
 * Sets a permission mode select to the default for agentType unless the person has picked a mode in it (a change marks it): so the select follows the agent type while it still
 * shows the default (Claude: Auto, the others: as before), and never overrides a choice.
 */
export function syncDefaultPermissionMode(selectEl, settings, agentType) {
    if (!selectEl) return;
    if (!selectEl.dataset.pickedByPerson && !selectEl.dataset.pickWired) {
        selectEl.dataset.pickWired = '1';
        selectEl.addEventListener('change', () => { selectEl.dataset.pickedByPerson = '1'; });
    }
    if (!selectEl.dataset.pickedByPerson) selectEl.value = defaultPermissionMode(settings, agentType);
}
