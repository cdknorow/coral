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
    if (allow.has('shell') && !hasDeny) return out('workspace-write', 'never');
    if (allow.has('shell')) return out('workspace-write', 'on-request');
    if (allow.has('file_read') && allow.has('file_write')) return out('workspace-write', 'on-request');
    if (allow.has('file_read')) return out('read-only', 'on-request');
    if (allow.has('web_access')) return out('read-only', 'on-request');
    return out('workspace-write', 'on-request');
}

/**
 * What a team member launches with, for the Mode column and its dialog (#6425): an explicit mode (the member's own, else the team's) is always applied, and for the CLIs that keep only a sandbox and an
 * approval mode (Codex, Antigravity) capabilities may only NARROW it; with no explicit mode ("default" or none) the capabilities decide. Mirrors internal/agent/permission_narrow.go.
 * @returns {{ narrowed: boolean, text?: string, warn?: boolean }} narrowed: the capabilities made it narrower than the mode (or decided it), and `text` says what it is in plain words; otherwise the mode's own name is the
 *   answer. warn: nothing is sandboxed or asked (Bypass Permissions, or Antigravity skipping prompts).
 */
const CODEX_MODE = { plan: ['read-only', 'on-request'], acceptEdits: ['workspace-write', 'on-request'], auto: ['workspace-write', 'never'], dontAsk: ['workspace-write', 'never'], bypassPermissions: ['none', 'never'] };
const CODEX_SANDBOX_RANK = { 'read-only': 1, 'workspace-write': 2, none: 3 };
const AGY_MODE_RANK = { plan: 1, dontAsk: 1, acceptEdits: 2, auto: 3, bypassPermissions: 3 };
const AGY_CAPS_RANK = { default: 0, plan: 1, 'accept-edits': 2, 'skip-permissions': 3 };
const AGY_RANK_TEXT = ['Asks first', 'Plan', 'Accepts edits', 'Skips permission prompts'];
export function launchedMode(agentType, mode, caps) {
    const family = _agentFamily(agentType);
    const hasCaps = !!(caps?.allow?.length || caps?.deny?.length);
    if (family === 'codex') {
        const m = codexEnforced(caps), mp = CODEX_MODE[mode];
        const sandboxText = sb => sb === 'read-only' ? 'Read-only sandbox' : 'Sandboxed';
        if (!mp) return m ? { narrowed: true, text: sandboxText(m.sandbox) } : { narrowed: false };
        // no capabilities, or Shell with no deny (Codex's full auto: it restricts nothing against an explicit mode, so the mode stands as chosen)
        if (!m || (m.sandbox === 'workspace-write' && m.approval === 'never')) return mp[0] === 'none' ? { narrowed: false, warn: true } : { narrowed: false };
        const narrowerSandbox = CODEX_SANDBOX_RANK[m.sandbox] < CODEX_SANDBOX_RANK[mp[0]];
        const asksMore = m.approval !== 'never' && mp[1] === 'never';
        if (!narrowerSandbox && !asksMore) return mp[0] === 'none' ? { narrowed: false, warn: true } : { narrowed: false };
        const sb = narrowerSandbox ? m.sandbox : mp[0];
        return { narrowed: true, text: sb === 'read-only' ? 'Read-only sandbox' : asksMore ? 'Sandboxed, asks first' : 'Sandboxed' };
    }
    if (family === 'agy') {
        const mr = AGY_MODE_RANK[mode], cr = AGY_CAPS_RANK[agyEnforced(caps)];
        if (mr === undefined) return hasCaps && cr > 0 ? { narrowed: true, text: AGY_RANK_TEXT[cr], ...(cr === 3 ? { warn: true } : {}) } : { narrowed: false };
        if (hasCaps && cr < mr) return { narrowed: true, text: AGY_RANK_TEXT[cr] };
        return mr === 3 ? { narrowed: false, warn: true } : { narrowed: false };
    }
    return { narrowed: false };
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
 * A saved Settings mode wins. Otherwise Claude uses Auto and Codex uses its
 * workspace-write sandbox with no approval prompts and Coral connectivity (#5925).
 * The remaining CLIs keep their existing defaults.
 */
export function defaultPermissionMode(settings, agentType) {
    const saved = settings && settings.default_permission_mode;
    if (saved) return saved;
    return !agentType || agentType === 'claude' || agentType === 'codex' ? 'auto' : 'bypassPermissions';
}

/**
 * Sets a permission mode select to the default for agentType unless the person has picked a mode in it (a change marks it): so the select follows the agent type while it still
 * shows the default (Claude: Auto, the others: as before), and never overrides a choice.
 */
export function syncDefaultPermissionMode(selectEl, settings, agentType) {
    if (!selectEl) return;
    trackPermissionPick(selectEl);
    if (!selectEl.dataset.pickedByPerson) selectEl.value = defaultPermissionMode(settings, agentType);
}

/** Marks a permission mode select as picked when its value changes (a change event): wired once, as soon as the select exists, so a pick made before the first sync still counts. */
export function trackPermissionPick(selectEl) {
    if (!selectEl || selectEl.dataset.pickWired) return;
    selectEl.dataset.pickWired = '1';
    selectEl.addEventListener('change', () => { selectEl.dataset.pickedByPerson = '1'; });
}
