/* What the Settings dialog sends on Save: only the settings the person changed.
 *
 * The dialog fills its fields from the settings the server had when it opened, and Save used to send every
 * field. So a dashboard tab that was stale (or any other client that saved a setting while the dialog was
 * open) overwrote a setting this tab never touched with the old value it still showed: a CLI path saved
 * elsewhere came back empty. Now a field is sent only when its value differs from what the dialog opened with. */

const GIB = 1024 * 1024 * 1024;

/** A setting's value as a comparable string: booleans (True, true, false) and empty or missing alike. */
function norm(v) {
    if (v === undefined || v === null) return '';
    if (typeof v === 'boolean') return v ? 'true' : 'false';
    const s = String(v).trim();
    const l = s.toLowerCase();
    return l === 'true' || l === 'false' ? l : s;
}

// Settings that share one input in the dialog: the input shows the first one set, and saving writes both.
const SHARED_INPUT = [['cli_path_agy', 'cli_path_gemini'], ['default_model_agy', 'default_model_gemini']];

// What a missing setting reads as in its field.
// default_permission_mode: unset shows Auto (Claude's default, #5836), so a dialog saved without touching it does not turn the default into a saved mode
const FIELD_DEFAULT = { artifact_retention_kinds: 'diffs', artifact_retention_days: '0', default_permission_mode: 'auto' };

/**
 * changedSettings returns the part of payload (what the dialog's fields hold now) that differs from baseline (the
 * settings the dialog opened with). With no baseline (the dialog was never opened) it returns all of payload.
 * themeValue is the theme select's value ('dark', 'custom:<name>'), which becomes the pair theme and custom_theme.
 * edited names settings whose field the person typed in, even if what they typed equals what it showed: those are sent.
 */
export function changedSettings(payload, baseline, themeValue, edited) {
    if (!baseline) return { ...payload };
    const skip = new Set(), force = new Set(edited || []);
    for (const [a, b] of SHARED_INPUT) {
        if (!(a in payload)) continue;
        if (force.has(a) || force.has(b)) { force.add(a); force.add(b); continue; }
        if (norm(payload[a]) === (norm(baseline[a]) || norm(baseline[b]))) { skip.add(a); skip.add(b); }
    }
    if ('theme' in payload && themeValue !== undefined) {
        const shown = baseline.custom_theme ? 'custom:' + baseline.custom_theme : (baseline.theme || 'dark');
        // One control, two settings: either both stay as they are or both are written.
        const keep = themeValue === shown ? skip : force;
        keep.add('theme'); keep.add('custom_theme');
    }
    const out = {};
    for (const [k, v] of Object.entries(payload)) {
        if (skip.has(k)) continue;
        if (force.has(k)) { out[k] = v; continue; }
        if (k === 'artifact_store_max_bytes') {
            // The field shows whole GiB: a limit that is not a whole number of them is unchanged until it is edited. Unset is the default 20 GiB
            // (not 0, which is no limit, #4602): a field left alone must not turn the default into a saved value.
            const was = baseline[k] === undefined || baseline[k] === '' ? 20 * GIB : Number(baseline[k]);
            if (Math.floor(Number(v) / GIB) === Math.floor(was / GIB)) continue;
        } else if (k === 'received_artifacts_max_bytes') {
            // Same, but unset is the default 2 GiB (not 0, which is no limit): a field left alone must not turn the default into a saved value (#4441).
            const was = baseline[k] === undefined || baseline[k] === '' ? 2 * GIB : Number(baseline[k]);
            if (Math.floor(Number(v) / GIB) === Math.floor(was / GIB)) continue;
        } else {
            const was = norm(baseline[k]) || (FIELD_DEFAULT[k] ?? '');
            if (norm(v) === was) continue;
        }
        out[k] = v;
    }
    return out;
}

const isPlainObject = v => v !== null && typeof v === 'object' && !Array.isArray(v);
const sameValue = (a, b) => JSON.stringify(a) === JSON.stringify(b);

/**
 * rebaseChanges is for a form that saves a whole object (#4423): it returns fresh (what the server has now) with what the person
 * changed applied, so a Save never puts back a value another client changed while the form was open. A value in current (what the
 * form holds) that differs from opened (what the form showed when it opened) is changed; plain objects are compared key by key,
 * anything else (arrays included) whole. Everything else keeps fresh's value, and a key only fresh has is kept.
 */
export function rebaseChanges(opened, current, fresh) {
    if (!isPlainObject(current) || !isPlainObject(opened)) return sameValue(opened, current) ? fresh : current;
    const out = isPlainObject(fresh) ? { ...fresh } : {};
    for (const [k, v] of Object.entries(current)) {
        if (isPlainObject(v) && isPlainObject(opened[k])) {
            if (!sameValue(opened[k], v)) out[k] = rebaseChanges(opened[k], v, isPlainObject(out[k]) ? out[k] : opened[k]);
        } else if (!sameValue(opened[k], v)) {
            out[k] = v;
        }
    }
    return out;
}

/** changedFields is the part of current whose value differs from opened (compared whole): what a merging PUT needs. */
/** What every form says when Save finds nothing changed (#4423): one line, so the forms agree. */
export const NOTHING_TO_SAVE = 'Nothing to save: no setting changed.';

export function changedFields(opened, current) {
    const out = {};
    for (const [k, v] of Object.entries(current)) if (!sameValue(opened?.[k], v)) out[k] = v;
    return out;
}
