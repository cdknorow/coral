/* Settings that are one JSON list in a single setting: saved personas, saved team templates and macros (#4423).
 *
 * They used to be saved as the page's whole copy of the list, loaded at start: a persona, template or macro another tab
 * or client had added meanwhile was lost on the next save, and a macro deleted by position could delete the wrong one.
 * updateSettingList reads the list from the server at the moment of the change, applies the person's change to THAT
 * list, and writes it back, so what others added stays. (Two saves in the same instant can still cross; that window is
 * one request wide, not "since this page loaded".) */

/** parseList reads a stored list: the fallback (copied) when it is empty, not JSON or not a list. */
export function parseList(raw, fallback = []) {
    if (!raw) return [...fallback];
    try {
        const v = JSON.parse(raw);
        return Array.isArray(v) ? v : [...fallback];
    } catch { return [...fallback]; }
}

/** upsertByName returns list with each entry replacing the item of the same name, or added at the end. */
export function upsertByName(list, entries) {
    const out = [...list];
    for (const e of entries) {
        const i = out.findIndex(x => x && x.name === e.name);
        if (i >= 0) out[i] = e; else out.push(e);
    }
    return out;
}

/** removeFirst returns list without its first item for which match is true (the list as it is when nothing matches). */
export function removeFirst(list, match) {
    const i = list.findIndex(match);
    if (i < 0) return [...list];
    return [...list.slice(0, i), ...list.slice(i + 1)];
}

/**
 * updateSettingList reads the list setting key from the server now (fallback when it has none), applies change(list)
 * to it, and saves the result. When the server cannot be read it starts from local (this page's copy), as a save always
 * did. It resolves to { list, raw } (raw is the stored JSON) and throws when the save is refused.
 */
export async function updateSettingList(key, change, { fallback = [], local = null } = {}) {
    let list = null;
    try {
        const res = await fetch('/api/settings');
        if (res.ok) list = parseList(((await res.json()).settings || {})[key], fallback);
    } catch { /* use this page's copy */ }
    if (!list) list = Array.isArray(local) ? [...local] : [...fallback];
    const next = change(list);
    const raw = JSON.stringify(next);
    const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ [key]: raw }),
    });
    if (!res.ok) throw new Error(`saving ${key} failed (${res.status})`);
    return { list: next, raw };
}
